package store

import (
	"strings"
	"testing"

	"github.com/happyarch/stigmergy/internal/serr"
)

func mustWrite(t *testing.T, db *DB, key string) {
	t.Helper()
	if _, err := write(t, db, key, "body for "+key, nil); err != nil {
		t.Fatalf("write %q: %v", key, err)
	}
}

// A link created in either argument order lands as one canonical row, and
// both endpoints see it.
func TestCreateLinkCanonicalOrderBothDirections(t *testing.T) {
	db := testProject(t)
	mustWrite(t, db, "alpha")
	mustWrite(t, db, "beta")

	l, err := db.CreateLink("beta", "alpha", "they share the dispatch table", "r-test", "claude-code")
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	if l.KeyA != "alpha" || l.KeyB != "beta" {
		t.Fatalf("CreateLink did not canonicalise order: got %q/%q", l.KeyA, l.KeyB)
	}

	neighbors, err := db.NeighborsOf([]string{"alpha", "beta"})
	if err != nil {
		t.Fatalf("NeighborsOf: %v", err)
	}
	if len(neighbors["alpha"]) != 1 || neighbors["alpha"][0].Key != "beta" {
		t.Fatalf("alpha's neighbors = %+v, want [beta]", neighbors["alpha"])
	}
	if len(neighbors["beta"]) != 1 || neighbors["beta"][0].Key != "alpha" {
		t.Fatalf("beta's neighbors = %+v, want [alpha]", neighbors["beta"])
	}
	if neighbors["alpha"][0].Reason != "they share the dispatch table" {
		t.Errorf("reason not carried through: %+v", neighbors["alpha"][0])
	}
	if neighbors["alpha"][0].Description == "" {
		t.Errorf("neighbor description not joined from memories")
	}
}

func TestCreateLinkRejectsSelfLink(t *testing.T) {
	db := testProject(t)
	mustWrite(t, db, "alpha")

	_, err := db.CreateLink("alpha", "alpha", "reason", "r-test", "claude-code")
	requireCode(t, err, serr.InvalidInput)
}

// A duplicate pair is rejected, and the error carries the existing edge so
// the caller can merge by unlink + relink instead of losing a reason.
func TestCreateLinkDuplicateCarriesExistingEdge(t *testing.T) {
	db := testProject(t)
	mustWrite(t, db, "alpha")
	mustWrite(t, db, "beta")

	if _, err := db.CreateLink("alpha", "beta", "first reason", "r-test", "claude-code"); err != nil {
		t.Fatalf("first CreateLink: %v", err)
	}
	_, err := db.CreateLink("beta", "alpha", "second reason", "r-test", "claude-code")
	e := requireCode(t, err, serr.CASConflict)
	cur, ok := e.Context["current"].(Link)
	if !ok {
		t.Fatalf("duplicate error does not carry the existing edge: %+v", e.Context)
	}
	if cur.Reason != "first reason" {
		t.Errorf("existing edge reason = %q, want %q", cur.Reason, "first reason")
	}
}

// The 17th link on one memory fails, naming the fan effect.
func TestCreateLinkCapNamesFanEffect(t *testing.T) {
	db := testProject(t)
	mustWrite(t, db, "hub")
	for i := 0; i < MaxLinksPerMemory; i++ {
		key := "spoke-" + string(rune('a'+i))
		mustWrite(t, db, key)
		if _, err := db.CreateLink("hub", key, "reason", "r-test", "claude-code"); err != nil {
			t.Fatalf("CreateLink #%d: %v", i, err)
		}
	}
	mustWrite(t, db, "one-too-many")
	_, err := db.CreateLink("hub", "one-too-many", "reason", "r-test", "claude-code")
	e := requireCode(t, err, serr.InvalidInput)
	if !strings.Contains(e.Message, "fan-effect") {
		t.Errorf("cap error does not name the fan effect: %q", e.Message)
	}
}

// The cap only refuses further growth; NeighborsOf itself reports every one
// of them (surfacing truncation is the MCP layer's job, on top of this).
func TestCreateLinkAtCapNeighborsOfReturnsAll(t *testing.T) {
	db := testProject(t)
	mustWrite(t, db, "hub")
	for i := 0; i < MaxLinksPerMemory; i++ {
		key := "spoke-" + string(rune('a'+i))
		mustWrite(t, db, key)
		if _, err := db.CreateLink("hub", key, "reason", "r-test", "claude-code"); err != nil {
			t.Fatalf("CreateLink #%d: %v", i, err)
		}
	}
	neighbors, err := db.NeighborsOf([]string{"hub"})
	if err != nil {
		t.Fatal(err)
	}
	if len(neighbors["hub"]) != MaxLinksPerMemory {
		t.Fatalf("NeighborsOf(hub) = %d neighbors, want %d", len(neighbors["hub"]), MaxLinksPerMemory)
	}
}

// The audit trail names what was linked and, on delete, what was severed —
// not just the response, which a caller could construct without ever writing
// the row.
func TestLinkAndUnlinkAreAudited(t *testing.T) {
	db := testProject(t)
	mustWrite(t, db, "alpha")
	mustWrite(t, db, "beta")

	if _, err := db.CreateLink("alpha", "beta", "shares the dispatch table", "r-test", "claude-code"); err != nil {
		t.Fatal(err)
	}
	var linkDetail string
	if err := db.QueryRow(`SELECT detail FROM audit_log WHERE action = 'memory_link'`).Scan(&linkDetail); err != nil {
		t.Fatalf("no memory_link audit row: %v", err)
	}
	if linkDetail != "shares the dispatch table" {
		t.Errorf("memory_link audit detail = %q, want the reason", linkDetail)
	}

	if _, err := db.DeleteLink("alpha", "beta", "r-test", "claude-code"); err != nil {
		t.Fatal(err)
	}
	var unlinkDetail string
	if err := db.QueryRow(`SELECT detail FROM audit_log WHERE action = 'memory_unlink'`).Scan(&unlinkDetail); err != nil {
		t.Fatalf("no memory_unlink audit row: %v", err)
	}
	if unlinkDetail != "shares the dispatch table" {
		t.Errorf("memory_unlink audit detail = %q, want the severed reason", unlinkDetail)
	}
}

// DeleteMemory's audit row must name what it severed — the whole point of
// reading links inside the same transaction as the delete.
func TestDeleteMemoryAuditNamesSeveredLinks(t *testing.T) {
	db := testProject(t)
	res, err := write(t, db, "alpha", "body", nil)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, db, "beta")
	if _, err := db.CreateLink("alpha", "beta", "reason", "r-test", "claude-code"); err != nil {
		t.Fatal(err)
	}

	if _, _, err := db.DeleteMemory("alpha", res.Memory.Version, "r-test", "claude-code"); err != nil {
		t.Fatal(err)
	}
	var detail string
	if err := db.QueryRow(`SELECT detail FROM audit_log WHERE action = 'memory_delete'`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "severed_links=beta") {
		t.Errorf("memory_delete audit detail = %q, want it to name severed_links=beta", detail)
	}
}

func TestCreateLinkReasonValidation(t *testing.T) {
	db := testProject(t)
	mustWrite(t, db, "alpha")
	mustWrite(t, db, "beta")

	if _, err := db.CreateLink("alpha", "beta", "", "r-test", "claude-code"); err == nil {
		t.Error("empty reason accepted")
	}
	if _, err := db.CreateLink("alpha", "beta", "line one\nline two", "r-test", "claude-code"); err == nil {
		t.Error("multi-line reason accepted")
	}
}

func TestCreateLinkRequiresBothMemoriesToExist(t *testing.T) {
	db := testProject(t)
	mustWrite(t, db, "alpha")

	_, err := db.CreateLink("alpha", "does-not-exist", "reason", "r-test", "claude-code")
	e := requireCode(t, err, serr.InvalidInput)
	if !strings.Contains(e.Message, "does-not-exist") {
		t.Errorf("error does not name the missing key: %q", e.Message)
	}
}

func TestDeleteLinkAbsentPairIsNotAnError(t *testing.T) {
	db := testProject(t)
	mustWrite(t, db, "alpha")
	mustWrite(t, db, "beta")

	removed, err := db.DeleteLink("alpha", "beta", "r-test", "claude-code")
	if err != nil {
		t.Fatalf("DeleteLink on an absent pair returned an error: %v", err)
	}
	if removed {
		t.Error("DeleteLink on an absent pair reported removed=true")
	}
}

func TestDeleteLinkRemovesTheEdge(t *testing.T) {
	db := testProject(t)
	mustWrite(t, db, "alpha")
	mustWrite(t, db, "beta")
	if _, err := db.CreateLink("alpha", "beta", "reason", "r-test", "claude-code"); err != nil {
		t.Fatal(err)
	}

	removed, err := db.DeleteLink("beta", "alpha", "r-test", "claude-code")
	if err != nil || !removed {
		t.Fatalf("DeleteLink: removed=%v err=%v, want true, nil", removed, err)
	}

	neighbors, err := db.NeighborsOf([]string{"alpha", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if len(neighbors["alpha"]) != 0 || len(neighbors["beta"]) != 0 {
		t.Errorf("neighbors survived delete: %+v", neighbors)
	}
}

// Deleting a memory cascades to its links. PRAGMA foreign_keys must be ON for
// this to be enforcement rather than a consistency check that happens to pass
// — see the memory-change-evidence-design lesson about the two being
// different claims.
func TestDeleteMemoryCascadesToLinks(t *testing.T) {
	db := testProject(t)

	var fkOn int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fkOn); err != nil {
		t.Fatal(err)
	}
	if fkOn != 1 {
		t.Fatal("PRAGMA foreign_keys is not ON — the cascade this test relies on would not be enforced")
	}

	res, err := write(t, db, "alpha", "body", nil)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, db, "beta")
	if _, err := db.CreateLink("alpha", "beta", "reason", "r-test", "claude-code"); err != nil {
		t.Fatal(err)
	}

	_, severed, err := db.DeleteMemory("alpha", res.Memory.Version, "r-test", "claude-code")
	if err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}
	if len(severed) != 1 || severed[0] != "beta" {
		t.Errorf("DeleteMemory severed = %v, want [beta]", severed)
	}

	neighbors, err := db.NeighborsOf([]string{"beta"})
	if err != nil {
		t.Fatal(err)
	}
	if len(neighbors["beta"]) != 0 {
		t.Errorf("link survived the memory's deletion: %+v", neighbors["beta"])
	}

	var count int
	if err := db.QueryRow(`SELECT count(*) FROM memory_links`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("memory_links has %d rows after the linked memory was deleted, want 0", count)
	}
}

// NeighborsOf batches over N keys in one query: both directions are found,
// descriptions are joined in, and timestamps come back canonical.
func TestNeighborsOfBatchedOverManyKeys(t *testing.T) {
	db := testProject(t)
	for _, k := range []string{"a", "b", "c", "d"} {
		mustWrite(t, db, k)
	}
	// a-b, b-c, c-d: a chain, so b and c each have two neighbors and a and d
	// have one.
	for _, pair := range [][2]string{{"a", "b"}, {"b", "c"}, {"c", "d"}} {
		if _, err := db.CreateLink(pair[0], pair[1], "chain", "r-test", "claude-code"); err != nil {
			t.Fatal(err)
		}
	}

	neighbors, err := db.NeighborsOf([]string{"a", "b", "c", "d"})
	if err != nil {
		t.Fatal(err)
	}
	if len(neighbors["a"]) != 1 || len(neighbors["d"]) != 1 {
		t.Errorf("endpoints of the chain: a=%+v d=%+v", neighbors["a"], neighbors["d"])
	}
	if len(neighbors["b"]) != 2 || len(neighbors["c"]) != 2 {
		t.Errorf("middle of the chain: b=%+v c=%+v", neighbors["b"], neighbors["c"])
	}
	for _, list := range neighbors {
		for _, n := range list {
			if _, err := CanonicalStamp(n.LinkedAt); err != nil {
				t.Errorf("neighbor %+v has a non-canonical timestamp", n)
			}
		}
	}
}

func TestLinkCounts(t *testing.T) {
	db := testProject(t)
	for _, k := range []string{"a", "b", "c"} {
		mustWrite(t, db, k)
	}
	if _, err := db.CreateLink("a", "b", "reason", "r-test", "claude-code"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateLink("a", "c", "reason", "r-test", "claude-code"); err != nil {
		t.Fatal(err)
	}

	counts, err := db.LinkCounts([]string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if counts["a"] != 2 || counts["b"] != 1 || counts["c"] != 1 {
		t.Errorf("LinkCounts = %+v, want a:2 b:1 c:1", counts)
	}
}

// Global scope works the same way, against global memories.
func TestCreateLinkGlobalScope(t *testing.T) {
	db := testGlobal(t)
	if _, err := db.WriteMemory(MemoryWrite{
		Key: "alpha", Type: "project", Description: "d", Body: "b", UpdatedBy: "r-test",
	}, "claude-code"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.WriteMemory(MemoryWrite{
		Key: "beta", Type: "project", Description: "d", Body: "b", UpdatedBy: "r-test",
	}, "claude-code"); err != nil {
		t.Fatal(err)
	}

	if _, err := db.CreateLink("alpha", "beta", "reason", "r-test", "claude-code"); err != nil {
		t.Fatalf("CreateLink on global scope: %v", err)
	}
	neighbors, err := db.NeighborsOf([]string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(neighbors["alpha"]) != 1 {
		t.Fatalf("global NeighborsOf = %+v, want one neighbor", neighbors["alpha"])
	}
}
