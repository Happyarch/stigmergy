package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/happyarch/stigmergy/internal/serr"
)

// ErrNoPolicy reports a memory with no evidence policy. Like ErrNotFound it is
// not a serr code: having no policy is the ordinary state of almost every
// memory, not a fault.
var ErrNoPolicy = errors.New("store: memory has no evidence policy")

// Evidence path kinds.
const (
	PathLiteral = "literal"
	PathGlob    = "glob"
)

// EvidencePath is one declared pattern within a member.
type EvidencePath struct {
	Kind    string `json:"kind"`
	Pattern string `json:"pattern"`
}

// EvidenceMember is one repository the policy observes, and the commit
// observation starts from.
type EvidenceMember struct {
	RepoID     string         `json:"repo"`
	BaseOID    string         `json:"base_oid"`
	CapturedAt string         `json:"captured_at"`
	Paths      []EvidencePath `json:"paths,omitempty"`
}

// EvidencePolicy is what was declared for one memory.
//
// It is configuration for an observation, not a claim about the world. Nothing
// here says what the memory is ABOUT — only where to look when someone wants to
// know what has changed since they last cared.
type EvidencePolicy struct {
	Key       string           `json:"key"`
	Version   int              `json:"version"`
	UpdatedBy string           `json:"updated_by"`
	UpdatedAt string           `json:"updated_at"`
	Members   []EvidenceMember `json:"members"`
}

// EvidenceSet declares a policy, replacing any policy already there.
//
// The baselines are captured by the CALLER, before this is invoked, and passed
// in already resolved. That split is deliberate: this package has no git and
// cannot import the one that resolves repositories, and — more importantly —
// capture must either succeed for every declared member or change nothing at
// all. Partial coverage is a thing an EVALUATION can report; it is never a valid
// half-written baseline.
type EvidenceSet struct {
	Key                   string
	ExpectedMemoryVersion int
	// ExpectedPolicyVersion is nil only when no policy exists yet. Replacing or
	// re-capturing one names its current version, exactly like a memory write.
	ExpectedPolicyVersion *int
	Members               []EvidenceMember
	Actor                 string
	AgentKind             string
}

// ReadEvidencePolicy returns one memory's policy, or ErrNoPolicy.
func (d *DB) ReadEvidencePolicy(key string) (*EvidencePolicy, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	all, err := d.evidencePolicies(key)
	if err != nil {
		return nil, err
	}
	p, ok := all[key]
	if !ok {
		return nil, ErrNoPolicy
	}
	return p, nil
}

// EvidencePolicies returns every policy in the database, keyed by memory key.
//
// One pass, because the caller that needs this is memory_list with drift asked
// for: it holds every entry already, and doing three queries per memory instead
// of three queries total is the difference between an opt-in flag and one nobody
// can afford to set.
func (d *DB) EvidencePolicies() (map[string]*EvidencePolicy, error) {
	return d.evidencePolicies("")
}

// evidencePolicies loads policies for one key, or for all of them when key is
// empty.
func (d *DB) evidencePolicies(key string) (map[string]*EvidencePolicy, error) {
	out := map[string]*EvidencePolicy{}

	where, args := "", []any(nil)
	if key != "" {
		where, args = " WHERE key = ?", []any{key}
	}

	rows, err := d.Query(`SELECT key, version, updated_by, updated_at FROM memory_evidence_policy`+where, args...)
	if err != nil {
		return nil, serr.Internalf(err, "failed to read evidence policies")
	}
	for rows.Next() {
		var p EvidencePolicy
		if err := rows.Scan(&p.Key, &p.Version, &p.UpdatedBy, &p.UpdatedAt); err != nil {
			rows.Close()
			return nil, serr.Internalf(err, "failed to read an evidence policy")
		}
		if err := canonicalStamps(p.Key, &p.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		p.Members = []EvidenceMember{}
		out[p.Key] = &p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, serr.Internalf(err, "failed to read evidence policies")
	}
	if len(out) == 0 {
		return out, nil
	}

	rows, err = d.Query(
		`SELECT key, repo_id, base_oid, captured_at FROM memory_evidence_member`+where+
			` ORDER BY key, repo_id`, args...)
	if err != nil {
		return nil, serr.Internalf(err, "failed to read evidence members")
	}
	for rows.Next() {
		var k string
		var m EvidenceMember
		if err := rows.Scan(&k, &m.RepoID, &m.BaseOID, &m.CapturedAt); err != nil {
			rows.Close()
			return nil, serr.Internalf(err, "failed to read an evidence member")
		}
		if err := canonicalStamps(k, &m.CapturedAt); err != nil {
			rows.Close()
			return nil, err
		}
		p, ok := out[k]
		if !ok {
			continue
		}
		p.Members = append(p.Members, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, serr.Internalf(err, "failed to read evidence members")
	}

	rows, err = d.Query(
		`SELECT key, repo_id, kind, pattern FROM memory_evidence_path`+where+
			` ORDER BY key, repo_id, kind, pattern`, args...)
	if err != nil {
		return nil, serr.Internalf(err, "failed to read evidence paths")
	}
	defer rows.Close()
	// Collected into a map first, then attached below. Holding a *EvidenceMember
	// into a slice that is still being appended to does not work: the append
	// reallocates and every pointer taken before it silently addresses the old
	// array, so the paths land nowhere.
	byMember := map[string][]EvidencePath{}
	for rows.Next() {
		var k, repo string
		var ep EvidencePath
		if err := rows.Scan(&k, &repo, &ep.Kind, &ep.Pattern); err != nil {
			return nil, serr.Internalf(err, "failed to read an evidence path")
		}
		byMember[k+"\x00"+repo] = append(byMember[k+"\x00"+repo], ep)
	}
	if err := rows.Err(); err != nil {
		return nil, serr.Internalf(err, "failed to read evidence paths")
	}
	for key, p := range out {
		for i := range p.Members {
			p.Members[i].Paths = byMember[key+"\x00"+p.Members[i].RepoID]
		}
	}
	return out, nil
}

// SetEvidencePolicy declares or replaces a policy under dual compare-and-swap.
//
// BOTH versions are checked. The memory version matters as much as the policy
// version: without it an agent holding an older proposition could attach a
// baseline to a memory that has since been rewritten into something else, and
// the evidence would then be measuring the wrong claim while looking perfectly
// well-formed.
func (d *DB) SetEvidencePolicy(s EvidenceSet) (*EvidencePolicy, error) {
	if err := ValidateKey(s.Key); err != nil {
		return nil, err
	}
	if s.Actor == "" {
		return nil, serr.E(serr.InvalidInput, "actor must be set")
	}
	if err := validateMembers(s.Members); err != nil {
		return nil, err
	}

	tx, err := d.Begin()
	if err != nil {
		return nil, serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	cur, err := readMemoryTx(tx, s.Key)
	if errors.Is(err, ErrNotFound) {
		return nil, serr.E(serr.CASConflict,
			"memory %q does not exist, so no evidence policy can be attached to it", s.Key).With("current", nil)
	} else if err != nil {
		return nil, err
	}
	if cur.Version != s.ExpectedMemoryVersion {
		return nil, serr.E(serr.CASConflict,
			"memory %q is at version %d, not %d — it changed under you; re-read it before declaring what to observe",
			s.Key, cur.Version, s.ExpectedMemoryVersion).With("current", cur)
	}

	// Every declared repository must be a member of this project. A policy naming
	// one that is not would be accepted, stored, and then report missing forever.
	for _, m := range s.Members {
		var exists int
		err := tx.QueryRow(`SELECT 1 FROM repos WHERE repo_id = ?`, m.RepoID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			known, _ := repoIDsTx(tx)
			hint := "this project has no registered repositories yet — run `stigmergy doctor` here"
			if len(known) > 0 {
				hint = "this project's repositories are: " + strings.Join(known, ", ")
			}
			return nil, serr.E(serr.InvalidInput,
				"%q is not a repository in this project (%s)", m.RepoID, hint)
		} else if err != nil {
			return nil, serr.Internalf(err, "failed to check repository %q", m.RepoID)
		}
	}

	prev, err := policyVersionTx(tx, s.Key)
	if err != nil {
		return nil, err
	}
	switch {
	case prev == 0 && s.ExpectedPolicyVersion != nil:
		return nil, serr.E(serr.CASConflict,
			"memory %q has no evidence policy, so there is none at version %d to replace; omit expected_policy_version to declare one",
			s.Key, *s.ExpectedPolicyVersion).With("current_policy_version", nil)
	case prev != 0 && s.ExpectedPolicyVersion == nil:
		return nil, serr.E(serr.CASConflict,
			"memory %q already has an evidence policy at version %d; pass expected_policy_version=%d to replace it. Replacing RESETS the baselines, and all evidence accumulated since they were captured goes with them",
			s.Key, prev, prev).With("current_policy_version", prev)
	case prev != 0 && *s.ExpectedPolicyVersion != prev:
		return nil, serr.E(serr.CASConflict,
			"memory %q has an evidence policy at version %d, not %d — another agent changed it; re-read it and retry",
			s.Key, prev, *s.ExpectedPolicyVersion).With("current_policy_version", prev)
	}

	next := prev + 1
	now := Now()
	if _, err := tx.Exec(
		`INSERT INTO memory_evidence_policy(key, version, updated_by, updated_at) VALUES(?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET version = excluded.version, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		s.Key, next, s.Actor, now,
	); err != nil {
		return nil, serr.Internalf(err, "failed to record the evidence policy")
	}
	// Members and paths are replaced wholesale rather than diffed. The paths
	// cascade off the members, so this one delete clears both.
	if _, err := tx.Exec(`DELETE FROM memory_evidence_member WHERE key = ?`, s.Key); err != nil {
		return nil, serr.Internalf(err, "failed to clear the previous evidence members")
	}
	for _, m := range s.Members {
		if _, err := tx.Exec(
			`INSERT INTO memory_evidence_member(key, repo_id, base_oid, captured_at) VALUES(?, ?, ?, ?)`,
			s.Key, m.RepoID, m.BaseOID, now,
		); err != nil {
			return nil, serr.Internalf(err, "failed to record the baseline for %q", m.RepoID)
		}
		for _, p := range m.Paths {
			if _, err := tx.Exec(
				`INSERT OR IGNORE INTO memory_evidence_path(key, repo_id, kind, pattern) VALUES(?, ?, ?, ?)`,
				s.Key, m.RepoID, p.Kind, p.Pattern,
			); err != nil {
				return nil, serr.Internalf(err, "failed to record a path for %q", m.RepoID)
			}
		}
	}

	if err := audit(tx, AuditEntry{
		Actor: s.Actor, AgentKind: s.AgentKind, Action: "evidence_policy_set",
		Target: string(d.Kind) + ":" + s.Key,
		Detail: fmt.Sprintf("policy_version=%d memory_version=%d members=%d", next, cur.Version, len(s.Members)),
	}); err != nil {
		return nil, serr.Internalf(err, "failed to write audit record")
	}
	if err := tx.Commit(); err != nil {
		return nil, serr.Internalf(err, "failed to commit the evidence policy")
	}

	out := &EvidencePolicy{Key: s.Key, Version: next, UpdatedBy: s.Actor, UpdatedAt: now, Members: s.Members}
	for i := range out.Members {
		out.Members[i].CapturedAt = now
	}
	return out, nil
}

// ClearEvidencePolicy removes a policy under the same dual CAS.
//
// The memory version is required here for the same reason it is on set: an agent
// working from an older proposition must not be able to strip the evidence off a
// memory that has since been rewritten.
func (d *DB) ClearEvidencePolicy(key string, expectedMemoryVersion, expectedPolicyVersion int, actor, agentKind string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	if actor == "" {
		return serr.E(serr.InvalidInput, "actor must be set")
	}
	tx, err := d.Begin()
	if err != nil {
		return serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	cur, err := readMemoryTx(tx, key)
	if errors.Is(err, ErrNotFound) {
		return serr.E(serr.CASConflict,
			"memory %q does not exist", key).With("current", nil)
	} else if err != nil {
		return err
	}
	if cur.Version != expectedMemoryVersion {
		return serr.E(serr.CASConflict,
			"memory %q is at version %d, not %d — it changed under you; re-read it before removing what observes it",
			key, cur.Version, expectedMemoryVersion).With("current", cur)
	}
	prev, err := policyVersionTx(tx, key)
	if err != nil {
		return err
	}
	if prev == 0 {
		return ErrNoPolicy
	}
	if prev != expectedPolicyVersion {
		return serr.E(serr.CASConflict,
			"memory %q has an evidence policy at version %d, not %d — another agent changed it; re-read it and retry",
			key, prev, expectedPolicyVersion).With("current_policy_version", prev)
	}
	// Members and paths cascade off the policy row.
	if _, err := tx.Exec(`DELETE FROM memory_evidence_policy WHERE key = ?`, key); err != nil {
		return serr.Internalf(err, "failed to remove the evidence policy")
	}
	if err := audit(tx, AuditEntry{
		Actor: actor, AgentKind: agentKind, Action: "evidence_policy_clear",
		Target: string(d.Kind) + ":" + key,
		Detail: fmt.Sprintf("policy_version=%d memory_version=%d", prev, cur.Version),
	}); err != nil {
		return serr.Internalf(err, "failed to write audit record")
	}
	if err := tx.Commit(); err != nil {
		return serr.Internalf(err, "failed to commit the evidence policy removal")
	}
	return nil
}

// HasEvidencePolicy reports whether a memory has one, without loading it.
//
// memory_write uses this to decide whether to say anything about baselines: a
// note on every write in the system would be noise, and noise is what agents
// learn to skip.
func (d *DB) HasEvidencePolicy(key string) bool {
	var v int
	err := d.QueryRow(`SELECT version FROM memory_evidence_policy WHERE key = ?`, key).Scan(&v)
	return err == nil
}

// policyVersionTx returns the current policy version, or 0 when there is none.
func policyVersionTx(tx *sql.Tx, key string) (int, error) {
	var v int
	err := tx.QueryRow(`SELECT version FROM memory_evidence_policy WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, serr.Internalf(err, "failed to read the evidence policy version")
	}
	return v, nil
}

func repoIDsTx(tx *sql.Tx) ([]string, error) {
	rows, err := tx.Query(`SELECT repo_id FROM repos ORDER BY repo_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func validateMembers(members []EvidenceMember) error {
	if len(members) == 0 {
		return serr.E(serr.InvalidInput,
			"an evidence policy must name at least one repository to observe")
	}
	seen := map[string]bool{}
	for _, m := range members {
		if m.RepoID == "" {
			return serr.E(serr.InvalidInput, "a repository name must not be empty")
		}
		if seen[m.RepoID] {
			return serr.E(serr.InvalidInput,
				"repository %q is named twice; each repository is observed once, with one set of paths", m.RepoID)
		}
		seen[m.RepoID] = true
		if m.BaseOID == "" {
			return serr.E(serr.InvalidInput, "repository %q has no base commit", m.RepoID)
		}
		for _, p := range m.Paths {
			if err := validatePattern(p); err != nil {
				return err
			}
		}
	}
	return nil
}

// validatePattern rejects anything that could not mean a path inside the
// repository it is declared against.
//
// Escaping matters more here than it looks. A pattern is compiled into a git
// pathspec, and a pathspec that climbs out of the repository would quietly
// observe a directory the declaring agent never named — so ".." and absolute
// paths are refused outright rather than cleaned up into something plausible.
func validatePattern(p EvidencePath) error {
	if p.Kind != PathLiteral && p.Kind != PathGlob {
		return serr.E(serr.InvalidInput,
			"path kind %q is invalid: use %q or %q", p.Kind, PathLiteral, PathGlob)
	}
	if strings.TrimSpace(p.Pattern) == "" {
		return serr.E(serr.InvalidInput, "a path pattern must not be empty")
	}
	if strings.ContainsRune(p.Pattern, 0) {
		return serr.E(serr.InvalidInput, "a path pattern must not contain a NUL byte")
	}
	if strings.HasPrefix(p.Pattern, "/") {
		return serr.E(serr.InvalidInput,
			"path %q is absolute; patterns are relative to the repository root", p.Pattern)
	}
	if slices.Contains(strings.Split(p.Pattern, "/"), "..") {
		return serr.E(serr.InvalidInput,
			"path %q contains \"..\"; a pattern may not climb out of the repository it observes", p.Pattern)
	}
	// A literal is normalised so "internal//store/" and "internal/store" cannot
	// both be stored as distinct rows meaning the same directory. A glob is left
	// exactly as written: path.Clean would eat the "**" that makes it a glob.
	if p.Kind == PathLiteral && path.Clean(p.Pattern) != p.Pattern {
		return serr.E(serr.InvalidInput,
			"path %q is not normalised; write it as %q", p.Pattern, path.Clean(p.Pattern))
	}
	return nil
}
