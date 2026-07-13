package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/happyarch/stigmergy/internal/serr"
)

func testProject(t *testing.T) *DB {
	t.Helper()
	db, err := OpenProject(t.TempDir())
	if err != nil {
		t.Fatalf("OpenProject: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func testGlobal(t *testing.T) *DB {
	t.Helper()
	db, err := OpenGlobal(filepath.Join(t.TempDir(), "global.sqlite3"))
	if err != nil {
		t.Fatalf("OpenGlobal: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func write(t *testing.T, db *DB, key, body string, expected *int) (*WriteResult, error) {
	t.Helper()
	return db.WriteMemory(MemoryWrite{
		Key: key, Type: "project", Description: "desc for " + key, Body: body,
		UpdatedBy: "r-test", ExpectedVersion: expected,
	}, "claude-code")
}

func intp(i int) *int { return &i }

func requireCode(t *testing.T, err error, want serr.Code) *serr.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s error, got nil", want)
	}
	e, ok := serr.As(err)
	if !ok {
		t.Fatalf("error %v is not a *serr.Error", err)
	}
	if e.Code != want {
		t.Fatalf("error code = %s, want %s (%v)", e.Code, want, err)
	}
	return e
}

// The CAS matrix is the whole point of the memory core: it is what stops two
// roots from silently overwriting each other.
func TestWriteMemoryCASMatrix(t *testing.T) {
	db := testProject(t)

	t.Run("create with nil expected_version", func(t *testing.T) {
		res, err := write(t, db, "build-system", "Uses go build.", nil)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if !res.Created || res.Memory.Version != 1 {
			t.Fatalf("created=%v version=%d, want true/1", res.Created, res.Memory.Version)
		}
	})

	t.Run("create over existing key conflicts and returns current", func(t *testing.T) {
		_, err := write(t, db, "build-system", "Something else.", nil)
		e := requireCode(t, err, serr.CASConflict)
		cur, ok := e.Context["current"].(*Memory)
		if !ok || cur.Version != 1 || cur.Body != "Uses go build." {
			t.Fatalf("conflict context did not carry the current entry: %#v", e.Context["current"])
		}
	})

	t.Run("update with correct version bumps", func(t *testing.T) {
		res, err := write(t, db, "build-system", "Uses cmake.", intp(1))
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		if res.Created || res.Memory.Version != 2 {
			t.Fatalf("created=%v version=%d, want false/2", res.Created, res.Memory.Version)
		}
	})

	t.Run("update with stale version conflicts", func(t *testing.T) {
		err := func() error { _, err := write(t, db, "build-system", "Racing write.", intp(1)); return err }()
		e := requireCode(t, err, serr.CASConflict)
		cur := e.Context["current"].(*Memory)
		if cur.Version != 2 || cur.Body != "Uses cmake." {
			t.Fatalf("stale-write conflict returned %#v, want current version 2", cur)
		}
		// The losing write must not have landed.
		got, err := db.ReadMemory("build-system")
		if err != nil {
			t.Fatal(err)
		}
		if got.Body != "Uses cmake." || got.Version != 2 {
			t.Fatalf("losing write mutated the entry: %#v", got)
		}
	})

	t.Run("update of a missing key conflicts", func(t *testing.T) {
		_, err := write(t, db, "no-such-key", "x", intp(1))
		requireCode(t, err, serr.CASConflict)
	})

	t.Run("create preserves created_at across updates", func(t *testing.T) {
		first, err := db.ReadMemory("build-system")
		if err != nil {
			t.Fatal(err)
		}
		res, err := write(t, db, "build-system", "Uses bazel.", intp(2))
		if err != nil {
			t.Fatal(err)
		}
		if res.Memory.CreatedAt != first.CreatedAt {
			t.Fatalf("created_at changed on update: %q → %q", first.CreatedAt, res.Memory.CreatedAt)
		}
	})
}

func TestWriteMemoryValidation(t *testing.T) {
	db := testProject(t)
	cases := []struct {
		name string
		w    MemoryWrite
	}{
		{"bad key", MemoryWrite{Key: "Bad Key", Type: "project", Description: "d", Body: "b", UpdatedBy: "r"}},
		{"leading hyphen", MemoryWrite{Key: "-x", Type: "project", Description: "d", Body: "b", UpdatedBy: "r"}},
		{"bad type", MemoryWrite{Key: "k", Type: "notes", Description: "d", Body: "b", UpdatedBy: "r"}},
		{"empty description", MemoryWrite{Key: "k", Type: "project", Description: "  ", Body: "b", UpdatedBy: "r"}},
		{"empty body", MemoryWrite{Key: "k", Type: "project", Description: "d", Body: "", UpdatedBy: "r"}},
		{"zero expected version", MemoryWrite{Key: "k", Type: "project", Description: "d", Body: "b", UpdatedBy: "r", ExpectedVersion: intp(0)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := db.WriteMemory(c.w, "claude-code")
			requireCode(t, err, serr.InvalidInput)
		})
	}
}

func TestReadListAndDelete(t *testing.T) {
	db := testProject(t)
	if _, err := write(t, db, "alpha", "first body", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := write(t, db, "beta", "second body", nil); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ReadMemory("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadMemory(missing) = %v, want ErrNotFound", err)
	}

	idx, err := db.ListMemories()
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) != 2 || idx[0].Key != "alpha" || idx[1].Key != "beta" {
		t.Fatalf("ListMemories = %#v, want alpha,beta in key order", idx)
	}

	// Delete is CAS-guarded: a stale version must not destroy data.
	if _, err := db.DeleteMemory("alpha", 99, "r-test", "claude-code"); err == nil {
		t.Fatal("delete with a stale version must conflict")
	} else {
		requireCode(t, err, serr.CASConflict)
	}
	if _, err := db.ReadMemory("alpha"); err != nil {
		t.Fatalf("failed delete removed the entry anyway: %v", err)
	}

	deleted, err := db.DeleteMemory("alpha", 1, "r-test", "claude-code")
	if err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}
	if deleted.Body != "first body" {
		t.Fatalf("DeleteMemory returned %#v, want the deleted entry", deleted)
	}
	if _, err := db.ReadMemory("alpha"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("entry survived delete: %v", err)
	}
	if _, err := db.DeleteMemory("alpha", 1, "r-test", "claude-code"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}

	// The audit trail must name what was destroyed.
	var detail string
	err = db.QueryRow(`SELECT detail FROM audit_log WHERE action='memory_delete'`).Scan(&detail)
	if err != nil {
		t.Fatalf("no memory_delete audit row: %v", err)
	}
	if want := BodyHash("first body"); !strings.Contains(detail, want) {
		t.Fatalf("audit detail %q lacks the deleted body hash %q", detail, want)
	}
}

func TestSearchRanksProjectBeforeGlobal(t *testing.T) {
	project, global := testProject(t), testGlobal(t)

	if _, err := write(t, project, "deploy-process", "Deploy with the release script.", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := global.WriteMemory(MemoryWrite{
		Key: "deploy-habits", Type: "user", Description: "global deploy note",
		Body: "Deploy carefully and announce the release.", UpdatedBy: "r-test",
	}, "claude-code"); err != nil {
		t.Fatal(err)
	}

	hits, err := SearchScopes(project, global, "deploy release", DefaultScopes)
	if err != nil {
		t.Fatalf("SearchScopes: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2: %#v", len(hits), hits)
	}
	if hits[0].Scope != "project" || hits[1].Scope != "global" {
		t.Fatalf("scope order = %s,%s — project must come first", hits[0].Scope, hits[1].Scope)
	}
	if hits[0].Snippet == "" {
		t.Fatal("hit carries no snippet")
	}

	// Asking for global first must not change the ordering guarantee.
	hits, err = SearchScopes(project, global, "deploy", Scopes{Global, Project})
	if err != nil {
		t.Fatal(err)
	}
	if hits[0].Scope != "project" {
		t.Fatalf("project must be listed first regardless of requested order, got %s", hits[0].Scope)
	}

	// Single-scope search stays single-scope.
	hits, err = SearchScopes(project, global, "deploy", Scopes{Global})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Scope != "global" {
		t.Fatalf("global-only search returned %#v", hits)
	}
}

func TestSearchHandlesHostileQueries(t *testing.T) {
	db := testProject(t)
	if _, err := write(t, db, "syntax", "Body about quotes and parens.", nil); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`"`, `AND OR NOT`, `(unbalanced`, `foo*`, `NEAR(a b)`, ``} {
		if _, err := db.SearchMemories(q); err != nil {
			t.Fatalf("SearchMemories(%q) errored: %v — queries must never hit FTS syntax errors", q, err)
		}
	}
}

func TestParseScopes(t *testing.T) {
	if got, err := ParseScopes(nil); err != nil || len(got) != 2 {
		t.Fatalf("ParseScopes(nil) = %v, %v — want the default pair", got, err)
	}
	if got, err := ParseScopes([]string{"global", "global"}); err != nil || len(got) != 1 {
		t.Fatalf("ParseScopes deduped = %v, %v", got, err)
	}
	if _, err := ParseScopes([]string{"local"}); err == nil {
		t.Fatal("ParseScopes must reject unknown scopes")
	} else {
		requireCode(t, err, serr.InvalidInput)
	}
}

func TestSuggestSimilarOnCreate(t *testing.T) {
	db := testProject(t)
	if _, err := write(t, db, "build-system", "How the build works", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := write(t, db, "unrelated-topic", "Nothing to do with it", nil); err != nil {
		t.Fatal(err)
	}

	// An AND query would find nothing here; suggestions must still surface the
	// near-duplicate, which is what stops agents creating parallel entries.
	got, err := db.SuggestSimilar("build pipeline configuration", 3)
	if err != nil {
		t.Fatalf("SuggestSimilar: %v", err)
	}
	if len(got) == 0 || got[0].Key != "build-system" {
		t.Fatalf("SuggestSimilar = %#v, want build-system first", got)
	}
	if len(got) > 3 {
		t.Fatalf("SuggestSimilar returned %d entries, want the limit respected", len(got))
	}
}

func TestPromoteMemory(t *testing.T) {
	project, global := testProject(t), testGlobal(t)
	if _, err := write(t, project, "review-style", "Prefer small diffs.", nil); err != nil {
		t.Fatal(err)
	}

	t.Run("stale project version does not touch global", func(t *testing.T) {
		_, err := PromoteMemory(project, global, Promote{
			Key: "review-style", ExpectedVersion: 7, Actor: "r-test", AgentKind: "claude-code",
		})
		requireCode(t, err, serr.CASConflict)
		if _, err := global.ReadMemory("review-style"); !errors.Is(err, ErrNotFound) {
			t.Fatal("a failed promote wrote to the global DB")
		}
	})

	t.Run("promotes as a copy", func(t *testing.T) {
		res, err := PromoteMemory(project, global, Promote{
			Key: "review-style", ExpectedVersion: 1, Actor: "r-test",
			AgentKind: "claude-code", SourceCommonDir: "/repo/.git",
		})
		if err != nil {
			t.Fatalf("PromoteMemory: %v", err)
		}
		if !res.Created || res.Global.Body != "Prefer small diffs." {
			t.Fatalf("promote result = %#v", res)
		}
		if _, err := project.ReadMemory("review-style"); err != nil {
			t.Fatalf("promote removed the project entry: %v", err)
		}
	})

	t.Run("re-promote requires the global version", func(t *testing.T) {
		_, err := PromoteMemory(project, global, Promote{
			Key: "review-style", ExpectedVersion: 1, Actor: "r-test", AgentKind: "claude-code",
		})
		requireCode(t, err, serr.CASConflict)

		res, err := PromoteMemory(project, global, Promote{
			Key: "review-style", ExpectedVersion: 1, GlobalKey: "review-style",
			ExpectedGlobalVersion: intp(1), Actor: "r-test", AgentKind: "claude-code",
		})
		if err != nil {
			t.Fatalf("re-promote with the right global version: %v", err)
		}
		if res.Created || res.Global.Version != 2 {
			t.Fatalf("re-promote = %#v, want an update to version 2", res)
		}
	})

	t.Run("renames into a different global key", func(t *testing.T) {
		res, err := PromoteMemory(project, global, Promote{
			Key: "review-style", ExpectedVersion: 1, GlobalKey: "code-review-style",
			Actor: "r-test", AgentKind: "claude-code",
		})
		if err != nil {
			t.Fatalf("promote with a renamed global key: %v", err)
		}
		if !res.Created || res.Global.Key != "code-review-style" {
			t.Fatalf("promote result = %#v", res)
		}
	})

	t.Run("audits both databases", func(t *testing.T) {
		var n int
		if err := global.QueryRow(`SELECT count(*) FROM audit_log WHERE action='memory_promote'`).Scan(&n); err != nil || n == 0 {
			t.Fatalf("global promote audit rows = %d, err=%v", n, err)
		}
		if err := project.QueryRow(`SELECT count(*) FROM audit_log WHERE action='memory_promote'`).Scan(&n); err != nil || n == 0 {
			t.Fatalf("project promote audit rows = %d, err=%v", n, err)
		}
	})
}

func TestWriteAuditsCreateAndUpdate(t *testing.T) {
	db := testProject(t)
	if _, err := write(t, db, "topic", "one", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := write(t, db, "topic", "two", intp(1)); err != nil {
		t.Fatal(err)
	}
	var creates, updates int
	if err := db.QueryRow(`SELECT count(*) FROM audit_log WHERE action='memory_create'`).Scan(&creates); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM audit_log WHERE action='memory_update'`).Scan(&updates); err != nil {
		t.Fatal(err)
	}
	if creates != 1 || updates != 1 {
		t.Fatalf("audit rows: creates=%d updates=%d, want 1/1", creates, updates)
	}
}
