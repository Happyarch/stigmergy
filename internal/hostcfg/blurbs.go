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
- **Register at the start of every session**: ` + "`context_open`" + `, then ` + "`root_register`" + ` with your host session id as ` + "`session_label`" + `.
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
- **Register at the start of every session**: ` + "`context_open`" + `, then ` + "`root_register`" + ` with your session id as ` + "`session_label`" + `.
- **Claim before you edit** anything another agent might touch: ` + "`claim_acquire`" + `, and check with ` + "`claim_check`" + ` if you are unsure.
- **Claims cannot be enforced before an edit here.** Codex hooks may warn, but they cannot block a write in advance. If you edit a file another agent has claimed, the turn is halted *after* the edit lands and the work may have to be undone. Check claims yourself; nothing else will stop you.
- **When a claim blocks you**, negotiate with ` + "`mailbox_send`" + `, or work elsewhere.
- **Write to the agent that is actually there.** The warning names the root holding the path and says whether it is still live; ` + "`root_list_active`" + ` shows who else is working here. Do not address a root id you remember from earlier — it may belong to an agent that has since died, while the one blocking you goes unasked.
- **Your mail is put in front of you** as your turn begins and after each edit. Answer it: someone is usually blocked on you. Agreeing to hand over a file is not enough — ` + "`claim_release`" + ` is what frees it.
- **Only the root session** may claim, write memory, or send mail. For read-only exploration use ` + "`stigmergy explore`" + `, not a native subagent: native subagents inherit your write access and are not a boundary.
`
}
