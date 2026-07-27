package store

import (
	"testing"
)

// register a root and take a claim in a named repository.
func claimIn(t *testing.T, db *DB, rootID, session, repoID, scope string, recursive bool) {
	t.Helper()
	if _, _, err := db.RegisterRoot(Registration{
		RootID: rootID, AgentKind: "claude-code", Worktree: "/wt", SessionLabel: session,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcquireClaim(ClaimRequest{
		ScopePath: scope, Recursive: recursive, RootID: rootID, RepoID: repoID,
		Worktree: "/wt", Reason: "working on it",
	}); err != nil {
		t.Fatal(err)
	}
}

// The reason the repo dimension exists at all.
//
// "README.md" names a different file in every repository. Without the dimension,
// a claim on the client's would block an agent editing the service's — a pure
// false positive, with no shared assumption behind it to justify the cost.
func TestTheSamePathInTwoRepositoriesIsTwoClaims(t *testing.T) {
	db := testProject(t)
	claimIn(t, db, "r-1", "sess-1", "client", "README.md", false)

	blocked, err := db.ClaimsCovering("client", "README.md", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 1 {
		t.Fatalf("the claim did not cover its own repo's README.md (got %d)", len(blocked))
	}

	free, err := db.ClaimsCovering("service", "README.md", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(free) != 0 {
		t.Errorf("a claim in 'client' covered the same path in 'service': %+v", free)
	}
}

// Two agents may hold the same path in different repositories at once. Under a
// repo-blind overlap test the second acquire would be refused as a conflict.
func TestTwoRepositoriesMayHoldTheSamePathAtOnce(t *testing.T) {
	db := testProject(t)
	claimIn(t, db, "r-1", "sess-1", "client", "src", true)

	if _, _, err := db.RegisterRoot(Registration{
		RootID: "r-2", AgentKind: "codex", Worktree: "/wt2", SessionLabel: "sess-2",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcquireClaim(ClaimRequest{
		ScopePath: "src", Recursive: true, RootID: "r-2", RepoID: "service",
		Worktree: "/wt2", Reason: "different repository entirely",
	}); err != nil {
		t.Fatalf("claiming src in a second repository was refused: %v", err)
	}
}

// Within one repository nothing changes: overlapping scopes still conflict, and
// the conflict still names the holder.
func TestOverlapWithinOneRepositoryIsUnchanged(t *testing.T) {
	db := testProject(t)
	claimIn(t, db, "r-1", "sess-1", "client", "src", true)

	if _, _, err := db.RegisterRoot(Registration{
		RootID: "r-2", AgentKind: "codex", Worktree: "/wt", SessionLabel: "sess-2",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := db.AcquireClaim(ClaimRequest{
		ScopePath: "src/api/handler.go", RootID: "r-2", RepoID: "client",
		Worktree: "/wt", Reason: "a file inside the claimed subtree",
	})
	if err == nil {
		t.Fatal("a file inside another root's recursive claim was allowed")
	}
}

// The empty repo id means "this project's only repository", and it must go on
// matching everything. This is what a database migrated to 0007 but not yet
// backfilled looks like, and it is the property that keeps the upgrade window
// down to the time it takes to install a binary.
func TestTheLegacyRepoIDStillBlocks(t *testing.T) {
	db := testProject(t)
	claimIn(t, db, "r-1", "sess-1", LegacyRepoID, "src", true)

	for _, repo := range []string{LegacyRepoID, "client", "anything-at-all"} {
		got, err := db.ClaimsCovering(repo, "src/main.go", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Errorf("a legacy claim did not block repo %q (got %d covering claims)", repo, len(got))
		}
	}
}

// Qualified is the one spelling agents read and write. A single-repo project
// must not start printing a prefix naming the only repository there is.
func TestQualifiedScopeSpelling(t *testing.T) {
	for _, tc := range []struct{ repo, scope, want string }{
		{LegacyRepoID, "src/main.go", "src/main.go"},
		{"client", "src/main.go", "client:src/main.go"},
		{"client", ".", "client:."},
	} {
		if got := QualifyScope(tc.repo, tc.scope); got != tc.want {
			t.Errorf("QualifyScope(%q, %q) = %q, want %q", tc.repo, tc.scope, got, tc.want)
		}
	}
}

func TestEnsureSelfRepoIsIdempotentAndAdoptsLegacyClaims(t *testing.T) {
	db := testProject(t)
	claimIn(t, db, "r-1", "sess-1", LegacyRepoID, "src", true)

	if err := db.EnsureSelfRepo("app", "/repo/.git", "/repo"); err != nil {
		t.Fatalf("EnsureSelfRepo: %v", err)
	}
	rows, err := db.Repos()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].RepoID != "app" {
		t.Fatalf("repos = %+v, want one row named app", rows)
	}

	active, err := db.ActiveClaims("")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].RepoID != "app" {
		t.Fatalf("the legacy claim was not adopted: %+v", active)
	}

	// Again, and from the same common dir under a different proposed name: the
	// existing membership wins rather than a second row appearing.
	if err := db.EnsureSelfRepo("renamed", "/repo/.git", "/repo"); err != nil {
		t.Fatalf("second EnsureSelfRepo: %v", err)
	}
	rows, _ = db.Repos()
	if len(rows) != 1 || rows[0].RepoID != "app" {
		t.Fatalf("repos = %+v after re-running, want the original single row", rows)
	}
}

// One checkout must not be able to join a project twice under two names: its
// claims would split in half, and neither half would block the other.
func TestARepositoryCannotBeAddedTwice(t *testing.T) {
	db := testProject(t)
	if err := db.AddRepo("app", "/repo/.git", "/repo"); err != nil {
		t.Fatal(err)
	}
	if err := db.AddRepo("app-again", "/repo/.git", "/repo"); err == nil {
		t.Fatal("the same common dir was accepted under a second name")
	}
}

// Removing a member must release its claims. A claim scoped to a repository
// nobody can resolve would block edits with no way to negotiate it away: the
// owner cannot release what it can no longer name.
func TestRemovingARepositoryReleasesItsClaims(t *testing.T) {
	db := testProject(t)
	if err := db.AddRepo("client", "/c/.git", "/c"); err != nil {
		t.Fatal(err)
	}
	claimIn(t, db, "r-1", "sess-1", "client", "src", true)

	if err := db.RemoveRepo("client"); err != nil {
		t.Fatalf("RemoveRepo: %v", err)
	}
	active, err := db.ActiveClaims("")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Errorf("claims survived their repository's removal: %+v", active)
	}
}

// The project name is printed for every project on the machine by
// `doctor --all`, and stored as the registry label. It is agent- or user-typed
// text rendered inline in a list, which is the same shape as a claim reason and
// gets the same rules.
func TestProjectNameIsValidated(t *testing.T) {
	db := testProject(t)

	for name, value := range map[string]string{
		"an ANSI escape":  "\x1b[2J\x1b[Hcleared",
		"a line break":    "first line\nsecond line",
		"NUL":             "before\x00after",
		"a bidi override": "the project is \u202esuoicilam",
	} {
		t.Run(name, func(t *testing.T) {
			if err := db.SetMeta(MetaNameKey, value); err == nil {
				t.Errorf("accepted %s — it renders in every doctor --all listing", name)
			}
		})
	}

	if err := db.SetMeta(MetaNameKey, "  the naviamp project  "); err != nil {
		t.Fatalf("an ordinary name was refused: %v", err)
	}
	if got := db.Name(); got != "the naviamp project" {
		t.Errorf("name = %q, want it trimmed", got)
	}
}
