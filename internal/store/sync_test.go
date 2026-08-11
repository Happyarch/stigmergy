package store

import (
	"strings"
	"testing"
	"time"

	"github.com/happyarch/stigmergy/internal/serr"
)

// These tests deliberately name no type from internal/syncx. Everything here is
// reached through *DB, so the suite proves behaviour rather than pinning which
// package the plain data types ended up in.

func importOf(key, body, created, updated string, expected *int) MemoryImport {
	return MemoryImport{
		Key: key, Type: "project", Description: "desc for " + key, Body: body,
		CreatedAt: created, UpdatedAt: updated,
		UpdatedBy: "sync:other-machine", ExpectedVersion: expected,
	}
}

// An imported memory keeps the timestamps it was written with on the other
// machine (docs/sync-model.md §3.9).
//
// WriteMemory stamps Now() unconditionally, and reusing it for imports would
// set every imported memory's updated_at to the import time — so a freshly
// synced machine would show its whole memory set as identically new, and
// memory_list order_by:"recent" would sort by nothing at all.
func TestImportMemoryPreservesTheOriginTimestamps(t *testing.T) {
	db := testProject(t)
	const created = "2026-07-21T09:14:02.113004212Z"
	const updated = "2026-08-02T15:00:06.547664879Z"

	if _, err := db.ImportMemory(importOf("alpha", "imported body", created, updated, nil), "claude-code"); err != nil {
		t.Fatalf("ImportMemory: %v", err)
	}

	m, err := db.ReadMemory("alpha")
	if err != nil {
		t.Fatalf("ReadMemory: %v", err)
	}
	if m.CreatedAt != created {
		t.Errorf("created_at = %q, want the origin's %q", m.CreatedAt, created)
	}
	if m.UpdatedAt != updated {
		t.Errorf("updated_at = %q, want the origin's %q", m.UpdatedAt, updated)
	}
}

// A timestamp arriving in any other RFC3339 spelling is canonicalised on the
// way in, never stored as handed over.
//
// Fixed width is load-bearing: every TTL and expiry predicate compares these
// as TEXT, and a short fractional part sorts wrong against a full-width
// neighbour. Architecture §5.4 names the import path as a caller of
// CanonicalStamp for exactly this reason.
func TestImportMemoryCanonicalisesInboundTimestamps(t *testing.T) {
	db := testProject(t)

	if _, err := db.ImportMemory(
		importOf("alpha", "body", "2026-07-21T09:14:02.113Z", "2026-08-02T15:00:06Z", nil),
		"claude-code",
	); err != nil {
		t.Fatalf("ImportMemory: %v", err)
	}

	m, err := db.ReadMemory("alpha")
	if err != nil {
		t.Fatalf("ReadMemory: %v", err)
	}
	for _, stamp := range []string{m.CreatedAt, m.UpdatedAt} {
		if _, err := time.Parse(TimeLayout, stamp); err != nil {
			t.Errorf("stored timestamp %q is not in TimeLayout: %v", stamp, err)
		}
	}
}

// The wire never sets version. §3.1: version is a CAS token in this database's
// own namespace, and every agent's expected_version is this number — so an
// imported memory takes the LOCAL counter's next value, whatever the other
// machine happened to be at.
func TestImportMemoryTakesTheLocalVersionNotTheWireVersion(t *testing.T) {
	db := testProject(t)

	res, err := db.ImportMemory(importOf("alpha", "first", "2026-07-01T00:00:00.000000000Z", "2026-07-01T00:00:00.000000000Z", nil), "claude-code")
	if err != nil {
		t.Fatalf("ImportMemory (create): %v", err)
	}
	if res.Memory.Version != 1 {
		t.Fatalf("a created import got version %d, want 1", res.Memory.Version)
	}

	res, err = db.ImportMemory(importOf("alpha", "second", "2026-07-01T00:00:00.000000000Z", "2026-07-02T00:00:00.000000000Z", intp(1)), "claude-code")
	if err != nil {
		t.Fatalf("ImportMemory (update): %v", err)
	}
	if res.Memory.Version != 2 {
		t.Errorf("an updated import got version %d, want the local counter's 2", res.Memory.Version)
	}
}

// ImportMemory shares casCheck with WriteMemory rather than deciding CAS a
// second time (architecture §5.5). A sync is just another writer: when an agent
// moved the memory while the run was in flight, the import is refused and the
// error carries the current entry, exactly as a racing agent's would.
func TestImportMemorySharesTheCASMatrix(t *testing.T) {
	db := testProject(t)
	mustWrite(t, db, "alpha") // version 1, written locally

	_, err := db.ImportMemory(
		importOf("alpha", "inbound", "2026-07-01T00:00:00.000000000Z", "2026-07-02T00:00:00.000000000Z", intp(7)),
		"claude-code",
	)
	e := requireCode(t, err, serr.CASConflict)
	if _, ok := e.Context["current"]; !ok {
		t.Error("CAS conflict does not carry the current entry, so the merger cannot re-plan without a second read")
	}

	// Creating over an existing key is the other half of the matrix.
	_, err = db.ImportMemory(
		importOf("alpha", "inbound", "2026-07-01T00:00:00.000000000Z", "2026-07-02T00:00:00.000000000Z", nil),
		"claude-code",
	)
	requireCode(t, err, serr.CASConflict)
}

// The FTS proof, and the reason every import goes through the memories table.
//
// memories_fts is external-content: it holds no text of its own and its three
// triggers maintain it from memories. An import that reached the shadow table
// directly, or bypassed the triggers with a raw INSERT, would leave an index
// that disagrees with its content table — and the failure is silent, because a
// missing row simply does not match. The snippet check is the second half: the
// search projection addresses body by column INDEX, so a schema drift here
// shows up as a snippet drawn from the wrong column rather than as an error.
func TestImportedMemoryIsSearchableAndSnippetsFromBody(t *testing.T) {
	db := testProject(t)

	if _, err := db.ImportMemory(importOf(
		"alpha",
		"the claim guard refuses an edit when the schema version differs",
		"2026-07-01T00:00:00.000000000Z", "2026-07-02T00:00:00.000000000Z", nil,
	), "claude-code"); err != nil {
		t.Fatalf("ImportMemory: %v", err)
	}

	hits, err := db.SearchMemories("schema version")
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	if len(hits) != 1 || hits[0].Key != "alpha" {
		t.Fatalf("imported memory is not searchable: hits = %+v", hits)
	}
	if !strings.Contains(hits[0].Snippet, "schema") {
		t.Errorf("snippet %q is not drawn from the body", hits[0].Snippet)
	}

	// An update through the import path must maintain the index too: the update
	// trigger deletes the old row before inserting the new one, and a stale
	// entry would keep matching text the memory no longer contains.
	if _, err := db.ImportMemory(importOf(
		"alpha", "entirely different prose about mailboxes",
		"2026-07-01T00:00:00.000000000Z", "2026-07-03T00:00:00.000000000Z", intp(1),
	), "claude-code"); err != nil {
		t.Fatalf("ImportMemory (update): %v", err)
	}
	stale, err := db.SearchMemories("schema version")
	if err != nil {
		t.Fatalf("SearchMemories after update: %v", err)
	}
	if len(stale) != 0 {
		t.Errorf("the FTS index still matches the replaced body: %+v", stale)
	}
}

// Inbound text is validated exactly as any other write is, so a record that
// would be refused from an agent is refused from the wire too (§7.10). The
// import path must not become a second, laxer door into the same table.
func TestImportMemoryValidatesInboundTextLikeAnyOtherWrite(t *testing.T) {
	db := testProject(t)

	_, err := db.ImportMemory(importOf("Not A Valid Key", "body", "2026-07-01T00:00:00.000000000Z", "2026-07-01T00:00:00.000000000Z", nil), "claude-code")
	requireCode(t, err, serr.InvalidInput)

	bad := importOf("alpha", "body", "2026-07-01T00:00:00.000000000Z", "2026-07-01T00:00:00.000000000Z", nil)
	bad.Type = "not-a-type"
	_, err = db.ImportMemory(bad, "claude-code")
	requireCode(t, err, serr.InvalidInput)
}

// A malformed inbound timestamp is refused rather than stored. A row that
// cannot be ordered would sort wrong inside a result set that claims to be
// sorted, and doctor would later report it as corruption this machine caused.
func TestImportMemoryRefusesAnUnreadableTimestamp(t *testing.T) {
	db := testProject(t)

	_, err := db.ImportMemory(importOf("alpha", "body", "yesterday afternoon", "2026-07-01T00:00:00.000000000Z", nil), "claude-code")
	if err == nil {
		t.Fatal("an unparseable created_at was accepted")
	}
}

// A delete leaves a tombstone, in the same transaction as the delete.
//
// Without one, a delete on this machine is indistinguishable from a create on
// the other, and the next run resurrects what the user deleted (§3.7, A.8).
func TestDeleteMemoryLeavesATombstone(t *testing.T) {
	db := testProject(t)
	res, err := write(t, db, "alpha", "the body", nil)
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, _, err := db.DeleteMemory("alpha", res.Memory.Version, "r-test", "claude-code"); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}

	tombs, err := db.SyncTombstones()
	if err != nil {
		t.Fatalf("SyncTombstones: %v", err)
	}
	var found bool
	for _, ts := range tombs {
		if ts.Kind == "memory" && ts.Ident == "alpha" {
			found = true
			if ts.Digest == "" {
				t.Error("tombstone carries no digest, so a delete cannot be told from a delete/edit divergence")
			}
			if ts.At == "" {
				t.Error("tombstone carries no timestamp")
			}
		}
	}
	if !found {
		t.Fatalf("no memory tombstone after DeleteMemory: %+v", tombs)
	}
}

// The base row goes with the memory. §3.2 makes that a cascade, and the rule
// set is written for the pair of states "no memory, no base, one tombstone" —
// a base row left behind would read as an agreement about content that no
// longer exists.
func TestDeleteMemoryDropsItsSyncBase(t *testing.T) {
	db := testProject(t)
	res, err := write(t, db, "alpha", "the body", nil)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := db.SetSyncBase("alpha", "sha256:whatever", "d-local"); err != nil {
		t.Fatalf("SetSyncBase: %v", err)
	}

	if _, _, err := db.DeleteMemory("alpha", res.Memory.Version, "r-test", "claude-code"); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}

	bases, err := db.SyncBases()
	if err != nil {
		t.Fatalf("SyncBases: %v", err)
	}
	if _, ok := bases["alpha"]; ok {
		t.Fatal("the sync base outlived the memory it described")
	}
}

// Deleting a memory must NOT emit a tombstone for each link the cascade
// severs. The memory's own tombstone already carries that delete to the other
// machine, and the link rows there disappear by the same cascade; emitting both
// would leave a link tombstone that outlives any link it could ever describe
// (§3.6).
func TestDeleteMemoryDoesNotTombstoneCascadeSeveredLinks(t *testing.T) {
	db := testProject(t)
	mustWrite(t, db, "alpha")
	mustWrite(t, db, "beta")
	if _, err := db.CreateLink("alpha", "beta", "they share a dispatch table", "r-test", "claude-code"); err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	m, err := db.ReadMemory("alpha")
	if err != nil {
		t.Fatalf("ReadMemory: %v", err)
	}
	if _, severed, err := db.DeleteMemory("alpha", m.Version, "r-test", "claude-code"); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	} else if len(severed) != 1 {
		t.Fatalf("expected one severed neighbour, got %v", severed)
	}

	tombs, err := db.SyncTombstones()
	if err != nil {
		t.Fatalf("SyncTombstones: %v", err)
	}
	for _, ts := range tombs {
		if ts.Kind == "link" {
			t.Errorf("a cascade-severed link produced its own tombstone: %+v", ts)
		}
	}
}

// An explicitly unlinked edge does get one — that delete has nothing else
// carrying it.
func TestDeleteLinkLeavesATombstone(t *testing.T) {
	db := testProject(t)
	mustWrite(t, db, "alpha")
	mustWrite(t, db, "beta")
	if _, err := db.CreateLink("alpha", "beta", "they share a dispatch table", "r-test", "claude-code"); err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	removed, err := db.DeleteLink("beta", "alpha", "r-test", "claude-code")
	if err != nil {
		t.Fatalf("DeleteLink: %v", err)
	}
	if !removed {
		t.Fatal("DeleteLink reported nothing removed")
	}

	tombs, err := db.SyncTombstones()
	if err != nil {
		t.Fatalf("SyncTombstones: %v", err)
	}
	var found bool
	for _, ts := range tombs {
		if ts.Kind == "link" {
			found = true
			// The identity is the canonical pair, in the order the schema's
			// CHECK (key_a < key_b) stores it — not the order the caller typed.
			if !strings.Contains(ts.Ident, "alpha") || !strings.Contains(ts.Ident, "beta") {
				t.Errorf("link tombstone identity %q does not name both endpoints", ts.Ident)
			}
			if strings.Index(ts.Ident, "alpha") > strings.Index(ts.Ident, "beta") {
				t.Errorf("link tombstone identity %q is not in canonical order", ts.Ident)
			}
		}
	}
	if !found {
		t.Fatalf("no link tombstone after DeleteLink: %+v", tombs)
	}
}

// A memory that is deleted and then written again must not stay tombstoned.
//
// This was a real bug, found by reading the merger against the write path
// rather than by any test failing: nothing cleared the tombstone, and Reconcile
// resolves tombstones before it consults the base table. Both consequences were
// silent. Against a remote still holding the old content the plan said
// delete-remote, destroying the other machine's copy of a memory this one had
// re-created; against a remote that had never seen the key it said "nothing to
// do", so the re-created memory would never have synced on any run, ever.
func TestRecreatingAMemoryClearsItsTombstone(t *testing.T) {
	db := testProject(t)
	res, err := write(t, db, "alpha", "original body", nil)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := db.DeleteMemory("alpha", res.Memory.Version, "r-test", "claude-code"); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}
	if _, err := write(t, db, "alpha", "re-created body", nil); err != nil {
		t.Fatalf("re-create: %v", err)
	}

	tombs, err := db.SyncTombstones()
	if err != nil {
		t.Fatalf("SyncTombstones: %v", err)
	}
	for _, ts := range tombs {
		if ts.Kind == "memory" && ts.Ident == "alpha" {
			t.Fatalf("a memory that exists again is still tombstoned: %+v", ts)
		}
	}
}

// The same, through the import path — a pull that re-creates a memory this
// machine had deleted must leave it syncable rather than shadowed.
func TestImportingOverATombstoneClearsIt(t *testing.T) {
	db := testProject(t)
	res, err := write(t, db, "alpha", "original body", nil)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := db.DeleteMemory("alpha", res.Memory.Version, "r-test", "claude-code"); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}

	if _, err := db.ImportMemory(importOf(
		"alpha", "the remote's copy",
		"2026-07-01T00:00:00.000000000Z", "2026-07-02T00:00:00.000000000Z", nil,
	), "claude-code"); err != nil {
		t.Fatalf("ImportMemory: %v", err)
	}

	tombs, err := db.SyncTombstones()
	if err != nil {
		t.Fatalf("SyncTombstones: %v", err)
	}
	for _, ts := range tombs {
		if ts.Kind == "memory" && ts.Ident == "alpha" {
			t.Fatalf("an imported memory is still tombstoned: %+v", ts)
		}
	}
}

// Re-linking a pair clears the tombstone the unlink left, for the same reason.
func TestRelinkingClearsTheLinkTombstone(t *testing.T) {
	db := testProject(t)
	mustWrite(t, db, "alpha")
	mustWrite(t, db, "beta")
	if _, err := db.CreateLink("alpha", "beta", "first reason", "r-test", "claude-code"); err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	if _, err := db.DeleteLink("alpha", "beta", "r-test", "claude-code"); err != nil {
		t.Fatalf("DeleteLink: %v", err)
	}
	if _, err := db.CreateLink("alpha", "beta", "a better reason", "r-test", "claude-code"); err != nil {
		t.Fatalf("re-link: %v", err)
	}

	tombs, err := db.SyncTombstones()
	if err != nil {
		t.Fatalf("SyncTombstones: %v", err)
	}
	for _, ts := range tombs {
		if ts.Kind == "link" {
			t.Fatalf("a link that exists again is still tombstoned: %+v", ts)
		}
	}
}

// The scopes' defaults are opposite, and that asymmetry is the design (§6.4,
// D6). A project memory is about the repository the fingerprint has already
// matched; a global memory may be about this physical box, and only the human
// knows which.
func TestSyncableMemoriesProjectSyncsUnlessExcluded(t *testing.T) {
	db := testProject(t)
	mustWrite(t, db, "alpha")
	mustWrite(t, db, "beta")

	got, err := db.SyncableMemories()
	if err != nil {
		t.Fatalf("SyncableMemories: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("project scope synced %d of 2 memories by default: %+v", len(got), got)
	}

	if err := db.SetSyncPolicy("beta", "exclude"); err != nil {
		t.Fatalf("SetSyncPolicy: %v", err)
	}
	got, err = db.SyncableMemories()
	if err != nil {
		t.Fatalf("SyncableMemories: %v", err)
	}
	if len(got) != 1 || got[0].Key != "alpha" {
		t.Fatalf("an excluded project memory still syncs: %+v", got)
	}
}

func TestSyncableMemoriesGlobalSyncsOnlyWhenIncluded(t *testing.T) {
	db := testGlobal(t)
	mustWrite(t, db, "machine-hard-locks")
	mustWrite(t, db, "a-portable-lesson")

	got, err := db.SyncableMemories()
	if err != nil {
		t.Fatalf("SyncableMemories: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("global scope synced %d memories with nothing opted in: %+v", len(got), got)
	}

	if err := db.SetSyncPolicy("a-portable-lesson", "include"); err != nil {
		t.Fatalf("SetSyncPolicy: %v", err)
	}
	got, err = db.SyncableMemories()
	if err != nil {
		t.Fatalf("SyncableMemories: %v", err)
	}
	if len(got) != 1 || got[0].Key != "a-portable-lesson" {
		t.Fatalf("global opt-in did not select exactly the opted-in memory: %+v", got)
	}
}

// A policy row is not a memory and must not keep a deleted key alive, nor
// outlive it as a dangling row the next opt-in would silently inherit.
func TestSyncPolicyGoesWithTheMemory(t *testing.T) {
	db := testProject(t)
	res, err := write(t, db, "alpha", "body", nil)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := db.SetSyncPolicy("alpha", "exclude"); err != nil {
		t.Fatalf("SetSyncPolicy: %v", err)
	}

	if _, _, err := db.DeleteMemory("alpha", res.Memory.Version, "r-test", "claude-code"); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}

	if _, set, err := db.SyncPolicy("alpha"); err != nil {
		t.Fatalf("SyncPolicy: %v", err)
	} else if set {
		t.Error("the sync policy outlived the memory it governed")
	}
}

// Mode is a closed set. An unrecognised value would read as neither include nor
// exclude, and whichever default the reader fell back to would be a silent
// answer to a question the user thought they had settled.
func TestSetSyncPolicyRejectsAnUnknownMode(t *testing.T) {
	db := testProject(t)
	mustWrite(t, db, "alpha")

	if err := db.SetSyncPolicy("alpha", "maybe"); err == nil {
		t.Fatal("an unknown policy mode was accepted")
	}
}
