package store

import (
	"testing"
	"time"

	"github.com/happyarch/stigmergy/internal/serr"
)

func expiredClaimFixture(t *testing.T) (*DB, *Claim) {
	t.Helper()
	db := testProject(t)
	for i, id := range []string{"r-first", "r-second"} {
		if _, _, err := db.RegisterRoot(Registration{
			RootID: id, AgentKind: "claude-code", Worktree: "/wt", SessionLabel: sessionLabel(i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	c, err := db.AcquireClaim(ClaimRequest{
		ScopePath: "src", Recursive: true, RootID: "r-first",
		Worktree: "/wt", Reason: "editing", TTLSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The TTL passes. Not released — just no longer binding.
	if _, err := db.Exec(`UPDATE claims SET expires_at = ? WHERE id = ?`,
		Stamp(NowTime().Add(-time.Hour)), c.ID); err != nil {
		t.Fatal(err)
	}
	return db, c
}

// Renewing an expired claim must not hand the same path to two agents.
//
// An expired claim has already stopped binding: other agents were told the path
// was free and one may be editing it right now. Extending the expiry without
// looking resurrects it underneath them, which is the same failure the lapse
// sweep exists to prevent — one function over, and reachable by any agent that
// renews on a timer without re-reading anything.
func TestRenewingAnExpiredClaimCannotStealItBack(t *testing.T) {
	db, first := expiredClaimFixture(t)

	// Someone else takes the free path, as they are entitled to.
	if _, err := db.AcquireClaim(ClaimRequest{
		ScopePath: "src", Recursive: true, RootID: "r-second",
		Worktree: "/wt", Reason: "took over", TTLSeconds: 1800,
	}); err != nil {
		t.Fatalf("the expired claim still blocked a new one: %v", err)
	}

	_, err := db.RenewClaim(first.ID, "r-first", 1800)
	e := requireCode(t, err, serr.ClaimConflict)
	if e.Context["conflict"] == nil {
		t.Error("the conflict does not carry the claim that now holds the path")
	}

	active, err := db.ActiveClaims("")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("%d agents hold src/**", len(active))
	}
	if active[0].RootID != "r-second" {
		t.Errorf("the path is held by %s, want the agent that legitimately took it", active[0].RootID)
	}
}

// The friendly half: if nobody took the path while the claim was expired,
// renewal is harmless and the agent carries on. Refusing here would make a
// perfectly safe renewal fail for no reason anyone could act on.
func TestRenewingAnExpiredClaimNobodyTookStillWorks(t *testing.T) {
	db, first := expiredClaimFixture(t)

	renewed, err := db.RenewClaim(first.ID, "r-first", 1800)
	if err != nil {
		t.Fatalf("renewing an uncontested expired claim was refused: %v", err)
	}
	if renewed.ExpiresAt <= Now() {
		t.Errorf("the claim is still expired after renewal: %s", renewed.ExpiresAt)
	}
	active, err := db.ActiveClaims("")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].RootID != "r-first" {
		t.Fatalf("active = %+v", active)
	}
}

// A claim overlapping but not identical must still be caught: the check is the
// same overlap test acquiring uses, not a string comparison of scope paths.
func TestRenewingAnExpiredClaimSeesOverlappingNotJustIdenticalScopes(t *testing.T) {
	db, first := expiredClaimFixture(t)

	// A narrower claim inside the expired subtree.
	if _, err := db.AcquireClaim(ClaimRequest{
		ScopePath: "src/main.go", RootID: "r-second",
		Worktree: "/wt", Reason: "one file", TTLSeconds: 1800,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := db.RenewClaim(first.ID, "r-first", 1800)
	requireCode(t, err, serr.ClaimConflict)
}

// A root that went silent past the TTL has already lost its claims to the sweep,
// so there is nothing left to renew. The honest answer is that the claim is
// gone — not a renewal that a trailing heartbeat would undo a moment later.
func TestALapsedRootCannotRenew(t *testing.T) {
	db := testProject(t)
	root, _, err := db.RegisterRoot(Registration{
		RootID: "r-quiet", AgentKind: "claude-code", Worktree: "/wt", SessionLabel: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := db.AcquireClaim(ClaimRequest{
		ScopePath: "src", Recursive: true, RootID: root.RootID,
		Worktree: "/wt", Reason: "editing", TTLSeconds: 1800,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE roots SET last_seen_at = ? WHERE root_id = ?`,
		Stamp(NowTime().Add(-RootTTL-time.Minute)), root.RootID); err != nil {
		t.Fatal(err)
	}

	if _, err := db.RenewClaim(c.ID, root.RootID, 1800); err == nil {
		t.Error("a lapsed root renewed a claim the sweep had already taken from it")
	}
	active, err := db.ActiveClaims("")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Errorf("%d claims survived a lapse plus a renewal", len(active))
	}
}

// Releasing still wins over renewing: a released claim is gone for good.
func TestAReleasedClaimCannotBeRenewed(t *testing.T) {
	db := testProject(t)
	if _, _, err := db.RegisterRoot(Registration{
		RootID: "r-one", AgentKind: "claude-code", Worktree: "/wt", SessionLabel: "s",
	}); err != nil {
		t.Fatal(err)
	}
	c, err := db.AcquireClaim(ClaimRequest{
		ScopePath: "src", Recursive: true, RootID: "r-one",
		Worktree: "/wt", Reason: "editing", TTLSeconds: 1800,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ReleaseClaim(c.ID, "r-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RenewClaim(c.ID, "r-one", 1800); err == nil {
		t.Error("a released claim was renewed back into existence")
	}
}
