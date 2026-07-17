// Package hosts is the one place a host is declared.
//
// Before this package there was no such place. A host was three string literals
// and four if-branches: a blurb in hostcfg, a case in init, a row in doctor, an
// entry in store.AgentKinds. Nothing tied them together and nothing tested that
// they agreed, so Antigravity shipped half-added — configured and working, but
// absent from README.md, docs/usage.md, and the server instructions, which went
// on describing a two-host world.
//
// The deeper problem was the text. Each host carried its own hand-written copy
// of the same advice, and the copies drifted: AGENTS.md opened "(Codex and
// Claude Code alike)" while CLAUDE.md said "(Claude Code and Codex alike)", for
// no reason anyone chose. Worse, the copies contradicted each other where it
// mattered — one said edits to a claimed file are "blocked outright", the other
// that "nothing else will stop you" — and a user who symlinks AGENTS.md to
// CLAUDE.md, which is common, got whichever `stigmergy init` wrote last. An
// agent told the wrong one stops checking claims, and its edits land on another
// agent's files. That is the failure stigmergy exists to prevent, caused by
// stigmergy's own instructions.
//
// So the prose lives here, written once, and each host declares only where it
// genuinely differs. What differs is not arbitrary: it is what the host's hooks
// can actually do to an agent. Those are the fields below.
//
// This package deliberately imports nothing. store, hooks, mcpserver, hostcfg
// and cli all need to agree about hosts, and a leaf is the only thing they can
// all depend on without a cycle.
package hosts

// Claims is whether a host's hooks can stop an edit before it lands.
//
// This is the sharpest difference between hosts and the one an agent most needs
// told, because being wrong about it in either direction is expensive: an agent
// that thinks it is guarded when it is not will overwrite someone's work, and
// one that thinks it is unguarded wastes its turn checking what the hook would
// have caught.
type Claims int

const (
	// ClaimsBlocked: the edit is refused before it happens and the agent is told
	// who holds the claim. Claude Code, Antigravity and opencode.
	ClaimsBlocked Claims = iota
	// ClaimsWarned: the hook cannot deny, so the edit lands and the turn is
	// halted afterwards. Codex.
	ClaimsWarned
)

// Mail is whether a host can make an agent look at its mail.
//
// The distinction is delivery versus enforcement. Every host delivers; only some
// can refuse to let a turn end on an unread message. Where it cannot be
// enforced we say so rather than implying a guarantee we do not have — an agent
// that believes mail is guaranteed will not check for it.
type Mail int

const (
	// MailEnforced: delivered at the end of the turn, and the turn cannot end
	// while a message is undelivered. Claude Code and Antigravity.
	MailEnforced Mail = iota
	// MailAdvisory: delivered, but the agent can walk away from it. Codex and
	// opencode, neither of which has a hook that can hold a turn open.
	MailAdvisory
)

// Subagents is whether stigmergy can tell a subagent from a root.
//
// Only roots may claim, write memory or send mail. Where the host gives us no
// way to identify a subagent we cannot enforce that, and the honest move is to
// ask the agent not to spawn one rather than to pretend the gate is working.
type Subagents int

const (
	// SubagentsGated: the host's payload identifies a subagent, so the root gate
	// denies it. Claude Code, and opencode via the session's parentID.
	SubagentsGated Subagents = iota
	// SubagentsUnseen: nothing in the payload distinguishes one, so the gate
	// cannot fire and the rule holds only because the agent keeps it.
	SubagentsUnseen
)

// Host is what stigmergy knows about one agent harness.
type Host struct {
	// Kind is the agent_kind stored on a root. It is the closed set
	// store.AgentKinds validates against.
	Kind string
	// Name is how a person refers to the host.
	Name string
	// Flag is the value `stigmergy init --host` takes.
	Flag string

	Claims    Claims
	Mail      Mail
	Subagents Subagents

	// SessionEnv and ProjectEnv are the environment variables the host exports
	// its session id and project root in, or "" if it exports neither. When both
	// are set the MCP server reads them at startup and registers the root itself
	// — the agent never runs the context_open/root_register handshake it
	// routinely forgets, and the claim guard already keys "my own claims" on the
	// same session id, so a root minted this way is one the guard recognizes.
	//
	// Claude Code is the only host known to do this. The variables are
	// undocumented, so this is a best-effort convenience: absent them, nothing
	// self-registers and the explicit handshake is the fallback.
	SessionEnv string
	ProjectEnv string

	// SessionLabel says where the agent's session_label comes from, in words the
	// agent can act on. It is host-specific because the hosts call it different
	// things, and getting it wrong is not a small error: the claim guard uses it
	// to tell an agent's own claims from everyone else's, so an agent that
	// registers under the wrong label is blocked by its own claims.
	SessionLabel string

	// SubagentNote, when set, replaces the generic explanation of why subagents
	// are not a boundary here. The hosts have different reasons and the reason is
	// the part that persuades.
	SubagentNote string
}

// registry is every host stigmergy supports, in the order a person should see
// them.
var registry = []Host{
	{
		Kind:         "claude-code",
		Name:         "Claude Code",
		Flag:         "claude",
		Claims:       ClaimsBlocked,
		Mail:         MailEnforced,
		Subagents:    SubagentsGated,
		SessionLabel: "your host session id",
		SessionEnv:   "CLAUDE_CODE_SESSION_ID",
		ProjectEnv:   "CLAUDE_PROJECT_DIR",
	},
	{
		Kind:         "codex",
		Name:         "Codex",
		Flag:         "codex",
		Claims:       ClaimsWarned,
		Mail:         MailAdvisory,
		Subagents:    SubagentsUnseen,
		SessionLabel: "your session id",
		SubagentNote: "a native subagent inherits your write access and is not a boundary",
	},
	{
		Kind:         "antigravity",
		Name:         "Antigravity",
		Flag:         "antigravity",
		Claims:       ClaimsBlocked,
		Mail:         MailEnforced,
		Subagents:    SubagentsUnseen,
		SessionLabel: "your conversationId — the pre-invocation hook tells you the exact one to use",
		SubagentNote: "an Antigravity subagent is a separate conversation with its own id, so stigmergy " +
			"cannot tell it from a root, and it will be told to register as one",
	},
	{
		Kind:      "opencode",
		Name:      "opencode",
		Flag:      "opencode",
		Claims:    ClaimsBlocked,
		Mail:      MailAdvisory,
		Subagents: SubagentsGated,
		// The plugin hook carries this: opencode's tool.execute.before payload
		// names the session, so the agent never has to work it out.
		SessionLabel: "your opencode session id (ses_…) — the plugin tells you the exact one to use",
	},
}

// OpenCodeEditTools is every opencode tool that writes to the working tree.
//
// It lives here, rather than next to either of its two users, because it has
// two: the plugin generated in hostcfg decides which calls to send to the claim
// guard, and the guard in hooks decides which to act on. Declared twice, they
// would drift, and the failure would be silent in the worst direction — a tool
// the plugin forwards but the guard ignores is a claimed file quietly written.
//
// Taken from opencode's own tool definitions, not its documentation. There is no
// "patch" and no "multiedit". apply_patch is the awkward one: it names no file,
// and its paths are headers inside patchText.
var OpenCodeEditTools = []string{"edit", "write", "apply_patch"}

// OpenCodeMCPPrefix matches stigmergy's own tools inside opencode.
//
// opencode prefixes an MCP tool with its server name and an underscore, so
// claim_acquire arrives as stigmergy_claim_acquire. Claude's
// mcp__stigmergy__(...) matcher does not transfer — which is exactly the sort of
// thing that looks like it should.
const OpenCodeMCPPrefix = "stigmergy_"

// All returns every host.
func All() []Host {
	out := make([]Host, len(registry))
	copy(out, registry)
	return out
}

// Get returns the host with this agent_kind.
func Get(kind string) (Host, bool) {
	for _, h := range registry {
		if h.Kind == kind {
			return h, true
		}
	}
	return Host{}, false
}

// SelfRegisters reports whether this host hands its session to the environment,
// so the MCP server can register the root without the agent's handshake.
func (h Host) SelfRegisters() bool { return h.SessionEnv != "" && h.ProjectEnv != "" }

// SelfRegistration finds the self-registering host whose session is present in
// the environment, and returns it with the session id and project root it
// exported. ok is false when no such variable is set — a non-Claude host, or a
// Claude version that has stopped exporting it.
//
// getenv is taken as an argument rather than called directly so this package
// keeps importing nothing: the caller passes os.Getenv, and tests pass a map.
func SelfRegistration(getenv func(string) string) (host Host, sessionID, projectDir string, ok bool) {
	for _, h := range registry {
		if !h.SelfRegisters() {
			continue
		}
		if id := getenv(h.SessionEnv); id != "" {
			return h, id, getenv(h.ProjectEnv), true
		}
	}
	return Host{}, "", "", false
}

// Kinds is the closed set of agent_kind values, and the source store.AgentKinds
// is built from.
func Kinds() []string {
	out := make([]string, 0, len(registry))
	for _, h := range registry {
		out = append(out, h.Kind)
	}
	return out
}

// Flags is the set `--host` accepts, excluding "all".
func Flags() []string {
	out := make([]string, 0, len(registry))
	for _, h := range registry {
		out = append(out, h.Flag)
	}
	return out
}

// Names returns the display names of the hosts matching want, in registry
// order. It is how the host-neutral text — the server instructions, the docs —
// says which hosts do what without anyone having to remember to update a
// sentence when a host is added.
func Names(want func(Host) bool) []string {
	var out []string
	for _, h := range registry {
		if want(h) {
			out = append(out, h.Name)
		}
	}
	return out
}

// List renders names as "a", "a and b", or "a, b and c".
//
// The last comma is omitted on purpose: this text is read by agents and people
// alike, and it should read like prose rather than like a struct dump.
func List(names []string) string {
	switch len(names) {
	case 0:
		return "no host"
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	}
	out := ""
	for i, n := range names[:len(names)-1] {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out + " and " + names[len(names)-1]
}

// ClaimRule is what this host's agents must know about claims before they edit.
func (h Host) ClaimRule() string {
	if h.Claims == ClaimsBlocked {
		return "Edits to a file claimed by another agent are blocked outright, and the block names the owner."
	}
	return "Claims cannot be enforced before an edit here: the hooks may warn, but they cannot " +
		"block a write in advance. If you edit a file another agent has claimed, the turn is halted " +
		"after the edit lands and the work may have to be undone. Check claims yourself with " +
		"claim_check; nothing else will stop you."
}

// MailRule is what this host's agents must know about their mailbox.
func (h Host) MailRule() string {
	if h.Mail == MailEnforced {
		return "Your mail is handed to you at the end of your turn, and you cannot finish while a " +
			"message is undelivered."
	}
	return "Your mail is put in front of you as your turn begins and after each edit. Nothing here " +
		"makes you read it, and no one will know that you did not."
}

// SubagentRule is what this host's agents must know about who may write.
func (h Host) SubagentRule() string {
	const lead = "Only the root session may claim, write memory, or send mail."
	if h.Subagents == SubagentsGated {
		return lead + " Subagents read and report back to you; let the root record what lasts."
	}
	note := h.SubagentNote
	if note == "" {
		note = "stigmergy cannot tell a subagent from a root here"
	}
	return lead + " For read-only exploration use `stigmergy explore`, not a native subagent: " +
		note + ". Nothing enforces this here — it holds only because you keep it."
}
