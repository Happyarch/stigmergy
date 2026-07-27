package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/happyarch/stigmergy/internal/gitx"
	"github.com/happyarch/stigmergy/internal/ids"
	"github.com/happyarch/stigmergy/internal/project"
	"github.com/happyarch/stigmergy/internal/store"
)

type multiRepo struct {
	db    *store.DB
	roots map[string]string // member id -> worktree
}

// newMultiRepo builds a project spanning two real git repositories that share
// one database outside both of them, exactly as `stigmergy project create` does.
func newMultiRepo(t *testing.T, members ...string) *multiRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	projectID := "p-" + ids.NewRootID()[2:]
	dbPath, err := project.DBPathFor(projectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenProjectAt(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	m := &multiRepo{db: db, roots: map[string]string{}}
	for _, id := range members {
		worktree, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("git", "-C", worktree, "init", "-b", "main").CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
		if err := os.MkdirAll(filepath.Join(worktree, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
		repo, err := gitx.Resolve(worktree)
		if err != nil {
			t.Fatal(err)
		}
		if err := project.WritePointer(repo.CommonDir, project.Pointer{
			ProjectID: projectID, RepoID: id, CreatedAt: store.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.AddRepo(id, repo.CommonDir, worktree); err != nil {
			t.Fatal(err)
		}
		m.roots[id] = worktree
	}
	return m
}

func (m *multiRepo) claim(t *testing.T, session, repoID, scope string, recursive bool) string {
	t.Helper()
	root, _, err := m.db.RegisterRoot(store.Registration{
		RootID: ids.NewRootID(), AgentKind: "claude-code",
		Worktree: m.roots[repoID], SessionLabel: session,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.db.AcquireClaim(store.ClaimRequest{
		ScopePath: scope, Recursive: recursive, RootID: root.RootID, RepoID: repoID,
		Worktree: m.roots[repoID], Reason: "refactoring",
	}); err != nil {
		t.Fatal(err)
	}
	return root.RootID
}

func (m *multiRepo) path(repoID, rel string) string {
	return filepath.Join(m.roots[repoID], rel)
}

// THE regression this whole change exists for.
//
// An agent standing in one repository edits a file in another repository of the
// same project, and that file is claimed by somebody else. Before the roster,
// Guard normalised every path against the cwd's worktree, decided the path was
// outside it, and skipped it as ungoverned — so the edit was ALLOWED, silently,
// straight through an active claim. Fails open, no error anywhere.
func TestAClaimInOneRepositoryBlocksAnEditFromAnother(t *testing.T) {
	m := newMultiRepo(t, "client", "service")
	m.claim(t, "owner-session", "service", "src", true)

	d := Guard("claude-code", "other-session", m.roots["client"],
		[]string{m.path("service", "src/handler.go")})

	if d.Allow {
		t.Fatal("an edit to a claimed file in a sibling repository was allowed")
	}
	if len(d.Conflicts) != 1 {
		t.Fatalf("got %d conflicts, want 1", len(d.Conflicts))
	}
	if got := d.Conflicts[0].RepoID; got != "service" {
		t.Errorf("conflict repo = %q, want service", got)
	}
	// The agent has to be told which repository, or it cannot act on this.
	if want := "service:src"; !strings.Contains(d.Reason, want) {
		t.Errorf("the denial does not name the repository (%q):\n%s", want, d.Reason)
	}
}

// The other direction, and the reason repo_id had to exist at all: the same
// relative path in two repositories is two different files.
func TestTheSamePathInASiblingRepositoryIsNotBlocked(t *testing.T) {
	m := newMultiRepo(t, "client", "service")
	m.claim(t, "owner-session", "service", "README.md", false)

	d := Guard("claude-code", "other-session", m.roots["client"],
		[]string{m.path("client", "README.md")})

	if !d.Allow {
		t.Fatalf("editing the client's own README was blocked by a claim on the service's:\n%s", d.Reason)
	}
}

// A path in neither member is still nobody's business.
func TestAPathOutsideEveryMemberIsUngoverned(t *testing.T) {
	m := newMultiRepo(t, "client", "service")
	m.claim(t, "owner-session", "service", ".", true)

	outside := filepath.Join(t.TempDir(), "elsewhere.go")
	d := Guard("claude-code", "other-session", m.roots["client"], []string{outside})
	if !d.Allow {
		t.Errorf("a path outside the project was blocked:\n%s", d.Reason)
	}
}

// One session, both repositories: its own claims must not block it wherever it
// is standing.
func TestARootsOwnClaimsDoNotBlockItAcrossRepositories(t *testing.T) {
	m := newMultiRepo(t, "client", "service")
	m.claim(t, "mine", "service", "src", true)

	d := Guard("claude-code", "mine", m.roots["client"],
		[]string{m.path("service", "src/handler.go")})
	if !d.Allow {
		t.Errorf("a root was blocked by its own claim in a sibling repository:\n%s", d.Reason)
	}
}

// An edit list spanning both repositories is decided per path, not by whichever
// one happened to come first.
func TestAnEditListSpanningBothRepositoriesIsDecidedPerPath(t *testing.T) {
	m := newMultiRepo(t, "client", "service")
	m.claim(t, "owner-session", "service", "src", true)

	d := Guard("claude-code", "other-session", m.roots["client"], []string{
		m.path("client", "src/free.go"),   // unclaimed
		m.path("service", "src/taken.go"), // claimed by someone else
	})
	if d.Allow {
		t.Fatal("a batch containing one claimed path was allowed")
	}
	if len(d.Conflicts) != 1 {
		t.Fatalf("got %d conflicts, want exactly the one claimed path", len(d.Conflicts))
	}
}

// GuardWorkspaces exists for a host whose mounted workspaces can belong to
// different projects entirely — separate databases, separate rosters.
func TestGuardWorkspacesSpansTwoProjects(t *testing.T) {
	// Two independent single-repo projects.
	a, b := newFixture(t), newFixture(t)
	a.claim(t, "owner-a", "src", true)

	cwdFor := func(p string) string {
		if strings.Contains(p, b.worktree) {
			return b.worktree
		}
		return a.worktree
	}
	d := GuardWorkspaces("claude-code", "other-session", cwdFor, []string{
		filepath.Join(b.worktree, "src", "fine.go"),  // project B, unclaimed
		filepath.Join(a.worktree, "src", "taken.go"), // project A, claimed
	})
	if d.Allow {
		t.Fatal("an edit claimed in the other project's database was allowed")
	}
	if len(d.Conflicts) != 1 {
		t.Fatalf("got %d conflicts, want 1", len(d.Conflicts))
	}
}
