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

// Scope is the claim's coverage, for the pure overlap logic.
func (c Claim) Scope() claims.Scope {
	return claims.Scope{Path: c.ScopePath, Recursive: c.Recursive}
}

// activeClaims selects claims that still bind. A claim binds only while it is
// unreleased AND unexpired AND its owner is still alive: a root that crashed
// must not hold the repository hostage, so root liveness is part of the
// predicate rather than something a cleanup daemon has to catch up on.
const activeClaims = `
SELECT c.id, c.scope_path, c.recursive, c.root_id, r.agent_kind, COALESCE(r.model, ''), c.worktree,
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
		if err := rows.Scan(&c.ID, &c.ScopePath, &c.Recursive, &c.RootID, &c.AgentKind, &c.OwnerModel,
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
func (d *DB) ClaimsCovering(path, selfRoot string) ([]Claim, error) {
	all, err := d.ActiveClaims(selfRoot)
	if err != nil {
		return nil, err
	}
	var out []Claim
	for _, c := range all {
		if claims.Covers(c.Scope(), path) {
			out = append(out, c)
		}
	}
	return out, nil
}

// ClaimRequest is a claim_acquire.
type ClaimRequest struct {
	ScopePath  string
	Recursive  bool
	RootID     string
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
	if req.Reason == "" {
		return nil, serr.E(serr.InvalidInput, "reason must not be empty: other agents see it when your claim blocks them")
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

	existing, err := activeIn(tx, req.RootID)
	if err != nil {
		return nil, err
	}
	want := claims.Scope{Path: req.ScopePath, Recursive: req.Recursive}
	for _, c := range existing {
		if !claims.Overlaps(c.Scope(), want) {
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
			req.ScopePath, c.RootID, c.OwnerLiveness, c.Worktree, c.Reason, c.ExpiresAt,
			c.RootID, c.RootID).
			With("conflict", c)
	}

	now := NowTime()
	res, err := tx.Exec(
		`INSERT INTO claims(scope_path, recursive, root_id, worktree, branch, reason, created_at, expires_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ScopePath, req.Recursive, req.RootID, req.Worktree, nullStr(req.Branch), req.Reason,
		Stamp(now), Stamp(now.Add(ttl)),
	)
	if err != nil {
		return nil, serr.Internalf(err, "failed to record the claim")
	}
	id, _ := res.LastInsertId()
	if err := audit(tx, AuditEntry{
		Actor: req.RootID, Action: "claim_acquire", Target: req.ScopePath, Detail: req.Reason,
	}); err != nil {
		return nil, serr.Internalf(err, "failed to write audit record")
	}
	if err := tx.Commit(); err != nil {
		return nil, serr.Internalf(err, "failed to commit the claim")
	}
	return &Claim{
		ID: id, ScopePath: req.ScopePath, Recursive: req.Recursive, RootID: req.RootID,
		Worktree: req.Worktree, Branch: req.Branch, Reason: req.Reason,
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

	owner, err := claimOwner(tx, id)
	if err != nil {
		return nil, err
	}
	if owner != rootID {
		return nil, notOwner(id, owner, "renew")
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
