package hostcfg

// ClaudeBlurb is the block written into CLAUDE.md.
//
// It says what the host cannot infer: that stigmergy replaces the agent's own
// memory here, and that claims are enforced. Keep it short — it is prepended to
// every session's context, and a wall of text competes with the user's own
// instructions for attention.
func ClaudeBlurb() string {
	return `## stigmergy

This project uses **stigmergy** for memory and coordination shared across every agent working here (Claude Code and Codex alike).

- **Memory lives in stigmergy, not in your own memory files.** Use ` + "`memory_search`" + ` before starting work, and record durable facts with ` + "`memory_write`" + `. Do not keep a private memory directory for this project.
- **Register at the start of every session**: ` + "`context_open`" + `, then ` + "`root_register`" + ` with your host session id as ` + "`session_label`" + `, and your own model id as ` + "`model`" + ` (the harness is ` + "`agent_kind`" + `; ` + "`model`" + ` is you).
- **Claim before you edit** anything another agent might touch: ` + "`claim_acquire`" + `. Edits to a file claimed by another agent are blocked outright.
- **When a claim blocks you**, the owner is named. Negotiate with ` + "`mailbox_send`" + `, or work elsewhere. Never edit around a claim.
- **Write to the agent that is actually there.** The conflict names the root holding the path and says whether it is still live; ` + "`root_list_active`" + ` shows who else is working here. Do not address a root id you remember from earlier — it may belong to an agent that has since died, while the one blocking you goes unasked.
- **Your mail is handed to you at the end of your turn**, and you cannot finish while a message is undelivered. Answer it: someone is usually blocked on you. Agreeing to hand over a file is not enough — ` + "`claim_release`" + ` is what frees it.
- **Only the root session** may claim, write memory, or send mail. Subagents read and report back.
`
}

// CodexBlurb is the block written into AGENTS.md.
//
// Codex gets an extra warning that Claude does not need: its hooks cannot deny
// a tool call before it runs, so a claimed file can only be protected after the
// edit has already landed. An agent that ignores claims here loses work rather
// than being politely stopped, and it deserves to know that.
func CodexBlurb() string {
	return `## stigmergy

This project uses **stigmergy** for memory and coordination shared across every agent working here (Codex and Claude Code alike).

- **Memory lives in stigmergy, not in Codex's own memories.** Use ` + "`memory_search`" + ` before starting work, and record durable facts with ` + "`memory_write`" + `.
- **Register at the start of every session**: ` + "`context_open`" + `, then ` + "`root_register`" + ` with your session id as ` + "`session_label`" + `, and your own model id as ` + "`model`" + ` (the harness is ` + "`agent_kind`" + `; ` + "`model`" + ` is you).
- **Claim before you edit** anything another agent might touch: ` + "`claim_acquire`" + `, and check with ` + "`claim_check`" + ` if you are unsure.
- **Claims cannot be enforced before an edit here.** Codex hooks may warn, but they cannot block a write in advance. If you edit a file another agent has claimed, the turn is halted *after* the edit lands and the work may have to be undone. Check claims yourself; nothing else will stop you.
- **When a claim blocks you**, negotiate with ` + "`mailbox_send`" + `, or work elsewhere.
- **Write to the agent that is actually there.** The warning names the root holding the path and says whether it is still live; ` + "`root_list_active`" + ` shows who else is working here. Do not address a root id you remember from earlier — it may belong to an agent that has since died, while the one blocking you goes unasked.
- **Your mail is put in front of you** as your turn begins and after each edit. Answer it: someone is usually blocked on you. Agreeing to hand over a file is not enough — ` + "`claim_release`" + ` is what frees it.
- **Only the root session** may claim, write memory, or send mail. For read-only exploration use ` + "`stigmergy explore`" + `, not a native subagent: native subagents inherit your write access and are not a boundary.
`
}

// AntigravityBlurb is written into rules/stigmergy.md inside the Antigravity
// plugin.
//
// Antigravity has full PreToolUse denial (same level as Claude Code), so the
// claims warning matches ClaudeBlurb. The Antigravity-specific notes are:
//   - session_label is the conversationId from the hook payload (injected at
//     the start of your session by the pre-invocation hook).
//   - Mail is delivered at Stop: you cannot finish while a message is undelivered.
//   - Subagents are asked, not stopped, and the ask is aimed at the root: don't
//     spawn one, the way CodexBlurb does. Codex's reason is that a native
//     subagent inherits the root's write access; Antigravity's is worse — a
//     subagent is a separate conversation, so the root gate cannot see it and
//     the session-start text will invite it to register as a root of its own.
//     The line addressed to the subagent itself is the backstop, and it arrives
//     by hook rather than by this file, which is what makes it dependable.
//
// Whether this file is loaded at all is not settled. Antigravity activates a
// rule in one of four modes — Manual, Always On, Model Decision, Glob — and the
// documentation does not say which applies to a rule that declares none, nor
// what syntax declares one. If the default is Manual, this reaches nobody until
// it is @-mentioned. Nothing load-bearing may rest on it: the pre-invocation
// hook injects the registration instructions itself, which is why registration
// works whether or not you are reading this.
func AntigravityBlurb() string {
	return `## stigmergy

This project uses **stigmergy** for memory and coordination shared across every agent working here (Antigravity, Claude Code and Codex alike).

- **Memory lives in stigmergy, not in your own memory files.** Use ` + "`memory_search`" + ` before starting work, and record durable facts with ` + "`memory_write`" + `. Do not keep a private memory directory for this project.
- **Register at the start of every session**: ` + "`context_open`" + `, then ` + "`root_register`" + ` with ` + "`agent_kind=\"antigravity\"`" + ` and your ` + "`conversationId`" + ` as ` + "`session_label`" + `, and your own model id as ` + "`model`" + `. The pre-invocation hook tells you the exact session_label to use.
- **Claim before you edit** anything another agent might touch: ` + "`claim_acquire`" + `. Edits to a file claimed by another agent are blocked outright.
- **When a claim blocks you**, the owner is named. Negotiate with ` + "`mailbox_send`" + `, or work elsewhere. Never edit around a claim.
- **Write to the agent that is actually there.** The conflict names the root holding the path and says whether it is still live; ` + "`root_list_active`" + ` shows who else is working here. Do not address a root id you remember from earlier — it may belong to an agent that has since died, while the one blocking you goes unasked.
- **Your mail is handed to you at the end of your turn**, and you cannot finish while a message is undelivered. Answer it: someone is usually blocked on you. Agreeing to hand over a file is not enough — ` + "`claim_release`" + ` is what frees it.
- **Only the root session** may claim, write memory, or send mail. For read-only exploration use ` + "`stigmergy explore`" + `, not ` + "`invoke_subagent`" + `: an Antigravity subagent is a separate conversation with its own id, so stigmergy cannot tell it from a root, and it will be told to register as one. Nothing enforces this here — it holds only because you keep it.
- **If another agent invoked you** rather than a person, you are not the root: do not register, do not claim, do not write memory. Report back to the agent that invoked you and let it record what lasts.
`
}
