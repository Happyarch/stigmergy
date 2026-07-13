package store

import (
	"path/filepath"
	"testing"
)

func TestOpenProjectMigratesAndFTS5Works(t *testing.T) {
	commonDir := t.TempDir()

	db, err := OpenProject(commonDir)
	if err != nil {
		t.Fatalf("OpenProject: %v", err)
	}
	defer db.Close()

	if err := db.ProbeFTS5(); err != nil {
		t.Fatalf("FTS5 probe failed — load-bearing dependency missing: %v", err)
	}

	v, err := db.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	latest, err := LatestVersion(Project)
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if v != latest {
		t.Fatalf("schema version %d, want latest %d", v, latest)
	}

	// FTS index must track the content table via triggers.
	_, err = db.Exec(
		`INSERT INTO memories(key, type, description, body, updated_by, created_at, updated_at)
		 VALUES('build-system', 'project', 'How the build works', 'Uses make and go build.', 'r-test', ?, ?)`,
		Now(), Now(),
	)
	if err != nil {
		t.Fatalf("insert memory: %v", err)
	}
	var key string
	err = db.QueryRow(
		`SELECT key FROM memories_fts WHERE memories_fts MATCH ?`, FTSQuery("go build"),
	).Scan(&key)
	if err != nil {
		t.Fatalf("FTS search: %v", err)
	}
	if key != "build-system" {
		t.Fatalf("FTS search returned %q, want build-system", key)
	}

	// Update must reindex; stale text must stop matching.
	if _, err := db.Exec(`UPDATE memories SET body = 'Uses cmake now.', version = 2 WHERE key = 'build-system'`); err != nil {
		t.Fatalf("update memory: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM memories_fts WHERE memories_fts MATCH ?`, FTSQuery("cmake")).Scan(&n); err != nil || n != 1 {
		t.Fatalf("FTS after update: n=%d err=%v, want 1 match", n, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM memories_fts WHERE memories_fts MATCH '"go" AND "build"'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("FTS stale text: n=%d err=%v, want 0 matches", n, err)
	}

	// Delete must remove from the index.
	if _, err := db.Exec(`DELETE FROM memories WHERE key = 'build-system'`); err != nil {
		t.Fatalf("delete memory: %v", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM memories_fts WHERE memories_fts MATCH ?`, FTSQuery("cmake")).Scan(&n); err != nil || n != 0 {
		t.Fatalf("FTS after delete: n=%d err=%v, want 0 matches", n, err)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	commonDir := t.TempDir()
	for i := 0; i < 2; i++ {
		db, err := OpenProject(commonDir)
		if err != nil {
			t.Fatalf("OpenProject #%d: %v", i+1, err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i+1, err)
		}
	}
}

func TestOpenGlobalHasNoProjectTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "global.sqlite3")
	db, err := OpenGlobal(path)
	if err != nil {
		t.Fatalf("OpenGlobal: %v", err)
	}
	defer db.Close()

	var n int
	err = db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('roots','claims','mailbox_threads')`,
	).Scan(&n)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	if n != 0 {
		t.Fatalf("global DB has %d project-only tables, want 0", n)
	}
	var kind string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='db_kind'`).Scan(&kind); err != nil || kind != "global" {
		t.Fatalf("meta db_kind=%q err=%v, want global", kind, err)
	}
}

func TestReadOnlyOpenRequiresExistingDB(t *testing.T) {
	commonDir := t.TempDir()
	if _, err := OpenProject(commonDir, ReadOnly()); err == nil {
		t.Fatal("ReadOnly open of a missing DB must fail, not create it")
	}
	db, err := OpenProject(commonDir)
	if err != nil {
		t.Fatalf("OpenProject: %v", err)
	}
	db.Close()
	ro, err := OpenProject(commonDir, ReadOnly(), BusyTimeout(250))
	if err != nil {
		t.Fatalf("ReadOnly open of existing DB: %v", err)
	}
	defer ro.Close()
	if _, err := ro.Exec(`INSERT INTO meta(key, value) VALUES('x','y')`); err == nil {
		t.Fatal("write through a ReadOnly handle must fail")
	}
}

func TestFTSQueryQuotesTokens(t *testing.T) {
	got := FTSQuery(`hello AND (world"`)
	want := `"hello" "AND" "(world"""`
	if got != want {
		t.Fatalf("FTSQuery = %q, want %q", got, want)
	}
}
