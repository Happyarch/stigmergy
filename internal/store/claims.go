package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/happyarch/stigmergy/internal/claims"
	"github.com/happyarch/stigmergy/internal/serr"
)

// Claim TTL bounds. A claim is a promise to come back; the default is one work
// session, and the ceiling stops a forgotten claim from becoming a permanent
// lock on the repository.
const (
	DefaultClaimTTL = 1800 * time.Second
	MinClaimTTL     = 60 * time.Second
	MaxClaimTTL     = 86400 * time.Second
)

// ErrNoClaim reports an unknown, released, or expired claim.
var ErrNoClaim = errors.New("store: claim not found")

// Claim is a reservation over a path.
type Claim struct {
	ID        int64  `json:"id"`
	ScopePath string `json:"scope_path"`
	Recursive bool   `json:"recursive"`
	RootID    string `json:"root_id"`
	// RepoID is the member repository scope_path is relative to. Empty means the
	// project's only repository — see LegacyRepoID.
	RepoID    string `json:"repo,omitempty"`
	AgentKind string `json:"agent_kind"`
	// OwnerModel is what the holder said it was, if it said. Advisory, like
	// every other use of it: it tells an agent who it is about to negotiate
	// with, and decides nothing.
	OwnerModel string `json:"owner_model,omitempty"`
	Worktree   string `json:"worktree"`
	Branch     string `json:"branch,omitempty"`
	Reason     string `json:"reason"`
	CreatedAt  string `json:"created_at"`
	ExpiresAt  string `json:"expires_at"`
	// OwnerLiveness is how recently the owner was heard from. A claim names
	// whoever took it, but "who holds this" and "who can answer me about it" are
	// different questions, and an agent that is about to negotiate needs the
	// second one answered — otherwise it writes to a root that is technically
	// still inside its TTL and functionally already dead.
	OwnerLiveness string `json:"owner_liveness"`
	// Own is set on the way out, relative to whoever asked: an agent needs to
	// know whether a claim in its way is its own.
	Own bool `json:"own"`
}

// RepoScope is a claim's coverage together with the repository it lives in.
//
// The repository is deliberately NOT a field on claims.Scope. That package is
// pure path algebra and stays that way — but the deciding reason is narrower: a
// Scope with a repo field would have a zero value that names no repository, and
// every construction site that forgot it would silently compare equal to every
// other one that did. A missing repo must not be spellable, so it lives out here
// where the only two constructors are the two below.
type RepoScope struct {
	Repo  string
	Scope claims.Scope
}

// RepoScope is the claim's coverage, for the overlap test.
func (c Claim) RepoScope() RepoScope {
	return RepoScope{Repo: c.RepoID, Scope: claims.Scope{Path: c.ScopePath, Recursive: c.Recursive}}
}

// overlaps reports whether two claims can be in each other's way.
//
// Two claims conflict when they name the same repository AND their paths
// overlap. The same path in two members is two different files, with no shared
// assumption behind them: without this, a claim on the client's README.md would
// block someone editing the service's.
//
// Note this does NOT reintroduce the worktree into the decision. Linked
// worktrees of one repository share a repo_id, so a claim taken in one still
// blocks the other — deliberately, because a claim protects assumptions rather
// than bytes, and two agents on two branches of one codebase are invalidating
// each other's work even though the files on disk are distinct.
func overlaps(a, b RepoScope) bool {
	return sameRepo(a.Repo, b.Repo) && claims.Overlaps(a.Scope, b.Scope)
}

// covers reports whether a claim governs a specific file in a specific repo.
//
// Two arguments, not one Scope, so a caller cannot pass a half-built value: the
// repo and the path arrive together or not at all.
func covers(s RepoScope, repo, path string) bool {
	return overlaps(s, RepoScope{Repo: repo, Scope: claims.Scope{Path: path}})
}

// sameRepo compares two repo ids, treating the empty one as matching anything.
//
// Empty means "this project's only repository" (LegacyRepoID). In a single-repo
// project every claim carries it and everything matches, which is the old
// behavior exactly. In a multi-repo project the backfill has given every claim a
// real name, so an empty one should not occur — and if one somehow does, this
// makes it block more rather than less. That is the safe direction: a false
// conflict costs one conversation, a missed one costs somebody's work.
func sameRepo(a, b string) bool {
	return a == b || a == LegacyRepoID || b == LegacyRepoID
}

// QualifyScope spells a claim the way agents read and write it: "repo:path" in a
// project with several repositories, and a bare path in one with a single
// repository, where a prefix would be noise naming the only option there is.
//
// One spelling, used everywhere a claim is shown — conflict messages, the
// roster, the audit trail, the hook's denial text — because an agent that is
// told "src/api is claimed" in a two-repository project has not been told enough
// to act, and a second spelling for the same thing is a second thing to get
// wrong.
func QualifyScope(repoID, scopePath string) string {
	if repoID == LegacyRepoID {
		return scopePath
	}
	return repoID + ":" + scopePath
}

// Qualified is the claim's scope in the spelling agents use.
func (c Claim) Qualified() string { return QualifyScope(c.RepoID, c.ScopePath) }

// activeClaims selects claims that still bind. A claim binds only while it is
// unreleased AND unexpired AND its owner is still alive: a root that crashed
// must not hold the repository hostage, so root liveness is part of the
// predicate rather than something a cleanup daemon has to catch up on.
const activeClaims = `
SELECT c.id, c.scope_path, c.recursive, c.root_id, c.repo_id, r.agent_kind, COALESCE(r.model, ''), c.worktree,
       COALESCE(c.branch, ''), c.reason, c.created_at, c.expires_at, r.last_seen_at
  FROM claims c JOIN roots r ON r.root_id = c.root_id
 WHERE c.released_at IS NULL
   AND c.expires_at > :now
   AND r.ended_at IS NULL
   AND r.last_seen_at > :cutoff`

func scanClaims(rows *sql.Rows, selfRoot string) ([]Claim, error) {
	defer rows.Close()
	out := []Claim{}
	for rows.Next() {
		var c Claim
		var owner Root
		if err := rows.Scan(&c.ID, &c.ScopePath, &c.Recursive, &c.RootID, &c.RepoID, &c.AgentKind, &c.OwnerModel,
			&c.Worktree, &c.Branch, &c.Reason, &c.CreatedAt, &c.ExpiresAt, &owner.LastSeenAt); err != nil {
			return nil, serr.Internalf(err, "failed to read claims")
		}
		c.OwnerLiveness = owner.Liveness()
		c.Own = selfRoot != "" && c.RootID == selfRoot
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, serr.Internalf(err, "failed to read claims")
	}
	return out, nil
}

type queryer interface {
	Query(string, ...any) (*sql.Rows, error)
}

func activeIn(q queryer, selfRoot string) ([]Claim, error) {
	rows, err := q.Query(activeClaims, sql.Named("now", Now()), sql.Named("cutoff", RootTTLCutoff()))
	if err != nil {
		return nil, serr.Internalf(err, "failed to query claims")
	}
	return scanClaims(rows, selfRoot)
}

// ActiveClaims lists every claim currently in force.
func (d *DB) ActiveClaims(selfRoot string) ([]Claim, error) { return activeIn(d.DB, selfRoot) }

// ClaimsCovering lists the active claims that govern a repo-relative path.
//
// The overlap test runs in Go, not SQL: it is component-wise (see package
// claims), and expressing that in SQL would mean a LIKE pattern whose escaping
// is easy to get subtly wrong. Claim counts are tiny — a handful of agents,
// each holding a few paths — so filtering in Go costs nothing and keeps one
// tested definition of overlap for both the server and the hook.
// The repo is a separate argument rather than part of the path so that a caller
// physically cannot ask about a path without saying where it lives.
func (d *DB) ClaimsCovering(repoID, path, selfRoot string) ([]Claim, error) {
	all, err := d.ActiveClaims(selfRoot)
	if err != nil {
		return nil, err
	}
	var out []Claim
	for _, c := range all {
		if covers(c.RepoScope(), repoID, path) {
			out = append(out, c)
		}
	}
	return out, nil
}

// ClaimRequest is a claim_acquire.
type ClaimRequest struct {
	ScopePath string
	Recursive bool
	RootID    string
	// RepoID is the member repository ScopePath is relative to. Empty is the
	// project's only repository, which is what a single-repo project always
	// passes and what every claim written before 0007 carries.
	RepoID     string
	Worktree   string
	Branch     string
	Reason     string
	TTLSeconds int
}

// NormalizeTTL clamps a requested TTL into the allowed band, defaulting when
// unset. A claim that never expires is a deadlock waiting to happen.
func NormalizeTTL(seconds int) (time.Duration, error) {
	if seconds == 0 {
		return DefaultClaimTTL, nil
	}
	ttl := time.Duration(seconds) * time.Second
	if ttl < MinClaimTTL || ttl > MaxClaimTTL {
		return 0, serr.E(serr.InvalidInput,
			"ttl_seconds must be between %d and %d, got %d",
			int(MinClaimTTL.Seconds()), int(MaxClaimTTL.Seconds()), seconds)
	}
	return ttl, nil
}

// AcquireClaim takes a claim, or reports who is in the way.
//
// Overlap check and insert share one immediate transaction: two agents racing
// for the same file must not both come away believing they hold it.
func (d *DB) AcquireClaim(req ClaimRequest) (*Claim, error) {
	req.Reason = NormalizeText(req.Reason)
	if req.Reason == "" {
		return nil, serr.E(serr.InvalidInput, "reason must not be empty: other agents see it when your claim blocks them")
	}
	// The reason is quoted back inside the conflict message of every agent this
	// claim blocks, and again in the roster and in doctor's output. It reaches
	// more terminals than almost anything else an agent writes.
	if err := ValidateLine("reason", req.Reason, MaxLineLength); err != nil {
		return nil, err
	}
	// Worktree and branch are echoed alongside it in the same places. No length
	// limit on the path: a long worktree is awkward, not wrong, and refusing one
	// would lock an agent out of its own checkout over cosmetics.
	if err := ValidateLine("worktree", req.Worktree, 0); err != nil {
		return nil, err
	}
	if err := ValidateLine("branch", req.Branch, MaxLineLength); err != nil {
		return nil, err
	}
	ttl, err := NormalizeTTL(req.TTLSeconds)
	if err != nil {
		return nil, err
	}

	tx, err := d.Begin()
	if err != nil {
		return nil, serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	// Refresh the acquiring root FIRST, inside this transaction.
	//
	// heartbeat() releases the claims of a root that had already lapsed, which is
	// correct — other agents were told those paths were free and one of them may
	// already be editing. But it made every caller that heartbeats AFTER its real
	// work destroy that work: claimAcquire inserted the claim, called touch(),
	// and the sweep released "all of this root's claims" including the one from a
	// moment earlier. The caller was handed the object captured before the
	// release, describing an active claim with a full TTL that no longer existed.
	// Silent and total, and the prescribed workflow — register, search, read for
	// a while, claim before editing — walks straight into it, because reads do
	// not heartbeat and RootTTL is 15m.
	//
	// Sweeping here instead makes the lapse, the overlap check and the insert one
	// atomic step: the stale claims are gone before activeIn() looks, so a
	// returning root re-acquiring its own scope gets a live claim rather than
	// being handed the dead one, and the trailing touch() finds nothing to do.
	//
	// Fixing it by reordering in the MCP layer would leave two transactions and
	// the same race.
	//
	// ErrNoRoot is tolerated: acquiring has never required a registered root, and
	// a claim from an unregistered one has no liveness to sweep.
	if err := heartbeat(tx, req.RootID); err != nil && !errors.Is(err, ErrNoRoot) {
		return nil, err
	}

	existing, err := activeIn(tx, req.RootID)
	if err != nil {
		return nil, err
	}
	want := RepoScope{Repo: req.RepoID, Scope: claims.Scope{Path: req.ScopePath, Recursive: req.Recursive}}
	for _, c := range existing {
		if !overlaps(c.RepoScope(), want) {
			continue
		}
		if c.RootID == req.RootID {
			// Already ours: acquiring twice is not an error, it is the same
			// promise. Hand back the claim we already hold.
			return &c, nil
		}
		// The owner's liveness is in the message, not just the payload: it is what
		// decides whether negotiating is even worth doing, and an agent that has
		// to go and look it up separately will not.
		return nil, serr.E(serr.ClaimConflict,
			"%s is claimed by %s — %s (worktree %s, reason: %q, expires %s). "+
				"Write to that root with mailbox_send(to_root=%q), or work elsewhere. "+
				"Do not address any other root about this path: %s is the one holding it.",
			c.Qualified(), c.RootID, c.OwnerLiveness, c.Worktree, c.Reason, c.ExpiresAt,
			c.RootID, c.RootID).
			With("conflict", c)
	}

	now := NowTime()
	res, err := tx.Exec(
		`INSERT INTO claims(scope_path, recursive, root_id, repo_id, worktree, branch, reason, created_at, expires_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ScopePath, req.Recursive, req.RootID, req.RepoID, req.Worktree, nullStr(req.Branch), req.Reason,
		Stamp(now), Stamp(now.Add(ttl)),
	)
	if err != nil {
		return nil, serr.Internalf(err, "failed to record the claim")
	}
	id, _ := res.LastInsertId()
	if err := audit(tx, AuditEntry{
		Actor: req.RootID, Action: "claim_acquire", Target: QualifyScope(req.RepoID, req.ScopePath),
		Detail: req.Reason,
	}); err != nil {
		return nil, serr.Internalf(err, "failed to write audit record")
	}
	if err := tx.Commit(); err != nil {
		return nil, serr.Internalf(err, "failed to commit the claim")
	}
	return &Claim{
		ID: id, ScopePath: req.ScopePath, Recursive: req.Recursive, RootID: req.RootID,
		RepoID: req.RepoID, Worktree: req.Worktree, Branch: req.Branch, Reason: req.Reason,
		CreatedAt: Stamp(now), ExpiresAt: Stamp(now.Add(ttl)), Own: true,
	}, nil
}

// RenewClaim extends a claim the caller owns.
func (d *DB) RenewClaim(id int64, rootID string, ttlSeconds int) (*Claim, error) {
	ttl, err := NormalizeTTL(ttlSeconds)
	if err != nil {
		return nil, err
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	// Refresh the renewing root first, inside this transaction, for the same
	// reason AcquireClaim does: heartbeat() releases a lapsed root's claims, and
	// a caller that heartbeats afterwards would be told its claim was renewed and
	// then have it swept away a moment later. Sweeping first turns that into the
	// honest answer — the claim is gone, ErrNoClaim, re-acquire.
	if err := heartbeat(tx, rootID); err != nil && !errors.Is(err, ErrNoRoot) {
		return nil, err
	}

	cur, err := claimForRenewal(tx, id)
	if err != nil {
		return nil, err
	}
	if cur.RootID != rootID {
		return nil, notOwner(id, cur.RootID, "renew")
	}

	// An EXPIRED claim has already stopped binding. Other agents were told the
	// path was free and one of them may be editing it right now, so extending the
	// expiry without looking would hand the same path to two roots — the exact
	// resurrection the lapse sweep exists to prevent, one function over.
	//
	// Re-running the overlap check rather than refusing outright is the friendlier
	// half of that: if nobody took the path, renewal is harmless and the agent
	// carries on. If somebody did, they are named, exactly as an acquire would
	// name them.
	if cur.ExpiresAt <= Now() {
		active, err := activeIn(tx, rootID)
		if err != nil {
			return nil, err
		}
		want := RepoScope{Repo: cur.RepoID, Scope: claims.Scope{Path: cur.ScopePath, Recursive: cur.Recursive}}
		for _, c := range active {
			if c.RootID == rootID || !overlaps(c.RepoScope(), want) {
				continue
			}
			return nil, serr.E(serr.ClaimConflict,
				"claim %d expired at %s and %s has since taken %s — %s (worktree %s, reason: %q). "+
					"It stopped binding when it expired, so renewing it now would give the same path to two agents. "+
					"Negotiate with mailbox_send(to_root=%q), or work elsewhere.",
				id, cur.ExpiresAt, c.RootID, c.Qualified(), c.OwnerLiveness, c.Worktree, c.Reason, c.RootID).
				With("conflict", c)
		}
	}

	expires := Stamp(NowTime().Add(ttl))
	res, err := tx.Exec(
		`UPDATE claims SET expires_at = ? WHERE id = ? AND released_at IS NULL`, expires, id)
	if err != nil {
		return nil, serr.Internalf(err, "failed to renew the claim")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNoClaim
	}
	if err := audit(tx, AuditEntry{Actor: rootID, Action: "claim_renew", Detail: "expires_at=" + expires}); err != nil {
		return nil, serr.Internalf(err, "failed to write audit record")
	}
	if err := tx.Commit(); err != nil {
		return nil, serr.Internalf(err, "failed to commit the renewal")
	}
	return d.getClaim(id, rootID)
}

// ReleaseClaim drops a claim the caller owns.
func (d *DB) ReleaseClaim(id int64, rootID string) error {
	tx, err := d.Begin()
	if err != nil {
		return serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	owner, err := claimOwner(tx, id)
	if err != nil {
		return err
	}
	if owner != rootID {
		return notOwner(id, owner, "release")
	}
	res, err := tx.Exec(`UPDATE claims SET released_at = ? WHERE id = ? AND released_at IS NULL`, Now(), id)
	if err != nil {
		return serr.Internalf(err, "failed to release the claim")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoClaim
	}
	if err := audit(tx, AuditEntry{Actor: rootID, Action: "claim_release", Target: ""}); err != nil {
		return serr.Internalf(err, "failed to write audit record")
	}
	if err := tx.Commit(); err != nil {
		return serr.Internalf(err, "failed to commit the release")
	}
	return nil
}

// claimForRenewal reads the fields renewal has to reason about: who owns it,
// what it covers, and whether it is still in force.
//
// Separate from claimOwner because renewal is the one operation that has to see
// the expiry. Everything else only cares whether the row is released.
func claimForRenewal(tx *sql.Tx, id int64) (*Claim, error) {
	var c Claim
	err := tx.QueryRow(
		`SELECT id, scope_path, recursive, root_id, repo_id, expires_at
		   FROM claims WHERE id = ? AND released_at IS NULL`, id).
		Scan(&c.ID, &c.ScopePath, &c.Recursive, &c.RootID, &c.RepoID, &c.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoClaim
	}
	if err != nil {
		return nil, serr.Internalf(err, "failed to read the claim")
	}
	return &c, nil
}

func claimOwner(tx *sql.Tx, id int64) (string, error) {
	var owner string
	err := tx.QueryRow(`SELECT root_id FROM claims WHERE id = ? AND released_at IS NULL`, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoClaim
	}
	if err != nil {
		return "", serr.Internalf(err, "failed to read the claim")
	}
	return owner, nil
}

func notOwner(id int64, owner, verb string) error {
	return serr.E(serr.NotOwner,
		"claim %d belongs to %s, so you cannot %s it — if it is blocking you, negotiate with mailbox_send",
		id, owner, verb).With("owner", owner)
}

func (d *DB) getClaim(id int64, selfRoot string) (*Claim, error) {
	rows, err := d.Query(activeClaims+` AND c.id = :id`,
		sql.Named("now", Now()), sql.Named("cutoff", RootTTLCutoff()), sql.Named("id", id))
	if err != nil {
		return nil, serr.Internalf(err, "failed to read the claim")
	}
	found, err := scanClaims(rows, selfRoot)
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, ErrNoClaim
	}
	return &found[0], nil
}
