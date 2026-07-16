package hooks

import (
	"strings"
	"testing"
)

func decodePreToolUse(t *testing.T, payload string) *AntigravityPreToolUse {
	t.Helper()
	in, err := DecodeAntigravityPreToolUse(strings.NewReader(payload))
	if err != nil {
		t.Fatalf("DecodeAntigravityPreToolUse: %v", err)
	}
	return in
}

// The payload shape is Antigravity's, not Claude Code's: the tool is nested
// under toolCall, the session is a conversationId, and there is no cwd.
func TestDecodingARealPreToolUsePayload(t *testing.T) {
	in := decodePreToolUse(t, `{
	  "toolCall": {
	    "name": "write_to_file",
	    "args": {"TargetFile": "/workspace/project/main.go", "CodeContent": "package main", "Overwrite": true}
	  },
	  "stepIdx": 19,
	  "conversationId": "ec33ebf9-0cba-4100-8142-c61503f6c587",
	  "workspacePaths": ["/workspace/project"],
	  "transcriptPath": "/tmp/transcript.jsonl",
	  "artifactDirectoryPath": "/tmp/artifacts"
	}`)

	if in.ToolCall.Name != "write_to_file" {
		t.Errorf("tool name = %q", in.ToolCall.Name)
	}
	if in.ConversationID != "ec33ebf9-0cba-4100-8142-c61503f6c587" {
		t.Errorf("conversationId = %q", in.ConversationID)
	}
	if got := in.EditedPaths(); len(got) != 1 || got[0] != "/workspace/project/main.go" {
		t.Errorf("EditedPaths() = %v, want the TargetFile", got)
	}
	// Raw must carry fields the struct does not model, or `hook dump` and any
	// future subagent field would be invisible.
	if in.Raw["artifactDirectoryPath"] != "/tmp/artifacts" {
		t.Errorf("Raw did not capture the whole payload: %v", in.Raw)
	}
}

// Every documented write tool carries exactly one TargetFile — including
// multi_replace_file_content, whose several edits all land in one file.
func TestEveryWriteToolReportsItsTargetFile(t *testing.T) {
	for _, tool := range []string{"write_to_file", "replace_file_content", "multi_replace_file_content"} {
		in := decodePreToolUse(t, `{"toolCall":{"name":"`+tool+`","args":{"TargetFile":"/ws/a.go"}},"workspacePaths":["/ws"]}`)
		if got := in.EditedPaths(); len(got) != 1 || got[0] != "/ws/a.go" {
			t.Errorf("%s: EditedPaths() = %v, want [/ws/a.go]", tool, got)
		}
	}
}

// An unrecognized tool shape must produce no paths, so Guard allows it. The
// temptation is to scan the arguments for something path-shaped; this is the
// test that says why not. CodeContent is a string beginning with "/" whenever
// the file it writes opens with a comment, and a guard that mistook it for a
// path would block on a claim that has nothing to do with the edit — or, worse,
// pick a different argument on the next run, because Go randomizes map order.
func TestArgumentsAreNotSearchedForSomethingPathShaped(t *testing.T) {
	in := decodePreToolUse(t, `{
	  "toolCall": {"name": "write_to_file", "args": {"CodeContent": "// a comment\npackage main", "Instruction": "/usr/bin/env"}},
	  "workspacePaths": ["/ws"]
	}`)
	if got := in.EditedPaths(); len(got) != 0 {
		t.Errorf("EditedPaths() = %v, want none: no TargetFile means an unknown shape", got)
	}
}

// Antigravity reports every mounted workspace, and `/add-dir` makes more than
// one ordinary. The repository an edit belongs to is the one that contains it —
// not whichever happens to be listed first, which would send the guard to the
// wrong database and let a claimed file through unremarked.
func TestTheWorkspaceContainingTheEditWins(t *testing.T) {
	in := decodePreToolUse(t, `{
	  "toolCall": {"name": "write_to_file", "args": {"TargetFile": "/ws/second/pkg/x.go"}},
	  "workspacePaths": ["/ws/first", "/ws/second"]
	}`)
	if got := in.CWDFor("/ws/second/pkg/x.go"); got != "/ws/second" {
		t.Errorf("CWDFor() = %q, want the workspace holding the file", got)
	}
}

// Nested workspaces resolve to the most specific one: a repo and a directory
// inside it can both be mounted.
func TestNestedWorkspacesResolveToTheInnermost(t *testing.T) {
	in := decodePreToolUse(t, `{"workspacePaths": ["/ws", "/ws/inner"]}`)
	if got := in.CWDFor("/ws/inner/x.go"); got != "/ws/inner" {
		t.Errorf("CWDFor() = %q, want /ws/inner", got)
	}
	if got := in.CWDFor("/ws/other/x.go"); got != "/ws" {
		t.Errorf("CWDFor() = %q, want /ws", got)
	}
}

// A path under no mounted workspace, or a relative one, falls back to the first
// workspace rather than guessing.
func TestPathsOutsideEveryWorkspaceFallBack(t *testing.T) {
	in := decodePreToolUse(t, `{"workspacePaths": ["/ws/first", "/ws/second"]}`)
	if got := in.CWDFor("/elsewhere/x.go"); got != "/ws/first" {
		t.Errorf("CWDFor(absolute outside) = %q, want the first workspace", got)
	}
	if got := in.CWDFor("relative/x.go"); got != "/ws/first" {
		t.Errorf("CWDFor(relative) = %q, want the first workspace", got)
	}
	empty := decodePreToolUse(t, `{"toolCall":{"name":"write_to_file"}}`)
	if got := empty.CWDFor("/x.go"); got != "" {
		t.Errorf("CWDFor with no workspaces = %q, want empty", got)
	}
}

// The documented payload contains nothing that identifies a subagent, so this
// reports root for everyone — including actual subagents. That is a deliberate
// direction (the alternative blocks the real root from ever writing memory) but
// it means the root gate is inert on this host, and this test exists so that
// the day the payload grows a parent field, it is a test that changes rather
// than a surprise in production. See docs/hosts.md.
func TestSubagentEvidenceFindsNothingInADocumentedPayload(t *testing.T) {
	in := decodePreToolUse(t, `{
	  "toolCall": {"name": "mcp__stigmergy__memory_write", "args": {}},
	  "stepIdx": 3,
	  "conversationId": "child-conversation-id",
	  "workspacePaths": ["/ws"],
	  "transcriptPath": "/tmp/t.jsonl",
	  "artifactDirectoryPath": "/tmp/a"
	}`)
	if isSub, field := in.SubagentEvidence(); isSub {
		t.Errorf("SubagentEvidence() = true (%s); the documented payload has no such field", field)
	}
}

// If a parent field ever appears, the gate must fire on it without further
// code changes.
func TestSubagentEvidenceFiresOnAParentFieldIfOneAppears(t *testing.T) {
	in := decodePreToolUse(t, `{"conversationId": "child", "parentConversationId": "parent", "workspacePaths": ["/ws"]}`)
	isSub, field := in.SubagentEvidence()
	if !isSub || field != "parentConversationId" {
		t.Errorf("SubagentEvidence() = (%v, %q), want (true, parentConversationId)", isSub, field)
	}
}

// Stop's decision must be "continue" — anything else lets the agent finish, and
// the mail would be marked delivered to an agent that never saw it.
func TestStopBlockUsesTheDecisionThatReentersTheLoop(t *testing.T) {
	d := NewAntigravityStopBlock("you have mail")
	if d.Decision != "continue" {
		t.Errorf("Decision = %q, want continue", d.Decision)
	}
	if d.Reason != "you have mail" {
		t.Errorf("Reason = %q", d.Reason)
	}
}

func TestDenyCarriesTheReasonTheAgentReads(t *testing.T) {
	d := NewAntigravityDeny("claimed by someone else")
	if d.Decision != "deny" {
		t.Errorf("Decision = %q, want deny", d.Decision)
	}
	if d.Reason != "claimed by someone else" {
		t.Errorf("Reason = %q", d.Reason)
	}
}

// PreInvocation injects an ephemeral message: shown to the model, not persisted
// as a turn.
func TestInjectionIsAnEphemeralMessage(t *testing.T) {
	out := NewAntigravityInject("register first")
	if len(out.InjectSteps) != 1 || out.InjectSteps[0].EphemeralMessage != "register first" {
		t.Errorf("InjectSteps = %#v", out.InjectSteps)
	}
}

func TestStopPayloadCarriesFullyIdle(t *testing.T) {
	in, err := DecodeAntigravityStop(strings.NewReader(
		`{"executionNum":1,"terminationReason":"model_stop","error":"","fullyIdle":false,"conversationId":"c","workspacePaths":["/ws"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.FullyIdle {
		t.Error("FullyIdle decoded as true")
	}
	if in.CWD() != "/ws" {
		t.Errorf("CWD() = %q", in.CWD())
	}
	if in.TerminationReason != "model_stop" {
		t.Errorf("TerminationReason = %q", in.TerminationReason)
	}
}

func TestPreInvocationPayload(t *testing.T) {
	in, err := DecodeAntigravityPreInvocation(strings.NewReader(
		`{"invocationNum":0,"initialNumSteps":10,"conversationId":"c","workspacePaths":["/ws"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.InvocationNum != 0 || in.InitialNumSteps != 10 {
		t.Errorf("decoded = %#v", in)
	}
	if in.CWD() != "/ws" {
		t.Errorf("CWD() = %q", in.CWD())
	}
}
