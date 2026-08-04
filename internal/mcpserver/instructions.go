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
- memory_evidence_set(key, expected_memory_version, repos) records which repositories and paths to watch for a memory, and the commits to compare from. memory_list(include_drift) then reports what has CHANGED in that scope since. Declaring no paths watches the whole repository, which is the safe default; a too-narrow scope reports a confident zero while observing nothing. This measures change, never truth: it cannot tell you a memory is still correct, and calling it verifies nothing. Editing a memory deliberately does NOT move its baseline — recapture explicitly if an edit reasserts the claim.
- memory_verify(key, outcome, expected_memory_version, reason) records that you actually CHECKED a memory: "reaffirmed", "revised" or "refuted". Nothing infers this — not an edit, not a read — so if you verify something and say nothing, the next agent has no way to know it was ever checked. It is the only timestamp here that means "someone looked" rather than "bytes changed". Refuting deletes nothing; it records what you found. memory_list(include_verification) and memory_history(key) read it back.
- memory_link(scope, key, other_key, reason) / memory_unlink record a connection between two memories in the same scope — your general knowledge already covers the concepts, so only record what your weights can't know: project-specific bindings. Every link needs its why; that reason is what a future reader acts on. Links surface unconditionally: memory_read always shows a memory's links, memory_search always shows a hit's "linked" neighbors. A memory linked to everything primes nothing — prefer a new intermediate memory over turning one into a hub. The "similar" list memory_write returns on create is your link-candidate list.
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

Episodes
- An episode is what HAPPENED — a session, an investigation, a failure and why — not what is true. episode_record(title, body, memory_keys?, note?, corrects_episode_id?, continues_episode_id?) writes one. Record failures and dead ends: they are the episodes future agents need most, not just what worked.
- IMMUTABLE. There is no update tool, ever. Wrong reasoning is corrected by a NEW episode with corrects_episode_id, or resumed with continues_episode_id — never by rewriting what you wrote. episode_read always returns the chain of later episodes that correct or continue the one you asked for, so superseded reasoning is never read alone.
- Semanticize what generalizes: when an episode teaches something durable, write the lesson as a memory and ground it (memory_keys on episode_record), so the memory carries a citation for where it came from. Episodes are history and may eventually be pruned if nothing cites or chains them; memories they ground are not.
- episode_list(since?, before?, query?, limit?) is the episodic search surface, recent-first. memory_search stays memories-only.
- Session roots record episodes for work their peers report to them — like memory and mail, this is session-root-only.

Roots, and the agents inside a session
- A "root" is one agent, addressable and answerable: usually a top-level session, and on hosts that identify them, each agent working inside one. Where they are identified, a peer gets a root of its own and holds its own claims — its neighbours in the same session are blocked by them exactly as strangers would be, which is the point.
- Memory and mail stay with the session root everywhere. A peer finishes in minutes: it cannot read an answer that arrives afterwards, and what it learned should reach the rest of us through the root that is still here. Report to your root and let it write.
- Where an agent cannot be identified — every host but Claude Code today — none of that is enforceable, and the rule is the old one: only the top-level session registers, claims and writes. Use "stigmergy explore" for read-only work rather than a native subagent.
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
