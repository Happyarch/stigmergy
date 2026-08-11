package store

import (
	"cmp"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/happyarch/stigmergy/internal/ids"
	"github.com/happyarch/stigmergy/internal/serr"
	"github.com/happyarch/stigmergy/internal/syncx"
)

// Meta keys the sync CLI (internal/cli/sync.go) reads and writes. Named here,
// beside the DAO that gives them meaning, rather than in cli — the same
// reason MetaNameKey lives in repos.go rather than in the command that sets it.
//
// docs/sync-model.md §4.1 describes sync_device_id as living in the GLOBAL
// database's meta specifically, because a device is a fact about the machine,
// not about any one project. LocalDeviceID below is nonetheless a method on
// *DB rather than hard-wired to the global scope, for a reason worth stating:
// DeleteMemory and DeleteLink need SOME identity to attribute a tombstone to
// at the moment of an ordinary delete, in whichever database — project or
// global — they are running against, and a delete must not open a second
// SQLite file just to ask the other one who it is. Calling LocalDeviceID on a
// project database before `sync enable` has ever run there mints and caches a
// project-local id, independent of the machine-wide one; `sync enable`
// (internal/cli/sync.go) is what makes them agree, by copying the global
// database's value into the project's meta once a human runs it. Until then, a
// tombstone's device_id column answers "which database wrote this", which is
// enough for Stage A: nothing in Reconcile (internal/syncx/merge.go) branches
// on its value — it is provenance, the same role updated_by already plays on
// a memories row.
const (
	MetaSyncDeviceIDKey    = "sync_device_id"
	MetaSyncDeviceLabelKey = "sync_device_label"
	MetaSyncProjectKey     = "sync_project"
	MetaSyncFingerprintKey = "sync_fingerprint"
)

// LocalDeviceID returns this database's cached device identity, minting and
// storing one via ids.NewDeviceID on first use. See the block comment above
// for why this is per-database rather than always reached through the global
// database.
func (d *DB) LocalDeviceID() (string, error) {
	if id := d.Meta(MetaSyncDeviceIDKey); id != "" {
		return id, nil
	}
	id := ids.NewDeviceID()
	if err := d.SetMeta(MetaSyncDeviceIDKey, id); err != nil {
		return "", err
	}
	return id, nil
}

// MemoryImport is one memory as it arrives from another machine's sync
// export, not from this one's own agent. It differs from MemoryWrite in
// exactly the ways docs/sync-model.md §3.9 requires: both timestamps travel
// with the record instead of being stamped by Now(), and UpdatedBy is the
// "sync:<device-label>" convention (§4.4) rather than a root id — root ids do
// not travel, because there is no foreign key to catch an imported one naming
// a session that never existed here.
type MemoryImport struct {
	Key, Type, Description, Body string
	CreatedAt, UpdatedAt         string // from the wire; canonicalised on the way in
	UpdatedBy                    string // "sync:<device-label>"
	ExpectedVersion              *int
}

func (m MemoryImport) normalize() MemoryImport {
	m.Description = NormalizeText(m.Description)
	m.Body = NormalizeText(m.Body)
	return m
}

// validate mirrors MemoryWrite.validate() rather than sharing it: the two
// differ in which fields they own (a wire record supplies its own timestamps;
// an agent's write never does), and a shared helper narrow enough to serve
// both would not save more than it obscured. Both call the same ValidateKey,
// ValidateType, ValidateLine and ValidateBlock underneath, so the actual rules
// — the ones that matter — cannot drift between the two paths.
func (m MemoryImport) validate() error {
	if err := ValidateKey(m.Key); err != nil {
		return err
	}
	if err := ValidateType(m.Type); err != nil {
		return err
	}
	if strings.TrimSpace(m.Description) == "" {
		return serr.E(serr.InvalidInput, "description must not be empty: it is what other agents see when listing memories")
	}
	if strings.TrimSpace(m.Body) == "" {
		return serr.E(serr.InvalidInput, "body must not be empty")
	}
	if err := ValidateLine("description", m.Description, MaxDescriptionLength); err != nil {
		return err
	}
	if err := ValidateBlock("body", m.Body); err != nil {
		return err
	}
	if m.UpdatedBy == "" {
		return serr.E(serr.InvalidInput, "updated_by must be set")
	}
	if m.ExpectedVersion != nil && *m.ExpectedVersion < 1 {
		return serr.E(serr.InvalidInput, "expected_version must be at least 1, or null to create a new memory")
	}
	return nil
}

// ImportMemory writes a memory whose timestamps come from another machine.
//
// It shares casCheck with WriteMemory — architecture.md §5.5 permits exactly
// one place to decide the CAS matrix, and this is not a second one — and it
// writes through the memories table under the same INSERT/UPDATE shape
// WriteMemory uses, so the three memories_fts triggers fire exactly as they do
// for any other write. A sync is therefore just another writer: an agent
// holding a stale expected_version from before an import gets an ordinary
// cas_conflict after it, with the current entry attached, and re-reads — the
// behaviour it already has for a concurrent agent, reached by a different
// route.
//
// It never sets version from the wire (D2: version never travels). The
// version this row gets is whatever WriteMemory would have computed —
// cur.Version + 1, or 1 on create — because the write path underneath is
// identical, not because ImportMemory checks for it separately.
//
// created_at is preserved from the wire only on create; an update leaves the
// existing row's created_at alone, the same rule WriteMemory follows and for
// the same reason — creation time does not move when content does.
// updated_at, in contrast, always comes from the wire (canonicalised), never
// from Now(): using the import's own clock would make every synced memory's
// updated_at the moment of the sync, destroying the last-mutation ordering
// memory_list's order_by:"recent" sorts on (§3.9).
func (d *DB) ImportMemory(m MemoryImport, agentKind string) (*WriteResult, error) {
	m = m.normalize()
	if err := m.validate(); err != nil {
		return nil, err
	}
	created, err := CanonicalStamp(m.CreatedAt)
	if err != nil {
		return nil, serr.E(serr.InvalidInput, "created_at %q is not a timestamp: %v", m.CreatedAt, err)
	}
	updated, err := CanonicalStamp(m.UpdatedAt)
	if err != nil {
		return nil, serr.E(serr.InvalidInput, "updated_at %q is not a timestamp: %v", m.UpdatedAt, err)
	}

	tx, err := d.Begin()
	if err != nil {
		return nil, serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	cur, err := readMemoryTx(tx, m.Key)
	switch {
	case errors.Is(err, ErrNotFound):
		cur = nil
	case err != nil:
		return nil, err
	}

	if err := casCheck(cur, m.ExpectedVersion, m.Key); err != nil {
		return nil, err
	}

	var res WriteResult
	if cur == nil {
		if _, err := tx.Exec(
			`INSERT INTO memories(key, type, description, body, version, updated_by, created_at, updated_at)
			 VALUES(?, ?, ?, ?, 1, ?, ?, ?)`,
			m.Key, m.Type, m.Description, m.Body, m.UpdatedBy, created, updated,
		); err != nil {
			return nil, serr.Internalf(err, "failed to import memory")
		}
		res = WriteResult{Created: true, Memory: &Memory{
			Key: m.Key, Type: m.Type, Description: m.Description, Body: m.Body,
			Version: 1, UpdatedBy: m.UpdatedBy, CreatedAt: created, UpdatedAt: updated,
		}}
	} else {
		next := cur.Version + 1
		if _, err := tx.Exec(
			`UPDATE memories SET type = ?, description = ?, body = ?, version = ?, updated_by = ?, updated_at = ?
			  WHERE key = ? AND version = ?`,
			m.Type, m.Description, m.Body, next, m.UpdatedBy, updated, m.Key, cur.Version,
		); err != nil {
			return nil, serr.Internalf(err, "failed to import memory")
		}
		res = WriteResult{Created: false, Memory: &Memory{
			Key: m.Key, Type: m.Type, Description: m.Description, Body: m.Body,
			Version: next, UpdatedBy: m.UpdatedBy, CreatedAt: cur.CreatedAt, UpdatedAt: updated,
		}}
	}

	// The key exists again, so any tombstone naming it is now false.
	if err := clearSyncTombstoneTx(tx, "memory", m.Key); err != nil {
		return nil, err
	}

	action := "memory_import_update"
	if res.Created {
		action = "memory_import_create"
	}
	if err := audit(tx, AuditEntry{
		Actor: m.UpdatedBy, AgentKind: agentKind, Action: action,
		Target: string(d.Kind) + ":" + m.Key,
		Detail: fmt.Sprintf("version=%d", res.Memory.Version),
	}); err != nil {
		return nil, serr.Internalf(err, "failed to write audit record")
	}
	if err := tx.Commit(); err != nil {
		return nil, serr.Internalf(err, "failed to commit memory import")
	}
	return &res, nil
}

// SyncBases returns every recorded base: the content both machines last agreed
// on, keyed by memory key (docs/sync-model.md §3.2).
func (d *DB) SyncBases() (map[string]syncx.Base, error) {
	rows, err := d.Query(`SELECT key, digest, device_id, at FROM memory_sync_base`)
	if err != nil {
		return nil, serr.Internalf(err, "failed to read sync bases")
	}
	defer rows.Close()
	out := map[string]syncx.Base{}
	for rows.Next() {
		var b syncx.Base
		if err := rows.Scan(&b.Key, &b.Digest, &b.DeviceID, &b.At); err != nil {
			return nil, serr.Internalf(err, "failed to read a sync base")
		}
		if err := canonicalStamps(b.Key, &b.At); err != nil {
			return nil, err
		}
		out[b.Key] = b
	}
	if err := rows.Err(); err != nil {
		return nil, serr.Internalf(err, "failed to read sync bases")
	}
	return out, nil
}

// SetSyncBase records (or replaces) the base for one key, after a run has
// pushed, pulled, or agreed on its content. An agree writes only this — no
// memory content moves, because both sides already hold the same digest.
func (d *DB) SetSyncBase(key, digest, deviceID string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	if strings.TrimSpace(digest) == "" {
		return serr.E(serr.InvalidInput, "digest must not be empty")
	}
	if strings.TrimSpace(deviceID) == "" {
		return serr.E(serr.InvalidInput, "device_id must not be empty")
	}
	_, err := d.Exec(
		`INSERT INTO memory_sync_base(key, digest, device_id, at) VALUES(?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET digest = excluded.digest, device_id = excluded.device_id, at = excluded.at`,
		key, digest, deviceID, Now())
	if err != nil {
		return serr.Internalf(err, "failed to record the sync base for %q", key)
	}
	return nil
}

// DeleteSyncBase removes one key's recorded base. Ordinarily this happens for
// free — the base row's ON DELETE CASCADE fires the moment DeleteMemory
// removes the memory itself (migration 0014) — so this exists for the caller
// that needs to clear a base without deleting the memory: a conflict
// resolution that adopts one side and wants the next run to compare against
// the new content rather than the stale one.
func (d *DB) DeleteSyncBase(key string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	if _, err := d.Exec(`DELETE FROM memory_sync_base WHERE key = ?`, key); err != nil {
		return serr.Internalf(err, "failed to delete the sync base for %q", key)
	}
	return nil
}

// SyncTombstones returns every recorded tombstone, sorted by kind then ident
// so a caller building a Side (internal/syncx) gets a deterministic order to
// build its map from.
func (d *DB) SyncTombstones() ([]syncx.Tombstone, error) {
	rows, err := d.Query(`SELECT kind, ident, digest, device_id, at FROM sync_tombstone ORDER BY kind, ident`)
	if err != nil {
		return nil, serr.Internalf(err, "failed to read sync tombstones")
	}
	defer rows.Close()
	out := []syncx.Tombstone{}
	for rows.Next() {
		var t syncx.Tombstone
		if err := rows.Scan(&t.Kind, &t.Ident, &t.Digest, &t.DeviceID, &t.At); err != nil {
			return nil, serr.Internalf(err, "failed to read a sync tombstone")
		}
		if err := canonicalStamps(t.Ident, &t.At); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, serr.Internalf(err, "failed to read sync tombstones")
	}
	return out, nil
}

// putSyncTombstoneTx is what DeleteMemory and DeleteLink call, inside the same
// transaction as the delete they are recording (docs/sync-model.md §3.7: "the
// only change this design makes to an existing write path"). execer (audit.go)
// is satisfied by both *sql.DB and *sql.Tx, which is what lets PutSyncTombstone
// below share this with the two in-transaction callers instead of duplicating
// the INSERT.
func putSyncTombstoneTx(x execer, t syncx.Tombstone) error {
	if t.Kind != "memory" && t.Kind != "link" {
		return serr.E(serr.InvalidInput, "tombstone kind %q is invalid: must be \"memory\" or \"link\"", t.Kind)
	}
	if strings.TrimSpace(t.Ident) == "" {
		return serr.E(serr.InvalidInput, "tombstone ident must not be empty")
	}
	if strings.TrimSpace(t.Digest) == "" {
		return serr.E(serr.InvalidInput, "tombstone digest must not be empty")
	}
	if strings.TrimSpace(t.DeviceID) == "" {
		return serr.E(serr.InvalidInput, "tombstone device_id must not be empty")
	}
	at := t.At
	if at == "" {
		at = Now()
	}
	// A tombstone is retained forever (§3.7), never CAS'd: the ON CONFLICT
	// update handles the one legitimate reason to see the same (kind, ident)
	// twice — the same memory or link deleted, re-created, and deleted again —
	// by keeping the latest deletion's digest and timestamp rather than
	// rejecting the second delete outright.
	_, err := x.Exec(
		`INSERT INTO sync_tombstone(kind, ident, digest, device_id, at) VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(kind, ident) DO UPDATE SET digest = excluded.digest, device_id = excluded.device_id, at = excluded.at`,
		t.Kind, t.Ident, t.Digest, t.DeviceID, at)
	if err != nil {
		return serr.Internalf(err, "failed to record the tombstone for %s %q", t.Kind, t.Ident)
	}
	return nil
}

// clearSyncTombstoneTx removes the tombstone for a key that exists again.
//
// A tombstone asserts "this key is deleted", and re-creating the key makes that
// assertion false. Retained forever (§3.7) governs how long a tombstone
// survives the passage of time, not whether it survives its subject coming
// back — and leaving a stale one behind is not a cosmetic untidiness. Reconcile
// resolves tombstones before it consults the base table, so a tombstone
// shadowing a live memory produces two failures, both silent: against a remote
// that still holds the old content the plan says delete-remote, destroying the
// other machine's copy of a memory this one has re-created; and against a
// remote that has never seen the key the plan says "nothing to do", so the
// re-created memory never syncs at all, on any run, forever.
//
// So every write path clears, unconditionally rather than only on create. An
// update should never find a tombstone, but if a hand-repaired database or an
// older row has left one, the next ordinary write to that key repairs it — a
// cheap DELETE against a primary key, on a table with one row per deleted
// memory.
func clearSyncTombstoneTx(x execer, kind, ident string) error {
	if _, err := x.Exec(`DELETE FROM sync_tombstone WHERE kind = ? AND ident = ?`, kind, ident); err != nil {
		return serr.Internalf(err, "failed to clear the tombstone for %s %q", kind, ident)
	}
	return nil
}

// PutSyncTombstone records a tombstone outside of any transaction, for a
// caller that is not also performing the delete it describes — a conflict
// resolution that adopts the remote's delete, say. DeleteMemory and DeleteLink
// use putSyncTombstoneTx directly, in their own transactions, rather than this.
func (d *DB) PutSyncTombstone(t syncx.Tombstone) error {
	return putSyncTombstoneTx(d.DB, t)
}

// SyncPolicy reports the per-key override, if one has been recorded. set is
// false when no row exists, which is the ordinary case for almost every key —
// the caller applies the scope's default (SyncableMemories) rather than
// treating an absent row as any particular mode itself.
func (d *DB) SyncPolicy(key string) (mode string, set bool, err error) {
	if err := ValidateKey(key); err != nil {
		return "", false, err
	}
	err = d.QueryRow(`SELECT mode FROM sync_policy WHERE key = ?`, key).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, serr.Internalf(err, "failed to read the sync policy for %q", key)
	}
	return mode, true, nil
}

// SetSyncPolicy records a per-key override: exclude a project memory that
// should not leave this machine, or include a global one that should
// (docs/sync-model.md §6.4). `stigmergy sync share`/`unshare` call this.
func (d *DB) SetSyncPolicy(key, mode string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	if mode != "include" && mode != "exclude" {
		return serr.E(serr.InvalidInput, "mode %q is invalid: must be \"include\" or \"exclude\"", mode)
	}
	_, err := d.Exec(
		`INSERT INTO sync_policy(key, mode, at) VALUES(?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET mode = excluded.mode, at = excluded.at`,
		key, mode, Now())
	if err != nil {
		return serr.Internalf(err, "failed to set the sync policy for %q", key)
	}
	return nil
}

// syncPolicies loads every recorded per-key override in one query, for
// SyncableMemories to apply against the whole memory set rather than issuing
// one SyncPolicy lookup per row.
func (d *DB) syncPolicies() (map[string]string, error) {
	rows, err := d.Query(`SELECT key, mode FROM sync_policy`)
	if err != nil {
		return nil, serr.Internalf(err, "failed to read sync policy")
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, mode string
		if err := rows.Scan(&key, &mode); err != nil {
			return nil, serr.Internalf(err, "failed to read a sync policy row")
		}
		out[key] = mode
	}
	return out, rows.Err()
}

// SyncableMemories returns every memory this scope will export, applying the
// scope's default (docs/sync-model.md §6.4, D6): a project memory syncs unless
// a sync_policy row excludes it; a global memory does not sync unless a
// sync_policy row includes it. The asymmetry is deliberate and lives here, in
// exactly one place, rather than in the identical schema the two scopes share
// (see migrations/global/0004_sync.sql) — the fingerprint exchanged at `sync
// enable` has already proven two project databases mean the same repository,
// so a project memory needs an explicit reason to stay home; a global memory
// may be a fact about this physical box, which only the human who wrote it can
// tell apart from a portable one (memory-model.md §8), so it stays home unless
// told otherwise.
func (d *DB) SyncableMemories() ([]Memory, error) {
	rows, err := d.Query(`SELECT ` + memoryCols + ` FROM memories`)
	if err != nil {
		return nil, serr.Internalf(err, "failed to read memories")
	}
	defer rows.Close()
	var all []*Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		all = append(all, m)
	}
	if err := rows.Err(); err != nil {
		return nil, serr.Internalf(err, "failed to read memories")
	}

	policy, err := d.syncPolicies()
	if err != nil {
		return nil, err
	}

	out := []Memory{}
	for _, m := range all {
		syncs := d.Kind == Project
		if mode, set := policy[m.Key]; set {
			syncs = mode == "include"
		}
		if syncs {
			out = append(out, *m)
		}
	}
	slices.SortFunc(out, func(a, b Memory) int { return cmp.Compare(a.Key, b.Key) })
	return out, nil
}
