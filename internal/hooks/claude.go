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
	"strings"

	"github.com/happyarch/stigmergy/internal/store"
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
	// AgentID and AgentType name the agent within the session, and are present
	// only when that is not the main thread. See Label.
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`
	// ToolUseID is the host's id for the call this payload describes. It is the
	// join between a hook and the MCP request it precedes: the same value arrives
	// at the server as _meta["claudecode/toolUseId"]. See store.CallerTicket.
	ToolUseID string `json:"tool_use_id"`
	// StopHookActive is set when the agent is only still running because a Stop
	// hook blocked it. It is the loop guard: a hook that blocks unconditionally
	// on every Stop would never let the agent finish at all.
	StopHookActive bool           `json:"stop_hook_active"`
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

// StopBlock refuses to let the agent end its turn, and says why.
//
// This is the only channel in Claude Code that reaches an agent which is not
// asking for anything. Mail cannot wait for the agent to think to call
// mailbox_inbox — the whole problem is that it does not think to — and it cannot
// wait for the next user prompt either, because the user may not send one for an
// hour, and the agent it is blocking is stuck the whole time. So delivery happens
// at the one moment every agent reliably reaches: the end of its turn.
//
// The reason text goes into the agent's context, and the turn continues.
type StopBlock struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// NewStopBlock builds a Stop decision that hands the agent its mail instead of
// letting it finish.
func NewStopBlock(reason string) StopBlock {
	return StopBlock{Decision: "block", Reason: reason}
}

// PromptContext injects text at the top of a user's turn.
type PromptContext struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// NewPromptContext builds a UserPromptSubmit injection. It is the gentle half of
// delivery: an agent coming back to a fresh instruction sees what is waiting
// before it plans around it.
func NewPromptContext(text string) PromptContext {
	var c PromptContext
	c.HookSpecificOutput.HookEventName = "UserPromptSubmit"
	c.HookSpecificOutput.AdditionalContext = text
	return c
}

// IsAgent reports whether this call came from an agent inside the session
// rather than from the session's main thread.
//
// This used to be a guess. The payload schema is not documented at this level of
// detail, so the code looked for any of eight plausible field names and treated
// "none present" as "this is the root" — safe in the direction that mattered,
// and wrong about everything else. It is now settled by observation: a live
// Claude Code 2.1.220 session, `stigmergy hook dump` on PreToolUse, one main
// thread and one peer.
//
//	main thread   no agent_id, no agent_type
//	peer agent    agent_id "a4d39a33457e0df63", agent_type "general-purpose"
//	              session_id IDENTICAL to the main thread's
//
// SubagentStop carries the same two fields, which is what lets an agent's root
// be ended the moment it finishes rather than waiting out the TTL.
//
// The absence rule is unchanged and still load-bearing: no agent_id means the
// main thread, and the main thread must keep the session's own label or it loses
// every claim it holds.
func (in *ClaudeInput) IsAgent() bool { return in.AgentID != "" }

// Label is the identity this call should act under: the session's own label for
// the main thread, and an agent label for anyone else.
func (in *ClaudeInput) Label() string {
	return store.AgentLabel(in.SessionID, in.AgentType, in.AgentID)
}

// sessionOnly are the tools that stay with the session root even now that the
// agents inside it have identities of their own.
//
// The line is drawn by lifetime, not by rank. A peer exists for a minute: it can
// be handed a file for that minute and give it back, which is what a claim is,
// but it cannot hold a conversation that outlives it, and a memory it writes
// will be read by agents who have no way to ask it what it meant. Mail addressed
// to an agent that ended before the reply arrived is worse than no mailbox at
// all — the sender waits on someone who is already gone.
//
// So: claims yes, memory and mail no. The claim tools are deliberately absent
// from this list; that is the whole point of the change that introduced it.
var sessionOnly = map[string]string{
	"memory_write":          "memories outlive you",
	"memory_promote":        "memories outlive you",
	"memory_delete":         "memories outlive you",
	"memory_evidence_set":   "memories outlive you",
	"memory_evidence_clear": "memories outlive you",
	"memory_verify":         "memories outlive you",
	"memory_link":           "memories outlive you",
	"memory_unlink":         "memories outlive you",
	"mailbox_send":          "a reply would arrive after you have finished",
	"mailbox_inbox":         "a reply would arrive after you have finished",
	"mailbox_threads":       "a reply would arrive after you have finished",
	"mailbox_thread":        "a reply would arrive after you have finished",
	"mailbox_mark_read":     "a reply would arrive after you have finished",
	"mailbox_resolve":       "a reply would arrive after you have finished",
	"root_register":         "your root is created for you, from the identity your host gave you",
	"root_deregister":       "your root ends when you do",
}

// SessionOnlyTool returns the refusal for a tool an agent inside a session may
// not call, or "" if it may.
//
// The refusal says which agent is being refused and what it may do instead,
// because the version of this that said only "you are a subagent" taught agents
// a false model of the system: they concluded they were shut out of coordination
// entirely and stopped trying to claim anything.
func SessionOnlyTool(toolName string) string {
	name := strings.TrimPrefix(toolName, "mcp__stigmergy__")
	why, blocked := sessionOnly[name]
	if !blocked {
		return ""
	}
	return "stigmergy: " + toolName + " belongs to the session root, not to you — " + why + ". " +
		"You do have an identity of your own and you may claim files with it: claim_acquire, claim_renew and " +
		"claim_release all work, and the claims you take are yours and block everyone else. " +
		"Report anything worth remembering to your root and let it record what lasts."
}
