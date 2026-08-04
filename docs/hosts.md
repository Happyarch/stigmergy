# Host integration

What `stigmergy init` writes into a project, what the hooks send and receive, and
where the hosts differ. Source: `internal/hosts/`, `internal/hostcfg/` and
`internal/hooks/`.

Read [§7 of architecture.md](architecture.md#7-the-asymmetry-is-inherent) first if
you only read one thing: some hosts can prevent a bad edit and some cannot, and
stigmergy never pretends otherwise. Everything below follows from that.

What a host can enforce is declared once, in `internal/hosts`, along the axes that
actually vary — whether a claimed edit can be refused, whether mail can be made
unignorable, whether a subagent can be told from a root, and where the session id
comes from. The text every agent reads is rendered from those axes rather than
written per host. That is not tidiness: the per-host copies used to contradict each
other, and an agent told the wrong one stops checking claims.

| | Claims | Mail | Agents inside a session |
|---|---|---|---|
| Claude Code | blocked before the edit | enforced at end of turn | identified: each holds its own claims |
| Antigravity | blocked before the edit | enforced at end of turn | not visible — advisory |
| opencode | blocked before the edit | advisory | gated, via the session's parent |
| Codex | warned, halted after the edit | advisory | not visible — advisory |

No two rows are the same, and no column implies another: opencode blocks edits as
firmly as Claude Code and still cannot be made to read its mail.

---

## Shared principles

**Host configs invoke the bare command name `stigmergy`**, never an absolute path.
A binary that moves should not break every project on the machine. The cost is that
`stigmergy` must be on `PATH` for the hosts that will run it — and if it isn't, the
failure is *invisible*: the MCP server simply never starts and the hooks silently
never run. `stigmergy doctor` checks this first, for exactly that reason.

**Every entry stigmergy owns is identified by the command prefix `stigmergy hook`.**
Install strips every matching entry and re-adds the current set, so installing twice
does not duplicate anything and an upgrade replaces rather than accumulates.
`init --remove` deletes exactly those entries and nothing else.

**Nothing is ever clobbered.** JSON config is merged, and malformed JSON is a hard
error rather than an excuse to overwrite:

> `<path> is not valid JSON (…) — fix or move it, and re-run; stigmergy will not overwrite it`

Writes are atomic (temp file in the same directory, then rename).

**Markdown blocks are delimited**, so they can be replaced in place and removed
cleanly:

```
<!-- stigmergy:begin — managed block, do not edit; `stigmergy init` regenerates it -->
…
<!-- stigmergy:end -->
```

An *unterminated* begin marker counts as "no block found" — a half-deleted block is
left alone rather than silently swallowed.

---

## Claude Code

`init --host claude` touches three files.

### `.mcp.json`

```json
{
  "mcpServers": {
    "stigmergy": {
      "args": ["mcp"],
      "command": "stigmergy"
    }
  }
}
```

### `.claude/settings.json`

Seven hook entries:

| Event | Matcher | Command |
|---|---|---|
| `SessionStart` | — | `stigmergy hook session-start` |
| `UserPromptSubmit` | — | `stigmergy hook mail-notify` |
| `PreToolUse` | `Edit\|Write\|NotebookEdit` | `stigmergy hook claim-guard` |
| `PreToolUse` | `mcp__stigmergy__(root_.*\|memory_write\|memory_promote\|memory_delete\|memory_evidence_set\|memory_evidence_clear\|memory_verify\|memory_link\|memory_unlink\|claim_.*\|mailbox_.*)` | `stigmergy hook root-gate` |
| `Stop` | — | `stigmergy hook mail-gate` |
| `SubagentStop` | — | `stigmergy hook subagent-stop` |
| `SessionEnd` | — | `stigmergy hook session-end` |

It also sets:

```json
{ "autoMemoryEnabled": false }
```

Claude Code's **auto memory** is on by default and writes markdown to
`~/.claude/projects/<slug>/memory/`. Left running in a project that has adopted
stigmergy, it is a second, divergent memory store — exactly the problem this tool
exists to end, and a silent one, because both stores look healthy while drifting
apart. It is not a permission-deniable tool, so `permissions.deny` cannot touch it
and this setting is the only mechanism. Asking the agent nicely in `CLAUDE.md` is
advice, and advice is not a mechanism.

**Import before you disable.** Turning auto memory off does not delete what is
already there; it just stops Claude reading or writing it, stranding anything you
had. Run `stigmergy import claude-memory` first.

`init --remove` deletes the key only if its value is still `false` — the value we
wrote. If you have since set it to `true` yourself, that is your decision and
removal leaves it alone.

`doctor` FAILs when an adopted project still has auto memory enabled.

### `CLAUDE.md` — no longer written

`init` used to put a marker block here. It does not any more, and it will not remove
one you already have; `stigmergy doctor` reports a leftover, and `init --remove` takes
it out.

The block said what the MCP server's instructions now say to every host at once, and
what the session-start hook says with the one thing a file cannot know — the agent's
session id. Three copies of the same advice drifted apart, and the Claude and Codex
copies ended up contradicting each other about whether a claimed file is protected.
That is not hypothetical harm: `AGENTS.md` is commonly symlinked to `CLAUDE.md`, so
`init` wrote one and then overwrote it with the other, and which host got the truth
depended on the order of two `if` statements.

`autoMemoryEnabled: false` still carries the other half of the old block's job: the
setting stops the mechanism, the instructions explain why.

### The hooks

**`session-start`** reads `session_id` and `cwd`, and injects
`hookSpecificOutput.additionalContext` telling the agent to register:

```
  1. context_open(project_root="<worktree>")
  2. root_register(agent_kind="claude-code", worktree="<worktree>", session_label="<session_id>")
```

with the warning that matters:

> Use session_label exactly as given: it is how stigmergy knows which claims are
> yours. Until you register, every claim in the repository — including any you made
> earlier — will block your edits.

It also lists any claims other agents currently hold, so the agent starts the
session knowing where the walls are. In a project that has not adopted stigmergy it
**emits nothing at all** and stays out of the way.

**`claim-guard`** is the only place claims are truly enforced. It reads `session_id`
and `agent_id` (so a peer is judged as itself, not as the session it lives in),
`cwd`, and the path from `tool_input` (`file_path`, `notebook_path`, or `path`). On
a conflict it emits:

```json
{"hookSpecificOutput":{
  "hookEventName":"PreToolUse",
  "permissionDecision":"deny",
  "permissionDecisionReason":"stigmergy: this edit is blocked by an active claim.\n…"}}
```

The reason names the scope, the owning root and its agent kind, the reason, the
worktree, the branch, and when the claim expires (as a wall-clock time *and* a
human "in 12m30s"), followed by what to do about it: negotiate with `mailbox_send`,
wait for expiry, or work elsewhere — and do not edit around the claim.

An allowed edit prints **nothing**. See
[§5.8 of architecture.md](architecture.md#58-failure-directions-are-chosen-per-failure-not-globally)
for why an unparseable payload allows and a broken database denies.

**`root-gate`** is what makes a session's agents visible to stigmergy as individuals.

Claude Code can run several agents at once inside one session — a main thread and the
peers it dispatches. They share a session id, and they share the one stigmergy server
that session started, so without this hook every one of them looks like the same
agent. What you get with it:

- **Each agent holds its own claims.** A file claimed by one peer blocks its
  neighbours exactly as it would block a stranger, and `stigmergy watch` lists them
  separately — `<session id>#general-purpose:a4d39a33…` beside the session itself.
- **Their claims are released when they finish**, not fifteen minutes later (see
  `subagent-stop` below).
- **Memory and mail stay with the session.** An agent that tries anyway is told so,
  and told what it *can* do:

  > `stigmergy: mcp__stigmergy__memory_write belongs to the session root, not to you — memories outlive you. You do have an identity of your own and you may claim files with it: claim_acquire, claim_renew and claim_release all work, and the claims you take are yours and block everyone else. Report anything worth remembering to your root and let it record what lasts.`

That division is about how long an agent lives, not about rank. A peer exists for a
minute or two: it can take a file and give it back, which is all a claim is, but it
cannot read a reply that arrives after it has finished, and a memory it writes will
be read by agents who have no way to ask it what it meant.

**If this hook is not installed**, nothing breaks — every call is attributed to the
session, which is how stigmergy behaved before any of this existed. What you lose is
the separation: two peers editing the same file stop blocking each other.

How it works, briefly, because it is not obvious that it *can*: Claude Code tells the
hook which agent is calling (`agent_id`, `agent_type` — present only for peers, and
alongside the same `session_id` the main thread reports), and tells the server the
id of the call being made (`_meta["claudecode/toolUseId"]`). The hook is handed that
same id, so it can leave a note under it that the server picks up. Neither value is
anything the model chooses, which is why this is identity rather than an assertion —
an agent claiming to be someone else changes nothing.

None of those fields appear in Claude Code's documentation; they were read off a live
2.1.220 session (see [Probing the hosts](#probing-the-hosts)). If a future version
stops sending them, calls fall back to the session root and the tests covering this
fail loudly rather than quietly re-merging everyone into one identity.

**`subagent-stop`** ends an agent and frees whatever it claimed, the moment it
finishes. Without it, a peer's claim would go on blocking the repository for the full
root TTL — held by an agent that is already gone and cannot be negotiated with, which
would make giving peers claims a worse deal than not having them. `session-end` does
the same for the session and every agent that ran inside it.

**`mail-gate`** (`Stop`) is how the mailbox is actually delivered. When an agent
tries to end its turn with mail it has never been shown, the hook returns
`{"decision":"block","reason":"…"}` — the messages, who sent them, whether those
senders are still alive, and what to do — and the agent goes on working instead of
walking away from someone who is blocked on it.

It has to be `Stop`, and this is worth being explicit about, because it is the only
hook that reaches an agent which is not asking for anything. A mailbox nobody is told
about is a pull channel with nothing pulling it: `mailbox_inbox` exists, the
instructions say to check it, and an agent deep in its own work does not — so mail
sits unread while its sender waits for an answer that is never coming. Telling the
agent harder does not fix that. The instruction is read once, at the start; the mail
arrives later.

The same hook also carries a **priming note**, when the agent's claims overlap what a
memory has declared as its evidence: which memories to re-check before finishing, and
their linked neighbors (see [association-model.md](association-model.md)). It rides
this exact path for the same reason mail does — a nudge nobody is looking for has to be
put in front of the agent, not left for it to go find — and composes into the same
block rather than costing a second interruption. `priming_delivered` gives it the same
once-only guarantee `notified_at` gives mail.

Two things keep the gate from becoming a nuisance:

- **`stop_hook_active`.** If the agent is only still running because this hook blocked
  it, the hook says nothing. Otherwise it would be a trap the agent could never leave.
- **`notified_at`** (mail) **and `priming_delivered`** (the note). Each message, and each
  primed memory, interrupts exactly once. Delivery is stigmergy's record that it put
  something in front of the agent — distinct from `read_at`, which is the agent's record
  that it looked. An agent that reads its mail and decides to press on is not nagged; a
  message that arrives mid-turn still gets its one interruption.

**`mail-notify`** (`UserPromptSubmit`) is the gentle half: it injects the same summary
as the turn begins, so mail can shape the work rather than interrupt it. It does *not*
mark anything delivered — a line an agent skims on its way to doing something else has
been mentioned, not delivered — so the gate will still stop the turn at the end if the
message was ignored.

**`session-end`** ends the root and releases its claims. It emits nothing and
swallows every error: it is a courtesy, not a guarantee. The real safety net is the
root TTL, which frees the claims of any root that goes quiet for fifteen minutes
whether or not this hook ever ran.

### Heartbeats, and why the TTL is short

`claim-guard`, `mail-gate` and `mail-notify` all refresh the root's `last_seen_at`
before doing anything else. That is not incidental bookkeeping — it is what pays for
a fifteen-minute root TTL.

Liveness used to depend on an agent choosing to call a stigmergy tool. An agent
heads-down in a long stretch of editing therefore looked exactly like an agent that
had crashed, so the TTL had to be generous enough to cover the longest plausible
silence — an hour — and a genuinely dead agent then went on holding its claims, and
accepting mail nobody would ever read, for that entire hour.

Hooks break the trade. They fire on the agent's own activity, so a working agent
proves it is alive as a side effect of working, and silence starts to mean what it
says. The TTL can then be set to the thing that actually matters: how long a dead
agent may keep blocking the living.

The cost is that coming back from the dead costs you your claims. A root that lapses
has had its paths declared free, and another agent may already have taken one; if the
original wakes up and heartbeats, its old claims are released rather than resurrected,
and it must re-acquire — which is the moment it discovers the path is spoken for.

---

## Google Antigravity

`init --host antigravity` writes one self-contained plugin, and touches nothing else:

```text
.agents/plugins/stigmergy/
├── plugin.json
├── mcp_config.json
└── hooks.json
```

This is the one place stigmergy gets an easier job than it does on Claude Code.
Antigravity discovers plugins under `.agents/plugins/`, and a plugin carries its own
MCP servers, hooks and rules — so `init` owns a directory rather than merging into
files somebody else also writes, and `--remove` is one `RemoveAll` rather than the
careful surgery `.claude/settings.json` needs. The workspace-wide
`.agents/mcp_config.json` is deliberately left alone.

### `hooks.json`

| Event | Matcher | Command |
|---|---|---|
| `PreInvocation` | — (ignored) | `stigmergy hook antigravity-pre-invocation` |
| `PreToolUse` | `write_to_file\|replace_file_content\|multi_replace_file_content` | `stigmergy hook antigravity-claim-guard` |
| `PreToolUse` | the mutating stigmergy MCP tools | `stigmergy hook antigravity-root-gate` |
| `Stop` | — (ignored) | `stigmergy hook antigravity-stop` |

`PreToolUse` nests its handlers under a matcher; `PreInvocation` and `Stop` take theirs
directly under the event key. Every documented write tool carries its target in
`toolCall.args.TargetFile` — including `multi_replace_file_content`, whose several edits
all land in one file.

### The hooks
- **`antigravity-pre-invocation`** replaces the `SessionStart` event Antigravity does not
  have. On `invocationNum == 0` it injects the registration instructions; on every
  invocation it injects pending mail as an `ephemeralMessage`. The `conversationId` is the
  `session_label`.
- **`antigravity-claim-guard`** denies the tool call outright, the same enforcement Claude
  Code has. It resolves the repository from the *edited path*, not from
  `workspacePaths[0]`: Antigravity reports every mounted workspace, and with two open, the
  first one is not necessarily the one holding the file.
- **`antigravity-root-gate`** is **inert on this host** — see below.
- **`antigravity-stop`** delivers mail by returning `decision: "continue"`, which re-enters
  the loop with the mail text as a system message. `fullyIdle` governs *deregistration*,
  not delivery: an agent with background tasks still running has not finished, so its
  claims are not released — but the loop is terminating either way, so its mail is handed
  over regardless. No loop guard is needed, because the mail that caused a block is marked
  delivered and cannot cause a second one.

### The root gate does not work here, and cannot yet

This is the asymmetry that matters on Antigravity, and it runs the opposite way to Codex's.

An Antigravity subagent is **a separate conversation with its own `conversationId`**. It
starts with a clean context, it can nest ten levels deep, and **no documented hook payload
field links it to a parent** — the payloads carry `conversationId`, `workspacePaths`,
`transcriptPath` and `artifactDirectoryPath`, and nothing else. So `SubagentEvidence` finds
nothing, treats the caller as a root (the only safe direction: the alternative blocks the
real root from ever writing memory), and the gate never denies.

It is worse than a gate that does nothing. `PreInvocation` fires for the subagent's first
model call too, so the registration text reaches it and tells it to register **as a root**,
with its own id. Left alone, every subagent becomes a root that can claim files and write
memory — a swarm of agents blocking each other's edits, which is the failure this tool
exists to prevent.

Nothing can enforce the boundary from the outside, so the boundary is a request — and it is
aimed where Codex aims it: at the root, before the subagent exists. The session text says
to use `stigmergy explore` rather than `invoke_subagent`, for the same reason Codex is
told to, one step worse. A second line, addressed to the subagent itself, tells it not to
register if another agent invoked it. Both arrive through `PreInvocation`, which is what
makes them dependable — an instruction file here might never have been read at all.

Prevention at the root, backstop at the child. An agent knows whether a person invoked it;
stigmergy does not.

To settle it properly, run `stigmergy hook dump` against a real `invoke_subagent` call and
read what the payload actually carries. If it names a parent, `antiSubagentKeys` in
`internal/hooks/antigravity.go` is where that field goes, and the gate starts working with
no other change. Until then, prefer `stigmergy explore` for read-only work here, for the
same reason Codex does.

### `rules/stigmergy.md` — no longer written

There used to be a rule file here, and it was always the weakest of the three
instruction files. Antigravity activates a rule in one of four modes — Manual, Always
On, Model Decision, or Glob — and the docs never said which applies to a rule that
declares none, nor what syntax declares one. If the default is Manual, it reached
nobody until `@`-mentioned, which is why nothing load-bearing was ever allowed to rest
on it.

Now nothing rests on it at all: the shared rules ride the MCP server's instructions and
`PreInvocation` carries the registration text, as it always did. A file whose delivery
we could never confirm is one less thing to keep true.

---

## opencode

Verified against **opencode v1.17.20**. Pin that version in your head while reading:
everything below was read out of the binary and then checked against a live session,
because opencode's published documentation is wrong in two places that matter.

`init --host opencode` touches two paths:

- `opencode.json` — the `mcp` entry, merged into your config.
- `.opencode/plugin/stigmergy.js` — the hook plugin, which stigmergy owns outright.

### Why there is a plugin at all

opencode has no hook system in the sense the other three hosts mean it. There is no
config file naming a command to run; there is a plugin API, in TypeScript or
JavaScript, loaded into opencode's own runtime. So stigmergy ships a plugin, and the
plugin shells out to `stigmergy hook opencode-*`.

The plugin is deliberately thin and deliberately dependency-free. It decides nothing —
every decision is made by the same Go code the other hosts use — and it imports nothing
but `node:child_process`, because a plugin needing `npm install` would make installing
stigmergy a toolchain problem. It is regenerated by `init`; do not edit it.

The one thing it decides is the one thing only it can: whether a session has a parent.

### The hooks

| opencode hook | stigmergy | what it does |
|---|---|---|
| `tool.execute.before` (`edit`, `write`, `apply_patch`) | `opencode-claim-guard` | **denies** — throws, and the tool never runs |
| `tool.execute.before` (`stigmergy_*`) | `opencode-root-gate` | **denies** — keeps subagents out of the root-only tools |
| `chat.message` | `opencode-context` | registration details and mail, once per real user prompt |

There is no end-of-turn hook, so there is no mail gate. See below.

### Blocking works here, and the reason reaches the agent

`Plugin.trigger` awaits each hook *before* the tool runs, with no `try`/`catch`, and it
dispatches through Effect's `promise` — so a throw is an unrecoverable defect rather
than a caught error. The tool does not run. This is deliberate on opencode's part: the
`config` and `dispose` hooks in the same function *are* wrapped and swallowed.

A live session confirmed the part that control flow cannot prove: the thrown message
reaches the model **verbatim**. So opencode is the only host besides Claude Code where
an agent is both stopped and told who holds the claim and how to reach them.

### The subagent gate works here — unlike anywhere else

opencode's `task` tool gives its child a session of its own, and records the parent on
it. A session with no parent has no `parentID` field at all; a child has one. The
plugin resolves it with `client.session.get`, caches it per session, and reports it.

This is the gate Codex cannot have (nothing identifies a subagent) and Antigravity
cannot have (a subagent is a separate conversation with no recorded parent). It is
worth noticing that opencode gives it to us almost by accident, through a REST field
rather than a hook payload.

"Could not ask" is reported as such, not guessed at. If the plugin cannot reach the
opencode server, the session is treated as a root — the same asymmetry the Antigravity
gate settled on, for the same reason: treating unknown as *subagent* would deny a real
root its claims and its memory, which breaks stigmergy outright, while the other way
costs one row in the roster.

### Mail is delivered but cannot be enforced

`session.idle` is a real event and it fires at the end of a turn, but it is dispatched
with its return value discarded, and its payload is only `{sessionID}`. There is
nothing to return and nothing to block.
`experimental.compaction.autocontinue` is not a general continue mechanism either: it
can only *suppress* the continue opencode already intended after a compaction.

So mail rides `chat.message`, which fires once per real user prompt — the
`UserPromptSubmit` analogue — and stigmergy tells opencode agents plainly that nothing
makes them read it. Same honesty as Codex, for a different reason.

### Two things the documentation gets wrong

Both were found by reading the binary, and both would have produced a broken host:

- **`tool.execute.before` carries `sessionID`.** The docs omit it. Without it there is
  no `session_label`, and without that the claim guard cannot tell an agent's own
  claims from everyone else's — every agent would be blocked by itself.
- **`permission.ask` does not exist.** opencode's *own* embedded reference doc lists it
  as a hook. There is no call site anywhere in the binary; permissions are decided
  entirely by a static ruleset. Anything built on it would never have run.

Also worth knowing, since none of it is documented:

- The write tools are `edit`, `write` and `apply_patch`. There is no `patch` and no
  `multiedit`. `apply_patch` names no file: its paths are headers inside `patchText`.
- MCP tools are prefixed with the server name — `stigmergy_claim_acquire` — so Claude's
  `mcp__stigmergy__(...)` matcher does not transfer.
- A message part's `id` must start with `prt`. Get it wrong and opencode rejects the
  whole message: the text never reaches the model, and the only trace is an "invalid
  user part before save" line in a log nobody is reading.
- `opencode --pure` disables external plugins. MCP still works, so the rules still
  arrive; the hooks do not, so nothing is enforced.

### `experimental.chat.system.transform` is a trap

It looks like the right home for the registration text — it fires on every request and
carries the `sessionID`. Do not use it.

A live probe showed it firing **three times for a single user prompt**, all with the
same `sessionID`, one of them for opencode's hidden **title generator**. Its input is
`{sessionID, model}` and carries no `agent` field, so there is no way to tell the real
agent from the internal `title`, `summary` and `compaction` agents. Anything pushed
there lands in all of their prompts. The system prompt is also cache-sensitive —
opencode collapses appended entries specifically for cache-friendliness — so volatile
text there costs a cache miss every turn.

stigmergy does not need it. The shared rules arrive through the MCP server's
`initialize.instructions`, which opencode wraps in `<mcp_instructions>` and puts in the
system prompt itself, with no plugin involved.

---

## Codex

`init --host codex` touches three files — **and none of them take effect until you
trust the project.**

> Codex loads project config and hooks **only for trusted projects**. Start Codex in
> the repository, trust it when prompted, and review the hooks with `/hooks`. Until
> then stigmergy is, from Codex's point of view, not installed. `init` prints this;
> `doctor` repeats it.

### `.codex/config.toml`

Appended as a delimited block. The file is **never decoded and re-encoded** — that
would reflow it and drop the user's comments. It is read as text, and the block is
appended or replaced as text.

```toml
# >>> stigmergy managed block — do not edit; `stigmergy init` regenerates it >>>
[mcp_servers.stigmergy]
command = "stigmergy"
args = ["mcp"]
startup_timeout_sec = 10.0
tool_timeout_sec = 60.0

[memories]
use_memories = false
# <<< stigmergy managed block <<<
```

`use_memories = false` is deliberate: Codex's own memory feature would be a second,
divergent store — the exact problem stigmergy exists to end. Whether Codex honors
this key at the *project* level is **unverified**; it is harmless if ignored, but do
not assume it is doing anything until someone has watched it.

If a `[mcp_servers.stigmergy]` section already exists *outside* the managed block,
`init` refuses rather than fight over it:

> `<path> already declares [mcp_servers.stigmergy] outside stigmergy's managed block — remove or rename it, then re-run `stigmergy init``

### `.codex/hooks.json`

| Event | Matcher | Command |
|---|---|---|
| `SessionStart` | `startup\|resume\|clear\|compact` | `stigmergy hook codex-session-start` |
| `UserPromptSubmit` | — | `stigmergy hook codex-mail-notify` |
| `PreToolUse` | `Edit\|Write` | `stigmergy hook codex-claim-warn` |
| `PostToolUse` | `Edit\|Write` | `stigmergy hook codex-claim-stop` |
| `PostToolUse` | `Edit\|Write` | `stigmergy hook codex-mail-notify` |

`apply_patch` is addressable as `Edit|Write`.

### `AGENTS.md` — no longer written

As with `CLAUDE.md`, and this is the file that made the case for stopping. The two
things Codex agents most need to know — that claims **cannot be enforced before an
edit here**, and that native subagents are not a boundary — are exactly the two the
Claude block denied. One symlink and a Codex agent reads "blocked outright", stops
checking claims, and writes over someone's work.

Both sentences are now rendered per host from `internal/hosts` and delivered by the
session hook, which knows which host it is talking to. A test fails if a host is ever
promised enforcement it does not have.

---

## What Codex enforcement is, and is not

Codex's `PreToolUse` hook **cannot deny a tool call**. It may return a
`systemMessage`, and the call proceeds anyway. (Returning `continue: false` there
does not block it — it marks the hook as *failed*, and the tool runs regardless.)
Only `PostToolUse` can halt, and by then the edit is on disk.

So:

**`codex-claim-warn`** (PreToolUse) emits a warning and nothing else:

```json
{"systemMessage":"stigmergy: you are about to edit a path claimed by another agent.\n…"}
```

and the text says exactly what it is:

> Codex cannot stop this write before it happens — this is a warning, not a block.
> If you proceed, the turn will be halted once the edit lands and you will have to
> undo it. Stop now: negotiate with the owner using `mailbox_send`, or work on
> something else.

There is a test asserting this text never claims the edit "is blocked by" anything.
The conflict detail is rendered by the same shared code as Claude's denial; only the
lead-in sentence differs, precisely so the honesty cannot drift.

**`codex-claim-stop`** (PostToolUse) halts the turn after the fact:

```json
{"continue": false,
 "stopReason": "stigmergy: edited a path claimed by another agent",
 "systemMessage": "stigmergy halted this turn: an edit landed on a path claimed by another agent.\n…"}
```

and tells the agent to revert (`git checkout -- <file>`), negotiate, and *not* build
further work on top of the change. It writes an audit record with action
`codex_post_edit_conflict` — the only record anywhere that a claim was actually
*violated* rather than merely enforced.

**`codex-session-start`** injects the same registration instructions as Claude's,
plus:

> Codex cannot block an edit to a claimed file before it happens. If you write to
> one anyway, the turn is halted after the fact and you will have to undo the
> change. Check claims yourself.

**`codex-mail-notify`** delivers the mailbox — on `UserPromptSubmit` and after every
edit, and **not** on `Stop`.

Codex has a `Stop` event, and it is the wrong tool here. Its output is limited to
`continue` / `stopReason` / `systemMessage`, and the manual describes `systemMessage`
as a warning surfaced "in the UI or event stream": it reaches the human, with no
promise it lands in the agent's context. Claude's `Stop` hook can block the turn and
put the mail *into the conversation*; Codex's cannot. So the asymmetry is the same one
that runs through the rest of this file, and it is stated rather than papered over:

| | Claude Code & Antigravity | Codex & opencode |
|---|---|---|
| When mail arrives | end of turn, and the turn cannot end until it does | start of turn, and after each edit |
| Can the agent finish while ignoring it | no | yes |

Every host marks the mail delivered when it shows it, because every channel demonstrably
reaches the agent — Codex's `PostToolUse` `systemMessage` is the same channel the claim
halt relies on, and opencode's `chat.message` part is saved into the conversation. What
Codex and opencode do not get is the guarantee: on Claude and Antigravity an agent
*cannot* walk away from an undelivered message, and on the other two it can.

Note that this line does not follow the claims line. opencode blocks a claimed edit as
firmly as Claude Code does, and still cannot be made to read its mail: the two depend on
different things. Blocking needs a hook that runs *before* a tool and can refuse it;
mail enforcement needs one that runs at the *end of a turn* and can refuse to let it
end. opencode has the first and not the second. That is why stigmergy describes hosts by
what each hook can do rather than sorting them into good and bad ones.

### Codex hooks fail open — including when the database is broken

`Guard` fails *closed* on a database error, but `codex-claim-warn` only emits when
there are real conflicts. A fail-closed decision carries no conflicts, so on Codex a
broken database produces **silence**.

This looks like a bug and is not. Codex could not have blocked the edit under any
circumstances, so a warning would promise protection that does not exist. Do not
"fix" it into one.

### `apply_patch` and multi-file edits

Codex's `tool_input` schema for `apply_patch` is undocumented, so `ExtractPaths` is
deliberately over-broad: it walks the
whole `tool_input` structure recursively, collects any string under a path-shaped key
(`file_path`, `filepath`, `path`, `notebook_path`), *and* regex-scans every other
string for patch headers:

```
^\*\*\*\s+(?:Add|Update|Delete|Move)\s+File:\s*(.+?)\s*$
```

A multi-file patch therefore yields every path it touches. The asymmetry is
intentional: an extra path costs a spurious warning, while a missed path costs a
silent conflict.

---

## Explorers

Native Codex subagents **inherit the parent's sandbox and permissions**. They are
not a read-only boundary, and using one as if it were is a mistake that looks fine
until it isn't.

`stigmergy explore "PROMPT"` runs instead:

```
codex exec --sandbox read-only -c approval_policy="never" --ephemeral \
  -c mcp_servers.stigmergy.enabled=false [--json] -- "PROMPT"
```

Every flag is load-bearing:

| flag | why |
|---|---|
| `--sandbox read-only` | cannot write the tree, so cannot dodge the claim guard |
| `-c approval_policy="never"` | fails closed on escalation instead of prompting into a void |
| `--ephemeral` | no session state carried between explorations |
| `-c mcp_servers.stigmergy.enabled=false` | the explorer cannot even *see* stigmergy's tools — it cannot register a root, take a claim, or write a memory |

The approval policy is set as a **config override and not as `--ask-for-approval
never`**, which is the obvious way to write it and does not work: that flag is
top-level only — the interactive TUI, where there is a human to ask — and `codex
exec` rejects it as an unknown argument, killing the explorer before it starts.
This is not a graceful degradation to guard against; it is a hard argument error,
and `stigmergy explore` shipped with it for months because nothing tests a flag
list and nothing runs codex in CI. `TestArgsNeverPassesAskForApproval` now pins it.

Two related traps, both verified against codex 0.144.5:

- **`-c` values are TOML**, so the quoting in `approval_policy="never"` is not
  decoration — a bare `never` is not the string. codex validates this key's value
  (`untrusted|on-failure|on-request|granular|never`), so a bad *value* fails loudly.
- **A bad *key* does not.** `-c totally_fake_key=1` is accepted in silence unless
  `--strict-config` is passed. So a future confinement flag added here is worth
  testing against a live codex rather than trusting that it took.

`-c mcp_servers.stigmergy.enabled=false` merges into the `[mcp_servers.stigmergy]`
table that `stigmergy init --host codex` writes. If codex has *not* been configured
for stigmergy, the override instead creates a table with no transport and codex
refuses to start: `Error loading config.toml: invalid transport`. That is confusing
but harmless — the explorer's whole purpose is to not see stigmergy, and on such a
machine it already cannot.

The explorer reports back; the root decides what to record. Memories and claims stay
the root's to write.

On Claude Code, Task subagents already flow through `claim-guard` and `root-gate`,
so no separate explorer mechanism is needed.

On Antigravity they do not. A subagent there is its own conversation and no payload field
identifies it, so the root gate never fires on it — `explore` is as useful here as it is on
Codex, and for a sharper reason: an ungated Antigravity subagent does not merely inherit
write access, it is actively invited to register as a root of its own.

---

## Probing the hosts

Several details of every host's hook payloads are documented poorly, not at all, or
wrongly. `stigmergy hook dump --tag <label>` is the instrument: wire it up as a hook,
and it appends the raw stdin JSON to `$XDG_STATE_HOME/stigmergy/probe.jsonl` (mode
`0600`) as `{"tag": …, "at": …, "payload": …}`. It is host-neutral — on opencode, a
throwaway plugin can pipe every hook into it.

The opencode work is the argument for doing this first, every time. Reading its binary
answered more than its documentation did, and a live session then contradicted the
binary reading: `experimental.chat.system.transform` looked like the obvious home for
the registration text right up until a probe caught it firing for the hidden title
generator, and part ids turned out to need a `prt` prefix that nothing anywhere
mentions — a plugin that gets it wrong has its text silently dropped and reports no
error at all. Neither would have been found by reasoning, and both would have shipped.

Use it to settle the assumptions this document flags as unverified. The largest of
them is now closed by exactly this method: dumping a main thread's `PreToolUse`
payload beside a peer agent's showed `agent_id` and `agent_type` present in one and
absent in the other, with the same `session_id` — the fields the whole per-agent
identity now rests on, none of which appear in any documentation.

### Probing the MCP side

Hooks are only half a host's contract, and `hook dump` cannot reach the other half:
what the host puts in a `tools/call`. There is no hook there, so wrap the server
instead. Two lines:

```sh
#!/bin/sh
exec tee -a "$0.in.jsonl" | exec stigmergy mcp
```

Name that script as the server command in a throwaway config
(`claude -p --mcp-config <file> --strict-mcp-config …`), drive one call through it,
and read the raw JSON-RPC. That is how `_meta["claudecode/toolUseId"]` was found —
undocumented, and the join that makes an agent's identity survive from the hook into
the server.

Two traps, both cost a run: `--allowedTools` is variadic and will swallow a trailing
positional prompt, so pipe the prompt in on stdin instead; and a host that has already
started keeps the server process it launched, so a probe must be a fresh session.
