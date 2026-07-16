package store

import (
	"testing"
)

// indexNames returns the indexes SQLite has attached to a table, excluding the
// automatic ones it creates for PRIMARY KEY and UNIQUE constraints.
func indexNames(t *testing.T, db *DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query(
		`SELECT name FROM sqlite_master
		  WHERE type = 'index' AND tbl_name = ? AND sql IS NOT NULL`, table)
	if err != nil {
		t.Fatalf("reading indexes on %s: %v", table, err)
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// A migration that rebuilds a table must leave its indexes behind, and this is
// the test that says so out loud.
//
// 0003 rebuilt roots to widen the agent_kind constraint and lost both of its
// indexes doing it: an index follows its table through a rename, so the DROP of
// the old table took them with it. Every row survived and the migration
// reported success, which is exactly why nothing caught it — the damage is
// invisible to any test that only checks data. It surfaced instead as
// RootBySession, on the claim guard's 250ms path, quietly becoming a full scan.
func TestRebuildingRootsKeepsItsIndexes(t *testing.T) {
	db := testProject(t)

	got := indexNames(t, db, "roots")
	for _, want := range []string{"idx_roots_session", "idx_roots_active"} {
		if !got[want] {
			t.Errorf("roots is missing %s after migration; have %v", want, got)
		}
	}
}

// The indexes must also come back for a database that already ran the version
// of 0003 that destroyed them — the state every existing project is in.
//
// The setup rewinds the database to a real v3 rather than only rewinding
// schema_migrations: migrate() resumes from MAX(version), so a half-rewound
// database would re-run migrations against schema they had already changed.
// That is not a hypothetical — writing this test that way is how 0005 got
// caught re-adding a column that was still there.
func TestRestoringIndexesOnADatabaseThatAlreadyLostThem(t *testing.T) {
	db := testProject(t)

	for _, idx := range []string{"idx_roots_session", "idx_roots_active"} {
		if _, err := db.Exec(`DROP INDEX ` + idx); err != nil {
			t.Fatalf("dropping %s: %v", idx, err)
		}
	}
	if _, err := db.Exec(`ALTER TABLE roots DROP COLUMN model`); err != nil {
		t.Fatalf("undoing 0005: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version >= 4`); err != nil {
		t.Fatal(err)
	}

	if err := db.migrate(); err != nil {
		t.Fatalf("re-running migrations: %v", err)
	}

	got := indexNames(t, db, "roots")
	for _, want := range []string{"idx_roots_session", "idx_roots_active"} {
		if !got[want] {
			t.Errorf("0004 did not restore %s; have %v", want, got)
		}
	}
}

// The rebuild in 0003 must not disturb the foreign keys pointing at roots.
// Without legacy_alter_table=ON, renaming roots out of the way rewrites every
// child REFERENCES clause to follow it, and the children end up pointing at a
// table that the migration then drops.
func TestRebuildingRootsKeepsChildForeignKeysIntact(t *testing.T) {
	db := testProject(t)

	root := mustRoot(t, db, "antigravity", "sess-fk")
	if _, err := db.Exec(
		`INSERT INTO claims(scope_path, recursive, root_id, worktree, reason, created_at, expires_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		"src", 1, root, "/wt", "holding", Now(), Now(),
	); err != nil {
		t.Fatalf("a claim could not reference its root: %v", err)
	}

	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Error("foreign_key_check reports violations after the roots rebuild")
	}
}

// The widened constraint has to actually admit the kind it was widened for,
// and still refuse everything else.
func TestAgentKindConstraintAfterTheRebuild(t *testing.T) {
	db := testProject(t)

	for _, kind := range AgentKinds {
		if _, err := db.Exec(
			`INSERT INTO roots(root_id, agent_kind, worktree, registered_at, last_seen_at)
			 VALUES(?, ?, ?, ?, ?)`,
			"r-"+kind, kind, "/wt", Now(), Now(),
		); err != nil {
			t.Errorf("agent_kind %q is in AgentKinds but the schema rejects it: %v", kind, err)
		}
	}
	if _, err := db.Exec(
		`INSERT INTO roots(root_id, agent_kind, worktree, registered_at, last_seen_at)
		 VALUES(?, ?, ?, ?, ?)`,
		"r-nope", "cursor", "/wt", Now(), Now(),
	); err == nil {
		t.Error("the schema accepted an agent_kind that is not in AgentKinds")
	}
}
