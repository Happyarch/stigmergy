package gitx

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepoWithWorktree creates a repo with one commit and one linked worktree.
func newRepoWithWorktree(t *testing.T) (repoDir, worktreeDir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	repoDir = filepath.Join(base, "repo")
	if err := os.MkdirAll(filepath.Join(repoDir, "sub", "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, base, "init", "-b", "main", repoDir)
	if err := os.WriteFile(filepath.Join(repoDir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repoDir, "add", ".")
	mustGit(t, repoDir, "commit", "-m", "init")
	worktreeDir = filepath.Join(base, "wt2")
	mustGit(t, repoDir, "worktree", "add", "-b", "wt2-branch", worktreeDir)
	return repoDir, worktreeDir
}

func TestResolveParityWithGit(t *testing.T) {
	repoDir, worktreeDir := newRepoWithWorktree(t)

	for _, dir := range []string{repoDir, filepath.Join(repoDir, "sub", "dir"), worktreeDir} {
		r, err := Resolve(dir)
		if err != nil {
			t.Fatalf("Resolve(%s): %v", dir, err)
		}
		wantCommon := mustGit(t, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
		wantTop := mustGit(t, dir, "rev-parse", "--show-toplevel")
		if r.CommonDir != wantCommon {
			t.Errorf("Resolve(%s).CommonDir = %q, git says %q", dir, r.CommonDir, wantCommon)
		}
		if r.WorktreeRoot != wantTop {
			t.Errorf("Resolve(%s).WorktreeRoot = %q, git says %q", dir, r.WorktreeRoot, wantTop)
		}
	}
}

func TestWorktreesShareCommonDirCloneDoesNot(t *testing.T) {
	repoDir, worktreeDir := newRepoWithWorktree(t)

	main, err := Resolve(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	linked, err := Resolve(worktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	if main.CommonDir != linked.CommonDir {
		t.Fatalf("linked worktree common dir %q != main %q", linked.CommonDir, main.CommonDir)
	}

	cloneDir := filepath.Join(t.TempDir(), "clone")
	mustGit(t, repoDir, "clone", repoDir, cloneDir)
	clone, err := Resolve(cloneDir)
	if err != nil {
		t.Fatal(err)
	}
	if clone.CommonDir == main.CommonDir {
		t.Fatal("separate clone must not share the common dir")
	}
}

func TestResolveNotARepo(t *testing.T) {
	_, err := Resolve(t.TempDir())
	if !errors.Is(err, ErrNotARepo) {
		t.Fatalf("err = %v, want ErrNotARepo", err)
	}
}
