<!-- stigmergy:begin — managed block, do not edit; `stigmergy init` regenerates it -->
## stigmergy

This project uses **stigmergy** for memory and coordination shared across every agent working here (Codex and Claude Code alike).

- **Memory lives in stigmergy, not in Codex's own memories.** Use `memory_search` before starting work, and record durable facts with `memory_write`.
- **Register at the start of every session**: `context_open`, then `root_register` with your session id as `session_label`, and your own model id as `model` (the harness is `agent_kind`; `model` is you).
- **Claim before you edit** anything another agent might touch: `claim_acquire`, and check with `claim_check` if you are unsure.
- **Claims cannot be enforced before an edit here.** Codex hooks may warn, but they cannot block a write in advance. If you edit a file another agent has claimed, the turn is halted *after* the edit lands and the work may have to be undone. Check claims yourself; nothing else will stop you.
- **When a claim blocks you**, negotiate with `mailbox_send`, or work elsewhere.
- **Write to the agent that is actually there.** The warning names the root holding the path and says whether it is still live; `root_list_active` shows who else is working here. Do not address a root id you remember from earlier — it may belong to an agent that has since died, while the one blocking you goes unasked.
- **Your mail is put in front of you** as your turn begins and after each edit. Answer it: someone is usually blocked on you. Agreeing to hand over a file is not enough — `claim_release` is what frees it.
- **Only the root session** may claim, write memory, or send mail. For read-only exploration use `stigmergy explore`, not a native subagent: native subagents inherit your write access and are not a boundary.
<!-- stigmergy:end -->
