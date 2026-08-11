package hooks

import (
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
)

// AntigravityPreToolUse is the payload Antigravity writes to a PreToolUse
// hook's stdin.
//
// Antigravity's hook schema differs from Claude Code's in several ways:
//   - The tool being called is in toolCall.name / toolCall.args, not at the
//     top level.
//   - The session identifier is conversationId, not session_id.
//   - There is no cwd. There is workspacePaths, and it is plural for a reason:
//     a user can mount several roots at once. See CWDFor.
//   - Nothing in the payload identifies a subagent. See SubagentEvidence.
//
// Raw is kept alongside the typed fields so SubagentEvidence can scan for
// fields the typed struct does not capture, and so `stigmergy hook dump` can
// capture the full shape for schema verification.
type AntigravityPreToolUse struct {
	ToolCall struct {
		Name string         `json:"name"`
		Args map[string]any `json:"args"`
	} `json:"toolCall"`
	StepIdx        int      `json:"stepIdx"`
	ConversationID string   `json:"conversationId"`
	WorkspacePaths []string `json:"workspacePaths"`
	TranscriptPath string   `json:"transcriptPath"`

	// Raw holds the full decoded payload for SubagentEvidence scanning.
	Raw map[string]any `json:"-"`
}

// DecodeAntigravityPreToolUse reads a PreToolUse payload from stdin.
func DecodeAntigravityPreToolUse(r io.Reader) (*AntigravityPreToolUse, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var in AntigravityPreToolUse
	if err := json.Unmarshal(data, &in); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(data, &in.Raw)
	return &in, nil
}

// CWD returns a workspace path to resolve the repository from. Prefer CWDFor
// when a specific path is being guarded: this one only knows about the first
// mounted workspace, which is the right answer only when there is one.
func (in *AntigravityPreToolUse) CWD() string {
	return firstWorkspace(in.WorkspacePaths)
}

// CWDFor returns the mounted workspace that contains editPath, falling back to
// the first one.
//
// Claude Code hands a hook a single cwd; Antigravity hands it every workspace
// the user has mounted, and `/add-dir` means more than one is ordinary. Taking
// workspacePaths[0] and calling it the cwd is right until the day someone edits
// a file in the second repo, at which point the guard resolves the wrong
// repository, reads the wrong database — or none — and allows an edit to a
// claimed path while reporting nothing. The path being guarded is the only
// thing that knows which workspace it belongs to, so it decides.
func (in *AntigravityPreToolUse) CWDFor(editPath string) string {
	if filepath.IsAbs(editPath) {
		if ws := workspaceContaining(in.WorkspacePaths, editPath); ws != "" {
			return ws
		}
	}
	return firstWorkspace(in.WorkspacePaths)
}

// workspaceContaining returns the longest mounted workspace that is a parent of
// path. Longest wins because workspaces nest: a repo and a directory inside it
// can both be mounted, and the inner one is the more specific answer.
func workspaceContaining(workspaces []string, path string) string {
	best := ""
	for _, ws := range workspaces {
		if ws == "" {
			continue
		}
		rel, err := filepath.Rel(ws, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if len(ws) > len(best) {
			best = ws
		}
	}
	return best
}

func firstWorkspace(workspaces []string) string {
	if len(workspaces) > 0 {
		return workspaces[0]
	}
	return ""
}

// EditedPaths returns the file path the tool call is about to write.
//
// Every documented Antigravity write tool — write_to_file,
// replace_file_content, multi_replace_file_content — carries exactly one
// TargetFile, including the multi one, whose several edits all land in the same
// file. So TargetFile is not a best guess at the shape; it is the shape.
//
// There is deliberately no fallback that scans the other arguments for
// something path-shaped. Guessing gets this wrong in both directions: the args
// are a map, so iteration order is random and the guard would be
// nondeterministic, and write_to_file's CodeContent is a string that begins
// with "/" whenever the file it writes begins with a comment. An unrecognized
// tool must return nothing and be allowed, which is what Guard does with an
// empty result — the same contract the Claude Code payload keeps.
func (in *AntigravityPreToolUse) EditedPaths() []string {
	if in.ToolCall.Args == nil {
		return nil
	}
	if v, ok := in.ToolCall.Args["TargetFile"].(string); ok && v != "" {
		return []string{v}
	}
	return nil
}

// antiSubagentKeys are the raw payload fields that would identify a call as
// coming from a subagent.
//
// None of them are documented, and as of the current hook schema none of them
// are sent: Antigravity's payloads carry conversationId, workspacePaths,
// transcriptPath and artifactDirectoryPath, and nothing that links a
// conversation to a parent. A subagent is simply another conversation with its
// own id. So this list is a standing question, not a mechanism — every name in
// it is a guess at what a future schema might add, and today SubagentEvidence
// answers "root" for everyone.
//
// That is the conservative direction, and it is the only one available: the
// alternative, treating absence as subagent, would block the root itself from
// ever writing memory. But it must not be mistaken for enforcement. On
// Antigravity the root gate is inert, the boundary is the blurb asking a
// subagent not to register, and `stigmergy hook dump` against a real
// invoke_subagent call is what would settle whether anything better is
// possible. See docs/hosts.md.
var antiSubagentKeys = []string{
	"parent_ids", "parentIds",
	"parent_id", "parentId",
	"parentConversationId", "parent_conversation_id",
	"isSubagent", "is_subagent",
}

// SubagentEvidence reports whether the payload identifies this call as coming
// from a subagent, and which field said so. Absence of all indicators is
// treated as root, not as subagent — which today means it always reports root.
func (in *AntigravityPreToolUse) SubagentEvidence() (bool, string) {
	for _, k := range antiSubagentKeys {
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
		case []any:
			if len(t) > 0 {
				return true, k
			}
		default:
			return true, k
		}
	}
	return false, ""
}

// AntigravityStop is the payload Antigravity writes to a Stop hook's stdin.
type AntigravityStop struct {
	ExecutionNum      int    `json:"executionNum"`
	TerminationReason string `json:"terminationReason"`
	Error             string `json:"error"`
	// FullyIdle reports whether the agent's background tasks and async commands
	// have finished. It is not Claude Code's stop_hook_active and must not be
	// used as one: it says nothing about whether this hook is the reason the
	// agent is still running, and the loop terminates either way. What it
	// governs here is deregistration, not delivery — an agent with tasks still
	// running has not finished, so its claims must not be released, but its mail
	// is as undelivered as anyone else's.
	FullyIdle      bool     `json:"fullyIdle"`
	ConversationID string   `json:"conversationId"`
	WorkspacePaths []string `json:"workspacePaths"`
}

// DecodeAntigravityStop reads a Stop payload from stdin.
func DecodeAntigravityStop(r io.Reader) (*AntigravityStop, error) {
	var in AntigravityStop
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return nil, err
	}
	return &in, nil
}

// CWD returns the primary workspace path.
func (in *AntigravityStop) CWD() string {
	if len(in.WorkspacePaths) > 0 {
		return in.WorkspacePaths[0]
	}
	return ""
}

// AntigravityPreInvocation is the payload Antigravity writes to a
// PreInvocation hook's stdin.
type AntigravityPreInvocation struct {
	InvocationNum   int      `json:"invocationNum"`
	InitialNumSteps int      `json:"initialNumSteps"`
	ConversationID  string   `json:"conversationId"`
	WorkspacePaths  []string `json:"workspacePaths"`
}

// DecodeAntigravityPreInvocation reads a PreInvocation payload from stdin.
func DecodeAntigravityPreInvocation(r io.Reader) (*AntigravityPreInvocation, error) {
	var in AntigravityPreInvocation
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return nil, err
	}
	return &in, nil
}

// CWD returns the primary workspace path.
func (in *AntigravityPreInvocation) CWD() string {
	if len(in.WorkspacePaths) > 0 {
		return in.WorkspacePaths[0]
	}
	return ""
}

// AntigravityDecision is the PreToolUse response that controls whether a tool
// call is allowed, denied, or handed to the user.
type AntigravityDecision struct {
	Decision string `json:"decision"` // "allow", "deny", "ask", "force_ask"
	Reason   string `json:"reason,omitempty"`
}

// NewAntigravityDeny builds a deny decision that blocks the tool call and
// shows the agent why.
func NewAntigravityDeny(reason string) AntigravityDecision {
	return AntigravityDecision{Decision: "deny", Reason: reason}
}

// AntigravityStopDecision is the Stop hook response.
// Setting Decision to "continue" re-enters the execution loop and injects
// Reason as a system message into the conversation.
type AntigravityStopDecision struct {
	Decision string `json:"decision"` // "continue" or ""
	Reason   string `json:"reason,omitempty"`
}

// NewAntigravityStopBlock builds a Stop decision that re-enters the execution
// loop and injects the mail text as a system message.
func NewAntigravityStopBlock(reason string) AntigravityStopDecision {
	return AntigravityStopDecision{Decision: "continue", Reason: reason}
}

// AntigravityInjectOutput is the PreInvocation response that injects steps
// into the conversation before the model is called.
type AntigravityInjectOutput struct {
	InjectSteps []AntigravityInjectedStep `json:"injectSteps,omitempty"`
}

// AntigravityInjectedStep is a single step injected before the model call.
// stigmergy uses ephemeralMessage exclusively: it is shown in context but does not
// persist as a real turn, which keeps the conversation history clean.
type AntigravityInjectedStep struct {
	EphemeralMessage string `json:"ephemeralMessage,omitempty"`
}

// NewAntigravityInject builds a PreInvocation response that injects the given
// text as an ephemeral system message before the model is called.
func NewAntigravityInject(text string) AntigravityInjectOutput {
	return AntigravityInjectOutput{
		InjectSteps: []AntigravityInjectedStep{{EphemeralMessage: text}},
	}
}
