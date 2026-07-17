package hooks

import (
	"encoding/json"
	"io"

	"github.com/happyarch/stigmergy/internal/hosts"
)

// opencode is the only host whose payload we design ourselves.
//
// The others hand us whatever their hook system emits, and the work is guessing
// what it means: Claude's session_id, Codex's tool_input, Antigravity's
// conversationId, each documented poorly or not at all. opencode has no hook
// system that can reach a subprocess — it has a plugin API, in TypeScript,
// inside its own runtime — so stigmergy ships a small plugin that calls this
// binary. Both ends of that wire are ours, which means the payload can say
// exactly what the Go side needs and nothing else.
//
// That is worth being deliberate about rather than mirroring opencode's
// internals into Go structs. The plugin is the adapter; this is the contract.
// If opencode changes the shape of its hook arguments, the plugin absorbs it and
// this file does not move.
//
// One thing the plugin resolves before calling, because only it can: whether the
// session is a subagent. opencode's tool payload carries the sessionID but not
// its parentage, and the answer needs an HTTP call to the opencode server, which
// the plugin holds a client for.

// OpenCodeInput is what the plugin sends on stdin, for every hook.
//
// Fields not relevant to a given hook are simply absent — the plugin sends what
// it has, and a missing tool or empty args is not an error.
type OpenCodeInput struct {
	// Tool is opencode's tool id: edit, write, apply_patch, bash, task, or an
	// MCP tool such as stigmergy_claim_acquire.
	Tool string `json:"tool"`
	// SessionID is opencode's session id (ses_…). It is the session_label a root
	// registers under, and what the claim guard uses to tell an agent's own
	// claims from everyone else's.
	SessionID string `json:"sessionID"`
	CallID    string `json:"callID"`

	// Directory is where the agent is working; Worktree is the repository root.
	// opencode reports both, and they are the same in the common case.
	Directory string `json:"directory"`
	Worktree  string `json:"worktree"`

	// Args is the tool's arguments, exactly as opencode passed them.
	Args map[string]any `json:"args"`

	// ParentID is set when this session was spawned by another — opencode's task
	// tool gives its child a session of its own, with the parent recorded. Its
	// presence is the whole subagent gate on this host.
	ParentID string `json:"parentID"`

	// ParentUnknown says the plugin could not find out. Absence of a parent and
	// failure to ask are different facts, and collapsing them is how a gate
	// starts denying real roots: an agent that cannot be checked must not be
	// treated as a subagent.
	ParentUnknown bool `json:"parentUnknown"`
}

// DecodeOpenCode reads a plugin payload.
func DecodeOpenCode(r io.Reader) (*OpenCodeInput, error) {
	var in OpenCodeInput
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return nil, err
	}
	return &in, nil
}

// CWD is the directory to resolve the repository from.
//
// Worktree first: opencode reports it as the git root, which is what the store
// keys on. Directory is the fallback for a session started somewhere else.
func (in *OpenCodeInput) CWD() string {
	if in.Worktree != "" {
		return in.Worktree
	}
	return in.Directory
}

// IsSubagent reports whether this session was spawned by another agent.
//
// Only a session known to have a parent counts. When the plugin could not ask,
// this is false — the same asymmetry the Antigravity gate settled on, and for
// the same reason: treating "we do not know" as "subagent" would block the real
// root from claiming or writing memory, which breaks the tool outright, while
// the other way round costs a subagent that should not have registered.
func (in *OpenCodeInput) IsSubagent() bool {
	return !in.ParentUnknown && in.ParentID != ""
}

// EditedPaths is every file this tool call is about to write.
//
// opencode's three write tools are edit, write and apply_patch — there is no
// patch and no multiedit. The first two carry an absolute filePath; apply_patch
// carries only patchText, with the paths inside it as headers. pathsIn handles
// both, and it is the same walker Codex uses: filePath lowercases to a key it
// already knows, and the patch format is shared.
func (in *OpenCodeInput) EditedPaths() []string {
	return pathsIn(in.Args)
}

// IsEdit reports whether this tool call writes to the working tree.
//
// The list is in hosts because the generated plugin needs the same one; see
// hosts.OpenCodeEditTools.
func (in *OpenCodeInput) IsEdit() bool {
	for _, t := range hosts.OpenCodeEditTools {
		if t == in.Tool {
			return true
		}
	}
	return false
}

// OpenCodeDeny is what the plugin gets back when a call must not proceed.
//
// The plugin turns this into a thrown error, which opencode treats as an
// unrecoverable defect in the tool call: the tool never runs, and — verified
// against a live session — the Reason reaches the model verbatim. So this host
// gets a real block that can also explain itself, which is the same standard as
// Claude Code and better than Codex, where the edit lands first.
type OpenCodeDeny struct {
	Deny   bool   `json:"deny"`
	Reason string `json:"reason,omitempty"`
}

// NewOpenCodeAllow is the answer for a call that may proceed.
//
// It is explicit rather than empty output. The other hosts read silence as
// allow, but here the plugin is waiting on a JSON reply, and a plugin that
// cannot tell "allowed" from "the binary died" would have to guess — and would
// guess allow, since throwing on every crash would make a missing stigmergy
// binary look like a locked repository.
func NewOpenCodeAllow() OpenCodeDeny { return OpenCodeDeny{Deny: false} }

// NewOpenCodeDeny refuses a call and says why.
func NewOpenCodeDeny(reason string) OpenCodeDeny {
	return OpenCodeDeny{Deny: true, Reason: reason}
}

// OpenCodeContext is text for the agent to read, returned to the plugin.
//
// The plugin appends it to the user's message as a synthetic part, which is the
// closest thing opencode has to Claude's UserPromptSubmit: it fires once per
// real user prompt, and never for the internal title, summary or compaction
// agents.
type OpenCodeContext struct {
	Context string `json:"context,omitempty"`
}
