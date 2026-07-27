package store

import (
	"testing"
	"time"
)

// A root coming back from silence must KEEP the claim it just took.
//
// The bug this pins was silent and total. Coming back from a lapse costs a root
// its claims, which is correct — other agents were told those paths were free
// and may already be editing them. But the sweep that does it lives in
// heartbeat(), and every caller that heartbeats AFTER acquiring inherited it: the
// claim was inserted, then the heartbeat released "all of this root's claims"
// including the one from a moment earlier, and the caller was handed the object
// captured before the release. It described an active claim with a full TTL that
// no longer existed.
//
// The workflow the tool blurbs prescribe is exactly the one that hits it —
// register, search memory, read for a while, claim before editing — because
// reads do not heartbeat and RootTTL is 15m. The most careful sessions were the
// most exposed, and b0be9e6 named re-acquiring as the way back from a lapse,
// which was the one operation that did not work.
func TestARootReturningFromSilenceKeepsTheClaimItJustTook(t *testing.T) {
	db := testProject(t)

	root, _, err := db.RegisterRoot(Registration{
		RootID: "r-lapsed", AgentKind: "claude-code", Worktree: "/wt", SessionLabel: "sess-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Silence for longer than RootTTL. Nothing else happens: this root simply
	// spent the time reading, which does not heartbeat.
	lapsed := NowTime().Add(-RootTTL - time.Minute)
	if _, err := db.Exec(`UPDATE roots SET last_seen_at = ? WHERE root_id = ?`,
		Stamp(lapsed), root.RootID); err != nil {
		t.Fatal(err)
	}

	claim, err := db.AcquireClaim(ClaimRequest{
		ScopePath: "internal", Recursive: true, RootID: root.RootID,
		Worktree: "/wt", Reason: "about to edit", TTLSeconds: 1800,
	})
	if err != nil {
		t.Fatalf("AcquireClaim: %v", err)
	}

	// The heartbeat that every MCP tool call makes after its real work.
	if err := db.Heartbeat(root.RootID); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	active, err := db.ActiveClaims("")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("the root holds %d claims after acquiring one; it was told it holds %q with a full TTL",
			len(active), claim.ScopePath)
	}
	if active[0].ScopePath != "internal" || active[0].RootID != root.RootID {
		t.Fatalf("active claim = %+v", active[0])
	}
}

// The nastier variant from the same report: a returning root's own STALE claims
// are still visible to the overlap check, so it is handed "already ours" — a
// claim the sweep is about to release — and never inserts a new one at all.
func TestALapsedRootReacquiringItsOwnStaleClaimGetsALiveOne(t *testing.T) {
	db := testProject(t)

	root, _, err := db.RegisterRoot(Registration{
		RootID: "r-lapsed", AgentKind: "claude-code", Worktree: "/wt", SessionLabel: "sess-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcquireClaim(ClaimRequest{
		ScopePath: "internal", Recursive: true, RootID: root.RootID,
		Worktree: "/wt", Reason: "first time", TTLSeconds: 1800,
	}); err != nil {
		t.Fatal(err)
	}

	lapsed := NowTime().Add(-RootTTL - time.Minute)
	if _, err := db.Exec(`UPDATE roots SET last_seen_at = ? WHERE root_id = ?`,
		Stamp(lapsed), root.RootID); err != nil {
		t.Fatal(err)
	}

	// Re-acquiring the same scope is what b0be9e6 tells a lapsed root to do.
	if _, err := db.AcquireClaim(ClaimRequest{
		ScopePath: "internal", Recursive: true, RootID: root.RootID,
		Worktree: "/wt", Reason: "back again", TTLSeconds: 1800,
	}); err != nil {
		t.Fatalf("re-acquiring after a lapse: %v", err)
	}
	if err := db.Heartbeat(root.RootID); err != nil {
		t.Fatal(err)
	}

	active, err := db.ActiveClaims("")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("after re-acquiring, the root holds %d claims, want 1", len(active))
	}
}
