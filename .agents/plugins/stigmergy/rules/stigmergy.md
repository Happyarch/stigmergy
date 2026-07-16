<!-- stigmergy:begin — managed block, do not edit; `stigmergy init` regenerates it -->
## stigmergy

This project uses **stigmergy** for memory and coordination shared across every agent working here (Antigravity, Claude Code and Codex alike).

- **Memory lives in stigmergy, not in your own memory files.** Use `memory_search` before starting work, and record durable facts with `memory_write`. Do not keep a private memory directory for this project.
- **Register at the start of every session**: `context_open`, then `root_register` with `agent_kind="antigravity"` and your `conversationId` as `session_label`, and your own model id as `model`. The pre-invocation hook tells you the exact session_label to use.
- **Claim before you edit** anything another agent might touch: `claim_acquire`. Edits to a file claimed by another agent are blocked outright.
- **When a claim blocks you**, the owner is named. Negotiate with `mailbox_send`, or work elsewhere. Never edit around a claim.
- **Write to the agent that is actually there.** The conflict names the root holding the path and says whether it is still live; `root_list_active` shows who else is working here. Do not address a root id you remember from earlier — it may belong to an agent that has since died, while the one blocking you goes unasked.
- **Your mail is handed to you at the end of your turn**, and you cannot finish while a message is undelivered. Answer it: someone is usually blocked on you. Agreeing to hand over a file is not enough — `claim_release` is what frees it.
- **Only the root session** may claim, write memory, or send mail. For read-only exploration use `stigmergy explore`, not `invoke_subagent`: an Antigravity subagent is a separate conversation with its own id, so stigmergy cannot tell it from a root, and it will be told to register as one. Nothing enforces this here — it holds only because you keep it.
- **If another agent invoked you** rather than a person, you are not the root: do not register, do not claim, do not write memory. Report back to the agent that invoked you and let it record what lasts.
<!-- stigmergy:end -->
