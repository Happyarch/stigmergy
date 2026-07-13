package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/happyarch/stigmergy/internal/serr"
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
	return &m, nil
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

// ListMemories returns every entry as a bodyless index, key-ordered.
func (d *DB) ListMemories() ([]IndexEntry, error) {
	rows, err := d.Query(`SELECT key, type, description, version, updated_at FROM memories ORDER BY key`)
	if err != nil {
		return nil, serr.Internalf(err, "failed to list memories")
	}
	defer rows.Close()
	return scanIndex(rows)
}

func scanIndex(rows *sql.Rows) ([]IndexEntry, error) {
	out := []IndexEntry{}
	for rows.Next() {
		var e IndexEntry
		if err := rows.Scan(&e.Key, &e.Type, &e.Description, &e.Version, &e.UpdatedAt); err != nil {
			return nil, serr.Internalf(err, "failed to read memory index")
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, serr.Internalf(err, "failed to read memory index")
	}
	return out, nil
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
	rows, err := d.Query(
		`SELECT m.key, m.type, m.description, m.version,
		        snippet(memories_fts, 2, '[', ']', ' … ', 24)
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
		if err := rows.Scan(&h.Key, &h.Type, &h.Description, &h.Version, &h.Snippet); err != nil {
			return nil, serr.Internalf(err, "failed to read search results")
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
func (d *DB) DeleteMemory(key string, expectedVersion int, actor, kind string) (*Memory, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	if actor == "" {
		return nil, serr.E(serr.InvalidInput, "actor must be set")
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	cur, err := readMemoryTx(tx, key)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	if err := casCheck(cur, &expectedVersion, key); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM memories WHERE key = ? AND version = ?`, key, expectedVersion); err != nil {
		return nil, serr.Internalf(err, "failed to delete memory")
	}
	if err := audit(tx, AuditEntry{
		Actor: actor, AgentKind: kind, Action: "memory_delete",
		Target: string(d.Kind) + ":" + key,
		Detail: fmt.Sprintf("version=%d body_sha256=%s", cur.Version, BodyHash(cur.Body)),
	}); err != nil {
		return nil, serr.Internalf(err, "failed to write audit record")
	}
	if err := tx.Commit(); err != nil {
		return nil, serr.Internalf(err, "failed to commit memory delete")
	}
	return cur, nil
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
