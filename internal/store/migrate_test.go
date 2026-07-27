package store

import (
	"strings"
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
	// Every migration after the one under test has to be undone here too, or the
	// replay runs it against schema it has already changed. That is the cost this
	// test charges each new migration, and it is the point: a migration that
	// cannot state how to undo itself is one nobody can reason about replaying.
	if _, err := db.Exec(`ALTER TABLE claims DROP COLUMN repo_id`); err != nil {
		t.Fatalf("undoing 0007 (column): %v", err)
	}
	if _, err := db.Exec(`DROP TABLE repos`); err != nil {
		t.Fatalf("undoing 0007 (table): %v", err)
	}
	// 0008 in reverse order of creation: the children reference the parent.
	for _, tbl := range []string{"memory_evidence_path", "memory_evidence_member", "memory_evidence_policy"} {
		if _, err := db.Exec(`DROP TABLE ` + tbl); err != nil {
			t.Fatalf("undoing 0008 (%s): %v", tbl, err)
		}
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

// Every declared host must be storable. This used to prove that AgentKinds and
// a CHECK constraint agreed; 0006 removed the CHECK, so it now proves the
// simpler thing the rebuild could have broken — that a real row still goes in.
func TestEveryAgentKindCanBeStored(t *testing.T) {
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
}

// The set is still closed — it is just closed in Go now.
//
// This is the half that matters, because it is the half an agent actually
// reaches: nothing writes a root without going through RegisterRoot first. The
// CHECK could only ever have caught a caller that had already bypassed this,
// and it would have caught it with a raw SQLite constraint error instead of a
// sentence naming the hosts that exist.
func TestRegisterStillRefusesAnUnknownHost(t *testing.T) {
	db := testProject(t)

	if _, _, err := db.RegisterRoot(Registration{
		AgentKind: "cursor",
		Worktree:  "/wt",
	}); err == nil {
		t.Fatal("RegisterRoot accepted a host that is not in AgentKinds")
	} else if !strings.Contains(err.Error(), "cursor") {
		t.Errorf("the rejection should name the kind it refused, got: %v", err)
	}
}

// 0006 deliberately left agent_kind unconstrained in the schema, so that adding
// a host is a Go edit rather than a table rebuild. If someone reintroduces a
// CHECK here, the next host to be added will silently fail to register in the
// field while passing every test that only exercises the hosts we already have —
// so say it out loud instead.
func TestTheSchemaDoesNotConstrainAgentKind(t *testing.T) {
	db := testProject(t)

	if _, err := db.Exec(
		`INSERT INTO roots(root_id, agent_kind, worktree, registered_at, last_seen_at)
		 VALUES(?, ?, ?, ?, ?)`,
		"r-future", "a-host-that-does-not-exist-yet", "/wt", Now(), Now(),
	); err != nil {
		t.Errorf("the schema is constraining agent_kind again: %v\n"+
			"0006 removed that on purpose — the closed set lives in internal/hosts, "+
			"and a CHECK here costs a table rebuild per host.", err)
	}
}
