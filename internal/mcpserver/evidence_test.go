package mcpserver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/happyarch/stigmergy/internal/project"
)

// evidenceHarness is a harness whose repository has a commit and is registered
// as a member — the state `stigmergy init` leaves behind, which is what evidence
// needs and what context_open alone does not currently produce.
func evidenceHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	commitFile(t, h.worktree, "README.md", "start", "initial")
	h.open()
	h.register()
	if err := h.session.project.EnsureSelfRepo(
		project.SlugFor(h.worktree), filepath.Join(h.worktree, ".git"), h.worktree); err != nil {
		t.Fatalf("EnsureSelfRepo: %v", err)
	}
	return h
}

func commitFile(t *testing.T, dir, rel, content, msg string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "-A")
	run(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", msg)
}

func (h *harness) writeMemory(key, body string) {
	h.t.Helper()
	h.call("memory_write", map[string]any{
		"scope": "project", "key": key, "type": "project",
		"description": "about " + key, "body": body,
	})
}

func TestEvidenceSetAndDrift(t *testing.T) {
	h := evidenceHarness(t)
	h.writeMemory("wire-format", "The wire format is versioned.")

	out := h.call("memory_evidence_set", map[string]any{
		"key": "wire-format", "expected_memory_version": 1,
	})
	policy := out["policy"].(map[string]any)
	if policy["version"].(float64) != 1 {
		t.Fatalf("policy = %#v", policy)
	}
	// Naming the repositories is optional here and only here: a project with one
	// repository has one possible answer.
	if len(policy["members"].([]any)) != 1 {
		t.Fatalf("members = %#v", policy["members"])
	}
	if out["note"] == "" {
		t.Error("setting a policy said nothing about what it does and does not mean")
	}

	commitFile(t, h.worktree, "src/wire.go", "v2", "change the wire format")
	commitFile(t, h.worktree, "docs/unrelated.md", "x", "unrelated")

	entries := h.call("memory_list", map[string]any{
		"scope": "project", "include_drift": true,
	})["entries"].([]any)
	ev := entries[0].(map[string]any)["evidence"].(map[string]any)

	if ev["configured"] != true || ev["state"] != "evaluated" {
		t.Fatalf("evidence = %#v", ev)
	}
	if ev["coverage"] != "complete" {
		t.Errorf("coverage = %v, want complete", ev["coverage"])
	}
	member := ev["members"].([]any)[0].(map[string]any)
	if member["outcome"] != "measured" {
		t.Fatalf("member = %#v", member)
	}
	if member["count"].(float64) != 2 {
		t.Errorf("count = %v, want 2", member["count"])
	}
	// The mode travels with the number so nobody reads it as "commits touching
	// these paths", which is not what git counts.
	if member["count_mode"] == "" {
		t.Error("the count arrived with no statement of what it counts")
	}
}

// The declared boundary comes back with every result, so a reader can see what
// was and was not observed.
func TestEvidenceEchoesItsBoundary(t *testing.T) {
	h := evidenceHarness(t)
	h.writeMemory("store-layer", "The store owns all SQLite access.")
	h.call("memory_evidence_set", map[string]any{
		"key": "store-layer", "expected_memory_version": 1,
		"repos": []any{map[string]any{
			"repo": project.SlugFor(h.worktree),
			"paths": []any{
				map[string]any{"kind": "glob", "pattern": "internal/**"},
			},
		}},
	})

	commitFile(t, h.worktree, "internal/store/a.go", "1", "inside the scope")
	commitFile(t, h.worktree, "outside.go", "1", "outside the scope")

	entries := h.call("memory_list", map[string]any{"scope": "project", "include_drift": true})["entries"].([]any)
	member := entries[0].(map[string]any)["evidence"].(map[string]any)["members"].([]any)[0].(map[string]any)
	if member["count"].(float64) != 1 {
		t.Errorf("count = %v, want 1 — the path narrowing was not applied", member["count"])
	}
	paths := member["paths"].([]any)
	if len(paths) != 1 || paths[0].(map[string]any)["pattern"] != "internal/**" {
		t.Errorf("the result does not echo the policy it came from: %#v", paths)
	}
}

// The ambiguity include_drift exists to remove: an omitted field cannot be told
// apart from "the caller never asked".
func TestDriftOnAMemoryWithNoPolicyIsStillReported(t *testing.T) {
	h := evidenceHarness(t)
	h.writeMemory("unanchored", "Nobody declared anything to observe here.")

	entries := h.call("memory_list", map[string]any{"scope": "project", "include_drift": true})["entries"].([]any)
	ev, ok := entries[0].(map[string]any)["evidence"].(map[string]any)
	if !ok {
		t.Fatal("evidence was omitted entirely for a memory with no policy")
	}
	if ev["configured"] != false || ev["state"] != "not_configured" {
		t.Fatalf("evidence = %#v", ev)
	}
	// Coverage is meaningless without a policy, and saying "unavailable" here
	// would read as "we tried and failed".
	if _, present := ev["coverage"]; present {
		t.Errorf("coverage was reported for a memory with no policy: %#v", ev)
	}
}

// Without the flag, nothing changes and nothing is paid for.
func TestDriftIsOptIn(t *testing.T) {
	h := evidenceHarness(t)
	h.writeMemory("wire-format", "x")
	entries := h.call("memory_list", map[string]any{"scope": "project"})["entries"].([]any)
	if _, present := entries[0].(map[string]any)["evidence"]; present {
		t.Error("evidence appeared without include_drift")
	}
}

// Git evidence is undefined for a memory about this machine. Rejected outright,
// because accepting the flag and returning nothing looks like a real answer.
func TestDriftIsRejectedInTheGlobalScope(t *testing.T) {
	h := evidenceHarness(t)
	if code := h.errCode("memory_list", map[string]any{
		"scope": "global", "include_drift": true,
	}); code != "invalid_input" {
		t.Fatalf("code = %q, want invalid_input", code)
	}
}

// A baseline that cannot be captured must leave NOTHING behind. Half a policy
// under-reports forever, and nothing downstream could tell it from a repository
// that simply has not changed.
func TestACaptureFailureStoresNothing(t *testing.T) {
	h := evidenceHarness(t)
	h.writeMemory("wire-format", "x")
	// A member that is registered but whose checkout is not there.
	if err := h.session.project.AddRepo("ghost", "/nonexistent/ghost/.git", "/nonexistent/ghost"); err != nil {
		t.Fatalf("AddRepo: %v", err)
	}

	_, _, e := h.tryCall("memory_evidence_set", map[string]any{
		"key": "wire-format", "expected_memory_version": 1,
		"repos": []any{
			map[string]any{"repo": project.SlugFor(h.worktree)},
			map[string]any{"repo": "ghost"},
		},
	})
	if e == nil {
		t.Fatal("a policy naming an unreachable repository was accepted")
	}
	if h.session.project.HasEvidencePolicy("wire-format") {
		t.Error("a failed declaration left a policy behind")
	}
}

func TestUnknownRepositoryIsNamedAndRefused(t *testing.T) {
	h := evidenceHarness(t)
	h.writeMemory("wire-format", "x")
	code := h.errCode("memory_evidence_set", map[string]any{
		"key": "wire-format", "expected_memory_version": 1,
		"repos": []any{map[string]any{"repo": "not-a-member"}},
	})
	if code != "invalid_input" {
		t.Fatalf("code = %q, want invalid_input", code)
	}
}

// A write must not touch the baseline — that is the §7 inference error in the
// one place where it destroys data rather than mis-ranking it — and it says so,
// but only where there is something to say.
func TestWriteLeavesTheBaselineAloneAndSaysSo(t *testing.T) {
	h := evidenceHarness(t)
	h.writeMemory("wire-format", "first")
	h.call("memory_evidence_set", map[string]any{"key": "wire-format", "expected_memory_version": 1})

	before, err := h.session.project.ReadEvidencePolicy("wire-format")
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, h.worktree, "later.go", "1", "a commit after the baseline")

	out := h.call("memory_write", map[string]any{
		"scope": "project", "key": "wire-format", "type": "project",
		"description": "about wire-format", "body": "second", "expected_version": 1,
	})
	if out["note"] == nil || out["note"] == "" {
		t.Error("an edit to a memory with a policy said nothing about its baseline")
	}

	after, err := h.session.project.ReadEvidencePolicy("wire-format")
	if err != nil {
		t.Fatal(err)
	}
	if after.Members[0].BaseOID != before.Members[0].BaseOID {
		t.Error("the write re-captured the baseline, erasing the evidence accumulated since")
	}
	if after.Version != before.Version {
		t.Error("the write bumped the policy version")
	}
}

func TestWriteSaysNothingWhenThereIsNoPolicy(t *testing.T) {
	h := evidenceHarness(t)
	h.writeMemory("plain", "first")
	out := h.call("memory_write", map[string]any{
		"scope": "project", "key": "plain", "type": "project",
		"description": "about plain", "body": "second", "expected_version": 1,
	})
	if note, ok := out["note"]; ok && note != "" {
		t.Errorf("note = %v — this would appear on every write in the system", note)
	}
}

// Promotion copies content only. The policy is project-local observation
// configuration, not part of what the memory asserts.
func TestPromoteLeavesThePolicyOnTheSource(t *testing.T) {
	h := evidenceHarness(t)
	h.writeMemory("machine-fact", "This holds on this machine everywhere.")
	h.call("memory_evidence_set", map[string]any{"key": "machine-fact", "expected_memory_version": 1})

	out := h.call("memory_promote", map[string]any{"key": "machine-fact", "expected_version": 1})
	note, _ := out["note"].(string)
	if note == "" {
		t.Fatal("promotion said nothing about the policy it did not copy")
	}
	if !h.session.project.HasEvidencePolicy("machine-fact") {
		t.Error("promotion took the policy off the source")
	}
	if h.session.global.HasEvidencePolicy("machine-fact") {
		t.Error("a git evidence policy was created in the global scope, where it has no meaning")
	}
}

// A member removed from the project after a policy was declared reports that it
// has nowhere to look, rather than a confident zero.
func TestADepartedMemberIsNotSilence(t *testing.T) {
	h := evidenceHarness(t)
	h.writeMemory("wire-format", "x")
	h.call("memory_evidence_set", map[string]any{"key": "wire-format", "expected_memory_version": 1})

	if err := h.session.project.RemoveRepo(project.SlugFor(h.worktree)); err != nil {
		t.Fatalf("RemoveRepo: %v", err)
	}

	entries := h.call("memory_list", map[string]any{"scope": "project", "include_drift": true})["entries"].([]any)
	ev := entries[0].(map[string]any)["evidence"].(map[string]any)
	// The member row cascaded away with the repository, so the policy now
	// observes nothing at all — and says so.
	if ev["coverage"] != "unavailable" {
		t.Errorf("coverage = %v, want unavailable", ev["coverage"])
	}
	if members, ok := ev["members"].([]any); ok && len(members) != 0 {
		t.Errorf("members = %#v, want none left", members)
	}
}

// Clearing needs both versions, and takes the baselines with it.
func TestEvidenceClear(t *testing.T) {
	h := evidenceHarness(t)
	h.writeMemory("wire-format", "x")
	h.call("memory_evidence_set", map[string]any{"key": "wire-format", "expected_memory_version": 1})

	if code := h.errCode("memory_evidence_clear", map[string]any{
		"key": "wire-format", "expected_memory_version": 1, "expected_policy_version": 9,
	}); code != "cas_conflict" {
		t.Errorf("stale policy version: code = %q, want cas_conflict", code)
	}
	out := h.call("memory_evidence_clear", map[string]any{
		"key": "wire-format", "expected_memory_version": 1, "expected_policy_version": 1,
	})
	if out["cleared"] != true {
		t.Fatalf("clear = %#v", out)
	}
	if h.session.project.HasEvidencePolicy("wire-format") {
		t.Error("the policy survived being cleared")
	}
}

// Declaring evidence is a mutation, so an unregistered session may not do it.
func TestEvidenceRequiresRegistration(t *testing.T) {
	h := newHarness(t)
	commitFile(t, h.worktree, "README.md", "start", "initial")
	h.open()
	if code := h.errCode("memory_evidence_set", map[string]any{
		"key": "anything", "expected_memory_version": 1,
	}); code != "wrong_state" {
		t.Fatalf("code = %q, want wrong_state", code)
	}
}

// The sanitation rules live in the store, but agents meet them through the
// protocol — so the refusal has to arrive as a proper tool error with a code
// they can branch on, not as a transport failure.
func TestHostileTextIsRefusedThroughTheProtocol(t *testing.T) {
	h := evidenceHarness(t)
	other := newHarnessIn(t, h.worktree, filepath.Join(t.TempDir(), "global.sqlite3"))
	other.open()
	otherRoot := other.register()

	const ansi = "\x1b[2J\x1b[H a cleared screen"
	cases := []struct {
		tool string
		args map[string]any
	}{
		{"memory_write", map[string]any{
			"scope": "project", "key": "hostile", "type": "project",
			"description": "d", "body": ansi,
		}},
		{"memory_write", map[string]any{
			"scope": "project", "key": "hostile", "type": "project",
			"description": ansi, "body": "b",
		}},
		{"mailbox_send", map[string]any{
			"to_root": otherRoot, "subject": ansi, "body": "b",
		}},
		{"mailbox_send", map[string]any{
			"to_root": otherRoot, "subject": "s", "body": ansi,
		}},
		{"claim_acquire", map[string]any{
			"scope_path": "some/file.go", "reason": ansi,
		}},
	}
	for _, tc := range cases {
		if code := h.errCode(tc.tool, tc.args); code != "invalid_input" {
			t.Errorf("%s accepted an ANSI escape (code %q)", tc.tool, code)
		}
	}
}
