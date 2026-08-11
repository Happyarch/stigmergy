package hooks

import (
	"slices"
	"strings"
	"testing"
)

func decodeOC(t *testing.T, payload string) *OpenCodeInput {
	t.Helper()
	in, err := DecodeOpenCode(strings.NewReader(payload))
	if err != nil {
		t.Fatalf("DecodeOpenCode: %v", err)
	}
	return in
}

// A real payload, as the plugin sends it. The tool payload here is the one the
// probe captured from a live opencode session rather than an invented one, so
// the fields are the ones that actually arrive.
func TestDecodingARealWritePayload(t *testing.T) {
	in := decodeOC(t, `{
	  "tool": "write",
	  "sessionID": "ses_0929439faffeSscT4oY55PuJYb",
	  "callID": "8byvBRnj62K9hiczREFn3rgv7xSh8Xal",
	  "directory": "/repo",
	  "worktree": "/repo",
	  "parentID": "",
	  "parentUnknown": false,
	  "args": {"content": "hello", "filePath": "/repo/notes.txt"}
	}`)

	if in.SessionID != "ses_0929439faffeSscT4oY55PuJYb" {
		t.Errorf("sessionID = %q", in.SessionID)
	}
	if !in.IsEdit() {
		t.Error("write is not recognised as an edit; it would go unguarded")
	}
	if got := in.EditedPaths(); len(got) != 1 || got[0] != "/repo/notes.txt" {
		t.Errorf("EditedPaths = %v, want [/repo/notes.txt]", got)
	}
	if in.IsSubagent() {
		t.Error("a session with no parent is a root")
	}
}

// opencode reports both, and they are the same in the common case. Worktree is
// the repository root the store keys on, so it wins.
func TestCWDPrefersTheWorktree(t *testing.T) {
	in := decodeOC(t, `{"directory":"/repo/sub","worktree":"/repo"}`)
	if in.CWD() != "/repo" {
		t.Errorf("CWD = %q, want /repo", in.CWD())
	}
	in = decodeOC(t, `{"directory":"/elsewhere"}`)
	if in.CWD() != "/elsewhere" {
		t.Errorf("CWD without a worktree = %q, want /elsewhere", in.CWD())
	}
}

// apply_patch names no file. Every path it touches is a header inside patchText,
// and a claim on any one of them is a conflict — so all of them must come out,
// not just the first.
func TestApplyPatchPathsComeOutOfThePatchText(t *testing.T) {
	in := decodeOC(t, `{
	  "tool": "apply_patch",
	  "args": {"patchText": "*** Begin Patch\n*** Update File: src/a.go\n@@\n-x\n+y\n*** Add File: src/b.go\n+new\n*** Delete File: src/c.go\n*** End Patch\n"}
	}`)
	if !in.IsEdit() {
		t.Fatal("apply_patch is not recognised as an edit")
	}
	got := in.EditedPaths()
	for _, want := range []string{"src/a.go", "src/b.go", "src/c.go"} {
		if !slices.Contains(got, want) {
			t.Errorf("EditedPaths missed %q; got %v", want, got)
		}
	}
}

// A rename writes its destination as surely as a create does. "Move to:" is the
// one header that does not say "File:", which is how it went unnoticed — on
// Codex as well, since both hosts share the format.
func TestApplyPatchFindsARenameDestination(t *testing.T) {
	in := decodeOC(t, `{
	  "tool": "apply_patch",
	  "args": {"patchText": "*** Begin Patch\n*** Update File: src/old.go\n*** Move to: src/new.go\n@@\n-x\n+y\n*** End Patch\n"}
	}`)
	got := in.EditedPaths()
	if !slices.Contains(got, "src/new.go") {
		t.Errorf("the rename destination is unguarded: EditedPaths = %v", got)
	}
	if !slices.Contains(got, "src/old.go") {
		t.Errorf("the rename source is unguarded: EditedPaths = %v", got)
	}
}

// Tools that do not write must not reach the guard: a bash command mentioning a
// claimed path is not an edit to it, and blocking it would be a false positive
// in the most annoying possible place.
func TestNonWriteToolsAreNotEdits(t *testing.T) {
	for _, tool := range []string{"read", "bash", "grep", "glob", "task", "stigmergy_claim_acquire"} {
		in := decodeOC(t, `{"tool":"`+tool+`"}`)
		if in.IsEdit() {
			t.Errorf("%q is treated as an edit", tool)
		}
	}
}

// The gate fires only on a session known to have a parent.
func TestSubagentDetectionNeedsAKnownParent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		want    bool
	}{
		{"a root has no parent", `{"sessionID":"ses_a","parentID":"","parentUnknown":false}`, false},
		{"a task child has one", `{"sessionID":"ses_b","parentID":"ses_a","parentUnknown":false}`, true},
		// The asymmetry that matters. If the plugin could not reach the opencode
		// server, treating the session as a subagent would deny the real root its
		// claims and its memory — stigmergy would break itself. Wrongly allowing a
		// subagent costs one row in the roster.
		{"unknown parentage is not a subagent", `{"sessionID":"ses_c","parentUnknown":true}`, false},
		{"unknown wins even if a parent is somehow set", `{"sessionID":"ses_d","parentID":"ses_a","parentUnknown":true}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeOC(t, tc.payload).IsSubagent(); got != tc.want {
				t.Errorf("IsSubagent = %v, want %v", got, tc.want)
			}
		})
	}
}

// Allow is said out loud on this host. The plugin is waiting on a JSON reply,
// and it cannot tell an empty answer from a crashed binary.
func TestAllowIsExplicit(t *testing.T) {
	if NewOpenCodeAllow().Deny {
		t.Error("allow denies")
	}
	d := NewOpenCodeDeny("src/main.go is held by r-abc")
	if !d.Deny {
		t.Error("deny allows")
	}
	// The reason is the whole advantage this host has over Codex: it is blocked
	// AND told who to talk to. A deny with nothing to read is a wall.
	if d.Reason == "" {
		t.Error("a deny carries no reason")
	}
}

func TestAGarbledPayloadIsAnError(t *testing.T) {
	if _, err := DecodeOpenCode(strings.NewReader("not json")); err == nil {
		t.Error("DecodeOpenCode accepted garbage; the caller must be able to allow and move on")
	}
}
