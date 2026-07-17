package mcpserver

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/happyarch/stigmergy/internal/gitx"
	"github.com/happyarch/stigmergy/internal/store"
)

// adoptedRepo makes a git repo with a stigmergy project database — what "enabled
// here" means — and returns its worktree.
func adoptedRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	worktree := t.TempDir()
	run(t, worktree, "init", "-b", "main")
	repo, err := gitx.Resolve(worktree)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenProject(repo.CommonDir)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	return worktree
}

// TestBootstrapFromEnvRegistersFromHostSession: the whole point — a host that
// exports its session lets the server register the root before it serves, so the
// agent never runs the handshake.
func TestBootstrapFromEnvRegistersFromHostSession(t *testing.T) {
	worktree := adoptedRepo(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-xyz")
	t.Setenv("CLAUDE_PROJECT_DIR", worktree)

	s := NewSession(filepath.Join(t.TempDir(), "global.sqlite3"))
	t.Cleanup(func() { s.Close() })
	s.bootstrapFromEnv()

	if s.state != Registered {
		t.Fatalf("bootstrap did not register the session: state=%d", s.state)
	}
	// The label must be the host's real session id, because the claim guard keys
	// "my own claims" on that same id — a mismatch would block the agent by its
	// own claims.
	if s.root == nil || s.root.SessionLabel != "sess-xyz" {
		t.Fatalf("root not registered under the host session: %+v", s.root)
	}
	if s.root.AgentKind != "claude-code" {
		t.Errorf("registered under the wrong agent_kind: %q", s.root.AgentKind)
	}
}

// TestBootstrapFromEnvIsInertWithoutHostSession: absent the environment, the
// server stays Unopened and the explicit handshake remains the only way in.
func TestBootstrapFromEnvIsInertWithoutHostSession(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	s := NewSession(filepath.Join(t.TempDir(), "global.sqlite3"))
	t.Cleanup(func() { s.Close() })
	s.bootstrapFromEnv()
	if s.state != Unopened {
		t.Fatalf("bootstrap acted with no host session in the environment: state=%d", s.state)
	}
}

// TestBootstrapFromEnvSkipsUnadoptedProjects: a hook installed host-wide fires in
// every repo; the server must not create a database in one that never enabled
// stigmergy.
func TestBootstrapFromEnvSkipsUnadoptedProjects(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	worktree := t.TempDir()
	run(t, worktree, "init", "-b", "main") // a repo, but no project database
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-xyz")
	t.Setenv("CLAUDE_PROJECT_DIR", worktree)

	s := NewSession(filepath.Join(t.TempDir(), "global.sqlite3"))
	t.Cleanup(func() { s.Close() })
	s.bootstrapFromEnv()
	if s.state != Unopened {
		t.Fatalf("bootstrap adopted a project that had not enabled stigmergy: state=%d", s.state)
	}
}
