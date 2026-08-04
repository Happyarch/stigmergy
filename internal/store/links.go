package store

import (
	"cmp"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/happyarch/stigmergy/internal/serr"
)

// The association web: an untyped, symmetric edge between two memories in the
// same scope, carrying a mandatory reason. See docs/association-model.md Part
// I §3 for why it is untyped, unweighted, and CAS-free — this file only
// implements what that document decided.
//
// The four constants below are the fan-effect counterweight: unweighted edges
// mean the structure has to be bounded instead, so it stays a labeled second
// tier rather than a dominating one. They are named, tunable constants, never
// load-bearing semantics — see docs/association-model.md §11.
const (
	// MaxLinksPerMemory is the hard cap enforced at create time, on both
	// endpoints. The error names the fan effect and suggests pruning or an
	// intermediate memory rather than a hub.
	MaxLinksPerMemory = 16
	// MaxNeighborsSurfaced caps memory_read's link list; the total is reported
	// separately when truncated.
	MaxNeighborsSurfaced = 8
	// MaxSearchNeighbors caps the neighbor stubs attached to one search hit.
	MaxSearchNeighbors = 4
	// MaxPrimingMemories caps how many memories one priming note names (Stage B).
	MaxPrimingMemories = 6
)

// Link is one stored edge, canonically ordered (KeyA < KeyB).
type Link struct {
	KeyA      string `json:"key_a"`
	KeyB      string `json:"key_b"`
	Reason    string `json:"reason"`
	CreatedBy string `json:"created_by"`
	AgentKind string `json:"agent_kind"`
	CreatedAt string `json:"created_at"`
}

// Neighbor is the other endpoint of an edge, as surfaced to a reader: enough
// to decide whether to go read it, without a second round trip.
type Neighbor struct {
	Key         string `json:"key"`
	Description string `json:"description"`
	Reason      string `json:"reason"`
	LinkedBy    string `json:"linked_by"`
	LinkedAt    string `json:"linked_at"`
}

// CreateLink asserts an association between two memories in this scope.
//
// Order does not matter to the caller: the pair is canonicalised (key_a <
// key_b, total under the key grammar) before anything is checked, so linking
// A to B and B to A are the same operation and either endpoint's read sees it.
// A duplicate pair is rejected with the existing edge attached, so the caller
// can merge reasons via DeleteLink + CreateLink rather than silently losing
// one. Both endpoints must exist, and both are checked against the fan-effect
// cap inside the same transaction as the insert.
func (d *DB) CreateLink(a, b, reason, actor, agentKind string) (Link, error) {
	if err := ValidateKey(a); err != nil {
		return Link{}, err
	}
	if err := ValidateKey(b); err != nil {
		return Link{}, err
	}
	if a == b {
		return Link{}, serr.E(serr.InvalidInput, "memory %q cannot be linked to itself", a)
	}
	if err := ValidateLine("reason", reason, MaxLineLength); err != nil {
		return Link{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return Link{}, serr.E(serr.InvalidInput,
			"reason must not be empty — it is the encoding context that makes the association meaningful, not metadata")
	}
	if actor == "" {
		return Link{}, serr.E(serr.InvalidInput, "actor must be set")
	}

	keyA, keyB := a, b
	if keyA > keyB {
		keyA, keyB = keyB, keyA
	}

	tx, err := d.Begin()
	if err != nil {
		return Link{}, serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	// Pre-checked so the error names the missing key rather than surfacing a
	// bare foreign-key failure.
	for _, k := range []string{a, b} {
		if _, err := readMemoryTx(tx, k); err != nil {
			if errors.Is(err, ErrNotFound) {
				return Link{}, serr.E(serr.InvalidInput,
					"memory %q does not exist, so it cannot be linked", k)
			}
			return Link{}, err
		}
	}

	switch existing, err := scanLinkTx(tx, keyA, keyB); {
	case err == nil:
		return Link{}, serr.E(serr.CASConflict,
			"%q and %q are already linked (%q); call memory_unlink first if you want to replace the reason",
			keyA, keyB, existing.Reason).With("current", existing)
	case !errors.Is(err, sql.ErrNoRows):
		return Link{}, err
	}

	for _, k := range []string{keyA, keyB} {
		var count int
		if err := tx.QueryRow(
			`SELECT count(*) FROM memory_links WHERE key_a = ? OR key_b = ?`, k, k,
		).Scan(&count); err != nil {
			return Link{}, serr.Internalf(err, "failed to count links on %q", k)
		}
		if count >= MaxLinksPerMemory {
			return Link{}, serr.E(serr.InvalidInput,
				"%q already has %d links, the fan-effect cap — a memory linked to everything primes nothing. "+
					"Prune an existing link with memory_unlink, or introduce an intermediate memory instead of a hub",
				k, MaxLinksPerMemory)
		}
	}

	now := Now()
	if _, err := tx.Exec(
		`INSERT INTO memory_links(key_a, key_b, reason, created_by, agent_kind, created_at) VALUES(?, ?, ?, ?, ?, ?)`,
		keyA, keyB, reason, actor, agentKind, now,
	); err != nil {
		return Link{}, serr.Internalf(err, "failed to create the link")
	}
	if err := audit(tx, AuditEntry{
		Actor: actor, AgentKind: agentKind, Action: "memory_link",
		Target: string(d.Kind) + ":" + keyA + "<->" + keyB,
		Detail: reason,
	}); err != nil {
		return Link{}, serr.Internalf(err, "failed to write audit record")
	}
	if err := tx.Commit(); err != nil {
		return Link{}, serr.Internalf(err, "failed to commit the link")
	}
	return Link{KeyA: keyA, KeyB: keyB, Reason: reason, CreatedBy: actor, AgentKind: agentKind, CreatedAt: now}, nil
}

// DeleteLink severs an association. An absent pair is reported as (false,
// nil), not an error: unlinking something already gone is not a fault.
func (d *DB) DeleteLink(a, b, actor, agentKind string) (bool, error) {
	if err := ValidateKey(a); err != nil {
		return false, err
	}
	if err := ValidateKey(b); err != nil {
		return false, err
	}
	if actor == "" {
		return false, serr.E(serr.InvalidInput, "actor must be set")
	}
	keyA, keyB := a, b
	if keyA > keyB {
		keyA, keyB = keyB, keyA
	}

	tx, err := d.Begin()
	if err != nil {
		return false, serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	existing, err := scanLinkTx(tx, keyA, keyB)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}

	if _, err := tx.Exec(`DELETE FROM memory_links WHERE key_a = ? AND key_b = ?`, keyA, keyB); err != nil {
		return false, serr.Internalf(err, "failed to remove the link")
	}
	if err := audit(tx, AuditEntry{
		Actor: actor, AgentKind: agentKind, Action: "memory_unlink",
		Target: string(d.Kind) + ":" + keyA + "<->" + keyB,
		Detail: existing.Reason,
	}); err != nil {
		return false, serr.Internalf(err, "failed to write audit record")
	}
	if err := tx.Commit(); err != nil {
		return false, serr.Internalf(err, "failed to commit the unlink")
	}
	return true, nil
}

// severedNeighborKeysTx returns the other endpoint of every link touching
// key, sorted, for DeleteMemory to report as severed. Read inside the same
// transaction that is about to delete the memory and cascade the links away,
// so the answer reflects exactly what the delete is about to destroy.
func severedNeighborKeysTx(tx *sql.Tx, key string) ([]string, error) {
	rows, err := tx.Query(`SELECT key_a, key_b FROM memory_links WHERE key_a = ? OR key_b = ?`, key, key)
	if err != nil {
		return nil, serr.Internalf(err, "failed to read links before delete")
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			return nil, serr.Internalf(err, "failed to read a link")
		}
		if a == key {
			out = append(out, b)
		} else {
			out = append(out, a)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, serr.Internalf(err, "failed to read links before delete")
	}
	slices.Sort(out)
	return out, nil
}

func scanLinkTx(tx *sql.Tx, keyA, keyB string) (Link, error) {
	var l Link
	err := tx.QueryRow(
		`SELECT key_a, key_b, reason, created_by, agent_kind, created_at
		   FROM memory_links WHERE key_a = ? AND key_b = ?`,
		keyA, keyB,
	).Scan(&l.KeyA, &l.KeyB, &l.Reason, &l.CreatedBy, &l.AgentKind, &l.CreatedAt)
	if err != nil {
		return Link{}, err
	}
	if err := canonicalStamps(l.KeyA, &l.CreatedAt); err != nil {
		return Link{}, err
	}
	return l, nil
}

// NeighborsOf batches a link lookup over many keys into one query, joined to
// memories for descriptions and assembled in Go — the EvidencePolicies
// precedent (evidence.go). The caller that needs this (memory_read,
// memory_search, priming) always has a whole key set in hand, and one query
// per set beats one per key by exactly the amount that makes push-not-pull
// exposure (§5) affordable.
//
// A key appears in the result only when it has at least one neighbor; absent
// from the map means none, same as a normal Go zero value would suggest.
func (d *DB) NeighborsOf(keys []string) (map[string][]Neighbor, error) {
	out := map[string][]Neighbor{}
	keys = dedupeKeys(keys)
	if len(keys) == 0 {
		return out, nil
	}

	ph := placeholders(len(keys))
	args := make([]any, 0, len(keys)*2)
	for _, k := range keys {
		args = append(args, k)
	}
	for _, k := range keys {
		args = append(args, k)
	}
	rows, err := d.Query(
		`SELECT l.key_a, l.key_b, l.reason, l.created_by, l.created_at, ma.description, mb.description
		   FROM memory_links l
		   JOIN memories ma ON ma.key = l.key_a
		   JOIN memories mb ON mb.key = l.key_b
		  WHERE l.key_a IN (`+ph+`) OR l.key_b IN (`+ph+`)`, args...)
	if err != nil {
		return nil, serr.Internalf(err, "failed to read links")
	}
	defer rows.Close()

	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}

	for rows.Next() {
		var keyA, keyB, reason, createdBy, createdAt, descA, descB string
		if err := rows.Scan(&keyA, &keyB, &reason, &createdBy, &createdAt, &descA, &descB); err != nil {
			return nil, serr.Internalf(err, "failed to read a link")
		}
		if err := canonicalStamps(keyA, &createdAt); err != nil {
			return nil, err
		}
		if want[keyA] {
			out[keyA] = append(out[keyA], Neighbor{Key: keyB, Description: descB, Reason: reason, LinkedBy: createdBy, LinkedAt: createdAt})
		}
		if want[keyB] {
			out[keyB] = append(out[keyB], Neighbor{Key: keyA, Description: descA, Reason: reason, LinkedBy: createdBy, LinkedAt: createdAt})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, serr.Internalf(err, "failed to read links")
	}
	for k := range out {
		slices.SortFunc(out[k], func(a, b Neighbor) int { return cmp.Compare(a.Key, b.Key) })
	}
	return out, nil
}

// LinkCounts batches a link-count lookup the same way NeighborsOf does, for
// memory_list's link_count — a structure signal, no bodies, no reasons.
func (d *DB) LinkCounts(keys []string) (map[string]int, error) {
	out := map[string]int{}
	keys = dedupeKeys(keys)
	if len(keys) == 0 {
		return out, nil
	}

	ph := placeholders(len(keys))
	args := make([]any, 0, len(keys)*2)
	for _, k := range keys {
		args = append(args, k)
	}
	for _, k := range keys {
		args = append(args, k)
	}
	rows, err := d.Query(
		`SELECT key_a, key_b FROM memory_links WHERE key_a IN (`+ph+`) OR key_b IN (`+ph+`)`, args...)
	if err != nil {
		return nil, serr.Internalf(err, "failed to count links")
	}
	defer rows.Close()

	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			return nil, serr.Internalf(err, "failed to read a link")
		}
		if want[a] {
			out[a]++
		}
		if want[b] {
			out[b]++
		}
	}
	return out, rows.Err()
}

// placeholders returns "?,?,...", n times, for splicing into an IN clause.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// dedupeKeys drops duplicates and empty strings, so a caller that hands in
// the same key twice (e.g. a search-hit key that is also a priming cue)
// builds one placeholder for it, not two.
func dedupeKeys(keys []string) []string {
	seen := make(map[string]bool, len(keys))
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}
