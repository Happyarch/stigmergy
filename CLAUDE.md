<!-- stigmergy:begin — managed block, do not edit; `stigmergy init` regenerates it -->
## stigmergy

This project uses **stigmergy** for memory and coordination shared across every agent working here (Claude Code and Codex alike).

- **Memory lives in stigmergy, not in your own memory files.** Use `memory_search` before starting work, and record durable facts with `memory_write`. Do not keep a private memory directory for this project.
- **Register at the start of every session**: `context_open`, then `root_register` with your host session id as `session_label`.
- **Claim before you edit** anything another agent might touch: `claim_acquire`. Edits to a file claimed by another agent are blocked outright.
- **When a claim blocks you**, the owner is named. Negotiate with `mailbox_send`, or work elsewhere. Never edit around a claim.
- **Only the root session** may claim, write memory, or send mail. Subagents read and report back.
<!-- stigmergy:end -->
