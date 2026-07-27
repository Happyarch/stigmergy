package mcpserver

import (
	"encoding/json"
	"testing"
)

func TestVerifyRecordsAnOutcome(t *testing.T) {
	h := evidenceHarness(t)
	h.writeMemory("wire-format", "The wire format is versioned.")

	out := h.call("memory_verify", map[string]any{
		"key": "wire-format", "outcome": "reaffirmed", "expected_memory_version": 1,
		"reason": "read internal/wire; the version constant is still there",
	})
	v := out["verification"].(map[string]any)
	if v["outcome"] != "reaffirmed" || v["memory_version"].(float64) != 1 {
		t.Fatalf("verification = %#v", v)
	}
	if v["at"] == "" {
		t.Error("no assertion time was recorded")
	}
	if out["note"] == "" {
		t.Error("the response said nothing about what was and was not established")
	}

	history := h.call("memory_history", map[string]any{"key": "wire-format"})["history"].([]any)
	if len(history) != 1 {
		t.Fatalf("history has %d entries, want 1", len(history))
	}
	if history[0].(map[string]any)["reason"] == "" {
		t.Error("the reason was not kept; it is what makes the record useful later")
	}
}

// The snapshot is the whole reason this is worth storing: an outcome is only
// interpretable against what the observer could actually see. Re-measuring later
// answers a different question.
func TestVerifySnapshotsTheEvidenceItWasJudgedAgainst(t *testing.T) {
	h := evidenceHarness(t)
	h.writeMemory("wire-format", "x")
	h.call("memory_evidence_set", map[string]any{"key": "wire-format", "expected_memory_version": 1})
	commitFile(t, h.worktree, "a.go", "1", "one change")

	h.call("memory_verify", map[string]any{
		"key": "wire-format", "outcome": "reaffirmed", "expected_memory_version": 1,
	})

	// More commits land AFTER the judgement. The stored snapshot must not move.
	commitFile(t, h.worktree, "b.go", "2", "another change")
	commitFile(t, h.worktree, "c.go", "3", "and another")

	history := h.call("memory_history", map[string]any{"key": "wire-format"})["history"].([]any)
	entry := history[0].(map[string]any)
	if entry["policy_version"].(float64) != 1 {
		t.Errorf("policy version = %v, want 1", entry["policy_version"])
	}

	var snap EvidenceInfo
	if err := json.Unmarshal([]byte(entry["evidence"].(string)), &snap); err != nil {
		t.Fatalf("the snapshot is not readable: %v", err)
	}
	if len(snap.Members) != 1 || snap.Members[0].Count == nil {
		t.Fatalf("snapshot = %+v", snap)
	}
	if *snap.Members[0].Count != 1 {
		t.Errorf("snapshot count = %d, want 1 — it moved with HEAD instead of staying put",
			*snap.Members[0].Count)
	}
}

// Verifying without a policy is a perfectly good way to check something — by
// reading the code, or by asking. A NULL policy version keeps that
// distinguishable from having had evidence that happened to show nothing.
func TestVerifyWithoutAPolicyIsNotAFailure(t *testing.T) {
	h := evidenceHarness(t)
	h.writeMemory("user-preference", "The user dislikes Rust.")

	h.call("memory_verify", map[string]any{
		"key": "user-preference", "outcome": "reaffirmed", "expected_memory_version": 1,
		"reason": "the user said so again today",
	})
	entry := h.call("memory_history", map[string]any{"key": "user-preference"})["history"].([]any)[0].(map[string]any)
	if _, present := entry["policy_version"]; present {
		t.Errorf("a policy version was recorded where there was no policy: %#v", entry)
	}
	if _, present := entry["evidence"]; present {
		t.Errorf("evidence was recorded where there was none: %#v", entry)
	}
}

func TestVerifyRefusesAVersionYouDidNotRead(t *testing.T) {
	h := evidenceHarness(t)
	h.writeMemory("wire-format", "first")
	h.call("memory_write", map[string]any{
		"scope": "project", "key": "wire-format", "type": "project",
		"description": "about wire-format", "body": "second", "expected_version": 1,
	})
	if code := h.errCode("memory_verify", map[string]any{
		"key": "wire-format", "outcome": "reaffirmed", "expected_memory_version": 1,
	}); code != "cas_conflict" {
		t.Fatalf("code = %q, want cas_conflict", code)
	}
}

func TestVerifyRejectsAnOutcomeOutsideTheVocabulary(t *testing.T) {
	h := evidenceHarness(t)
	h.writeMemory("wire-format", "x")
	if code := h.errCode("memory_verify", map[string]any{
		"key": "wire-format", "outcome": "looks-fine", "expected_memory_version": 1,
	}); code != "invalid_input" {
		t.Fatalf("code = %q, want invalid_input", code)
	}
}

// Refuting says what was found. It does not delete: memories are the state of
// the system, and what to do about a refuted one is a separate decision.
func TestRefutingDeletesNothing(t *testing.T) {
	h := evidenceHarness(t)
	h.writeMemory("wrong-thing", "This turned out to be false.")
	h.call("memory_verify", map[string]any{
		"key": "wrong-thing", "outcome": "refuted", "expected_memory_version": 1,
		"reason": "the flag was removed upstream",
	})
	out := h.call("memory_read", map[string]any{"scope": "project", "key": "wrong-thing"})
	if out["found"] != true {
		t.Fatal("refuting a memory deleted it")
	}
}

func TestListIncludeVerification(t *testing.T) {
	h := evidenceHarness(t)
	h.writeMemory("checked", "Somebody looked at this.")
	h.writeMemory("unchecked", "Nobody ever did.")
	h.call("memory_verify", map[string]any{
		"key": "checked", "outcome": "reaffirmed", "expected_memory_version": 1,
	})

	entries := h.call("memory_list", map[string]any{
		"scope": "project", "include_verification": true,
	})["entries"].([]any)

	byKey := map[string]map[string]any{}
	for _, e := range entries {
		m := e.(map[string]any)
		byKey[m["key"].(string)] = m
	}

	checked := byKey["checked"]["verification"].(map[string]any)
	if checked["total"].(float64) != 1 {
		t.Errorf("checked total = %v, want 1", checked["total"])
	}
	if checked["last"].(map[string]any)["outcome"] != "reaffirmed" {
		t.Errorf("checked last = %#v", checked["last"])
	}

	// Present but empty, never omitted: "nobody has checked" is a real answer
	// and must not look like "you did not ask".
	unchecked, ok := byKey["unchecked"]["verification"].(map[string]any)
	if !ok {
		t.Fatal("verification was omitted for a memory with no history")
	}
	if _, present := unchecked["last"]; present {
		t.Errorf("a never-checked memory reported a last verification: %#v", unchecked)
	}
	if unchecked["total"].(float64) != 0 {
		t.Errorf("unchecked total = %v, want 0", unchecked["total"])
	}
}

func TestVerificationIsRejectedInTheGlobalScope(t *testing.T) {
	h := evidenceHarness(t)
	if code := h.errCode("memory_list", map[string]any{
		"scope": "global", "include_verification": true,
	}); code != "invalid_input" {
		t.Fatalf("code = %q, want invalid_input", code)
	}
}

func TestVerifyRequiresRegistration(t *testing.T) {
	h := newHarness(t)
	commitFile(t, h.worktree, "README.md", "start", "initial")
	h.open()
	if code := h.errCode("memory_verify", map[string]any{
		"key": "anything", "outcome": "reaffirmed", "expected_memory_version": 1,
	}); code != "wrong_state" {
		t.Fatalf("code = %q, want wrong_state", code)
	}
}

// Nothing anywhere derives a ranking, score or threshold from this history.
// Stage 3 of docs/memory-model.md is explicit that it cannot be calibrated until
// the history is long enough, and the history has just started.
func TestNoRankingIsExposed(t *testing.T) {
	h := evidenceHarness(t)
	h.writeMemory("anything", "x")
	for _, order := range []string{"least_verified", "stale", "priority", "freshness"} {
		if code := h.errCode("memory_list", map[string]any{
			"scope": "project", "order_by": order,
		}); code != "invalid_input" {
			t.Errorf("order_by %q was accepted (code %q) — no ranking may exist yet", order, code)
		}
	}
}
