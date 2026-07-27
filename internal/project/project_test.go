package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/happyarch/stigmergy/internal/gitx"
	"github.com/happyarch/stigmergy/internal/store"
)

// newRepo makes a real git repository, optionally adopted by stigmergy.
func newRepo(t *testing.T, adopt bool) (worktree string, repo *gitx.Repo) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	worktree, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", worktree, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	repo, err = gitx.Resolve(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if adopt {
		db, err := store.OpenProject(repo.CommonDir)
		if err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
	return worktree, repo
}

// The shape every existing project has. No pointer file, database in the git
// common dir, and nothing about resolution changed for it.
func TestASingleRepoProjectResolvesToItsGitDatabase(t *testing.T) {
	worktree, repo := newRepo(t, true)

	p, err := Resolve(worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if p.MultiRepo() {
		t.Error("a repository with no pointer reported itself as multi-repo")
	}
	if p.ID != "" {
		t.Errorf("ID = %q, want empty for a single-repo project", p.ID)
	}
	if want := store.ProjectDBPath(repo.CommonDir); p.DBPath != want {
		t.Errorf("DBPath = %q, want %q", p.DBPath, want)
	}
}

// An un-adopted repository still resolves — to the place its database WOULD go.
//
// The two questions are deliberately separate. context_open answers "where would
// state live?" and then creates the database, which is how an agent adopts a
// project from inside a session. Hooks answer "is stigmergy enabled here?" via
// Adopted, and stay out of the way when it is false. Collapsing them would mean
// either hooks creating databases in every repository an agent wanders into, or
// agents unable to adopt anything.
func TestAnUnadoptedRepositoryResolvesButIsNotAdopted(t *testing.T) {
	worktree, repo := newRepo(t, false)

	p, err := Resolve(worktree)
	if err != nil {
		t.Fatalf("Resolve on an un-adopted repository = %v, want the path its database would take", err)
	}
	if p.Adopted() {
		t.Error("a repository with no database reported itself adopted")
	}
	if want := store.ProjectDBPath(repo.CommonDir); p.DBPath != want {
		t.Errorf("DBPath = %q, want %q", p.DBPath, want)
	}
}

func TestAnAdoptedRepositorySaysSo(t *testing.T) {
	worktree, _ := newRepo(t, true)
	p, err := Resolve(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Adopted() {
		t.Error("a repository with a database reported itself un-adopted")
	}
}

func TestSomewhereThatIsNotARepositoryIsNotAProject(t *testing.T) {
	if _, err := Resolve(t.TempDir()); err != ErrNotARepo {
		t.Fatalf("Resolve outside a repo = %v, want ErrNotARepo", err)
	}
}

// A pointer redirects the project to a shared database outside every member.
func TestAPointerRedirectsToTheSharedDatabase(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	worktree, repo := newRepo(t, true)

	if err := WritePointer(repo.CommonDir, Pointer{
		ProjectID: "p-abc123", RepoID: "sidecar", CreatedAt: "now",
	}); err != nil {
		t.Fatal(err)
	}

	p, err := Resolve(worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !p.MultiRepo() || p.ID != "p-abc123" {
		t.Errorf("ID = %q, MultiRepo = %v", p.ID, p.MultiRepo())
	}
	if p.SelfID != "sidecar" {
		t.Errorf("SelfID = %q, want sidecar", p.SelfID)
	}
	want, _ := DBPathFor("p-abc123")
	if p.DBPath != want {
		t.Errorf("DBPath = %q, want %q", p.DBPath, want)
	}
	// The pointer wins even though a database still sits in the git common dir:
	// membership is an explicit statement and outranks what is lying around.
	if p.DBPath == store.ProjectDBPath(repo.CommonDir) {
		t.Error("the pointer was ignored in favour of the local database")
	}
}

// Resolution never walks up looking for a project and never infers membership
// from a sibling. A repository joins a project because someone wrote a pointer
// into it, and nothing else — otherwise a stray file high in a directory tree
// would quietly adopt everything beneath it.
func TestAProjectIsNeverInheritedFromAParentDirectory(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A pointer-shaped file in the parent, which is not even a git repo.
	if err := os.WriteFile(filepath.Join(parent, PointerName),
		[]byte(`{"project":"p-parent","repo":"parent"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", child, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	repo, err := gitx.Resolve(child)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenProject(repo.CommonDir)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	p, err := Resolve(child)
	if err != nil {
		t.Fatal(err)
	}
	if p.MultiRepo() {
		t.Fatalf("a repository was captured by a parent directory's pointer (project %q)", p.ID)
	}
}

func TestAMalformedPointerIsAnErrorNotSilence(t *testing.T) {
	worktree, repo := newRepo(t, true)
	if err := os.WriteFile(PointerPath(repo.CommonDir), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(worktree); err == nil || err == ErrNotARepo {
		t.Fatalf("a malformed pointer resolved to %v; it must say what is wrong", err)
	}
}

// Members can nest — a repository checked out inside another's tree is unusual
// but legal — so the longest matching worktree root wins.
func TestContainingPrefersTheInnermostMember(t *testing.T) {
	p := &Project{
		ID: "p-1",
		Members: []Member{
			{ID: "outer", WorktreeRoot: "/code/app"},
			{ID: "inner", WorktreeRoot: "/code/app/vendor/lib"},
		},
	}
	for _, tc := range []struct{ path, want string }{
		{"/code/app/main.go", "outer"},
		{"/code/app/vendor/lib/x.go", "inner"},
		{"/code/app/vendor/lib", "inner"},
	} {
		got := p.Containing(tc.path)
		if got == nil || got.ID != tc.want {
			t.Errorf("Containing(%q) = %v, want %s", tc.path, got, tc.want)
		}
	}
	if got := p.Containing("/somewhere/else/x.go"); got != nil {
		t.Errorf("Containing outside every member = %v, want nil", got)
	}
}

// Component-wise, never a raw string prefix — the same rule the overlap test
// rests on. A member at /code/app must not swallow /code/app-old.
func TestContainingDoesNotMatchOnASharedPrefix(t *testing.T) {
	p := &Project{ID: "p-1", Members: []Member{{ID: "app", WorktreeRoot: "/code/app"}}}
	if got := p.Containing("/code/app-old/main.go"); got != nil {
		t.Errorf("Containing matched a sibling sharing a prefix: %v", got)
	}
}

func TestSlugFor(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/code/naviamp", "naviamp"},
		{"/code/naviamp-sidecar", "naviamp-sidecar"},
		{"/code/My Project", "my-project"},
		{"/code/weird__name!!", "weird-name"},
		{"/code/...", "repo"},
	} {
		if got := SlugFor(tc.in); got != tc.want {
			t.Errorf("SlugFor(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if err := ValidateMemberID(SlugFor(tc.in)); err != nil {
			t.Errorf("SlugFor(%q) produced an invalid member id: %v", tc.in, err)
		}
	}
}

func TestValidateMemberID(t *testing.T) {
	for _, bad := range []string{"", "-leading", "Upper", "has space", "has/slash", "has:colon"} {
		if err := ValidateMemberID(bad); err == nil {
			t.Errorf("ValidateMemberID(%q) was accepted", bad)
		}
	}
	for _, ok := range []string{"a", "naviamp", "naviamp-sidecar", "app2"} {
		if err := ValidateMemberID(ok); err != nil {
			t.Errorf("ValidateMemberID(%q) = %v", ok, err)
		}
	}
}

// Resolution runs on the claim-guard path, which is a fresh process on every
// Edit and Write inside a budget measured in milliseconds. It must therefore
// never spawn a subprocess.
//
// Tested by taking `git` away: PATH is emptied, so anything that shelled out
// would fail. Resolve must still work, because it reads .git itself.
//
// An import-graph assertion was tried first and was wrong — this package does
// legitimately reach os/exec and database/sql, through gitx.ResolveWithFallback
// and Load, which exist for callers that are NOT on the hook path. The property
// worth defending is about Resolve, not about the package.
func TestResolveNeverSpawnsASubprocess(t *testing.T) {
	worktree, _ := newRepo(t, true)

	t.Setenv("PATH", "")
	p, err := Resolve(worktree)
	if err != nil {
		t.Fatalf("Resolve with no executables on PATH: %v — it shelled out to something", err)
	}
	if !p.Adopted() {
		t.Error("resolved, but did not see the database")
	}

	// The contrast that makes the above meaningful: the fallback DOES spawn, and
	// with no PATH it cannot. It is the function hooks must never call.
	if _, err := ResolveWithFallback(t.TempDir()); err == nil {
		t.Log("note: ResolveWithFallback succeeded without PATH; it may have found a repo another way")
	}
}
