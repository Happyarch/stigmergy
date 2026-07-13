<!-- stigmergy:begin — managed block, do not edit; `stigmergy init` regenerates it -->
## stigmergy

This project uses **stigmergy** for memory and coordination shared across every agent working here (Codex and Claude Code alike).

- **Memory lives in stigmergy, not in Codex's own memories.** Use `memory_search` before starting work, and record durable facts with `memory_write`.
- **Register at the start of every session**: `context_open`, then `root_register` with your session id as `session_label`.
- **Claim before you edit** anything another agent might touch: `claim_acquire`, and check with `claim_check` if you are unsure.
- **Claims cannot be enforced before an edit here.** Codex hooks may warn, but they cannot block a write in advance. If you edit a file another agent has claimed, the turn is halted *after* the edit lands and the work may have to be undone. Check claims yourself; nothing else will stop you.
- **When a claim blocks you**, negotiate with `mailbox_send`, or work elsewhere.
- **Only the root session** may claim, write memory, or send mail. For read-only exploration use `stigmergy explore`, not a native subagent: native subagents inherit your write access and are not a boundary.
<!-- stigmergy:end -->
