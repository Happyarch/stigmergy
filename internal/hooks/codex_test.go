package hooks

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func decode(t *testing.T, payload string) *CodexInput {
	t.Helper()
	in, err := DecodeCodex(strings.NewReader(payload))
	if err != nil {
		t.Fatalf("DecodeCodex: %v", err)
	}
	return in
}

// The tool_input schema is undocumented and differs per tool, so extraction has
// to survive several shapes. A missed path on Codex is worse than elsewhere:
// the edit lands, and nothing halts the turn.
func TestExtractPaths(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    []string
	}{
		{
			"a plain file_path",
			`{"tool_name":"Edit","tool_input":{"file_path":"src/main.go"}}`,
			[]string{"src/main.go"},
		},
		{
			"a nested file_path",
			`{"tool_name":"Edit","tool_input":{"input":{"file_path":"src/main.go"}}}`,
			[]string{"src/main.go"},
		},
		{
			"apply_patch touching several files",
			`{"tool_name":"apply_patch","tool_input":{"patch":"*** Begin Patch\n*** Update File: src/api/handlers.go\n@@\n-old\n+new\n*** Add File: src/api/new.go\n+package api\n*** Delete File: src/old.go\n*** End Patch"}}`,
			[]string{"src/api/handlers.go", "src/api/new.go", "src/old.go"},
		},
		{
			"a list of edits",
			`{"tool_input":{"edits":[{"path":"a.go"},{"path":"b.go"}]}}`,
			[]string{"a.go", "b.go"},
		},
		{
			"duplicates collapse",
			`{"tool_input":{"file_path":"a.go","input":{"path":"a.go"}}}`,
			[]string{"a.go"},
		},
		{
			"a tool that touches no files",
			`{"tool_name":"Bash","tool_input":{"command":"ls -la"}}`,
			nil,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ExtractPaths(decode(t, c.payload))
			if len(got) != len(c.want) {
				t.Fatalf("ExtractPaths = %v, want %v", got, c.want)
			}
			found := map[string]bool{}
			for _, p := range got {
				found[p] = true
			}
			for _, w := range c.want {
				if !found[w] {
					t.Fatalf("ExtractPaths = %v, missing %q — an unnoticed path means an unhalted conflict", got, w)
				}
			}
		})
	}
}

// Codex's PreToolUse cannot deny, so the warning must be honest about what will
// happen next rather than implying the write was stopped.
func TestCodexWarnTextTellsTheAgentWhatWillHappen(t *testing.T) {
	f := newFixture(t)
	owner := f.claim(t, "sess-owner", "src", true)

	d := Guard("codex", "sess-other", f.worktree, []string{filepath.Join(f.worktree, "src", "main.go")})
	if d.Allow {
		t.Fatal("the guard should have found a conflict")
	}
	warn := CodexWarnText(d)
	for _, want := range []string{owner, "halted", "mailbox_send", "not a block"} {
		if !strings.Contains(warn, want) {
			t.Errorf("the warning does not mention %q:\n%s", want, warn)
		}
	}
	// Claiming the edit was blocked would be a lie on Codex, and would leave
	// the agent trusting a protection it does not have.
	if strings.Contains(warn, "is blocked by") {
		t.Errorf("the Codex warning claims the edit was blocked, which Codex cannot do:\n%s", warn)
	}

	halt := CodexHaltText(d)
	for _, want := range []string{"already on disk", "Revert", owner} {
		if !strings.Contains(halt, want) {
			t.Errorf("the halt message does not mention %q:\n%s", want, halt)
		}
	}
}

// The halt payload must match what Codex honors for PostToolUse: continue:false
// plus a stopReason. Getting this shape wrong means claims are unenforceable on
// Codex entirely.
func TestCodexHaltSerializesToTheDocumentedShape(t *testing.T) {
	data, err := json.Marshal(CodexHalt{
		Continue: false, StopReason: "stigmergy: claim conflict", SystemMessage: "details",
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["continue"] != false {
		t.Fatalf("continue = %v, want false — the turn would not halt", got["continue"])
	}
	if got["stopReason"] == "" || got["systemMessage"] == "" {
		t.Fatalf("halt payload = %v, want a stopReason and a systemMessage", got)
	}

	// A warning must NOT carry continue/stopReason: on PreToolUse those are
	// unsupported, and sending them makes Codex mark the hook failed and run
	// the tool call anyway — turning a warning into nothing at all.
	data, err = json.Marshal(CodexWarning{SystemMessage: "careful"})
	if err != nil {
		t.Fatal(err)
	}
	var warn map[string]any
	if err := json.Unmarshal(data, &warn); err != nil {
		t.Fatal(err)
	}
	if len(warn) != 1 {
		t.Fatalf("the PreToolUse warning carries unsupported fields %v — Codex would mark the hook failed", warn)
	}
	if _, ok := warn["systemMessage"]; !ok {
		t.Fatal("the warning must carry a systemMessage")
	}
}

func TestCodexSessionStartContextShape(t *testing.T) {
	data, err := json.Marshal(NewCodexContext("register with stigmergy"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.HookSpecificOutput.HookEventName != "SessionStart" || got.HookSpecificOutput.AdditionalContext == "" {
		t.Fatalf("SessionStart output = %s, want the documented hookSpecificOutput shape", data)
	}
}
