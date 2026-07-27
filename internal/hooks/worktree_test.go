package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/happyarch/stigmergy/internal/gitx"
	"github.com/happyarch/stigmergy/internal/ids"
	"github.com/happyarch/stigmergy/internal/store"
)

// A claim taken in one linked worktree blocks an edit in another worktree of the
// same repository, and that is deliberate.
//
// It is also surprising enough to be worth an explicit test, because the two
// files are genuinely distinct on disk — different directories, usually
// different branches. The reasoning is in overlaps(): a claim protects an
// ASSUMPTION, not a byte range. Two agents refactoring the same function on two
// branches of one codebase are invalidating each other's work whether or not
// they are touching the same inode, and one of them is going to have to redo it.
//
// Mechanically this falls out of repo identity being the git COMMON dir: linked
// worktrees share it, so they share a repo_id, so their claims meet.
func TestAClaimInOneLinkedWorktreeBlocksAnother(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	f := newFixture(t)

	// The project database lives in the common dir, so both worktrees find the
	// same one without anything being told about the other.
	linked := filepath.Join(t.TempDir(), "wt-feature")
	if out, err := exec.Command("git", "-C", f.worktree,
		"-c", "user.name=t", "-c", "user.email=t@example.com",
		"commit", "--allow-empty", "-qm", "initial").CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", f.worktree,
		"worktree", "add", "-q", "-b", "feature", linked).CombinedOutput(); err != nil {
		t.Skipf("git worktree add unavailable: %v\n%s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(linked, "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Both worktrees must resolve to the same common dir, or the premise is gone.
	main, err := gitx.Resolve(f.worktree)
	if err != nil {
		t.Fatal(err)
	}
	other, err := gitx.Resolve(linked)
	if err != nil {
		t.Fatal(err)
	}
	if main.CommonDir != other.CommonDir {
		t.Fatalf("the fixture is wrong: common dirs differ\n  %s\n  %s", main.CommonDir, other.CommonDir)
	}

	owner := f.claim(t, "sess-owner", "src", true)
	t.Logf("%s holds src/** via %s", owner, f.worktree)

	d := Guard("claude-code", "sess-other", linked, []string{filepath.Join(linked, "src", "main.go")})
	if d.Allow {
		t.Error("an edit in a linked worktree was allowed past a claim held in the main one — " +
			"two agents can now refactor the same code on two branches, each believing it holds it")
	}
}

// Members can nest: a repository checked out inside another member's tree is
// unusual but legal, and Containing resolves it by longest worktree root.
//
// The file belongs to the INNER repository, so an edit to it is governed by the
// inner repository's claims and not the outer one's. That is the right answer —
// they are separate repositories with separate histories — but it is worth
// asserting, because the outer claim looks like it should cover the path and
// does not.
func TestANestedMemberIsGovernedByTheInnerRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	m := newMultiRepo(t, "outer")

	// A second repository living inside the first one's tree.
	innerRoot := filepath.Join(m.roots["outer"], "vendor", "inner")
	if err := os.MkdirAll(filepath.Join(innerRoot, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", innerRoot, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	innerRepo, err := gitx.Resolve(innerRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.db.AddRepo("inner", innerRepo.CommonDir, innerRoot); err != nil {
		t.Fatal(err)
	}
	m.roots["inner"] = innerRoot

	// The outer repository claims everything it has.
	m.claim(t, "sess-outer", "outer", ".", true)

	nested := filepath.Join(innerRoot, "src", "main.go")
	if d := Guard("claude-code", "sess-someone", m.roots["outer"], []string{nested}); !d.Allow {
		t.Errorf("the outer repository's claim reached into a nested member: %s", d.Reason)
	}

	// And the inner repository's own claim does govern it.
	root, _, err := m.db.RegisterRoot(store.Registration{
		RootID: ids.NewRootID(), AgentKind: "claude-code",
		Worktree: innerRoot, SessionLabel: "sess-inner",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.db.AcquireClaim(store.ClaimRequest{
		ScopePath: "src", Recursive: true, RootID: root.RootID, RepoID: "inner",
		Worktree: innerRoot, Reason: "vendored work",
	}); err != nil {
		t.Fatal(err)
	}
	if d := Guard("claude-code", "sess-someone", m.roots["outer"], []string{nested}); d.Allow {
		t.Error("the nested member's own claim did not govern a file inside it")
	}

	// A file in the outer repository, at a path that merely looks nested, is
	// still the outer repository's.
	outerFile := filepath.Join(m.roots["outer"], "vendor", "notes.md")
	if d := Guard("claude-code", "sess-someone", m.roots["outer"], []string{outerFile}); d.Allow {
		t.Error("the outer claim on \".\" did not cover a file in the outer repository")
	}
}
