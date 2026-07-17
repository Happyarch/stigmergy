package hooks

import (
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

// CodexInput is the payload Codex writes to a hook's stdin.
//
// The common fields are documented (session_id, cwd, hook_event_name, and for
// SessionStart, source). The shape of tool_input is not — so it is kept as a
// raw map and mined by ExtractPaths rather than modeled as a struct that would
// break the first time Codex changed it.
type CodexInput struct {
	SessionID     string         `json:"session_id"`
	CWD           string         `json:"cwd"`
	HookEventName string         `json:"hook_event_name"`
	ToolName      string         `json:"tool_name"`
	Source        string         `json:"source"`
	ToolInput     map[string]any `json:"tool_input"`
	Raw           map[string]any `json:"-"`
}

// DecodeCodex reads a Codex hook payload.
func DecodeCodex(r io.Reader) (*CodexInput, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var in CodexInput
	if err := json.Unmarshal(data, &in); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(data, &in.Raw)
	return &in, nil
}

// pathKeys are the field names that hold a file path in a tool call.
var pathKeys = map[string]bool{
	"file_path": true, "filepath": true, "path": true, "notebook_path": true,
}

// patchFileHeader matches the file headers inside an apply_patch payload:
//
//	*** Add File: src/main.go
//	*** Update File: src/api/handlers.go
//	*** Delete File: old.go
//	*** Move to: src/renamed.go
//
// One apply_patch call can touch many files, which is why path extraction
// returns a list rather than a single path: a claim on any one of them is a
// conflict, and reading only the first would let the rest through unnoticed.
//
// "Move to:" is the odd one out — it does not say "File:" — and it was missed
// until opencode's copy of the apply_patch format made us read the grammar
// again. A rename writes its destination as surely as a create does, so a claim
// on the destination is a conflict; without this the halt never fired for it.
// Both hosts share the format and both were affected.
var patchFileHeader = regexp.MustCompile(`(?m)^\*\*\*\s+(?:(?:Add|Update|Delete)\s+File|Move\s+to):\s*(.+?)\s*$`)

// ExtractPaths finds every file path a Codex tool call is about to write.
//
// It walks the payload rather than reading fixed fields, because the tool_input
// schema is not documented and differs between apply_patch, Edit and Write.
// Over-collecting is the safe direction here: an extra path costs one claim
// lookup, while a missed path means an unnoticed conflict — which on Codex
// means the edit lands, and the halt never fires.
func ExtractPaths(in *CodexInput) []string {
	return pathsIn(in.ToolInput)
}

// pathsIn walks an arbitrary decoded tool payload and returns every file path it
// can find, by key name or by reading apply_patch headers out of any string.
//
// It is shared with opencode, whose tool arguments are a different shape but the
// same problem: filePath lowercases to a key we already know, and its
// apply_patch is the same format. One walker means one place for a path to be
// missed, rather than one per host.
func pathsIn(payload any) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}

	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, val := range t {
				if s, ok := val.(string); ok {
					if pathKeys[strings.ToLower(k)] {
						add(s)
						continue
					}
					// apply_patch carries its files inside the patch text.
					for _, m := range patchFileHeader.FindAllStringSubmatch(s, -1) {
						add(m[1])
					}
					continue
				}
				walk(val)
			}
		case []any:
			for _, item := range t {
				walk(item)
			}
		}
	}
	walk(payload)
	return out
}

// CodexWarning is a PreToolUse response.
//
// Codex's PreToolUse cannot deny a tool call: only systemMessage is supported,
// and returning continue:false there marks the hook failed and lets the call
// proceed anyway. So this warns, and nothing more. The enforcement that Claude
// gets before the edit, Codex can only get after it — see CodexHalt.
type CodexWarning struct {
	SystemMessage string `json:"systemMessage"`
}

// CodexHalt is a PostToolUse response that stops the turn.
//
// The edit has already landed by the time this runs. Halting is damage
// limitation, not prevention: it stops the agent from building further work on
// top of a conflicting change, and tells it to put the file back.
type CodexHalt struct {
	Continue      bool   `json:"continue"`
	StopReason    string `json:"stopReason"`
	SystemMessage string `json:"systemMessage"`
}

// CodexContext is a SessionStart response, matching the documented
// hookSpecificOutput shape.
type CodexContext struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// NewCodexContext builds a SessionStart context injection.
func NewCodexContext(text string) CodexContext {
	var c CodexContext
	c.HookSpecificOutput.HookEventName = "SessionStart"
	c.HookSpecificOutput.AdditionalContext = text
	return c
}

// CodexWarnText is what an agent sees before an edit into a foreign claim.
//
// It must not say the edit was "blocked", because on Codex it was not and
// cannot be. Telling an agent it is protected by something that is not
// protecting it is worse than saying nothing: it would go on trusting a
// guarantee that does not exist.
func CodexWarnText(d Decision) string {
	var sb strings.Builder
	sb.WriteString("stigmergy: you are about to edit a path claimed by another agent.\n")
	sb.WriteString(ConflictDetail(d.Conflicts))
	sb.WriteString("\nCodex cannot stop this write before it happens — this is a warning, not a block. " +
		"If you proceed, the turn will be halted once the edit lands and you will have to undo it. " +
		"Stop now: negotiate with the owner using mailbox_send, or work on something else.")
	return sb.String()
}

// CodexHaltText is what an agent sees after the edit landed anyway.
func CodexHaltText(d Decision) string {
	var sb strings.Builder
	sb.WriteString("stigmergy halted this turn: an edit landed on a path claimed by another agent.\n")
	sb.WriteString(ConflictDetail(d.Conflicts))
	sb.WriteString("\nThe edit is already on disk. Revert it (`git checkout -- <file>`, or restore it from git), " +
		"then negotiate with the claim's owner using mailbox_send before trying again. " +
		"Do not build further work on top of this change.")
	return sb.String()
}
