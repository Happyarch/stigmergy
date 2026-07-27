package store

import (
	"testing"
	"time"
)

// A root is an agent, not a directory.
//
// RegisterRoot used to key resume on (agent_kind, worktree, session_label) while
// RootBySession — the read path the claim guard uses — keyed on (agent_kind,
// session_label). One session working in two worktrees therefore minted two
// roots, and the guard resolved whichever was most recent. This is the test that
// says the two sides now agree.
func TestOneSessionIsOneRootAcrossWorktrees(t *testing.T) {
	db := testProject(t)

	first, _, err := db.RegisterRoot(Registration{
		RootID: "r-1", AgentKind: "claude-code", Worktree: "/wt/a", SessionLabel: "sess-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	second, resumed, err := db.RegisterRoot(Registration{
		RootID: "r-2", AgentKind: "claude-code", Worktree: "/wt/b", SessionLabel: "sess-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resumed {
		t.Fatal("registering the same session from another worktree minted a second root")
	}
	if second.RootID != first.RootID {
		t.Fatalf("root id changed: %s then %s", first.RootID, second.RootID)
	}
	// The worktree follows the session, because a stale one is worse than no
	// answer: it is what a conflict message and the roster show a blocked agent.
	if second.Worktree != "/wt/b" {
		t.Errorf("Worktree = %q, want the one most recently registered", second.Worktree)
	}

	// And the guard's own lookup must land on that same root — the disagreement
	// this whole change is about.
	viaSession, err := db.RootBySession("claude-code", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if viaSession.RootID != first.RootID {
		t.Errorf("RootBySession found %s, RegisterRoot returned %s", viaSession.RootID, first.RootID)
	}
	if viaSession.Worktree != "/wt/b" {
		t.Errorf("RootBySession worktree = %q, want the refreshed one", viaSession.Worktree)
	}
}

// The claims a session holds must survive it moving between worktrees. Under the
// old key the second registration created a second root, and the claims stayed
// with the first — which then went silent and lost them.
func TestClaimsSurviveRegisteringFromAnotherWorktree(t *testing.T) {
	db := testProject(t)

	root, _, err := db.RegisterRoot(Registration{
		RootID: "r-1", AgentKind: "claude-code", Worktree: "/wt/a", SessionLabel: "sess-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcquireClaim(ClaimRequest{
		ScopePath: "src", Recursive: true, RootID: root.RootID,
		Worktree: "/wt/a", Reason: "refactoring",
	}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := db.RegisterRoot(Registration{
		RootID: "r-2", AgentKind: "claude-code", Worktree: "/wt/b", SessionLabel: "sess-1",
	}); err != nil {
		t.Fatal(err)
	}

	// Own claims must still read as own, from the resolved root's point of view.
	covering, err := db.ClaimsCovering(LegacyRepoID, "src/main.go", root.RootID)
	if err != nil {
		t.Fatal(err)
	}
	if len(covering) != 1 {
		t.Fatalf("got %d covering claims, want 1", len(covering))
	}
	if !covering[0].Own {
		t.Error("the session's own claim became foreign to it after moving worktree")
	}
}

// Distinct sessions stay distinct. The relaxed key must not collapse two real
// agents into one root just because they share a host.
func TestDifferentSessionsRemainDifferentRoots(t *testing.T) {
	db := testProject(t)

	a, _, err := db.RegisterRoot(Registration{
		RootID: "r-1", AgentKind: "claude-code", Worktree: "/wt", SessionLabel: "sess-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, resumed, err := db.RegisterRoot(Registration{
		RootID: "r-2", AgentKind: "claude-code", Worktree: "/wt", SessionLabel: "sess-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed || a.RootID == b.RootID {
		t.Fatalf("two sessions collapsed into one root (%s / %s, resumed=%v)", a.RootID, b.RootID, resumed)
	}
}

// Same session label, different host: still two roots. agent_kind remains part
// of the key, and a Codex session that happens to share an id with a Claude one
// must not inherit its claims.
func TestSameLabelOnDifferentHostsStaysSeparate(t *testing.T) {
	db := testProject(t)

	if _, _, err := db.RegisterRoot(Registration{
		RootID: "r-1", AgentKind: "claude-code", Worktree: "/wt", SessionLabel: "shared",
	}); err != nil {
		t.Fatal(err)
	}
	other, resumed, err := db.RegisterRoot(Registration{
		RootID: "r-2", AgentKind: "codex", Worktree: "/wt", SessionLabel: "shared",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed || other.RootID != "r-2" {
		t.Fatalf("a codex session resumed a claude-code root (resumed=%v, id=%s)", resumed, other.RootID)
	}
}

// An unlabelled session cannot be matched, so it always mints a new root. This
// is unchanged, and it is what stops every anonymous session in a repository
// from sharing one identity.
func TestAnUnlabelledSessionAlwaysMintsANewRoot(t *testing.T) {
	db := testProject(t)

	a, _, err := db.RegisterRoot(Registration{
		RootID: "r-1", AgentKind: "claude-code", Worktree: "/wt",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, resumed, err := db.RegisterRoot(Registration{
		RootID: "r-2", AgentKind: "claude-code", Worktree: "/wt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed || a.RootID == b.RootID {
		t.Fatalf("an unlabelled session resumed (resumed=%v, %s / %s)", resumed, a.RootID, b.RootID)
	}
}

// A root that has lapsed past the TTL is gone, not resumable: registering again
// mints a new root rather than reviving one whose claims the rest of the system
// has already stopped honouring.
func TestALapsedRootIsNotResumed(t *testing.T) {
	db := testProject(t)

	base := time.Now().UTC()
	restore := SetClock(func() time.Time { return base })
	if _, _, err := db.RegisterRoot(Registration{
		RootID: "r-1", AgentKind: "claude-code", Worktree: "/wt", SessionLabel: "sess-1",
	}); err != nil {
		t.Fatal(err)
	}
	restore()

	restore = SetClock(func() time.Time { return base.Add(RootTTL + time.Minute) })
	defer restore()

	fresh, resumed, err := db.RegisterRoot(Registration{
		RootID: "r-2", AgentKind: "claude-code", Worktree: "/wt", SessionLabel: "sess-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed || fresh.RootID != "r-2" {
		t.Fatalf("a root silent past the TTL was resumed (resumed=%v, id=%s)", resumed, fresh.RootID)
	}
}
