package mcpserver

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/happyarch/stigmergy/internal/store"
)

// twoRoots wires two independent MCP sessions onto the same repository — the
// real scenario stigmergy exists for: two agents, one worktree.
func twoRoots(t *testing.T) (*harness, *harness) {
	t.Helper()
	a := newHarness(t)
	b := newHarnessIn(t, a.worktree, a.session.globalPath)

	a.open()
	a.call("root_register", map[string]any{
		"agent_kind": "claude-code", "worktree": a.worktree, "session_label": "sess-a",
	})
	b.open()
	b.call("root_register", map[string]any{
		"agent_kind": "codex", "worktree": b.worktree, "session_label": "sess-b",
	})
	return a, b
}

// Claims are repo-wide, not worktree-scoped, and that is deliberate.
//
// Two agents in two linked worktrees edit physically different files, usually on
// different branches, so they cannot clobber each other on disk — and it is
// tempting to conclude that the worktree column belongs in the conflict test.
// It does not. The point of blocking here is not to protect bytes; it is to stop
// the second agent writing a pile of code against assumptions the first agent is
// in the middle of invalidating, which nobody discovers until the merge, when the
// work has to be thrown away. A false conflict costs one conversation. A missed
// one costs a rewrite.
//
// The conflict names the holder's worktree and branch precisely so the blocked
// agent can go and consult it. If this test starts failing, someone has "fixed"
// an intended property: read this comment before changing it back.
func TestClaimsBlockAcrossWorktrees(t *testing.T) {
	a := newHarness(t)
	b := newHarnessIn(t, a.worktree, a.session.globalPath)

	// Both roots share one project database (linked worktrees share a git common
	// dir), but they sit in different working trees.
	a.open()
	a.call("root_register", map[string]any{
		"agent_kind": "claude-code", "worktree": a.worktree, "session_label": "sess-main",
	})
	b.open()
	b.call("root_register", map[string]any{
		"agent_kind": "codex", "worktree": filepath.Join(a.worktree, "..", "wt-feature"),
		"branch": "feature", "session_label": "sess-feature",
	})

	held := a.call("claim_acquire", map[string]any{
		"scope_path": "src/api", "recursive": true, "reason": "reshaping the request types",
	})["claim"].(map[string]any)

	_, _, e := b.tryCall("claim_acquire", map[string]any{
		"scope_path": "src/api/handlers.go", "reason": "adding an endpoint on my branch",
	})
	if e == nil || e["code"] != "claim_conflict" {
		t.Fatalf("a claim must hold across worktrees: %#v, want claim_conflict", e)
	}

	// And the blocked agent must be able to find whom to talk to: the owner's
	// worktree is what tells it this is a cross-branch conversation, not a
	// same-tree collision.
	conflict := e["conflict"].(map[string]any)
	if conflict["root_id"] != held["root_id"] {
		t.Fatalf("conflict names %v, want the holder %v", conflict["root_id"], held["root_id"])
	}
	if conflict["worktree"] != a.worktree {
		t.Fatalf("conflict worktree = %v, want the holder's %v", conflict["worktree"], a.worktree)
	}
}

func TestClaimConflictBetweenTwoRoots(t *testing.T) {
	a, b := twoRoots(t)

	claim := a.call("claim_acquire", map[string]any{
		"scope_path": "src/api", "recursive": true, "reason": "refactoring the API layer",
	})["claim"].(map[string]any)
	if claim["own"] != true {
		t.Fatalf("acquiring agent must own its claim: %#v", claim)
	}

	// The second root must be refused, and told exactly who holds it and why —
	// an agent that cannot see the owner cannot negotiate.
	_, _, e := b.tryCall("claim_acquire", map[string]any{
		"scope_path": "src/api/handlers.go", "reason": "fixing a bug",
	})
	if e == nil || e["code"] != "claim_conflict" {
		t.Fatalf("overlapping claim: %#v, want claim_conflict", e)
	}
	conflict, ok := e["conflict"].(map[string]any)
	if !ok {
		t.Fatalf("claim_conflict must name the conflicting claim: %#v", e)
	}
	if conflict["root_id"] != claim["root_id"] || conflict["reason"] != "refactoring the API layer" {
		t.Fatalf("conflict = %#v, want the holder's id and reason", conflict)
	}

	// A non-overlapping path is free.
	b.call("claim_acquire", map[string]any{"scope_path": "docs/readme.md", "reason": "docs"})

	// claim_check tells each root whether the claim in its way is its own.
	own := a.call("claim_check", map[string]any{"path": "src/api/handlers.go"})
	if own["claimed"] != true || own["claims"].([]any)[0].(map[string]any)["own"] != true {
		t.Fatalf("the holder must see its own claim as own: %#v", own)
	}
	foreign := b.call("claim_check", map[string]any{"path": "src/api/handlers.go"})
	if foreign["claimed"] != true || foreign["claims"].([]any)[0].(map[string]any)["own"] != false {
		t.Fatalf("another root must see the claim as foreign: %#v", foreign)
	}

	// Release frees it, and the waiting root can then take it.
	a.call("claim_release", map[string]any{"claim_id": claim["id"]})
	got := b.call("claim_acquire", map[string]any{
		"scope_path": "src/api/handlers.go", "reason": "fixing a bug",
	})["claim"].(map[string]any)
	if got["own"] != true {
		t.Fatalf("after release the path must be claimable: %#v", got)
	}
}

func TestClaimOwnershipIsEnforced(t *testing.T) {
	a, b := twoRoots(t)
	claim := a.call("claim_acquire", map[string]any{
		"scope_path": "src/main.go", "reason": "editing",
	})["claim"].(map[string]any)
	id := claim["id"]

	// Another root may not release or renew what it does not own — otherwise a
	// claim would be worth nothing.
	if code := b.errCode("claim_release", map[string]any{"claim_id": id}); code != "not_owner" {
		t.Fatalf("releasing another root's claim: code=%q, want not_owner", code)
	}
	if code := b.errCode("claim_renew", map[string]any{"claim_id": id}); code != "not_owner" {
		t.Fatalf("renewing another root's claim: code=%q, want not_owner", code)
	}

	// The owner may.
	a.call("claim_renew", map[string]any{"claim_id": id, "ttl_seconds": 600})
	a.call("claim_release", map[string]any{"claim_id": id})

	// Releasing twice is not silently fine: the claim is gone.
	if code := a.errCode("claim_release", map[string]any{"claim_id": id}); code != "invalid_input" {
		t.Fatalf("double release: code=%q, want invalid_input", code)
	}
}

func TestReacquiringYourOwnClaimIsIdempotent(t *testing.T) {
	a, _ := twoRoots(t)
	first := a.call("claim_acquire", map[string]any{"scope_path": "src", "recursive": true, "reason": "work"})["claim"].(map[string]any)
	second := a.call("claim_acquire", map[string]any{"scope_path": "src/main.go", "reason": "same work"})["claim"].(map[string]any)
	if second["id"] != first["id"] {
		t.Fatalf("re-claiming inside your own claim must return the held claim, got %#v", second)
	}
}

func TestClaimTTLExpiry(t *testing.T) {
	a, b := twoRoots(t)

	a.call("claim_acquire", map[string]any{
		"scope_path": "src/main.go", "reason": "editing", "ttl_seconds": 60,
	})
	if code := b.errCode("claim_acquire", map[string]any{
		"scope_path": "src/main.go", "reason": "also editing",
	}); code != "claim_conflict" {
		t.Fatalf("a live claim must block: code=%q", code)
	}

	// Wind the clock past the TTL. An abandoned claim must stop blocking
	// everyone — expiry is what keeps a crashed agent from locking the repo.
	restore := store.SetClock(func() time.Time { return time.Now().Add(2 * time.Minute) })
	defer restore()

	if out := b.call("claim_check", map[string]any{"path": "src/main.go"}); out["claimed"] != false {
		t.Fatalf("an expired claim still reads as claimed: %#v", out)
	}
	b.call("claim_acquire", map[string]any{"scope_path": "src/main.go", "reason": "also editing"})
}

// A root that dies takes its claims with it. Without this, a crashed agent
// would hold files hostage for the full claim TTL.
func TestDeadRootReleasesClaims(t *testing.T) {
	a, b := twoRoots(t)
	a.call("claim_acquire", map[string]any{
		"scope_path": "src/main.go", "reason": "editing", "ttl_seconds": 86400,
	})

	// Deregistering is the clean exit: claims free up at once.
	a.call("root_deregister", map[string]any{})
	if out := b.call("claim_check", map[string]any{"path": "src/main.go"}); out["claimed"] != false {
		t.Fatalf("a deregistered root's claims still block: %#v", out)
	}

	// And the unclean exit: a root that simply goes silent past the root TTL,
	// even with a long-lived claim, stops blocking.
	a.call("root_register", map[string]any{
		"agent_kind": "claude-code", "worktree": a.worktree, "session_label": "sess-a2",
	})
	a.call("claim_acquire", map[string]any{
		"scope_path": "src/main.go", "reason": "editing again", "ttl_seconds": 86400,
	})
	if out := b.call("claim_check", map[string]any{"path": "src/main.go"}); out["claimed"] != true {
		t.Fatalf("the fresh claim should block: %#v", out)
	}

	restore := store.SetClock(func() time.Time { return time.Now().Add(store.RootTTL + time.Minute) })
	defer restore()
	if out := b.call("claim_check", map[string]any{"path": "src/main.go"}); out["claimed"] != false {
		t.Fatalf("a silent root past its TTL still holds claims: %#v", out)
	}
}

func TestClaimScopeValidation(t *testing.T) {
	a, _ := twoRoots(t)
	for _, bad := range []string{"", "/etc/passwd", "../outside"} {
		if code := a.errCode("claim_acquire", map[string]any{
			"scope_path": bad, "reason": "r",
		}); code != "invalid_input" {
			t.Errorf("scope_path %q: code=%q, want invalid_input", bad, code)
		}
	}
	if code := a.errCode("claim_acquire", map[string]any{
		"scope_path": "src", "reason": "r", "ttl_seconds": 5,
	}); code != "invalid_input" {
		t.Errorf("a 5-second TTL should be rejected: code=%q", code)
	}
	if code := a.errCode("claim_acquire", map[string]any{
		"scope_path": "src", "reason": "",
	}); code != "invalid_input" {
		t.Errorf("an empty reason should be rejected: code=%q", code)
	}
}

// Paths outside the repository are none of stigmergy's business: it coordinates
// a repository, and must not pretend to govern the rest of the filesystem.
func TestClaimCheckOutsideWorktree(t *testing.T) {
	a, _ := twoRoots(t)
	a.call("claim_acquire", map[string]any{"scope_path": ".", "reason": "everything"})

	out := a.call("claim_check", map[string]any{"path": filepath.Join(t.TempDir(), "elsewhere.go")})
	if out["claimed"] != false {
		t.Fatalf("a path outside the worktree must never read as claimed: %#v", out)
	}
	// ...but a repo-root claim does cover everything inside.
	if in := a.call("claim_check", map[string]any{"path": "deep/nested/file.go"}); in["claimed"] != true {
		t.Fatalf("a recursive claim on \".\" must cover the whole repo: %#v", in)
	}
}
