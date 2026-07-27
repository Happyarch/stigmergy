package hooks

import (
	"testing"

	"github.com/happyarch/stigmergy/internal/store"
)

// rollBackOneVersion makes the fixture's database look like it was last touched
// by an older stigmergy, by forgetting that the newest migration was applied.
// Nothing about the tables changes — only the recorded version — which is
// exactly the situation a hook meets after someone installs a binary carrying a
// migration and has not run `stigmergy doctor` yet.
func rollBackOneVersion(t *testing.T, f *fixture) int {
	t.Helper()
	latest, err := store.LatestVersion(store.Project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`DELETE FROM schema_migrations WHERE version = ?`, latest); err != nil {
		t.Fatal(err)
	}
	if v, _ := f.db.SchemaVersion(); v == latest {
		t.Fatalf("rollback did not take: still v%d", v)
	}
	return latest
}

func schemaVersion(t *testing.T, f *fixture) int {
	t.Helper()
	v, err := f.db.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// No hook may migrate the schema, and these three used to.
//
// EndSession, AuditCodexConflict and AuditDenial each opened the project with a
// plain store.OpenProject, which creates and migrates. So ending a session or
// recording a blocked edit would silently upgrade the schema of whatever
// repository the agent happened to be standing in — under a 250ms lock timeout,
// racing whatever else held the database open, and in a repository the user may
// never have run `stigmergy init` in this decade. The delivery hooks had been
// fixed for exactly this reason (see the comment on openProject) and these three
// were simply missed.
//
// The rule they now share: a database this binary does not recognise is left
// alone. The agent is not stranded by that, because the claim guard checks the
// same version and fails closed loudly.
func TestWriteHooksNeverMigrateTheSchema(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(f *fixture)
	}{
		{"EndSession", func(f *fixture) {
			EndSession("claude-code", "sess-1", f.worktree)
		}},
		{"AuditCodexConflict", func(f *fixture) {
			AuditCodexConflict("sess-1", f.worktree, Decision{
				Conflicts: []store.Claim{{ID: 1, ScopePath: "src/a.go", RootID: "r-x", Reason: "why"}},
			})
		}},
		{"AuditDenial", func(f *fixture) {
			AuditDenial("claude-code", "sess-1", f.worktree, Decision{
				Conflicts: []store.Claim{{ID: 1, ScopePath: "src/a.go", RootID: "r-x", Reason: "why"}},
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			latest := rollBackOneVersion(t, f)
			before := schemaVersion(t, f)

			tc.run(f)

			if after := schemaVersion(t, f); after != before {
				t.Errorf("%s migrated the database from v%d to v%d; hooks must never migrate",
					tc.name, before, after)
			}
			if before == latest {
				t.Fatal("the fixture was not actually rolled back")
			}
		})
	}
}

// The roster shown at session start reads claims out of the database, so it must
// be held to the same version gate as everything else that reads them. It was
// the one reader that was not: it opened read-only and went straight to
// ActiveClaims, so a schema this binary does not understand would have been
// rendered as though it did.
func TestActiveClaimSummaryRespectsTheVersionGate(t *testing.T) {
	f := newFixture(t)
	f.claim(t, "other-session", "src", true)

	if got := claimSummaryAt(f.worktree); got == "" {
		t.Fatal("a held claim was not summarised at all")
	}

	rollBackOneVersion(t, f)
	if got := claimSummaryAt(f.worktree); got != "" {
		t.Errorf("claims were summarised from a schema this binary does not expect:\n%s", got)
	}
}

// claimSummaryAt summarises the claims in a working directory the way the
// session-start hook does: one open, and nothing at all to say when the project
// cannot be opened.
func claimSummaryAt(cwd string) string {
	p, ok := openQuietly(cwd, readOnly)
	if !ok {
		return ""
	}
	defer p.DB.Close()
	return activeClaimSummary(p)
}

// The three-way outcome is the whole reason resolveProject exists, so it is
// worth stating directly rather than only through its callers.
func TestResolveProjectSeparatesItsThreeOutcomes(t *testing.T) {
	t.Run("not a git repository", func(t *testing.T) {
		p, why := resolveProject(t.TempDir(), readOnly)
		if p != nil || why != nil {
			t.Fatalf("got p=%v why=%v, want both nil — nothing to govern", p, why)
		}
	})

	t.Run("a project that is here and readable", func(t *testing.T) {
		f := newFixture(t)
		p, why := resolveProject(f.worktree, readOnly)
		if why != nil {
			t.Fatalf("unexpected unavailable: %+v", why)
		}
		if p == nil {
			t.Fatal("an adopted project resolved to nothing")
		}
		defer p.DB.Close()
		if p.Repo == nil || p.Repo.WorktreeRoot != f.worktree {
			t.Errorf("resolved the wrong worktree: %+v", p.Repo)
		}
	})

	t.Run("a project that cannot be verified", func(t *testing.T) {
		f := newFixture(t)
		rollBackOneVersion(t, f)
		p, why := resolveProject(f.worktree, readOnly)
		if p != nil {
			p.DB.Close()
			t.Fatal("a schema-mismatched project was opened anyway")
		}
		if why == nil {
			t.Fatal("a schema mismatch was reported as 'nothing to govern'")
		}
		if why.Reason == "" {
			t.Error("no reason given; the guard puts this in front of the agent")
		}
	})
}
