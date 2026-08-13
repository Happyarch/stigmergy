package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/happyarch/stigmergy/internal/store"
	"github.com/happyarch/stigmergy/internal/xdg"
)

// Two machines, one directory between them.
//
// These drive exportScope and importScope directly rather than the cobra
// commands, because what is under test is the machine — Reconcile, the
// serializer, ImportMemory and the tombstone rules acting on two real
// databases — not argument parsing. Every database is a fresh t.TempDir(); none
// of this ever touches the repository's own.

// isolate redirects the XDG directories at a temporary tree.
//
// Staging a conflict writes under $XDG_STATE_HOME (§3.5), so a test that omits
// this leaves files in the developer's real ~/.local/state — which is both a
// side effect a test has no business having and a way for one run to be seen by
// the next. Every test here that can reach the conflict path calls it.
func isolate(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
}

func machine(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.OpenProject(t.TempDir())
	if err != nil {
		t.Fatalf("OpenProject: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func put(t *testing.T, db *store.DB, key, body string, expected *int) *store.WriteResult {
	t.Helper()
	res, err := db.WriteMemory(store.MemoryWrite{
		Key: key, Type: "project", Description: "desc for " + key, Body: body,
		UpdatedBy: "r-test", ExpectedVersion: expected,
	}, "claude-code")
	if err != nil {
		t.Fatalf("WriteMemory %q: %v", key, err)
	}
	return res
}

func exportTo(t *testing.T, dir string, db *store.DB, device string) exportStats {
	t.Helper()
	stats, err := exportScope(dir, db, device)
	if err != nil {
		t.Fatalf("exportScope: %v", err)
	}
	return stats
}

func importFrom(t *testing.T, db *store.DB, dir, label string) importStats {
	t.Helper()
	stats, err := importScope(db, "project", dir, label)
	if err != nil {
		t.Fatalf("importScope: %v", err)
	}
	return stats
}

func has(t *testing.T, db *store.DB, key string) bool {
	t.Helper()
	_, err := db.ReadMemory(key)
	return err == nil
}

// A memory written on one machine arrives on the other with its own timestamps
// intact — the first item on docs/sync-model.md's live-verification list.
func TestRoundTripCarriesAMemoryBetweenTwoMachines(t *testing.T) {
	isolate(t)
	a, b := machine(t), machine(t)
	dir := t.TempDir()

	put(t, a, "shared-lesson", "the claim guard fails closed on schema skew", nil)
	original, err := a.ReadMemory("shared-lesson")
	if err != nil {
		t.Fatalf("ReadMemory: %v", err)
	}

	exportTo(t, dir, a, "d-a")
	if stats := importFrom(t, b, dir, "d-a"); stats.pulled != 1 {
		t.Fatalf("import pulled %d, want 1 (%s)", stats.pulled, stats)
	}

	got, err := b.ReadMemory("shared-lesson")
	if err != nil {
		t.Fatalf("the memory did not arrive: %v", err)
	}
	if got.Body != original.Body {
		t.Errorf("body did not survive: got %q want %q", got.Body, original.Body)
	}
	if got.CreatedAt != original.CreatedAt || got.UpdatedAt != original.UpdatedAt {
		t.Errorf("timestamps were restamped on import: got %q/%q want %q/%q",
			got.CreatedAt, got.UpdatedAt, original.CreatedAt, original.UpdatedAt)
	}

	// And it is searchable there, which is the FTS triggers firing through the
	// import path on a database that has never seen this key before.
	hits, err := b.SearchMemories("schema skew")
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("an imported memory is not searchable on the receiving machine: %+v", hits)
	}
}

// Importing the same tree twice changes nothing the second time. A sync that is
// not idempotent cannot be safely re-run after an interruption, and §7.2 leans
// on exactly that when it says a crash leaves a prefix applied.
func TestImportingTwiceIsANoOp(t *testing.T) {
	isolate(t)
	a, b := machine(t), machine(t)
	dir := t.TempDir()

	put(t, a, "shared-lesson", "the body", nil)
	exportTo(t, dir, a, "d-a")
	importFrom(t, b, dir, "d-a")

	before, err := b.ReadMemory("shared-lesson")
	if err != nil {
		t.Fatalf("ReadMemory: %v", err)
	}
	stats := importFrom(t, b, dir, "d-a")
	after, err := b.ReadMemory("shared-lesson")
	if err != nil {
		t.Fatalf("ReadMemory: %v", err)
	}

	if after.Version != before.Version {
		t.Errorf("a second import bumped the version %d -> %d; the run is not idempotent",
			before.Version, after.Version)
	}
	if stats.pulled != 0 {
		t.Errorf("a second import pulled %d records, want 0 (%s)", stats.pulled, stats)
	}
}

// A delete on one machine removes the memory on the other. Without this the
// user learns that delete does not work, which is A.8's whole argument.
func TestADeletePropagatesToTheOtherMachine(t *testing.T) {
	isolate(t)
	a, b := machine(t), machine(t)
	dir := t.TempDir()

	res := put(t, a, "shared-lesson", "the body", nil)
	exportTo(t, dir, a, "d-a")
	importFrom(t, b, dir, "d-a")
	if !has(t, b, "shared-lesson") {
		t.Fatal("setup failed: the memory never reached the second machine")
	}

	if _, _, err := a.DeleteMemory("shared-lesson", res.Memory.Version, "r-test", "claude-code"); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}
	exportTo(t, dir, a, "d-a")
	if stats := importFrom(t, b, dir, "d-a"); stats.deletedLocal != 1 {
		t.Fatalf("import deleted %d locally, want 1 (%s)", stats.deletedLocal, stats)
	}

	if has(t, b, "shared-lesson") {
		t.Fatal("the deletion did not propagate")
	}
}

// ...and the machine that received the delete does not push it back. This is
// the resurrection loop §3.7 exists to close: B deleted it because A did, so B
// must not now offer it to A as something A is missing.
func TestAPropagatedDeleteIsNotResurrected(t *testing.T) {
	isolate(t)
	a, b := machine(t), machine(t)
	dir := t.TempDir()

	res := put(t, a, "shared-lesson", "the body", nil)
	exportTo(t, dir, a, "d-a")
	importFrom(t, b, dir, "d-a")
	if _, _, err := a.DeleteMemory("shared-lesson", res.Memory.Version, "r-test", "claude-code"); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}
	exportTo(t, dir, a, "d-a")
	importFrom(t, b, dir, "d-a")

	// B now exports its own view, and A imports it back.
	exportTo(t, dir, b, "d-b")
	importFrom(t, a, dir, "d-b")

	if has(t, a, "shared-lesson") {
		t.Fatal("the deleted memory came back on the machine that deleted it")
	}
	if has(t, b, "shared-lesson") {
		t.Fatal("the deleted memory came back on the machine that received the deletion")
	}
}

// Both machines edit the same memory: neither body is written, and the key is
// staged instead (D4, §3.5). The most important negative in the whole design —
// a sync that silently picks a winner destroys work.
func TestADivergedMemoryIsStagedAndNeitherSideIsWritten(t *testing.T) {
	isolate(t)
	a, b := machine(t), machine(t)
	dir := t.TempDir()

	res := put(t, a, "shared-lesson", "agreed text", nil)
	exportTo(t, dir, a, "d-a")
	importFrom(t, b, dir, "d-a")

	put(t, a, "shared-lesson", "A's version", &res.Memory.Version)
	bCur, err := b.ReadMemory("shared-lesson")
	if err != nil {
		t.Fatalf("ReadMemory: %v", err)
	}
	put(t, b, "shared-lesson", "B's version", &bCur.Version)

	exportTo(t, dir, a, "d-a")
	stats := importFrom(t, b, dir, "d-a")
	if stats.conflicts != 1 {
		t.Fatalf("import staged %d conflicts, want 1 (%s)", stats.conflicts, stats)
	}
	if stats.pulled != 0 {
		t.Fatalf("import pulled %d records during a conflict, want 0 (%s)", stats.pulled, stats)
	}

	after, err := b.ReadMemory("shared-lesson")
	if err != nil {
		t.Fatalf("ReadMemory: %v", err)
	}
	if after.Body != "B's version" {
		t.Fatalf("the local body was overwritten during a conflict: %q", after.Body)
	}
}

// Re-exporting an unchanged database rewrites the same bytes. The exporter
// writes into a working tree a human reads, so a run that reformats every file
// it touches produces a diff on every sync and hides the one real change in it.
func TestReExportingIsByteIdentical(t *testing.T) {
	isolate(t)
	a := machine(t)
	dir := t.TempDir()

	put(t, a, "alpha", "first body", nil)
	put(t, a, "beta", "second body\nwith two lines\n", nil)
	exportTo(t, dir, a, "d-a")

	first := treeBytes(t, dir)
	exportTo(t, dir, a, "d-a")
	second := treeBytes(t, dir)

	if len(first) != len(second) {
		t.Fatalf("re-export changed the file set: %d files then %d", len(first), len(second))
	}
	for name, want := range first {
		if got := second[name]; got != want {
			t.Errorf("re-export rewrote %s:\n got %q\nwant %q", name, got, want)
		}
	}
}

// A memory that travels A -> B -> A is unchanged when it gets home. Anything
// the format quietly normalises would show up here as a diverging digest and,
// on the next run, as a conflict conjured out of nothing.
func TestARoundTripHomeAgainChangesNothing(t *testing.T) {
	isolate(t)
	a, b := machine(t), machine(t)
	dir := t.TempDir()

	body := "  indented\ntabs\there\ntrailing spaces   \n"
	put(t, a, "alpha", body, nil)
	before, err := a.ReadMemory("alpha")
	if err != nil {
		t.Fatalf("ReadMemory: %v", err)
	}

	exportTo(t, dir, a, "d-a")
	importFrom(t, b, dir, "d-a")
	exportTo(t, dir, b, "d-b")
	stats := importFrom(t, a, dir, "d-b")

	after, err := a.ReadMemory("alpha")
	if err != nil {
		t.Fatalf("ReadMemory: %v", err)
	}
	if after.Body != before.Body {
		t.Errorf("the body changed on the way home:\n got %q\nwant %q", after.Body, before.Body)
	}
	if after.Version != before.Version {
		t.Errorf("a round trip rewrote the memory (version %d -> %d) though nothing changed",
			before.Version, after.Version)
	}
	if stats.conflicts != 0 {
		t.Errorf("a round trip manufactured %d conflict(s) (%s)", stats.conflicts, stats)
	}
}

func treeBytes(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walking the export tree: %v", err)
	}
	return out
}

// TestBareSyncUsesARealBareRepository covers the Stage B boundary without an
// exec mock: setup, clone, commit, and push are all actual git operations.
func TestBareSyncUsesARealBareRepository(t *testing.T) {
	isolate(t)
	path, err := xdg.GlobalDBPath()
	if err != nil {
		t.Fatal(err)
	}
	g, err := store.OpenGlobal(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.WriteMemory(store.MemoryWrite{Key: "portable", Type: "project", Description: "portable", Body: "a workflow lesson", UpdatedBy: "r-test"}, "codex"); err != nil {
		t.Fatal(err)
	}
	if err := g.SetSyncPolicy("portable", "include"); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(t.TempDir(), "project.sqlite3")
	p, err := store.OpenProjectAt(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetMeta(store.MetaSyncProjectKey, "known-project"); err != nil {
		t.Fatal(err)
	}
	if err := p.SetMeta(store.MetaSyncFingerprintKey, "test-fingerprint"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.WriteMemory(store.MemoryWrite{Key: "project-memory", Type: "project", Description: "project", Body: "a project lesson", UpdatedBy: "r-test"}, "codex"); err != nil {
		t.Fatal(err)
	}
	p.Close()
	if err := g.RememberProject(projectPath, "known project"); err != nil {
		t.Fatal(err)
	}
	g.Close()
	bare := filepath.Join(t.TempDir(), "memories.git")
	if b, err := exec.Command("git", "init", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, b)
	}
	var out bytes.Buffer
	if err := runSyncInit(&out, bare); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runSync(&out, false, false, true); err != nil {
		t.Fatalf("bare sync: %v\n%s", err, out.String())
	}
	if b, err := exec.Command("git", "--git-dir", bare, "show", "main:global/memories/portable.md").CombinedOutput(); err != nil {
		t.Fatalf("pushed tree has no memory: %v\n%s", err, b)
	}
	if b, err := exec.Command("git", "--git-dir", bare, "show", "main:projects/known-project/memories/project-memory.md").CombinedOutput(); err != nil {
		t.Fatalf("bare sync missed an enabled known project: %v\n%s", err, b)
	}
}

func TestBareSyncDirectoryTransportWritesTheSameTree(t *testing.T) {
	isolate(t)
	path, err := xdg.GlobalDBPath()
	if err != nil {
		t.Fatal(err)
	}
	g, err := store.OpenGlobal(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.WriteMemory(store.MemoryWrite{Key: "portable", Type: "project", Description: "portable", Body: "a workflow lesson", UpdatedBy: "r-test"}, "codex"); err != nil {
		t.Fatal(err)
	}
	if err := g.SetSyncPolicy("portable", "include"); err != nil {
		t.Fatal(err)
	}
	g.Close()
	dir := filepath.Join(t.TempDir(), "shared")
	var out bytes.Buffer
	if err := runSyncInit(&out, "dir:"+dir); err != nil {
		t.Fatal(err)
	}
	if err := runSync(&out, false, false, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "global", "memories", "portable.md")); err != nil {
		t.Fatalf("directory transport did not export the memory: %v", err)
	}
}

func TestBareSyncRetriesANonFastForwardPush(t *testing.T) {
	isolate(t)
	path := mustGlobalPath(t)
	g, err := store.OpenGlobal(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.WriteMemory(store.MemoryWrite{Key: "first", Type: "project", Description: "first", Body: "first body", UpdatedBy: "r-test"}, "codex"); err != nil {
		t.Fatal(err)
	}
	if err := g.SetSyncPolicy("first", "include"); err != nil {
		t.Fatal(err)
	}
	g.Close()
	bare := filepath.Join(t.TempDir(), "memories.git")
	if b, err := exec.Command("git", "init", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, b)
	}
	var out bytes.Buffer
	if err := runSyncInit(&out, bare); err != nil {
		t.Fatal(err)
	}
	if err := runSync(&out, false, false, true); err != nil {
		t.Fatal(err)
	}

	other := filepath.Join(t.TempDir(), "other")
	if b, err := exec.Command("git", "clone", bare, other).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, b)
	}
	if b, err := exec.Command("git", "-C", other, "checkout", "-b", "main", "origin/main").CombinedOutput(); err != nil {
		t.Fatalf("git checkout main: %v\n%s", err, b)
	}
	if err := os.WriteFile(filepath.Join(other, "other"), []byte("other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-C", other, "add", "other"}, {"-C", other, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-m", "other"}, {"-C", other, "push", "origin", "HEAD:main"}} {
		if b, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, b)
		}
	}
	g, err = store.OpenGlobal(path, store.NoMigrate())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.WriteMemory(store.MemoryWrite{Key: "second", Type: "project", Description: "second", Body: "second body", UpdatedBy: "r-test"}, "codex"); err != nil {
		t.Fatal(err)
	}
	if err := g.SetSyncPolicy("second", "include"); err != nil {
		t.Fatal(err)
	}
	g.Close()
	if err := runSync(&out, false, false, false); err != nil {
		t.Fatalf("retrying sync: %v\n%s", err, out.String())
	}
	t.Log(out.String())
	if b, err := exec.Command("git", "--git-dir", bare, "show", "main:global/memories/second.md").CombinedOutput(); err != nil {
		t.Fatalf("retry did not push local memory: %v\n%s", err, b)
	}
}

func TestRollbackIsRefused(t *testing.T) {
	isolate(t)
	db, err := store.OpenGlobal(mustGlobalPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := db.LocalDeviceID()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetMeta(store.MetaSyncSeqKey, "2"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := writeDeviceSequence(dir, id, "other", 3); err != nil {
		t.Fatal(err)
	}
	err = refuseRollback(dir, db, false)
	if err == nil || !strings.Contains(err.Error(), "restored backup") || !strings.Contains(err.Error(), "cloned from one image") {
		t.Fatalf("rollback refusal did not name both explanations: %v", err)
	}
}

func mustGlobalPath(t *testing.T) string {
	t.Helper()
	p, err := xdg.GlobalDBPath()
	if err != nil {
		t.Fatal(err)
	}
	return p
}
