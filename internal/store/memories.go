package store

import (
	"cmp"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/happyarch/stigmergy/internal/serr"
	"github.com/happyarch/stigmergy/internal/syncx"
)

// ErrNotFound reports a missing memory. It is deliberately not a serr code:
// callers render it as a normal "found: false" result rather than an error,
// because asking for a memory that does not exist is routine, not a fault.
var ErrNotFound = errors.New("store: memory not found")

// MaxSearchHits caps hits per scope, per the pinned protocol decisions.
const MaxSearchHits = 20

var keyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,127}$`)

// ValidTypes is the closed set enforced by the schema CHECK constraint.
var ValidTypes = []string{"user", "feedback", "project", "reference"}

// Memory is a full memory entry, bodies included.
type Memory struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Version     int    `json:"version"`
	UpdatedBy   string `json:"updated_by"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// IndexEntry is the bodyless projection returned by memory_list and by write
// suggestions. Agents get enough to decide what to read without paying for
// every body in context.
type IndexEntry struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Version     int    `json:"version"`
	UpdatedAt   string `json:"updated_at"`
}

// SearchHit is one FTS result, tagged with the scope it came from.
type SearchHit struct {
	Scope       string `json:"scope"`
	Key         string `json:"key"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Version     int    `json:"version"`
	Snippet     string `json:"snippet"`
	// UpdatedAt is when this memory was last MUTATED — not when anyone last
	// checked that it is still true. stigmergy has never recorded an assertion
	// time, and a write is not proof of verification: a typo fix moves this
	// forward exactly as far as a full rewrite does. It is reported alongside a
	// hit and never folded into the ranking, which stays bm25.
	UpdatedAt string `json:"updated_at"`
}

// ValidateKey enforces the key grammar shared by both scopes.
func ValidateKey(key string) error {
	if !keyRe.MatchString(key) {
		return serr.E(serr.InvalidInput,
			"key %q is invalid: use lowercase letters, digits and hyphens, starting with a letter or digit (max 128 chars)", key)
	}
	return nil
}

// ValidateType enforces the memory type enum.
func ValidateType(t string) error {
	for _, v := range ValidTypes {
		if t == v {
			return nil
		}
	}
	return serr.E(serr.InvalidInput, "type %q is invalid: must be one of %s", t, strings.Join(ValidTypes, ", "))
}

const memoryCols = `key, type, description, body, version, updated_by, created_at, updated_at`

func scanMemory(row interface{ Scan(...any) error }) (*Memory, error) {
	var m Memory
	err := row.Scan(&m.Key, &m.Type, &m.Description, &m.Body, &m.Version, &m.UpdatedBy, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, serr.Internalf(err, "failed to read memory")
	}
	if err := canonicalStamps(m.Key, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	return &m, nil
}

// canonicalStamps rewrites scanned timestamps in place, or reports the row as
// corrupt. Every projection that returns a timestamp goes through here, so no
// caller has to wonder whether what it was handed is comparable.
func canonicalStamps(key string, stamps ...*string) error {
	for _, p := range stamps {
		c, err := CanonicalStamp(*p)
		if err != nil {
			return serr.E(serr.Internal,
				"memory %q has an unreadable timestamp: %v — run `stigmergy doctor`, which reports it", key, err)
		}
		*p = c
	}
	return nil
}

// ReadMemory returns one entry, or ErrNotFound.
func (d *DB) ReadMemory(key string) (*Memory, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	return scanMemory(d.QueryRow(`SELECT `+memoryCols+` FROM memories WHERE key = ?`, key))
}

func readMemoryTx(tx *sql.Tx, key string) (*Memory, error) {
	return scanMemory(tx.QueryRow(`SELECT `+memoryCols+` FROM memories WHERE key = ?`, key))
}

// Memory index orderings. OrderByKey is the default because it is stable and
// says nothing: an index is for finding out what exists.
const (
	OrderByKey    = "key"
	OrderByRecent = "recent"
)

// MemoryQuery selects and orders the memory index.
//
// The bounds are on last-mutation time, which is the only time this schema has
// ever recorded. That makes this a way to find what has and has not been touched
// lately — not a way to find what is or is not still true. Nothing here ranks,
// scores, or judges; it filters and orders, and the caller decides what that
// means.
//
// Both bounds are INCLUSIVE, and both must be RFC3339.
type MemoryQuery struct {
	UpdatedSince  string // inclusive lower bound; empty means unbounded
	UpdatedBefore string // inclusive upper bound; empty means unbounded
	OrderBy       string // "" or OrderByKey (default), or OrderByRecent
}

func (q MemoryQuery) normalize() (MemoryQuery, error) {
	out := MemoryQuery{OrderBy: q.OrderBy}
	if out.OrderBy == "" {
		out.OrderBy = OrderByKey
	}
	if out.OrderBy != OrderByKey && out.OrderBy != OrderByRecent {
		return out, serr.E(serr.InvalidInput,
			"order_by %q is invalid: use %q (default) or %q", q.OrderBy, OrderByKey, OrderByRecent)
	}
	for _, b := range []struct {
		name string
		in   string
		out  *string
	}{
		{"updated_since", q.UpdatedSince, &out.UpdatedSince},
		{"updated_before", q.UpdatedBefore, &out.UpdatedBefore},
	} {
		if b.in == "" {
			continue
		}
		c, err := CanonicalStamp(b.in)
		if err != nil {
			// Rejected rather than ignored: a bound nobody could parse, silently
			// dropped, returns a result set that looks answered and is not.
			return out, serr.E(serr.InvalidInput,
				"%s %q is not a timestamp: use RFC3339, e.g. \"2026-07-01T00:00:00Z\"", b.name, b.in)
		}
		*b.out = c
	}
	if out.UpdatedSince != "" && out.UpdatedBefore != "" && out.UpdatedSince > out.UpdatedBefore {
		return out, serr.E(serr.InvalidInput,
			"updated_since (%s) is after updated_before (%s), so nothing can match", q.UpdatedSince, q.UpdatedBefore)
	}
	return out, nil
}

// QueryMemories returns the bodyless index, filtered and ordered.
//
// Filtering and sorting happen in Go, permanently and deliberately. `WHERE
// updated_at >= ?` and `ORDER BY updated_at` compare raw TEXT, and the stored
// text is not uniformly wide — so SQLite would exclude or misorder a
// non-canonical row BEFORE Go ever got the chance to canonicalise it. Tolerant
// parsing on the way out cannot undo a decision SQLite already made.
//
// `stigmergy doctor` repairs those rows, which should make the clean case the
// common one. It must never become a correctness precondition: an unrepaired
// database still has to sort correctly. The cost of doing it here is nil —
// ListMemories has always fetched every row with no LIMIT, and memory sets are
// measured in dozens.
func (d *DB) QueryMemories(q MemoryQuery) ([]IndexEntry, error) {
	q, err := q.normalize()
	if err != nil {
		return nil, err
	}
	rows, err := d.Query(`SELECT key, type, description, version, updated_at FROM memories`)
	if err != nil {
		return nil, serr.Internalf(err, "failed to list memories")
	}
	defer rows.Close()
	entries, err := scanIndex(rows)
	if err != nil {
		return nil, err
	}

	if q.UpdatedSince != "" || q.UpdatedBefore != "" {
		kept := entries[:0]
		for _, e := range entries {
			if q.UpdatedSince != "" && e.UpdatedAt < q.UpdatedSince {
				continue
			}
			if q.UpdatedBefore != "" && e.UpdatedAt > q.UpdatedBefore {
				continue
			}
			kept = append(kept, e)
		}
		entries = kept
	}

	// The key tie-break is not cosmetic: memories written in one call share a
	// timestamp to the nanosecond, and an unstable order among them makes a
	// paging upkeep routine skip entries it has not seen.
	slices.SortFunc(entries, func(a, b IndexEntry) int {
		if q.OrderBy == OrderByRecent {
			if c := cmp.Compare(b.UpdatedAt, a.UpdatedAt); c != 0 {
				return c
			}
		}
		return cmp.Compare(a.Key, b.Key)
	})
	return entries, nil
}

// ListMemories returns every entry as a bodyless index, key-ordered.
func (d *DB) ListMemories() ([]IndexEntry, error) {
	return d.QueryMemories(MemoryQuery{})
}

func scanIndex(rows *sql.Rows) ([]IndexEntry, error) {
	out := []IndexEntry{}
	for rows.Next() {
		var e IndexEntry
		if err := rows.Scan(&e.Key, &e.Type, &e.Description, &e.Version, &e.UpdatedAt); err != nil {
			return nil, serr.Internalf(err, "failed to read memory index")
		}
		if err := canonicalStamps(e.Key, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, serr.Internalf(err, "failed to read memory index")
	}
	return out, nil
}

// TimestampRepair reports what a repair pass did.
type TimestampRepair struct {
	Repaired int      // rows rewritten into canonical form
	Bad      []string // keys whose timestamps could not be parsed, left untouched
}

// stampColumns are the timestamp columns outside `memories`, and the reason this
// list exists rather than only the memory one.
//
// Every one of these is compared as raw TEXT in SQL, which is only correct while
// every value is the same fixed width — that is what TimeLayout is for. A single
// short-form value skews its comparisons: "…:00Z" sorts ABOVE "…:00.000000000Z"
// because 'Z' beats '.', so a row can read as newer than rows written after it.
//
// For most of these the consequence is cosmetic. For two it is not:
//
//	claims.expires_at    decides whether a claim still binds. A value that
//	                     compares as newer than it is means a claim that never
//	                     expires and goes on blocking every other agent.
//	roots.last_seen_at   decides whether a root is alive, and therefore whether
//	                     its claims are swept and whether it can be written to.
//
// Nothing writes a non-canonical stamp today — every path goes through Now() —
// so this is a repair for what older versions and outside writers left behind,
// and a backstop if a future one regresses. One such value exists on this
// machine, in the global audit log.
var stampColumns = []struct{ table, key, column string }{
	{"audit_log", "id", "at"},
	{"claims", "id", "created_at"},
	{"claims", "id", "expires_at"},
	{"claims", "id", "released_at"},
	{"roots", "root_id", "registered_at"},
	{"roots", "root_id", "last_seen_at"},
	{"roots", "root_id", "ended_at"},
	{"mailbox_messages", "id", "sent_at"},
	{"mailbox_messages", "id", "read_at"},
	{"mailbox_messages", "id", "notified_at"},
	{"mailbox_threads", "id", "created_at"},
	{"mailbox_threads", "id", "updated_at"},
	// memory_links and priming_delivered have composite primary keys, not a
	// single equality column repairColumn's UPDATE...WHERE key=? can target —
	// rowid is the implicit single column every non-WITHOUT-ROWID table has.
	{"memory_links", "rowid", "created_at"},
	{"priming_delivered", "rowid", "at"},
	// episodes.at decides GC retention (docs/association-model.md §10.3) by
	// comparing this column as raw TEXT — the same short-form-vs-canonical
	// skew this whole list exists to repair, and here it can silently exempt
	// an episode from pruning instead of just sorting wrong.
	{"episodes", "id", "at"},
}

// RepairStampColumns canonicalises timestamps outside the memory tables.
//
// Deliberately separate from RepairMemoryTimestamps: a memory is content an
// agent wrote and is reported by key, while these are the system's own
// bookkeeping and are reported only as a count. The repair is the same lossless
// rewrite either way — the instant is preserved exactly, only its spelling
// changes — and it touches nothing else about the row.
//
// A column the schema does not have is skipped rather than failing: this runs
// against the global database too, which has only some of these tables.
func (d *DB) RepairStampColumns() (TimestampRepair, error) {
	var rep TimestampRepair
	for _, c := range stampColumns {
		n, err := d.repairColumn(c.table, c.key, c.column)
		if err != nil {
			return rep, err
		}
		rep.Repaired += n
	}
	return rep, nil
}

func (d *DB) repairColumn(table, key, column string) (int, error) {
	rows, err := d.Query(fmt.Sprintf(
		`SELECT %s, %s FROM %s WHERE %s IS NOT NULL AND %s != ''`, key, column, table, column, column))
	if err != nil {
		// Almost always "no such table" in the scope that does not have it.
		return 0, nil
	}
	type fix struct {
		id    any
		value string
	}
	var fixes []fix
	for rows.Next() {
		var id any
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return 0, serr.Internalf(err, "failed to read %s.%s", table, column)
		}
		canonical, err := CanonicalStamp(raw)
		if err != nil {
			// Unreadable: left alone, exactly as for memories. There is no safe
			// instant to invent, and doctor reports the count.
			continue
		}
		if canonical != raw {
			fixes = append(fixes, fix{id, canonical})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, serr.Internalf(err, "failed to read %s.%s", table, column)
	}
	if len(fixes) == 0 {
		return 0, nil
	}

	tx, err := d.Begin()
	if err != nil {
		return 0, serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()
	for _, f := range fixes {
		if _, err := tx.Exec(fmt.Sprintf(
			`UPDATE %s SET %s = ? WHERE %s = ?`, table, column, key), f.value, f.id); err != nil {
			return 0, serr.Internalf(err, "failed to repair %s.%s", table, column)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, serr.Internalf(err, "failed to commit %s.%s repairs", table, column)
	}
	return len(fixes), nil
}

// RepairMemoryTimestamps rewrites parseable non-canonical timestamps in place.
//
// It is lossless — the instant is preserved exactly, only its rendering
// changes — and it touches neither version, updated_by, nor any content. A
// repair is not a write in the sense the rest of this file means it: nobody
// asserted anything, so nothing about the memory's own history moves.
//
// This lives in doctor rather than in a migration for two reasons. The known bad
// row is in the GLOBAL database, and a project migration cannot reach it. And
// robust RFC3339/RFC3339Nano parsing with UTC and nanosecond normalisation is
// not something static embedded SQL does well.
//
// Unparseable values are counted and returned, never guessed at. There is no
// safe default instant for a timestamp nobody can read.
func (d *DB) RepairMemoryTimestamps() (TimestampRepair, error) {
	var rep TimestampRepair
	rows, err := d.Query(`SELECT key, created_at, updated_at FROM memories`)
	if err != nil {
		return rep, serr.Internalf(err, "failed to read memory timestamps")
	}
	type fix struct{ key, created, updated string }
	var fixes []fix
	for rows.Next() {
		var f fix
		if err := rows.Scan(&f.key, &f.created, &f.updated); err != nil {
			rows.Close()
			return rep, serr.Internalf(err, "failed to read memory timestamps")
		}
		created, cerr := CanonicalStamp(f.created)
		updated, uerr := CanonicalStamp(f.updated)
		if cerr != nil || uerr != nil {
			rep.Bad = append(rep.Bad, f.key)
			continue
		}
		if created != f.created || updated != f.updated {
			fixes = append(fixes, fix{f.key, created, updated})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return rep, serr.Internalf(err, "failed to read memory timestamps")
	}
	rows.Close()
	if len(fixes) == 0 {
		return rep, nil
	}

	tx, err := d.Begin()
	if err != nil {
		return rep, serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()
	for _, f := range fixes {
		if _, err := tx.Exec(
			`UPDATE memories SET created_at = ?, updated_at = ? WHERE key = ?`,
			f.created, f.updated, f.key,
		); err != nil {
			return rep, serr.Internalf(err, "failed to repair the timestamps on memory %q", f.key)
		}
		rep.Repaired++
	}
	if err := tx.Commit(); err != nil {
		return rep, serr.Internalf(err, "failed to commit timestamp repairs")
	}
	return rep, nil
}

// SearchMemories runs an FTS5 query, best match first (bm25), capped at
// MaxSearchHits. An empty query matches nothing rather than everything.
func (d *DB) SearchMemories(query string) ([]SearchHit, error) {
	return d.searchWith(FTSQuery(query))
}

// SuggestSimilar backs the create-path suggestions on memory_write: it ORs the
// tokens so a near-miss key ("build-system" vs "build-setup") still surfaces,
// where the AND-joined search query would return nothing.
func (d *DB) SuggestSimilar(text string, limit int) ([]IndexEntry, error) {
	match := FTSQueryAny(text)
	if match == "" {
		return []IndexEntry{}, nil
	}
	rows, err := d.Query(
		`SELECT m.key, m.type, m.description, m.version, m.updated_at
		   FROM memories_fts f JOIN memories m ON m.rowid = f.rowid
		  WHERE memories_fts MATCH ?
		  ORDER BY bm25(memories_fts) LIMIT ?`, match, limit)
	if err != nil {
		// A malformed MATCH must not block a legitimate write; suggestions are
		// a convenience, so degrade to none.
		return []IndexEntry{}, nil
	}
	defer rows.Close()
	return scanIndex(rows)
}

func (d *DB) searchWith(match string) ([]SearchHit, error) {
	if match == "" {
		return []SearchHit{}, nil
	}
	// snippet()'s second argument is a column index into memories_fts, NOT into
	// this SELECT list, so adding a column here cannot move it. The virtual
	// table's own column list is the thing that must not change.
	rows, err := d.Query(
		`SELECT m.key, m.type, m.description, m.version,
		        snippet(memories_fts, 2, '[', ']', ' … ', 24), m.updated_at
		   FROM memories_fts f JOIN memories m ON m.rowid = f.rowid
		  WHERE memories_fts MATCH ?
		  ORDER BY bm25(memories_fts) LIMIT ?`, match, MaxSearchHits)
	if err != nil {
		return nil, serr.E(serr.UnsupportedSearch,
			"search query could not be evaluated: %v", err).Wrap(err)
	}
	defer rows.Close()

	out := []SearchHit{}
	for rows.Next() {
		h := SearchHit{Scope: string(d.Kind)}
		if err := rows.Scan(&h.Key, &h.Type, &h.Description, &h.Version, &h.Snippet, &h.UpdatedAt); err != nil {
			return nil, serr.Internalf(err, "failed to read search results")
		}
		if err := canonicalStamps(h.Key, &h.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, serr.Internalf(err, "failed to read search results")
	}
	return out, nil
}

// MemoryWrite is one compare-and-swap write.
//
// ExpectedVersion nil means "create, and fail if the key already exists".
// A non-nil value means "update, and fail unless this is the current version".
// There is no unconditional overwrite: every clobber is either intentional or
// reported, which is what keeps two roots from silently overwriting each other.
type MemoryWrite struct {
	Key             string
	Type            string
	Description     string
	Body            string
	UpdatedBy       string
	ExpectedVersion *int
}

// normalize cleans the unambiguous formatting problems before anything is
// checked or stored: a BOM, CRLF endings, a trailing newline. See text.go for
// why these are fixed silently while anything ambiguous is refused instead.
func (w MemoryWrite) normalize() MemoryWrite {
	w.Description = NormalizeText(w.Description)
	w.Body = NormalizeText(w.Body)
	return w
}

func (w MemoryWrite) validate() error {
	if err := ValidateKey(w.Key); err != nil {
		return err
	}
	if err := ValidateType(w.Type); err != nil {
		return err
	}
	if strings.TrimSpace(w.Description) == "" {
		return serr.E(serr.InvalidInput, "description must not be empty: it is what other agents see when listing memories")
	}
	if strings.TrimSpace(w.Body) == "" {
		return serr.E(serr.InvalidInput, "body must not be empty")
	}
	// A memory is written once and read by everyone afterwards, so the moment to
	// refuse unreadable text is here — while the agent that produced it is still
	// holding the original and can send it again correctly.
	if err := ValidateLine("description", w.Description, MaxDescriptionLength); err != nil {
		return err
	}
	if err := ValidateBlock("body", w.Body); err != nil {
		return err
	}
	if w.UpdatedBy == "" {
		return serr.E(serr.InvalidInput, "updated_by must be set")
	}
	if w.ExpectedVersion != nil && *w.ExpectedVersion < 1 {
		return serr.E(serr.InvalidInput, "expected_version must be at least 1, or null to create a new memory")
	}
	return nil
}

// WriteResult reports what a CAS write did.
type WriteResult struct {
	Created bool    `json:"created"`
	Memory  *Memory `json:"memory"`
}

// WriteMemory applies a CAS write. On conflict it returns a serr.CASConflict
// error carrying the current entry under the "current" context key, so the
// agent can merge and retry in one round trip.
func (d *DB) WriteMemory(w MemoryWrite, kind string) (*WriteResult, error) {
	w = w.normalize()
	if err := w.validate(); err != nil {
		return nil, err
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	cur, err := readMemoryTx(tx, w.Key)
	switch {
	case errors.Is(err, ErrNotFound):
		cur = nil
	case err != nil:
		return nil, err
	}

	if err := casCheck(cur, w.ExpectedVersion, w.Key); err != nil {
		return nil, err
	}

	now := Now()
	var res WriteResult
	if cur == nil {
		if _, err := tx.Exec(
			`INSERT INTO memories(key, type, description, body, version, updated_by, created_at, updated_at)
			 VALUES(?, ?, ?, ?, 1, ?, ?, ?)`,
			w.Key, w.Type, w.Description, w.Body, w.UpdatedBy, now, now,
		); err != nil {
			return nil, serr.Internalf(err, "failed to create memory")
		}
		res = WriteResult{Created: true, Memory: &Memory{
			Key: w.Key, Type: w.Type, Description: w.Description, Body: w.Body,
			Version: 1, UpdatedBy: w.UpdatedBy, CreatedAt: now, UpdatedAt: now,
		}}
	} else {
		next := cur.Version + 1
		if _, err := tx.Exec(
			`UPDATE memories SET type = ?, description = ?, body = ?, version = ?, updated_by = ?, updated_at = ?
			  WHERE key = ? AND version = ?`,
			w.Type, w.Description, w.Body, next, w.UpdatedBy, now, w.Key, cur.Version,
		); err != nil {
			return nil, serr.Internalf(err, "failed to update memory")
		}
		res = WriteResult{Created: false, Memory: &Memory{
			Key: w.Key, Type: w.Type, Description: w.Description, Body: w.Body,
			Version: next, UpdatedBy: w.UpdatedBy, CreatedAt: cur.CreatedAt, UpdatedAt: now,
		}}
	}

	// A memory that exists again is not a deleted one, whatever an earlier
	// delete recorded. See clearSyncTombstoneTx for what a stale tombstone
	// costs — it is silent in both directions.
	if err := clearSyncTombstoneTx(tx, "memory", w.Key); err != nil {
		return nil, err
	}

	action := "memory_update"
	if res.Created {
		action = "memory_create"
	}
	if err := audit(tx, AuditEntry{
		Actor: w.UpdatedBy, AgentKind: kind, Action: action,
		Target: string(d.Kind) + ":" + w.Key,
		Detail: fmt.Sprintf("version=%d", res.Memory.Version),
	}); err != nil {
		return nil, serr.Internalf(err, "failed to write audit record")
	}
	if err := tx.Commit(); err != nil {
		return nil, serr.Internalf(err, "failed to commit memory write")
	}
	return &res, nil
}

// casCheck is the single place the CAS matrix is decided, so create, update,
// delete and promote can never drift apart.
func casCheck(cur *Memory, expected *int, key string) error {
	switch {
	case cur == nil && expected != nil:
		return serr.E(serr.CASConflict,
			"memory %q does not exist, so it cannot be updated at version %d; write it with expected_version=null to create it",
			key, *expected).With("current", nil)
	case cur != nil && expected == nil:
		return serr.E(serr.CASConflict,
			"memory %q already exists at version %d; re-read it, merge your change, and write with expected_version=%d",
			key, cur.Version, cur.Version).With("current", cur)
	case cur != nil && *expected != cur.Version:
		return serr.E(serr.CASConflict,
			"memory %q is at version %d, not %d — another agent changed it; re-read it, merge your change, and retry",
			key, cur.Version, *expected).With("current", cur)
	}
	return nil
}

// DeleteMemory hard-deletes an entry under CAS. The exact current version must
// be named, so a delete can never race an unseen update. The audit row records
// the body hash: enough to prove what vanished, without retaining it.
//
// The second return is the keys of every memory this one was linked to —
// read inside this same transaction, before the FK cascade takes the rows
// away, so a caller can report them as SEVERED rather than silently lost.
func (d *DB) DeleteMemory(key string, expectedVersion int, actor, kind string) (*Memory, []string, error) {
	if err := ValidateKey(key); err != nil {
		return nil, nil, err
	}
	if actor == "" {
		return nil, nil, serr.E(serr.InvalidInput, "actor must be set")
	}
	// Resolved before the transaction opens, and deliberately so: LocalDeviceID
	// reads and, on first use, writes meta through d.DB directly (the pool, not
	// a transaction), and SetMaxOpenConns(1) means there is exactly one
	// connection to hand out. Calling it once BEGIN IMMEDIATE below already
	// holds that connection would not queue politely — it would deadlock, the
	// transaction waiting on a statement that is itself waiting for the
	// connection the transaction is holding.
	deviceID, err := d.LocalDeviceID()
	if err != nil {
		return nil, nil, err
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, nil, serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	cur, err := readMemoryTx(tx, key)
	if errors.Is(err, ErrNotFound) {
		return nil, nil, ErrNotFound
	} else if err != nil {
		return nil, nil, err
	}
	if err := casCheck(cur, &expectedVersion, key); err != nil {
		return nil, nil, err
	}
	severed, err := severedNeighborKeysTx(tx, key)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(`DELETE FROM memories WHERE key = ? AND version = ?`, key, expectedVersion); err != nil {
		return nil, nil, serr.Internalf(err, "failed to delete memory")
	}
	// docs/sync-model.md §3.7: a delete without a tombstone is indistinguishable
	// from a create on another machine, so the next sync would resurrect
	// exactly what was just removed. Written in the same transaction as the
	// DELETE above, the only change this design makes to an existing write
	// path. The memory_sync_base row for this key, if any, is gone already —
	// its ON DELETE CASCADE fired in the statement above — which is the pair of
	// states internal/syncx.Reconcile expects: a tombstone, and no base.
	//
	// No tombstone is written for the links severedNeighborKeysTx just read.
	// DeleteMemory already knows every neighbor the FK cascade is about to
	// take, and the memory's own tombstone covers them: a remote that still
	// holds one of those links has both its endpoints disappear the moment it
	// imports this memory's tombstone, which deletes the link locally there
	// exactly as the cascade did here. A second tombstone per severed link
	// would say the same thing twice.
	if err := putSyncTombstoneTx(tx, syncx.Tombstone{
		Kind: "memory", Ident: key,
		Digest:   syncx.Digest(cur.Key, cur.Type, cur.Description, cur.Body),
		DeviceID: deviceID, At: Now(),
	}); err != nil {
		return nil, nil, err
	}
	detail := fmt.Sprintf("version=%d body_sha256=%s", cur.Version, BodyHash(cur.Body))
	if len(severed) > 0 {
		detail += fmt.Sprintf(" severed_links=%s", strings.Join(severed, ","))
	}
	if err := audit(tx, AuditEntry{
		Actor: actor, AgentKind: kind, Action: "memory_delete",
		Target: string(d.Kind) + ":" + key,
		Detail: detail,
	}); err != nil {
		return nil, nil, serr.Internalf(err, "failed to write audit record")
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, serr.Internalf(err, "failed to commit memory delete")
	}
	return cur, severed, nil
}

// Promote copies a project memory into the global scope. It is a copy, not a
// move: the project entry stays authoritative for this repo, and the global
// one becomes visible everywhere.
//
// The two databases cannot share a transaction, so ordering carries the
// guarantee: the global write lands first, and only then is the project entry
// stamped. A failure at either step leaves the global DB as the sole mutation,
// which is idempotent under a retry — whereas the reverse order could mark a
// memory promoted that never arrived.
type Promote struct {
	Key                   string
	ExpectedVersion       int
	GlobalKey             string
	ExpectedGlobalVersion *int
	Actor                 string
	AgentKind             string
	SourceCommonDir       string
}

// PromoteResult reports the resulting global entry.
type PromoteResult struct {
	Created bool    `json:"created"`
	Global  *Memory `json:"global"`
	Source  *Memory `json:"source"`
}

// PromoteMemory performs the cross-database promotion described by Promote.
func PromoteMemory(project, global *DB, p Promote) (*PromoteResult, error) {
	if project.Kind != Project || global.Kind != Global {
		return nil, serr.E(serr.InvalidInput, "promote requires a project database and a global database")
	}
	if err := ValidateKey(p.Key); err != nil {
		return nil, err
	}
	if p.GlobalKey == "" {
		p.GlobalKey = p.Key
	}
	if err := ValidateKey(p.GlobalKey); err != nil {
		return nil, err
	}

	src, err := project.ReadMemory(p.Key)
	if errors.Is(err, ErrNotFound) {
		return nil, serr.E(serr.CASConflict,
			"project memory %q does not exist, so it cannot be promoted", p.Key).With("current", nil)
	} else if err != nil {
		return nil, err
	}
	if src.Version != p.ExpectedVersion {
		return nil, serr.E(serr.CASConflict,
			"project memory %q is at version %d, not %d — re-read it and retry",
			p.Key, src.Version, p.ExpectedVersion).With("current", src)
	}

	res, err := global.WriteMemory(MemoryWrite{
		Key:             p.GlobalKey,
		Type:            src.Type,
		Description:     src.Description,
		Body:            src.Body,
		UpdatedBy:       p.Actor,
		ExpectedVersion: p.ExpectedGlobalVersion,
	}, p.AgentKind)
	if err != nil {
		return nil, err
	}

	// Best-effort provenance in both logs. The promotion itself has already
	// succeeded; a failed audit write must not report it as failed.
	_ = global.Audit(AuditEntry{
		Actor: p.Actor, AgentKind: p.AgentKind, Action: "memory_promote",
		Target: "global:" + p.GlobalKey,
		Detail: fmt.Sprintf("from=%s project_key=%s project_version=%d", p.SourceCommonDir, p.Key, src.Version),
	})
	_ = project.Audit(AuditEntry{
		Actor: p.Actor, AgentKind: p.AgentKind, Action: "memory_promote",
		Target: "project:" + p.Key,
		Detail: fmt.Sprintf("to=global:%s global_version=%d", p.GlobalKey, res.Memory.Version),
	})

	return &PromoteResult{Created: res.Created, Global: res.Memory, Source: src}, nil
}
