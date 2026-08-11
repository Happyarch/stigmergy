package store

import (
	"testing"
)

// The model is whatever the agent says it is. agent_kind names the harness and
// is a closed set the schema enforces; model names the thing behind it, is
// self-reported, and is checked by nobody.
func TestAModelIsRecordedAsGiven(t *testing.T) {
	db := testProject(t)

	root, _, err := db.RegisterRoot(Registration{
		RootID: "r-1", AgentKind: "claude-code", Worktree: "/wt",
		SessionLabel: "sess-1", Model: "claude-opus-4-8",
	})
	if err != nil {
		t.Fatal(err)
	}
	if root.Model != "claude-opus-4-8" {
		t.Errorf("Model = %q on the way out of RegisterRoot", root.Model)
	}

	got, err := db.RootBySession("claude-code", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "claude-opus-4-8" {
		t.Errorf("Model = %q when read back", got.Model)
	}
}

// Nothing may depend on it, so nothing may reject it. An agent that does not
// answer registers exactly as before — the field arrived after the roots that
// predate it, and they are not broken.
func TestAModelIsOptional(t *testing.T) {
	db := testProject(t)

	root, _, err := db.RegisterRoot(Registration{
		RootID: "r-2", AgentKind: "codex", Worktree: "/wt", SessionLabel: "sess-2",
	})
	if err != nil {
		t.Fatalf("registering without a model: %v", err)
	}
	if root.Model != "" {
		t.Errorf("Model = %q, want empty", root.Model)
	}
	if got, err := db.RootBySession("codex", "sess-2"); err != nil || got.Model != "" {
		t.Errorf("read back: model = %q, err = %v", got.Model, err)
	}
}

// A resume may carry a model the original registration did not — the host
// restarts the MCP server mid-session, and the agent answers this time. It must
// also not erase what an earlier session already reported by staying silent.
func TestResumingKeepsTheLastModelItWasTold(t *testing.T) {
	db := testProject(t)

	if _, _, err := db.RegisterRoot(Registration{
		RootID: "r-3", AgentKind: "claude-code", Worktree: "/wt", SessionLabel: "sess-3",
	}); err != nil {
		t.Fatal(err)
	}

	// Resume, answering this time.
	resumed, wasResumed, err := db.RegisterRoot(Registration{
		RootID: "r-ignored", AgentKind: "claude-code", Worktree: "/wt",
		SessionLabel: "sess-3", Model: "claude-haiku-4-5",
	})
	if err != nil || !wasResumed {
		t.Fatalf("resume: resumed=%v err=%v", wasResumed, err)
	}
	if resumed.Model != "claude-haiku-4-5" {
		t.Errorf("a resume did not record the model: %q", resumed.Model)
	}

	// Resume again, silent. The answer already recorded must survive.
	again, _, err := db.RegisterRoot(Registration{
		RootID: "r-ignored-2", AgentKind: "claude-code", Worktree: "/wt", SessionLabel: "sess-3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.Model != "claude-haiku-4-5" {
		t.Errorf("a silent resume erased the model: %q", again.Model)
	}
}

// The point of the field: an agent deciding whether to negotiate learns who is
// on the other side, not just which harness it runs in.
func TestAClaimConflictCarriesTheOwnersModel(t *testing.T) {
	db := testProject(t)

	if _, _, err := db.RegisterRoot(Registration{
		RootID: "r-owner", AgentKind: "claude-code", Worktree: "/wt",
		SessionLabel: "owner", Model: "claude-opus-4-8",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcquireClaim(ClaimRequest{
		ScopePath: "src", Recursive: true, RootID: "r-owner",
		Worktree: "/wt", Reason: "refactor", TTLSeconds: 600,
	}); err != nil {
		t.Fatal(err)
	}

	active, err := db.ActiveClaims("")
	if err != nil || len(active) != 1 {
		t.Fatalf("ActiveClaims: %v (%d)", err, len(active))
	}
	if active[0].OwnerModel != "claude-opus-4-8" {
		t.Errorf("OwnerModel = %q, want the model the owner reported", active[0].OwnerModel)
	}
	if active[0].AgentKind != "claude-code" {
		t.Errorf("AgentKind = %q — the harness must still be reported too", active[0].AgentKind)
	}
}

// A claim held by a root that never said what it was is still a claim, and must
// render without inventing anything.
func TestAClaimFromASilentRootHasNoModel(t *testing.T) {
	db := testProject(t)

	mustRoot(t, db, "codex", "quiet")
	if _, err := db.AcquireClaim(ClaimRequest{
		ScopePath: "docs", RootID: "r-quiet", Worktree: "/wt",
		Reason: "editing", TTLSeconds: 600,
	}); err != nil {
		t.Fatal(err)
	}
	active, err := db.ActiveClaims("")
	if err != nil || len(active) != 1 {
		t.Fatalf("ActiveClaims: %v (%d)", err, len(active))
	}
	if active[0].OwnerModel != "" {
		t.Errorf("OwnerModel = %q, want empty", active[0].OwnerModel)
	}
}
