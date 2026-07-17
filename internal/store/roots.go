package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/happyarch/stigmergy/internal/hosts"
	"github.com/happyarch/stigmergy/internal/serr"
)

// Root activity TTL: a root that has not been heard from in this long is
// treated as gone, and its claims stop blocking anyone. This is what makes a
// crashed agent self-healing rather than a permanent lock.
//
// It is short on purpose. The TTL is not a guess at how long an agent might
// idle — it is how long a *dead* agent goes on looking alive, and everything
// bad follows from that window: its claims keep blocking, and mail addressed to
// it is accepted and never read. Fifteen minutes is survivable; the hour this
// used to be was not.
//
// What makes fifteen minutes safe is that liveness no longer depends on an
// agent choosing to call stigmergy. The hooks heartbeat on every edit and every
// turn (see hooks.Heartbeat), so an agent that is doing anything at all stays
// live, and only one that has genuinely stopped goes silent.
const RootTTL = 900 * time.Second

// StaleRootSilence is when a root is still inside the TTL but has been quiet
// long enough to be worth flagging. It changes no decision — a root inside the
// TTL is live, full stop — but an agent about to negotiate with someone who has
// not been heard from in twelve minutes deserves to know that before it commits
// to waiting for an answer.
const StaleRootSilence = 5 * time.Minute

// StaleRootAge is when a silent root gets its ended_at stamped for good, during
// opportunistic cleanup.
const StaleRootAge = 7 * 24 * time.Hour

// AgentKinds is the closed set of hosts, declared once in the hosts package.
//
// It used to be a second hand-written list, kept in step with the hosts it
// duplicated only by whoever remembered both. A CHECK constraint on
// roots.agent_kind still enforces the same set from the schema side; migration
// 0006 removes it, because it costs a table rebuild per host — that is how 0003
// silently dropped both roots indexes and 0004 had to put them back — and buys
// nothing an agent ever hits, since every write goes through
// Registration.validate first.
var AgentKinds = hosts.Kinds()

// ErrNoRoot reports an unknown or already-ended root.
var ErrNoRoot = errors.New("store: root not found")

// Root is a registered top-level agent session.
type Root struct {
	RootID       string `json:"root_id"`
	AgentKind    string `json:"agent_kind"`
	SessionLabel string `json:"session_label,omitempty"`
	Worktree     string `json:"worktree"`
	Branch       string `json:"branch,omitempty"`
	// Model is what the agent says it is, and is never checked. See
	// Registration.Model.
	Model        string `json:"model,omitempty"`
	RegisteredAt string `json:"registered_at"`
	LastSeenAt   string `json:"last_seen_at"`
}

// RootTTLCutoff is the last_seen_at below which a root counts as inactive. It
// appears in every active-claim predicate.
func RootTTLCutoff() string { return Stamp(NowTime().Add(-RootTTL)) }

// SilentFor is how long since this root was last heard from. A negative or
// unparseable stamp reads as zero: a clock that has gone backwards is not
// evidence that an agent is dead.
func (r Root) SilentFor() time.Duration {
	t, err := ParseStamp(r.LastSeenAt)
	if err != nil {
		return 0
	}
	if d := NowTime().Sub(t); d > 0 {
		return d.Round(time.Second)
	}
	return 0
}

// Liveness describes a root the way an agent needs to hear it: not a timestamp,
// but whether there is anybody there to answer.
//
// The distinction it draws is the one that matters when deciding who to write
// to. "live" means mail will be read. "quiet" means the root is still inside the
// TTL — so it holds its claims and can be written to — but has not been heard
// from in a while, and may be about to lapse. An agent that knows the difference
// can choose to wait out a claim rather than open a negotiation with a corpse.
func (r Root) Liveness() string {
	d := r.SilentFor()
	switch {
	case d >= RootTTL:
		return fmt.Sprintf("gone (silent for %s)", short(d))
	case d >= StaleRootSilence:
		return fmt.Sprintf("quiet (last seen %s ago; lapses in %s if it stays silent)",
			short(d), short(RootTTL-d))
	default:
		return fmt.Sprintf("live (last seen %s ago)", short(d))
	}
}

// Live reports whether the root is inside the TTL: whether its claims bind and
// its mail can be delivered.
func (r Root) Live() bool { return r.SilentFor() < RootTTL }

// short renders a duration the way a person says it. time.Duration's own String
// gives "12m3.000000001s", which is not something to put in front of an agent.
func short(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// Registration is a root_register request.
type Registration struct {
	RootID       string // pre-generated by the caller (internal/ids)
	AgentKind    string
	Worktree     string
	Branch       string
	SessionLabel string
	// Model is the agent's own account of which model it is — "claude-opus-4-8",
	// "gemini-3-pro". It is optional, unvalidated, and load-bearing for nothing:
	// agent_kind is the harness, and only the agent knows what is behind it.
	// Asking is the only way to find out, and a wrong answer costs a line of
	// roster output, so it is not worth defending against.
	Model string
}

func (r Registration) validate() error {
	if !contains(AgentKinds, r.AgentKind) {
		// Built from AgentKinds rather than written out: this sentence named the
		// three hosts it knew about for as long as there were three, and would
		// have gone on naming them afterwards.
		quoted := make([]string, 0, len(AgentKinds))
		for _, k := range AgentKinds {
			quoted = append(quoted, strconv.Quote(k))
		}
		return serr.E(serr.InvalidInput, "agent_kind %q is invalid: must be one of %s",
			r.AgentKind, strings.Join(quoted, ", "))
	}
	if !filepath.IsAbs(r.Worktree) {
		return serr.E(serr.InvalidInput, "worktree must be an absolute path, got %q", r.Worktree)
	}
	return nil
}

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

const rootCols = `root_id, agent_kind, COALESCE(session_label, ''), worktree, COALESCE(branch, ''), COALESCE(model, ''), registered_at, last_seen_at`

func scanRoot(row interface{ Scan(...any) error }) (*Root, error) {
	var r Root
	err := row.Scan(&r.RootID, &r.AgentKind, &r.SessionLabel, &r.Worktree, &r.Branch, &r.Model, &r.RegisteredAt, &r.LastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoRoot
	}
	if err != nil {
		return nil, serr.Internalf(err, "failed to read root")
	}
	return &r, nil
}

// RegisterRoot creates a root, or resumes an existing one.
//
// Resume is not an optimization: hosts restart the MCP server process mid
// session (Claude on /clear, Codex on compact), and a fresh root_id each time
// would strand the previous root's claims — live, unreleasable, blocking every
// other agent until TTL. Matching on (agent_kind, worktree, session_label)
// reconnects the session to the claims it already owns. An empty session_label
// cannot be matched on, so it always mints a new root.
func (d *DB) RegisterRoot(reg Registration) (root *Root, resumed bool, err error) {
	if err := reg.validate(); err != nil {
		return nil, false, err
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, false, serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	now := Now()
	if reg.SessionLabel != "" {
		existing, err := scanRoot(tx.QueryRow(
			`SELECT `+rootCols+` FROM roots
			  WHERE agent_kind = ? AND worktree = ? AND session_label = ?
			    AND ended_at IS NULL AND last_seen_at > ?
			  ORDER BY registered_at DESC LIMIT 1`,
			reg.AgentKind, reg.Worktree, reg.SessionLabel, RootTTLCutoff()))
		switch {
		case err == nil:
			// A resuming session may report a model the first one did not, or a
			// different one: the host can be restarted onto another model mid
			// session. COALESCE keeps the last non-empty answer rather than
			// letting a silent resume erase what an earlier one told us.
			if _, err := tx.Exec(
				`UPDATE roots SET last_seen_at = ?, branch = ?, model = COALESCE(?, model) WHERE root_id = ?`,
				now, nullStr(reg.Branch), nullStr(reg.Model), existing.RootID); err != nil {
				return nil, false, serr.Internalf(err, "failed to refresh root")
			}
			if reg.Model != "" {
				existing.Model = reg.Model
			}
			if err := audit(tx, AuditEntry{
				Actor: existing.RootID, AgentKind: reg.AgentKind, Action: "root_resume",
				Target: reg.Worktree, Detail: "session_label=" + reg.SessionLabel,
			}); err != nil {
				return nil, false, serr.Internalf(err, "failed to write audit record")
			}
			if err := tx.Commit(); err != nil {
				return nil, false, serr.Internalf(err, "failed to commit root resume")
			}
			existing.LastSeenAt, existing.Branch = now, reg.Branch
			return existing, true, nil
		case errors.Is(err, ErrNoRoot): // fall through to create
		default:
			return nil, false, err
		}
	}

	if _, err := tx.Exec(
		`INSERT INTO roots(root_id, agent_kind, session_label, worktree, branch, model, registered_at, last_seen_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		reg.RootID, reg.AgentKind, nullStr(reg.SessionLabel), reg.Worktree, nullStr(reg.Branch),
		nullStr(reg.Model), now, now,
	); err != nil {
		return nil, false, serr.Internalf(err, "failed to register root")
	}
	if err := audit(tx, AuditEntry{
		Actor: reg.RootID, AgentKind: reg.AgentKind, Action: "root_register",
		Target: reg.Worktree, Detail: "session_label=" + reg.SessionLabel,
	}); err != nil {
		return nil, false, serr.Internalf(err, "failed to write audit record")
	}
	if err := tx.Commit(); err != nil {
		return nil, false, serr.Internalf(err, "failed to commit root registration")
	}
	return &Root{
		RootID: reg.RootID, AgentKind: reg.AgentKind, SessionLabel: reg.SessionLabel,
		Worktree: reg.Worktree, Branch: reg.Branch, Model: reg.Model,
		RegisteredAt: now, LastSeenAt: now,
	}, false, nil
}

// GetRoot returns an active root by id.
// ActiveRoots lists the roots currently working in this project: registered, not
// ended, and heard from within the root TTL. The same liveness test the claim
// predicate uses, so what `stigmergy watch` shows as active is exactly what is
// capable of blocking an edit.
func (d *DB) ActiveRoots() ([]Root, error) {
	rows, err := d.Query(
		`SELECT `+rootCols+` FROM roots
		  WHERE ended_at IS NULL AND last_seen_at > ?
		  ORDER BY registered_at`, RootTTLCutoff())
	if err != nil {
		return nil, serr.Internalf(err, "failed to list roots")
	}
	defer rows.Close()

	out := []Root{}
	for rows.Next() {
		r, err := scanRoot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (d *DB) GetRoot(rootID string) (*Root, error) {
	return scanRoot(d.QueryRow(
		`SELECT `+rootCols+` FROM roots WHERE root_id = ? AND ended_at IS NULL`, rootID))
}

// RootBySession resolves the root owning a host session label — how a hook
// tells "my own claim" from "someone else's".
func (d *DB) RootBySession(agentKind, sessionLabel string) (*Root, error) {
	if sessionLabel == "" {
		return nil, ErrNoRoot
	}
	return scanRoot(d.QueryRow(
		`SELECT `+rootCols+` FROM roots
		  WHERE agent_kind = ? AND session_label = ? AND ended_at IS NULL AND last_seen_at > ?
		  ORDER BY registered_at DESC LIMIT 1`,
		agentKind, sessionLabel, RootTTLCutoff()))
}

// Heartbeat refreshes a root's liveness. Every registered call does this, so an
// agent that is working never loses its claims to the TTL.
func (d *DB) Heartbeat(rootID string) error {
	tx, err := d.Begin()
	if err != nil {
		return serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()
	if err := heartbeat(tx, rootID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return serr.Internalf(err, "failed to commit heartbeat")
	}
	return nil
}

// HeartbeatSession refreshes liveness for a host session, without the caller
// having to know its root id.
//
// This is the hook path. A hook knows the host's session id and nothing else —
// it has no MCP session, no registered root, no memory of a previous call — so
// resolving the root and refreshing it has to be one round trip. It returns
// ErrNoRoot for an unregistered session, which hooks ignore: an agent that never
// registered has no claims to keep alive, and nothing to heartbeat.
func (d *DB) HeartbeatSession(agentKind, sessionLabel string) error {
	if sessionLabel == "" {
		return ErrNoRoot
	}
	tx, err := d.Begin()
	if err != nil {
		return serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	var rootID string
	err = tx.QueryRow(
		`SELECT root_id FROM roots
		  WHERE agent_kind = ? AND session_label = ? AND ended_at IS NULL
		  ORDER BY registered_at DESC LIMIT 1`, agentKind, sessionLabel).Scan(&rootID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNoRoot
	}
	if err != nil {
		return serr.Internalf(err, "failed to resolve the session's root")
	}
	if err := heartbeat(tx, rootID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return serr.Internalf(err, "failed to commit heartbeat")
	}
	return nil
}

// heartbeat refreshes a root, and releases its claims first if it had already
// lapsed.
//
// A lapsed root's claims have stopped binding: the active predicate ignores
// them, other agents have been told those paths are free, and one of them may
// already have claimed and started editing. If the original root then comes back
// — a long silence, then one tool call — a bare UPDATE of last_seen_at would
// bring those claims back to life, and two agents would hold the same path, each
// having been told it was theirs.
//
// So coming back from the dead costs you your claims. The agent is welcome, its
// promises are not: it must re-acquire, which is the moment the overlap check
// runs and it learns the path is spoken for. Silence is the only thing stigmergy
// can read as absence, and this is what makes that reading safe.
func heartbeat(tx *sql.Tx, rootID string) error {
	var lastSeen string
	err := tx.QueryRow(`SELECT last_seen_at FROM roots WHERE root_id = ? AND ended_at IS NULL`,
		rootID).Scan(&lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNoRoot
	}
	if err != nil {
		return serr.Internalf(err, "failed to read the root")
	}

	now := Now()
	if lastSeen <= RootTTLCutoff() {
		res, err := tx.Exec(
			`UPDATE claims SET released_at = ? WHERE root_id = ? AND released_at IS NULL`, now, rootID)
		if err != nil {
			return serr.Internalf(err, "failed to release the lapsed root's claims")
		}
		if n, _ := res.RowsAffected(); n > 0 {
			if err := audit(tx, AuditEntry{
				Actor: rootID, Action: "root_lapsed_claims_released",
				Detail: fmt.Sprintf("root returned after %s of silence; %d claim(s) released, it must re-acquire",
					short(NowTime().Sub(mustParse(lastSeen))), n),
			}); err != nil {
				return serr.Internalf(err, "failed to write audit record")
			}
		}
	}
	if _, err := tx.Exec(`UPDATE roots SET last_seen_at = ? WHERE root_id = ?`, now, rootID); err != nil {
		return serr.Internalf(err, "failed to record heartbeat")
	}
	return nil
}

// mustParse is for stamps that came out of our own database, where an
// unparseable value is a bug rather than an input error. It reads as "now", so
// the worst case is a silence reported as zero.
func mustParse(stamp string) time.Time {
	t, err := ParseStamp(stamp)
	if err != nil {
		return NowTime()
	}
	return t
}

// DeregisterRoot ends a root and releases everything it held. A clean exit
// should not make others wait out the TTL.
func (d *DB) DeregisterRoot(rootID string) error {
	tx, err := d.Begin()
	if err != nil {
		return serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	now := Now()
	res, err := tx.Exec(`UPDATE roots SET ended_at = ? WHERE root_id = ? AND ended_at IS NULL`, now, rootID)
	if err != nil {
		return serr.Internalf(err, "failed to deregister root")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoRoot
	}
	if _, err := tx.Exec(
		`UPDATE claims SET released_at = ? WHERE root_id = ? AND released_at IS NULL`, now, rootID,
	); err != nil {
		return serr.Internalf(err, "failed to release claims")
	}
	if err := audit(tx, AuditEntry{Actor: rootID, Action: "root_deregister"}); err != nil {
		return serr.Internalf(err, "failed to write audit record")
	}
	if err := tx.Commit(); err != nil {
		return serr.Internalf(err, "failed to commit deregistration")
	}
	return nil
}

// ReapStaleRoots ends roots that went silent long ago. Claims are already
// ignored past RootTTL by the active predicate; this is bookkeeping, run
// opportunistically on context_open, never on a hot path.
func (d *DB) ReapStaleRoots() (int, error) {
	now := NowTime()
	res, err := d.Exec(
		`UPDATE roots SET ended_at = ? WHERE ended_at IS NULL AND last_seen_at < ?`,
		Stamp(now), Stamp(now.Add(-StaleRootAge)))
	if err != nil {
		return 0, serr.Internalf(err, "failed to reap stale roots")
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
