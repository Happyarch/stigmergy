package mcpserver

import "github.com/happyarch/stigmergy/internal/hosts"

// PriorityBudget is how much of the instructions Codex prioritizes (first 512
// chars, per the Codex manual). Anything the agent MUST do to avoid corrupting
// shared state has to fit inside it; everything else is elaboration.
const PriorityBudget = 512

// priority is the mandatory workflow. It is kept under PriorityBudget by a
// test, so edits here cannot silently push a rule out of the prioritized
// window. Order matters: it is the order an agent must act in.
const priority = `stigmergy is the shared memory and coordination layer for every agent in this repo. Use it instead of your own memory files.

1. context_open(project_root) — absolute path — then root_register(agent_kind, worktree, session_label=<your host session id>).
2. memory_search before starting work, and again before writing.
3. claim_acquire before editing shared files; claim_check first if unsure.
4. Writes are compare-and-swap: pass expected_version.
`

// extended elaborates on priority. The two sentences that name hosts are
// rendered from the hosts registry rather than written out: both of them said
// "Claude Code" and "Codex" for as long as those were the only hosts, and went
// on saying it after Antigravity arrived. This text reaches every agent on every
// host, so a stale sentence here is not a documentation problem — it is an agent
// being told the wrong thing about whether its edits are guarded.
func extended() string {
	claims := hosts.List(hosts.Names(func(h hosts.Host) bool { return h.Claims == hosts.ClaimsBlocked }))
	warned := hosts.List(hosts.Names(func(h hosts.Host) bool { return h.Claims == hosts.ClaimsWarned }))
	enforced := hosts.List(hosts.Names(func(h hosts.Host) bool { return h.Mail == hosts.MailEnforced }))
	advisory := hosts.List(hosts.Names(func(h hosts.Host) bool { return h.Mail == hosts.MailAdvisory }))

	return `
Memories
- Scopes: "project" (this repo) and "global" (this machine, all repos). Search returns project hits first.
- memory_write(scope, key, type, description, body, expected_version): expected_version=null creates and fails if the key exists; an integer updates and fails unless it is the current version. On a cas_conflict the error carries the current entry — re-read it, merge, retry. Never work around a conflict by inventing a new key: that is how two agents end up with two half-true memories.
- memory_list gives keys and descriptions only. memory_read(scope, key) gives the body.
- memory_list also takes updated_since / updated_before (RFC3339, both inclusive) and order_by "recent". These are last-CHANGE times, not last-checked times: nothing here records when anyone verified a memory, so a long-untouched entry is a candidate for a look, never a verdict that it has gone stale.
- memory_promote copies a project memory to global. Promote what is true of you or your machine everywhere; leave repo-specific facts in the project scope.
- memory_delete(scope, key, expected_version) hard-deletes. It is audited.
- Good memories are durable and non-obvious: conventions, decisions, constraints, preferences. Not things the code or git history already says.

Claims
- A claim reserves a file or a directory subtree while you work on it, so two agents do not edit the same thing at once.
- claim_acquire(scope_path, recursive, reason, ttl_seconds) takes the narrowest scope that covers your edit. Claims expire; renew with claim_renew if you are still working, release with claim_release as soon as you are done.
- A project may span several git repositories. Where it does, context_open lists them and a scope is spelled "repo:path" — the same spelling you are shown in conflicts, in claim_list_active and in the roster. A bare path always means the repository you opened, so single-repository projects never need the prefix.
- If claim_acquire returns a claim_conflict, the owner is named. Do not wait silently and do not edit anyway: use mailbox_send to negotiate, or work elsewhere.
- Hosts differ in what they can enforce, and stigmergy never pretends otherwise. An edit to a claimed path is blocked outright in ` + claims + `. In ` + warned + ` it cannot be blocked before it lands, so the turn is halted after the fact — respect claims there or you will lose work.

Mailbox
- mailbox_send(to_root, subject, body) reaches another root; mailbox_inbox reads yours.
- Mail is delivered to you, not left for you to find. In ` + enforced + ` it is put in front of you at the end of your turn and you cannot finish while a message is undelivered; in ` + advisory + ` it arrives as your turn begins and after each edit, and nothing makes you read it. Deal with it there and then — the agent that wrote to you is usually blocked on your answer.
- Address the root that actually holds the path. Claim conflicts name the owner and say whether it is still live; root_list_active shows who is here and what each of them holds. Never write to a root id you are recalling from earlier in the session without checking it: that agent may be gone, and the one blocking you never asked.
- A root that has gone silent cannot be written to at all, and the send is refused. That is not a dead end: its claims lapsed with it, so the path you wanted is free. Take it.
- Agreeing to release a claim is not releasing it. Call claim_release, then mailbox_resolve to close the thread.

Roots and subagents
- A "root" is a top-level agent session. Only roots may register, claim, write memory or send mail. Subagents and explorers read; they must report findings to their root and let the root write.
- Heartbeat: your tool calls and your edits refresh your liveness. Go quiet for fifteen minutes and you are treated as gone — your claims stop blocking others, and if you come back you must re-acquire them.
`
}

// Instructions is the server-wide guidance sent in initialize.instructions.
//
// Every host surfaces this to the model — Claude Code and Codex prepend it,
// opencode wraps it in <mcp_instructions> — which makes it the one channel that
// reaches an agent without a hook, a config file, or the agent choosing to look.
// That is why the shared rules live here and not in a per-host file.
func Instructions() string { return priority + extended() }
