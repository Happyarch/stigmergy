package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/happyarch/stigmergy/internal/serr"
)

// Verification outcomes. The vocabulary is closed and small on purpose: three
// things an agent can have concluded, each of which means something different to
// anyone reading the history later.
const (
	// Reaffirmed: checked, still true, unchanged. The only outcome that
	// constitutes an assertion that the proposition currently holds.
	Reaffirmed = "reaffirmed"
	// Revised: was wrong or incomplete, and has been corrected. The memory was
	// edited; this records that the edit was a correction rather than a tidy-up.
	Revised = "revised"
	// Refuted: no longer true, and not fixable by editing — the memory should
	// probably go. Recording this does NOT delete anything (see gc.go:23); it
	// says what was found, and a human or agent decides what to do about it.
	Refuted = "refuted"
)

// ValidOutcomes is the closed set the schema CHECK enforces.
var ValidOutcomes = []string{Reaffirmed, Revised, Refuted}

// Verification is one recorded judgement.
type Verification struct {
	ID      int64  `json:"id"`
	Key     string `json:"key"`
	Outcome string `json:"outcome"`
	// MemoryVersion is the version that was assessed, so an outcome can never be
	// read as applying to text its author never saw.
	MemoryVersion int    `json:"memory_version"`
	PolicyVersion *int   `json:"policy_version,omitempty"`
	Evidence      string `json:"evidence,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Actor         string `json:"actor"`
	AgentKind     string `json:"agent_kind"`
	At            string `json:"at"`
}

// VerificationRecord is one judgement to store.
type VerificationRecord struct {
	Key     string
	Outcome string
	// ExpectedMemoryVersion must be the current version. This is a guard rather
	// than a compare-and-swap — nothing is being overwritten — but it is the same
	// idea and it matters more here than almost anywhere: an outcome recorded
	// against a version the agent never read is a judgement of something else.
	ExpectedMemoryVersion int
	PolicyVersion         *int
	Evidence              string
	Reason                string
	Actor                 string
	AgentKind             string
}

// RecordVerification appends one explicit judgement.
//
// Append-only. There is no update and no delete: a verification is a statement
// somebody made at a moment, and a history that can be edited afterwards is not
// evidence of anything. Rows leave only with the memory they belong to.
func (d *DB) RecordVerification(r VerificationRecord) (*Verification, error) {
	if err := ValidateKey(r.Key); err != nil {
		return nil, err
	}
	if err := ValidateOutcome(r.Outcome); err != nil {
		return nil, err
	}
	if r.Actor == "" {
		return nil, serr.E(serr.InvalidInput, "actor must be set")
	}
	// The reason is the part of a verification anyone reads later — it is what
	// makes the record worth having — so it goes through the same rules as a
	// memory body. Multi-line, because explaining what you checked often is.
	r.Reason = NormalizeText(r.Reason)
	if err := ValidateBlock("reason", r.Reason); err != nil {
		return nil, err
	}

	tx, err := d.Begin()
	if err != nil {
		return nil, serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	cur, err := readMemoryTx(tx, r.Key)
	if errors.Is(err, ErrNotFound) {
		return nil, serr.E(serr.CASConflict,
			"memory %q does not exist, so there is nothing to have checked", r.Key).With("current", nil)
	} else if err != nil {
		return nil, err
	}
	if cur.Version != r.ExpectedMemoryVersion {
		return nil, serr.E(serr.CASConflict,
			"memory %q is at version %d, not %d — it changed while you were assessing it. Re-read it: the version you judged is not the one that is there now",
			r.Key, cur.Version, r.ExpectedMemoryVersion).With("current", cur)
	}

	now := Now()
	res, err := tx.Exec(
		`INSERT INTO memory_verification(key, outcome, memory_version, policy_version, evidence, reason, actor, agent_kind, at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Key, r.Outcome, cur.Version, r.PolicyVersion, nullIfEmpty(r.Evidence), nullIfEmpty(r.Reason),
		r.Actor, r.AgentKind, now,
	)
	if err != nil {
		return nil, serr.Internalf(err, "failed to record the verification")
	}
	id, _ := res.LastInsertId()

	if err := audit(tx, AuditEntry{
		Actor: r.Actor, AgentKind: r.AgentKind, Action: "memory_verify",
		Target: string(d.Kind) + ":" + r.Key,
		Detail: fmt.Sprintf("outcome=%s memory_version=%d", r.Outcome, cur.Version),
	}); err != nil {
		return nil, serr.Internalf(err, "failed to write audit record")
	}
	if err := tx.Commit(); err != nil {
		return nil, serr.Internalf(err, "failed to commit the verification")
	}

	return &Verification{
		ID: id, Key: r.Key, Outcome: r.Outcome, MemoryVersion: cur.Version,
		PolicyVersion: r.PolicyVersion, Evidence: r.Evidence, Reason: r.Reason,
		Actor: r.Actor, AgentKind: r.AgentKind, At: now,
	}, nil
}

// VerificationHistory returns one memory's judgements, oldest first.
//
// Oldest first because the interesting shape is the sequence — reaffirmed,
// reaffirmed, revised — and a reversed list makes that read backwards.
func (d *DB) VerificationHistory(key string) ([]Verification, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	rows, err := d.Query(
		`SELECT id, key, outcome, memory_version, policy_version, evidence, reason, actor, agent_kind, at
		   FROM memory_verification WHERE key = ? ORDER BY at, id`, key)
	if err != nil {
		return nil, serr.Internalf(err, "failed to read the verification history")
	}
	defer rows.Close()
	return scanVerifications(rows)
}

// VerificationSummary is the compact per-memory view for an index.
//
// Deliberately NOT a score and not a ranking. Counts and a last outcome are
// facts; anything derived from them is a Stage 3 question that this history has
// to get long enough to answer first.
type VerificationSummary struct {
	// Last is the most recent judgement, or nil if nobody has ever checked.
	Last *Verification `json:"last,omitempty"`
	// Counts is keyed by outcome. Absent outcomes are absent, not zero.
	Counts map[string]int `json:"counts,omitempty"`
	// Total is every judgement ever recorded for this memory.
	Total int `json:"total"`
}

// VerificationSummaries returns the compact view for every memory that has any
// history, in one pass — the same reasoning as EvidencePolicies: a per-memory
// query would make the flag too expensive to set.
func (d *DB) VerificationSummaries() (map[string]*VerificationSummary, error) {
	out := map[string]*VerificationSummary{}

	rows, err := d.Query(`SELECT key, outcome, count(*) FROM memory_verification GROUP BY key, outcome`)
	if err != nil {
		return nil, serr.Internalf(err, "failed to summarise verifications")
	}
	for rows.Next() {
		var key, outcome string
		var n int
		if err := rows.Scan(&key, &outcome, &n); err != nil {
			rows.Close()
			return nil, serr.Internalf(err, "failed to summarise verifications")
		}
		s, ok := out[key]
		if !ok {
			s = &VerificationSummary{Counts: map[string]int{}}
			out[key] = s
		}
		s.Counts[outcome] = n
		s.Total += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, serr.Internalf(err, "failed to summarise verifications")
	}
	if len(out) == 0 {
		return out, nil
	}

	// The latest row per key. Ordering by (at, id) and taking the last one keeps
	// this correct when several land in the same nanosecond, which they do when
	// an upkeep pass records a batch.
	rows, err = d.Query(
		`SELECT id, key, outcome, memory_version, policy_version, evidence, reason, actor, agent_kind, at
		   FROM memory_verification ORDER BY key, at, id`)
	if err != nil {
		return nil, serr.Internalf(err, "failed to read the latest verifications")
	}
	defer rows.Close()
	all, err := scanVerifications(rows)
	if err != nil {
		return nil, err
	}
	for i := range all {
		v := all[i]
		if s, ok := out[v.Key]; ok {
			s.Last = &v
		}
	}
	return out, nil
}

func scanVerifications(rows *sql.Rows) ([]Verification, error) {
	out := []Verification{}
	for rows.Next() {
		var v Verification
		var policy sql.NullInt64
		var evidence, reason sql.NullString
		if err := rows.Scan(&v.ID, &v.Key, &v.Outcome, &v.MemoryVersion, &policy,
			&evidence, &reason, &v.Actor, &v.AgentKind, &v.At); err != nil {
			return nil, serr.Internalf(err, "failed to read a verification")
		}
		if policy.Valid {
			n := int(policy.Int64)
			v.PolicyVersion = &n
		}
		v.Evidence, v.Reason = evidence.String, reason.String
		if err := canonicalStamps(v.Key, &v.At); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, serr.Internalf(err, "failed to read verifications")
	}
	return out, nil
}

// ValidateOutcome enforces the outcome vocabulary.
func ValidateOutcome(outcome string) error {
	for _, v := range ValidOutcomes {
		if outcome == v {
			return nil
		}
	}
	return serr.E(serr.InvalidInput,
		"outcome %q is invalid: use %q (checked, still true), %q (was wrong, now corrected), or %q (no longer true)",
		outcome, Reaffirmed, Revised, Refuted)
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
