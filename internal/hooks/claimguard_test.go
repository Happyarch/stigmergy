package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/happyarch/stigmergy/internal/gitx"
	"github.com/happyarch/stigmergy/internal/ids"
	"github.com/happyarch/stigmergy/internal/store"
)

type fixture struct {
	worktree string
	db       *store.DB
}

func newFixture(t *testing.T) *fixture {
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
	if err := os.MkdirAll(filepath.Join(worktree, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo, err := gitx.Resolve(worktree)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenProject(repo.CommonDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &fixture{worktree: worktree, db: db}
}

// claim registers a root and has it claim a path, returning the root id.
func (f *fixture) claim(t *testing.T, sessionLabel, scope string, recursive bool) string {
	t.Helper()
	root, _, err := f.db.RegisterRoot(store.Registration{
		RootID: ids.NewRootID(), AgentKind: "claude-code",
		Worktree: f.worktree, SessionLabel: sessionLabel,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.AcquireClaim(store.ClaimRequest{
		ScopePath: scope, Recursive: recursive, RootID: root.RootID,
		Worktree: f.worktree, Reason: "refactoring",
	}); err != nil {
		t.Fatal(err)
	}
	return root.RootID
}

func (f *fixture) guard(sessionLabel string, paths ...string) Decision {
	return Guard("claude-code", sessionLabel, f.worktree, paths)
}

func TestGuardBlocksForeignClaims(t *testing.T) {
	f := newFixture(t)
	owner := f.claim(t, "sess-owner", "src", true)

	d := f.guard("sess-other", filepath.Join(f.worktree, "src", "main.go"))
	if d.Allow {
		t.Fatal("an edit inside another agent's claim must be blocked")
	}
	// The agent has to be able to act on the denial, which means knowing who to
	// talk to and why.
	for _, want := range []string{owner, "refactoring", "mailbox_send"} {
		if !strings.Contains(d.Reason, want) {
			t.Errorf("deny reason does not mention %q:\n%s", want, d.Reason)
		}
	}
}

// The claim holder must not be blocked by its own claim — that would make
// claiming a file self-defeating.
func TestGuardAllowsOwnClaims(t *testing.T) {
	f := newFixture(t)
	f.claim(t, "sess-owner", "src", true)

	if d := f.guard("sess-owner", filepath.Join(f.worktree, "src", "main.go")); !d.Allow {
		t.Fatalf("the claim holder was blocked by its own claim: %s", d.Reason)
	}
}

// An unregistered session cannot be recognized, so every claim is foreign to
// it. That is deliberate: it pushes the agent to register rather than silently
// letting it edit around other agents' claims.
func TestGuardTreatsUnregisteredSessionsAsForeign(t *testing.T) {
	f := newFixture(t)
	f.claim(t, "sess-owner", "src", true)

	if d := f.guard("", filepath.Join(f.worktree, "src", "main.go")); d.Allow {
		t.Fatal("an unregistered session must not be treated as the claim owner")
	}
	if d := f.guard("sess-never-registered", filepath.Join(f.worktree, "src", "main.go")); d.Allow {
		t.Fatal("an unknown session must not be treated as the claim owner")
	}
}

func TestGuardAllowsUnclaimedPaths(t *testing.T) {
	f := newFixture(t)
	f.claim(t, "sess-owner", "src", true)

	for _, p := range []string{
		filepath.Join(f.worktree, "docs", "readme.md"),
		filepath.Join(f.worktree, "srcextra", "x.go"), // the prefix trap, end to end
	} {
		if d := f.guard("sess-other", p); !d.Allow {
			t.Errorf("editing the unclaimed path %s was blocked: %s", p, d.Reason)
		}
	}
}

func TestGuardIgnoresPathsOutsideTheRepo(t *testing.T) {
	f := newFixture(t)
	f.claim(t, "sess-owner", ".", true) // claims the entire repository

	outside := filepath.Join(t.TempDir(), "elsewhere.go")
	if d := f.guard("sess-other", outside); !d.Allow {
		t.Fatalf("a path outside the repository must not be governed by claims: %s", d.Reason)
	}
}

// A project that has not adopted stigmergy must be untouched by a hook that is
// installed user-wide.
func TestGuardAllowsWhenProjectHasNoDatabase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	worktree := t.TempDir()
	if out, err := exec.Command("git", "-C", worktree, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if d := Guard("claude-code", "s", worktree, []string{filepath.Join(worktree, "f.go")}); !d.Allow {
		t.Fatalf("a project without a stigmergy database must not be guarded: %s", d.Reason)
	}
}

func TestGuardAllowsOutsideGitRepos(t *testing.T) {
	dir := t.TempDir()
	if d := Guard("claude-code", "s", dir, []string{filepath.Join(dir, "f.go")}); !d.Allow {
		t.Fatalf("a non-repository must not be guarded: %s", d.Reason)
	}
}

// An unrecognized tool shape yields no paths. Blocking on that would break
// edits stigmergy simply does not understand.
func TestGuardAllowsWhenNoPathsAreFound(t *testing.T) {
	f := newFixture(t)
	f.claim(t, "sess-owner", ".", true)
	if d := f.guard("sess-other"); !d.Allow {
		t.Fatal("a tool call with no recognizable path must be allowed")
	}
}

// The one case where uncertainty must block: if the database cannot be read,
// allowing the edit risks silently overwriting another agent's work.
func TestGuardFailsClosedOnAnUnreadableDatabase(t *testing.T) {
	f := newFixture(t)
	f.claim(t, "sess-owner", "src", true)
	f.db.Close()

	repo, err := gitx.Resolve(f.worktree)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := store.ProjectDBPath(repo.CommonDir)
	if err := os.WriteFile(dbPath, []byte("this is not a database"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := f.guard("sess-other", filepath.Join(f.worktree, "src", "main.go"))
	if d.Allow {
		t.Fatal("a corrupt database must fail closed, not allow the edit")
	}
	if !strings.Contains(d.Reason, "doctor") {
		t.Errorf("a fail-closed denial must say how to fix it:\n%s", d.Reason)
	}
	// It must not read as a claim conflict: there is no one to negotiate with.
	if strings.Contains(d.Reason, "mailbox_send") {
		t.Errorf("a fail-closed denial must not look like a claim conflict:\n%s", d.Reason)
	}
}

// A database written by a newer stigmergy may encode claims this binary cannot
// read. Ignoring them would mean ignoring real claims.
func TestGuardFailsClosedOnSchemaMismatch(t *testing.T) {
	f := newFixture(t)
	f.claim(t, "sess-owner", "src", true)
	if _, err := f.db.Exec(`UPDATE meta SET value = '99' WHERE key = 'schema_version'`); err != nil {
		// The version lives in its own table; find it rather than assume.
		if _, err2 := f.db.Exec(`UPDATE schema_migrations SET version = 99`); err2 != nil {
			t.Skipf("could not simulate a schema mismatch: %v / %v", err, err2)
		}
	}

	d := f.guard("sess-other", filepath.Join(f.worktree, "src", "main.go"))
	if d.Allow {
		t.Fatal("a newer schema must fail closed: this binary cannot see claims it does not understand")
	}
}

func TestEditedPaths(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want []string
	}{
		{"Edit and Write", map[string]any{"file_path": "/repo/a.go"}, []string{"/repo/a.go"}},
		{"NotebookEdit", map[string]any{"notebook_path": "/repo/n.ipynb"}, []string{"/repo/n.ipynb"}},
		{"unknown shape", map[string]any{"command": "rm -rf /"}, nil},
		{"empty", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := &ClaudeInput{ToolInput: c.in}
			got := in.EditedPaths()
			if len(got) != len(c.want) {
				t.Fatalf("EditedPaths() = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("EditedPaths() = %v, want %v", got, c.want)
				}
			}
		})
	}
}

// The payloads here are verbatim shapes from a live Claude Code 2.1.220 session
// (`stigmergy hook dump`), not invented ones. That is the whole reason this
// identity can be relied on, so the test is written to fail loudly if a future
// version stops sending them.
func TestAgentIdentityComesFromThePayload(t *testing.T) {
	peer := &ClaudeInput{
		SessionID: "a2238126-6f3d-4f31-a3b9-288a01bb0415",
		AgentID:   "a4d39a33457e0df63",
		AgentType: "general-purpose",
	}
	if !peer.IsAgent() {
		t.Fatal("a payload carrying agent_id must be read as an agent")
	}
	want := "a2238126-6f3d-4f31-a3b9-288a01bb0415#general-purpose:a4d39a33457e0df63"
	if got := peer.Label(); got != want {
		t.Fatalf("Label() = %q, want %q", got, want)
	}

	// The main thread carries neither field, and MUST keep the session's own
	// label: anything else strands every claim it already holds.
	main := &ClaudeInput{SessionID: "a2238126-6f3d-4f31-a3b9-288a01bb0415"}
	if main.IsAgent() {
		t.Fatal("a payload with no agent_id is the main thread")
	}
	if got := main.Label(); got != main.SessionID {
		t.Fatalf("Label() = %q, want the bare session id %q", got, main.SessionID)
	}
}

func TestSessionOnlyToolsRefuseMemoryAndMailButNeverClaims(t *testing.T) {
	// The bug this whole mechanism exists to fix: a peer refused a claim.
	for _, tool := range []string{
		"mcp__stigmergy__claim_acquire", "mcp__stigmergy__claim_renew",
		"mcp__stigmergy__claim_release", "mcp__stigmergy__claim_check",
		"mcp__stigmergy__root_heartbeat",
	} {
		if why := SessionOnlyTool(tool); why != "" {
			t.Errorf("SessionOnlyTool(%q) = %q, want it allowed", tool, why)
		}
	}
	for _, tool := range []string{
		"mcp__stigmergy__memory_write", "mcp__stigmergy__memory_delete",
		"mcp__stigmergy__memory_link", "mcp__stigmergy__memory_unlink",
		"mcp__stigmergy__mailbox_send", "mcp__stigmergy__root_register",
	} {
		why := SessionOnlyTool(tool)
		if why == "" {
			t.Errorf("SessionOnlyTool(%q) = allowed, want refused", tool)
			continue
		}
		// The refusal has to say what the agent MAY do, or it teaches the same
		// false lesson the old one did: that it is shut out of coordination.
		if !strings.Contains(why, "claim_acquire") {
			t.Errorf("SessionOnlyTool(%q) does not say claims are still available: %q", tool, why)
		}
	}
}
