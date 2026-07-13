// Package hooks implements the host hook handlers: the code that runs on every
// edit an agent attempts.
//
// Two rules govern everything here. First, latency: these run in the agent's
// critical path, so the budget is tens of milliseconds, which is why the hook
// path opens the database read-only, never migrates, and never shells out to
// git. Second, failure: a hook that cannot tell whether a path is claimed must
// not guess. On Claude Code, where denial is possible, it denies.
package hooks

import (
	"encoding/json"
	"io"
)

// ClaudeInput is the payload Claude Code writes to a hook's stdin.
//
// Raw is kept alongside the typed fields because the payload carries host
// details we cannot pin down from the docs alone (see SubagentEvidence), and
// because `stigmergy hook dump` exists to capture exactly that.
type ClaudeInput struct {
	SessionID      string         `json:"session_id"`
	TranscriptPath string         `json:"transcript_path"`
	CWD            string         `json:"cwd"`
	HookEventName  string         `json:"hook_event_name"`
	ToolName       string         `json:"tool_name"`
	ToolInput      map[string]any `json:"tool_input"`
	Raw            map[string]any `json:"-"`
}

// DecodeClaude reads a hook payload from stdin.
func DecodeClaude(r io.Reader) (*ClaudeInput, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var in ClaudeInput
	if err := json.Unmarshal(data, &in); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(data, &in.Raw)
	return &in, nil
}

// EditedPaths returns the file paths a tool call is about to write.
//
// An empty result means "this call does not touch a file we recognize", which
// callers must treat as allow: guessing at an unknown tool's arguments would
// block edits stigmergy does not understand, and a coordination tool that
// blocks unpredictably is worse than one that occasionally misses.
func (in *ClaudeInput) EditedPaths() []string {
	if in.ToolInput == nil {
		return nil
	}
	var out []string
	for _, key := range []string{"file_path", "notebook_path", "path"} {
		if v, ok := in.ToolInput[key].(string); ok && v != "" {
			out = append(out, v)
		}
	}
	return out
}

// PreToolUseDeny is the Claude Code response that blocks a tool call.
type PreToolUseDeny struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

// NewDeny builds a deny decision carrying the reason the agent will read.
func NewDeny(reason string) PreToolUseDeny {
	var d PreToolUseDeny
	d.HookSpecificOutput.HookEventName = "PreToolUse"
	d.HookSpecificOutput.PermissionDecision = "deny"
	d.HookSpecificOutput.PermissionDecisionReason = reason
	return d
}

// SessionStartContext injects text into an agent's context at session start.
type SessionStartContext struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// NewSessionContext builds a SessionStart injection.
func NewSessionContext(text string) SessionStartContext {
	var c SessionStartContext
	c.HookSpecificOutput.HookEventName = "SessionStart"
	c.HookSpecificOutput.AdditionalContext = text
	return c
}

// subagentKeys are the payload fields that would identify a call as coming from
// a subagent rather than the root session.
//
// Which of these Claude Code actually sends is an open question:
// the hook payload schema is not documented at this level of detail, and it
// changes between versions. So instead of betting on one field name, we look
// for any of them, and — critically — treat "none present" as "this is a root",
// not as "this is a subagent". Guessing wrong in that direction would block the
// root itself from ever writing memory, which breaks the tool completely; the
// cost of guessing wrong the other way is that a subagent's write slips through
// to be caught by the agent-file boundary instead. Run `stigmergy hook dump`
// against a real subagent to settle this and prune the list.
var subagentKeys = []string{
	"subagent_type", "subagentType",
	"agent_type", "agentType",
	"is_subagent", "isSubagent",
	"parent_tool_use_id", "parentToolUseId",
}

// SubagentEvidence reports whether the payload identifies this call as coming
// from a subagent, and which field said so.
func (in *ClaudeInput) SubagentEvidence() (bool, string) {
	for _, k := range subagentKeys {
		v, ok := in.Raw[k]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case string:
			if t != "" {
				return true, k
			}
		case bool:
			if t {
				return true, k
			}
		default:
			return true, k
		}
	}
	return false, ""
}
