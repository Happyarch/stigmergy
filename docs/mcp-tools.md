# MCP tool reference

The `stigmergy` MCP server exposes 21 tools over stdio. Hosts namespace them, so
an agent sees them as `mcp__stigmergy__context_open` and so on.

Source: `internal/mcpserver/`. If this document and the code disagree, the code is
right and this document is a bug.

- [Session state machine](#session-state-machine)
- [Errors](#errors)
- [Context and roots](#context-and-roots)
- [Memory](#memory)
- [Claims](#claims)
- [Mailbox](#mailbox)
- [Server instructions](#server-instructions)

---

## Session state machine

Each connection moves through three states:

```
Unopened  --context_open-->  Opened  --root_register-->  Registered
```

- **Unopened** — nothing is available except `context_open`.
- **Opened** — the databases are open. Everything that only *reads* works:
  `memory_search`, `memory_read`, `memory_list`, `claim_check`,
  `claim_list_active`, plus `root_register` itself.
- **Registered** — the session has a root. Everything that *mutates* works, plus
  the whole mailbox.

Gating is enforced in one place per handler (`requireOpened` / `requireRegistered`
in `session.go`), and the error tells the agent exactly which call it skipped:

> `no project is open — call context_open with the absolute path of your working directory first`

> `this session has no root — call root_register (with your host session id as session_label) before changing anything`

Every successful call in the **Registered** state also refreshes the root's
liveness (`touch()`). An agent that is doing anything at all is, by definition,
alive — so an explicit `root_heartbeat` is only needed during long silences.

`context_open` is callable in any state and is idempotent for the same repository.
Opening a *different* repository resets the session to **Opened** and drops the
root, because a root belongs to a repository.

### Which state each tool needs

| State | Tools |
|---|---|
| Unopened (ungated) | `context_open` |
| Opened | `root_register`, `root_list_active`, `memory_search`, `memory_read`, `memory_list`, `claim_check`, `claim_list_active` |
| Registered | `root_heartbeat`, `root_deregister`, `memory_write`, `memory_promote`, `memory_delete`, `claim_acquire`, `claim_renew`, `claim_release`, and all six `mailbox_*` |

---

## Errors

Errors come back as the tool result's error text, and that text **is a JSON
document**, so an agent can parse and branch on it:

```json
{"error":{"code":"cas_conflict","message":"…","current":{…}}}
```

Structured context is merged into the `error` object at the top level — not
nested — so a conflict's payload sits right next to its code.

| Context key | Appears on | Carries |
|---|---|---|
| `current` | `cas_conflict` | the current memory (or `null` when the key doesn't exist) |
| `conflict` | `claim_conflict` | the blocking claim |
| `owner` | `not_owner` | the owning `root_id` |
| `root_id` | `recipient_inactive` | the dead root, when it is known |

### Codes

The set is **closed and stable**. Agents branch on these, so they are part of the
protocol: never rename one, never repurpose one. An unrecognized internal error is
coerced to `internal` rather than leaking a raw driver message under an unstable
code.

| Code | Meaning |
|---|---|
| `wrong_state` | you skipped `context_open` or `root_register` |
| `invalid_input` | malformed argument — the message names the field and what is allowed |
| `not_a_repo` | the path given to `context_open` is not inside a git repository |
| `cas_conflict` | someone changed the memory under you; `current` holds their version |
| `claim_conflict` | another root holds a claim covering your scope; `conflict` names it |
| `not_owner` | that claim or thread is not yours to renew, release, or resolve |
| `recipient_inactive` | the root you are writing to is gone, so it cannot answer |
| `unsupported_search` | the search query could not be evaluated by FTS5 |
| `internal` | a bug or an I/O failure on our side |

Every tool can return `internal`; it is not repeated in the per-tool lists below.

---

## Context and roots

### `context_open`

> Open the stigmergy databases for a git repository. Call this first, with the absolute path of your working directory.

| Parameter | Type | Required | Notes |
|---|---|---|---|
| `project_root` | string | yes | absolute path inside the git repository |

**Returns** `worktree_root`, `project_common_dir`, `schema_version`, `reopened`.

**Errors** — `invalid_input` (path is not absolute), `not_a_repo`.

Idempotent for the same repository. Creates and migrates the databases if they do
not exist.

### `root_register`

> Register (or resume) this session as a root: the agent that may claim files and write memory. Pass your host session id as session_label so your own claims never block you.

| Parameter | Type | Required | Notes |
|---|---|---|---|
| `agent_kind` | string | yes | which host you are: claude-code, codex, antigravity, or opencode |
| `worktree` | string | yes | absolute path of the worktree you are in |
| `branch` | string | no | if known |
| `session_label` | string | no | **supply it** — see below |
| `model` | string | no | which model you are — see below |
| | | | State: **Opened** |

**Returns** `root` (`root_id`, `agent_kind`, `session_label`, `worktree`, `branch`,
`model`, `registered_at`, `last_seen_at`) and `resumed`.

`model` is asked, not detected, because there is nothing to detect: no hook payload and
no MCP handshake carries it. `agent_kind` is the harness — two roots reading `claude-code`
may be an Opus and a Haiku, and which one you are about to argue with over a file is
usually the more useful fact. So the agent is asked to say, and the answer is taken at
face value.

Nothing validates it and nothing keys off it. Roots are identified by `root_id`; `model`
is there so a person reading `stigmergy watch`, or an agent reading a claim conflict, can
tell two agents in the same host apart. That is exactly why asking is acceptable: the cost
of a wrong answer is one wrong line of output, and an agent has no reason to lie about
what it is. It is optional, and a root that never answers registers as it always did.

A resume may supply a `model` the original registration omitted — hosts restart the MCP
server mid-session. A resume that stays silent keeps whatever it was last told rather than
erasing it.

**Errors** — `wrong_state`, `invalid_input` (bad `agent_kind`; non-absolute
`worktree`).

`session_label` is nominally optional and practically mandatory. It is how the
claim guard recognizes *your* edits as yours. Register without it and the guard
cannot match your session to your root, so **your own claims will block your own
edits**. The session-start hook tells the agent exactly what value to use.

`resumed: true` means an existing live root matching
`(agent_kind, worktree, session_label)` was reconnected rather than a new one
created — same `root_id`, claims intact. This is what keeps a host restart
(Claude `/clear`, Codex compact) from stranding live claims.

`worktree` is genuinely required: an empty one is rejected with `invalid_input`,
never defaulted. Because a root resumes on `(agent_kind, worktree, session_label)`,
registering under a worktree the caller never named would make the *next* session's
resume miss — and strand this root's claims until they timed out. `context_open`
returns `worktree_root`; pass that.

### `root_list_active`

> List the agents actually working in this repository right now, what each holds, and how recently each was heard from.

No parameters. State: **Opened**.

**Returns** `roots[]`: `root_id`, `agent_kind`, `worktree`, `branch`, `liveness`,
`holds[]`, `is_you`.

This answers the question an agent has to get right before it can negotiate at all,
and which nothing used to answer: *who is actually here?* Without it, an agent that
wanted to write to the owner of a claim had to have kept the root id in its head —
across compaction, across its own summarizing, across everything else it had been
doing since. A root id remembered wrongly is not an error, it is an address: the mail
is delivered, to nobody, while the agent that actually holds the file is never asked.

`liveness` is prose rather than a timestamp, because the decision it feeds is not a
calculation:

| | meaning |
|---|---|
| `live (last seen 20s ago)` | it will read your mail |
| `quiet (last seen 9m ago; lapses in 6m if it stays silent)` | still holds its claims, still reachable — but may be about to lapse |

A root past the TTL appears here not at all. It is gone, its claims are free, and it
cannot be written to.

### `root_heartbeat`

> Refresh this root's liveness so its claims keep holding. Any tool call also does this.

No parameters. State: **Registered**. Returns `root_id`, `last_seen_at`.

Rarely needed by hand. A root unheard-from for fifteen minutes (`RootTTL`) stops
holding claims — but every tool call refreshes liveness, and so does every *edit*, via
the host hooks. An agent that is doing anything at all is proving it is alive as a side
effect of doing it.

The short TTL is what makes a dead agent stop blocking the living quickly. Its price:
a root that has lapsed and then comes back **loses its claims** rather than
resurrecting them — the paths were declared free, someone may already have taken one,
and two agents each believing they hold the same file is the one outcome stigmergy
exists to prevent. Re-acquire, and find out.

**Errors** — `wrong_state`, including when the root has vanished:
> `your root is no longer active — it expired or was ended; call root_register again (your previous claims have been released)`

### `root_deregister`

> End this root and release its claims. Call it when your work is done.

No parameters. State: **Registered** → drops back to **Opened**. Returns `root_id`,
`ended`.

---

## Memory

### `memory_search`

> Search shared memories. Do this before starting work and before writing a new memory. Project hits are listed before global ones.

| Parameter | Type | Required | Notes |
|---|---|---|---|
| `query` | string | yes | free text; all words must appear |
| `scopes` | string[] | no | `project`, `global`, or both (the default) |
| | | | State: **Opened** |

**Returns** `hits[]`: `scope`, `key`, `type`, `description`, `version`, `snippet`.

Project hits always precede global ones regardless of the order you asked for
them, because a fact recorded about *this* repository beats a general one.
Ranking is bm25; at most 20 hits per scope.

**Errors** — `wrong_state`, `invalid_input` (bad scope), `unsupported_search`.

### `memory_read`

> Read one memory's full body.

| Parameter | Type | Required |
|---|---|---|
| `scope` | string | yes (`project` or `global`) |
| `key` | string | yes |
| | | State: **Opened** |

**Returns** `found` (bool) and, when found, `memory`: `key`, `type`, `description`,
`body`, `version`, `updated_by`, `created_at`, `updated_at`.

A missing key is **not an error** — it is a normal answer, `{"found": false}`.

### `memory_list`

> List the keys and descriptions in a scope, without bodies.

| Parameter | Type | Required |
|---|---|---|
| `scope` | string | yes |
| | | State: **Opened** |

**Returns** `entries[]`: `key`, `type`, `description`, `version`, `updated_at`.

### `memory_write`

> Create or update a memory under compare-and-swap. Omit expected_version to create; pass the current version to update. On a conflict the current entry is returned: merge and retry.

| Parameter | Type | Required | Notes |
|---|---|---|---|
| `scope` | string | yes | `project` or `global` |
| `key` | string | yes | `^[a-z0-9][a-z0-9-]{0,127}$` |
| `type` | string | yes | `user`, `feedback`, `project`, or `reference` |
| `description` | string | yes | one line; what other agents see when listing |
| `body` | string | yes | |
| `expected_version` | int | no | omit to create; pass the current version to update |
| | | | State: **Registered** |

**Returns** `created` (bool), `memory`, and — **only on a create** — `similar[]`:
up to 3 existing entries whose key and description look like this one.

Those suggestions are computed *before* the write, and they are the anti-duplicate
nudge: the most likely reason an agent is creating `auth-notes` is that it forgot
about `authentication-conventions`.

**Errors** — `wrong_state`; `invalid_input` (bad scope, key, type, empty
description or body, `expected_version < 1`); `cas_conflict` (carrying `current`).

The CAS matrix:

| `expected_version` | key exists | outcome |
|---|---|---|
| omitted | no | created at version 1 |
| omitted | yes | `cas_conflict` — `current` is theirs |
| *n* | yes, at *n* | updated to *n+1* |
| *n* | yes, at *m ≠ n* | `cas_conflict` |
| *n* | no | `cas_conflict`, `current: null` |

On a conflict: re-read, merge, retry. **Never** work around it by inventing a new
key — that is how you end up with two half-true memories instead of one true one.

### `memory_promote`

> Copy a project memory into the global scope, for facts that hold in every repository.

| Parameter | Type | Required | Notes |
|---|---|---|---|
| `key` | string | yes | the project memory to promote |
| `expected_version` | int | yes | its current version |
| `global_key` | string | no | defaults to the same key |
| `expected_global_version` | int | no | omit if the global key is new |
| | | | State: **Registered** |

**Returns** `created` (bool), `global` (the resulting global memory).

A **copy, not a move** — the project memory stays. Both sides are CAS-checked, and
the global write happens first: if it fails, nothing has changed anywhere.

Promote what is true of you or your machine *everywhere*. Leave repo-specific facts
in the project scope.

**Errors** — `wrong_state`, `cas_conflict` (source missing, source version
mismatch, or a conflict on the global side), `invalid_input`.

### `memory_delete`

> Permanently delete a memory. Requires its current version, and is audited.

| Parameter | Type | Required | Notes |
|---|---|---|---|
| `scope` | string | yes | |
| `key` | string | yes | |
| `expected_version` | int | yes | a delete must never race an update you have not seen |
| | | | State: **Registered** |

**Returns** `deleted`, `key`, `version`. Hard delete. The audit record includes a
hash of the body, so the log records *what* was destroyed, not merely that
something was.

**Errors** — `wrong_state`, `invalid_input` (no such key), `cas_conflict`.

---

## Claims

TTLs: default **1800s** (30 min), minimum **60s**, maximum **86400s** (24h). Out of
range is *rejected*, not clamped — silently giving an agent a different TTL than it
asked for is how you get an agent that thinks it still holds a claim it doesn't.

### Scopes in a multi-repository project

A project may span several git repositories ([architecture.md §3](architecture.md#3-storage)).
Where it does, a claim scope is spelled `repo:path` — `naviamp-sidecar:internal/api.go` —
and that is the spelling you are shown back in conflicts, in `claim_list_active`,
in the roster and in the hook's denial text. One notation, written and read the
same way.

`context_open` returns the roster, so the names come from a call you already
make. A **bare path always means the repository you opened**, so a project with
one repository never needs a prefix and nothing about it changed.

The `:` is decidable, not ambiguous: the split takes the *first* colon, and the
head counts as a repository only if it matches a member. `weird:name.go` is
therefore still a filename. A head that looks like a repository but matches none
is **refused** rather than silently read as a path — `sidecar:src/x.go` when the
member is `naviamp-sidecar` would otherwise claim a file that does not exist
while the real one stayed unguarded.

### `claim_acquire`

> Reserve a file or directory before editing it, so no other agent edits it at the same time. Take the narrowest scope that covers your work, and release it when you are done.

| Parameter | Type | Required | Notes |
|---|---|---|---|
| `scope_path` | string | yes | repo-relative; `"."` is the whole repo |
| `recursive` | bool | no | true to claim a directory and everything under it |
| `reason` | string | yes | other agents read this when your claim blocks them |
| `ttl_seconds` | int | no | 60–86400, default 1800 |
| | | | State: **Registered** |

**Returns** `claim`: `id`, `scope_path`, `recursive`, `root_id`, `agent_kind`,
`worktree`, `branch`, `reason`, `created_at`, `expires_at`, `own`.

`scope_path: "."` is forced recursive — a non-recursive claim on the repo root
would be a claim on a directory entry nobody edits.

Re-acquiring a claim you already hold is **not** an error: it returns the claim you
have.

**Errors** — `wrong_state`; `invalid_input` (bad scope path, empty reason, TTL out
of range); `claim_conflict`:

> `<path> is claimed by <root> — live (last seen 20s ago) (worktree <w>, reason: "<why>", expires <when>). Write to that root with mailbox_send(to_root="<root>"), or work elsewhere. Do not address any other root about this path: <root> is the one holding it.`

with the full blocking claim in `conflict`, including `owner_liveness`. The owner is
always named, because a conflict you cannot negotiate is just a wall — and its
liveness is named in the same breath, because "who holds this" and "who can answer me
about it" are different questions, and an agent that has to go and look the second one
up separately will not.

The id is put in front of the agent at the exact moment it needs it, so it never has
to reconstruct one later from a context that may have been compacted since. That is
not a nicety: a root id recalled wrongly is a working address for an agent that no
longer exists.

### `claim_check`

> Check whether a path is claimed, and by whom, before you edit it.

| Parameter | Type | Required | Notes |
|---|---|---|---|
| `path` | string | yes | absolute, or relative to the worktree root |
| | | | State: **Opened** |

**Returns** `path`, `claimed` (bool), `claims[]`.

A path outside the worktree is not an error — claims do not govern it, so the
answer is simply `claimed: false`.

### `claim_list_active`

> List every claim currently in force in this repository.

No parameters. State: **Opened**. Returns `claims[]`. Each carries `own`, so an
agent can tell its own claims from everyone else's.

### `claim_renew`

> Extend one of your claims because you are still working on it.

| Parameter | Type | Required | Notes |
|---|---|---|---|
| `claim_id` | int | yes | |
| `ttl_seconds` | int | no | new lifetime *from now*; 60–86400, default 1800 |
| | | | State: **Registered** |

**Errors** — `wrong_state`; `invalid_input` (TTL out of range; or the claim is no
longer active: *"it expired or was already released; acquire it again if you still
need it"*); `not_owner`, carrying `owner`:

> `claim <n> belongs to <root>, so you cannot renew it — if it is blocking you, negotiate with mailbox_send`

### `claim_release`

> Release one of your claims. Do this as soon as you are done: someone may be waiting.

| Parameter | Type | Required |
|---|---|---|
| `claim_id` | int | yes |
| | | State: **Registered** |

**Returns** `claim_id`, `released`. **Errors** — `wrong_state`, `invalid_input`,
`not_owner`.

---

## Mailbox

The mailbox exists so that "you are blocked" has an answer other than "wait" or
"barge through". Threads have a state: `open`, `resolved`, or `abandoned`.

**Mail is delivered, not left lying about.** This is not an MCP concern — no tool
pushes anything — but it governs how the tools below behave, so it belongs here. The
host hooks put unread mail in front of the agent: on Claude Code and Antigravity by
refusing to let a turn end while a message has never been shown, on Codex and opencode
at the start of a turn and after each edit — neither of those two has a hook that can
hold a turn open, so there the agent can still walk away, and it is told so. See
[hosts.md](hosts.md).

Two timestamps, deliberately distinct:

| | whose record | set by |
|---|---|---|
| `notified_at` | stigmergy's: we put this in front of the agent | the delivery hooks, once per message |
| `read_at` | the agent's: it looked | `mailbox_mark_read` |

Conflating them is what made the mailbox a pull channel with nothing pulling it: an
unread message was indistinguishable from an undelivered one, so nothing could tell
whether an agent had ignored its mail or had simply never been told it had any. There
is no tool to set `notified_at` — an agent that could suppress its own notifications
would eventually do so.

### `mailbox_send`

> Write to another root — use this when a claim blocks you, rather than waiting silently or editing around it. Pass a thread_id to continue an existing conversation.

| Parameter | Type | Required | Notes |
|---|---|---|---|
| `to_root` | string | yes | the `root_id`; claim conflicts name the owner, `root_list_active` lists them all |
| `subject` | string | yes | |
| `body` | string | yes | say what you need *and what you propose* |
| `claim_id` | int | no | the claim this is about |
| `thread_id` | int | no | omit to start a new thread |
| | | | State: **Registered** |

**Returns** `message`: `id`, `thread_id`, `from_root`, `to_root`, `subject`, `body`,
`sent_at`, `read_at`, `notified_at`.

**Errors** — `wrong_state`; `invalid_input` (missing fields, no such thread, or
sending to yourself); `recipient_inactive`.

That last one is a *loud* failure on purpose. Mail to a dead root would sit unread
forever while the sender waited for an answer that could never come. So instead the
sender is told the root is gone — and, crucially, what that implies:

> `root <r> is no longer active, so it cannot answer you — its claims have lapsed with it, and the paths it held are free. The agents actually active here are: r-a1b2 (claude-code, holds src/api/**); r-c3d4 (codex). Address the one that holds the path you want — root_list_active shows this too.`

The blocked agent's problem has, in fact, just solved itself. But refusing is not
enough on its own, which is why the roster is in the error and in `active_roots` on
the payload. An agent that addressed a dead root got the id from *somewhere* — an old
message, a memory, its own compacted context — and if all it is told is "wrong", it
will guess again, with no more reason to be right the second time. So it is handed the
answer instead: who is here, and what each of them holds.

If nobody else is active at all, the error says that too, and says what follows from
it: nothing is holding the path against you, so go ahead.

### `mailbox_inbox`

> Read mail addressed to you. Check it when you are blocked, and answer promptly when another agent is blocked on you.

| Parameter | Type | Required |
|---|---|---|
| `unread_only` | bool | no |
| | | State: **Registered** |

**Returns** `messages[]`, newest first.

The inbox is *mail addressed to you*. It does **not** contain messages you sent —
for those, see `mailbox_threads`.

### `mailbox_threads`

> List the conversations you are part of, including ones where you are waiting for an answer. Your inbox does not show messages you sent, so check here before assuming silence means consent.

| Parameter | Type | Required | Notes |
|---|---|---|---|
| `state` | string | no | `open`, `resolved`, or `abandoned`; omit for all |
| | | | State: **Registered** |

**Returns** `threads[]`, most recently active first: `id`, `claim_id`, `state`,
`resolution`, `with` (the other root), `with_liveness`, `subject` (of the latest
message), `last_message_at`, `last_from_self`, `unread`, `messages` (count).

This exists because an inbox is not a conversation list. A message you sent that
nobody has answered yet appears in *no* inbox — least of all your own. Without this,
an agent compacted mid-negotiation loses the thread id and has no route back to the
conversation it opened: it would re-send the same message, or read the silence as
consent and edit anyway.

`last_from_self: true` means the ball is in their court. `with_liveness` says whether
anyone is still in that court to play it — and the combination is the one thing an
agent waiting on a reply cannot otherwise work out. Silence from a live agent means
"they are thinking about it". Silence from a dead one means nobody will ever answer,
and the claim you were waiting on lapsed with them. Those call for opposite actions,
and they look identical from the outside.

So: `last_from_self: true` plus a `with_liveness` of `gone …` means **stop waiting**.
The path is free. Take the claim, and close the thread with
`mailbox_resolve(abandoned=true)`.

### `mailbox_thread`

> Read a whole conversation, including every message in it.

| Parameter | Type | Required |
|---|---|---|
| `thread_id` | int | yes |
| | | State: **Registered** |

**Returns** `thread`: `id`, `claim_id`, `created_by`, `state`, `resolution`,
`created_at`, `updated_at`, `messages[]`.

### `mailbox_mark_read`

> Acknowledge messages you have read, so the sender knows they landed.

| Parameter | Type | Required |
|---|---|---|
| `message_ids` | int[] | yes |
| | | State: **Registered** |

**Returns** `marked_read` (count). Scoped to your own mail by query — you cannot
mark someone else's messages read.

### `mailbox_resolve`

> Close a negotiation and record what was agreed.

| Parameter | Type | Required | Notes |
|---|---|---|---|
| `thread_id` | int | yes | |
| `resolution` | string | yes | what was agreed; other agents will read this |
| `abandoned` | bool | no | true if the matter was dropped rather than settled |
| | | | State: **Registered** |

**Returns** the `thread`. Any *participant* may resolve — a negotiation is not
owned by whoever opened it. A non-participant gets `not_owner`.

---

## Server instructions

The server ships an `instructions` string that hosts inject as system guidance.
Codex prioritizes the **first 512 characters** of it, so the mandatory workflow is
packed into that window and a unit test enforces the budget (`PriorityBudget = 512`
in `instructions.go`). If you add to the priority block, something else has to come
out.

The priority block, verbatim:

```
stigmergy is the shared memory and coordination layer for every agent in this repo. Use it instead of your own memory files.

1. context_open(project_root) — absolute path — then root_register(agent_kind, worktree, session_label=<your host session id>).
2. memory_search before starting work, and again before writing.
3. claim_acquire before editing shared files; claim_check first if unsure.
4. Writes are compare-and-swap: pass expected_version.
```

The extended block that follows covers memories (CAS, scopes, what makes a memory
worth keeping), claims (narrowest scope, renew, release, negotiate on conflict, and
what each host can and cannot enforce), the mailbox, and the root/subagent rule.
It is in `internal/mcpserver/instructions.go` and is the single place to change what
every agent is told. The sentences naming hosts are rendered from
`internal/hosts`, so a new host cannot leave a stale one behind.
