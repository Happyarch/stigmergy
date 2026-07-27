package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

// The guard resolves a path in two steps with different strictness, and only the
// second one understands symlinks:
//
//	absolutize(cwd, edit)            — string join, no filesystem
//	p.Project.Containing(abs)        — string prefix against each worktree root
//	  → nil? continue, i.e. ALLOW
//	paths.Normalize(root, cwd, edit) — resolves symlinks, the strict authority
//
// Normalize is only reached once Containing has already placed the path inside a
// member. So if a symlink is what makes the path belong to the repository,
// Containing misses it and the strict check never runs.
//
// Every existing fixture calls EvalSymlinks on the worktree before doing
// anything, so none of them can see this.

// TestGuardThroughASymlinkedWorktree is the ordinary-setup version, and it is
// the one that matters: nobody has to be attacking anything.
//
// A worktree is registered by its real path — gitx resolves it — while the agent
// arrives through a symlink, which is what happens when ~/code is a link to
// another disk, or on any macOS where /tmp is /private/tmp. Every edit the agent
// makes is addressed through the link, Containing matches none of them, and the
// project silently governs nothing at all.
func TestGuardThroughASymlinkedWorktree(t *testing.T) {
	f := newFixture(t)
	owner := f.claim(t, "sess-owner", "src", true)
	t.Logf("src/** is claimed by %s at real path %s", owner, f.worktree)

	// The same tree, reached by another name.
	link := filepath.Join(t.TempDir(), "link-to-repo")
	if err := os.Symlink(f.worktree, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	real := filepath.Join(f.worktree, "src", "main.go")
	viaLink := filepath.Join(link, "src", "main.go")

	// Sanity: these are genuinely the same file.
	if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(viaLink); err != nil {
		t.Fatalf("fixture is wrong, the link does not reach the file: %v", err)
	}

	direct := Guard("claude-code", "sess-intruder", f.worktree, []string{real})
	if direct.Allow {
		t.Fatal("the direct path was allowed past a foreign claim; the fixture is broken")
	}

	linked := Guard("claude-code", "sess-intruder", link, []string{viaLink})
	if linked.Allow {
		t.Errorf("BYPASS: %s is blocked but %s is allowed — the same file.\n"+
			"An agent whose worktree is reached through a symlink is governed by nothing.", real, viaLink)
	}
}

// TestGuardThroughASymlinkIntoTheRepo is the adversarial shape of the same bug:
// a link outside the repository pointing at a claimed file inside it.
func TestGuardThroughASymlinkIntoTheRepo(t *testing.T) {
	f := newFixture(t)
	f.claim(t, "sess-owner", "src", true)

	real := filepath.Join(f.worktree, "src", "main.go")
	if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	link := filepath.Join(outside, "shortcut.go")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// cwd stays inside the project, so the project itself resolves fine; only the
	// edit path leaves and comes back.
	d := Guard("claude-code", "sess-intruder", f.worktree, []string{link})
	if d.Allow {
		t.Errorf("BYPASS: writing %s reaches the claimed file %s, and was allowed", link, real)
	}
}

// The mirror case, which must keep working: a path that only LOOKS like it is
// inside the repository, but resolves out of it. Normalize is the authority and
// it is right to allow this — the bytes land somewhere nobody claimed.
func TestGuardAllowsASymlinkOutOfTheRepo(t *testing.T) {
	f := newFixture(t)
	f.claim(t, "sess-owner", "src", true)

	outside := t.TempDir()
	target := filepath.Join(outside, "elsewhere.go")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(f.worktree, "src", "escape.go")
	if err := os.Symlink(target, escape); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if d := Guard("claude-code", "sess-intruder", f.worktree, []string{escape}); !d.Allow {
		t.Errorf("a path resolving outside every worktree was blocked: %s", d.Reason)
	}
}
