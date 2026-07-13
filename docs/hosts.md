# Host integration

What `stigmergy init` writes into a project, what the hooks send and receive, and
where the two hosts differ. Source: `internal/hostcfg/` and `internal/hooks/`.

Read [§7 of architecture.md](architecture.md#7-the-asymmetry-is-inherent) first if
you only read one thing: Claude Code can prevent a bad edit, and Codex cannot.
Everything below follows from that.

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

Four hook entries:

| Event | Matcher | Command |
|---|---|---|
| `SessionStart` | — | `stigmergy hook session-start` |
| `PreToolUse` | `Edit\|Write\|NotebookEdit` | `stigmergy hook claim-guard` |
| `PreToolUse` | `mcp__stigmergy__(root_register\|root_heartbeat\|root_deregister\|memory_write\|memory_promote\|memory_delete\|claim_acquire\|claim_renew\|claim_release\|mailbox_.*)` | `stigmergy hook root-gate` |
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

### `CLAUDE.md`

A marker block telling the agent that memory lives in stigmergy and not in its own
memory files, to register at session start, to claim before editing, to negotiate
rather than edit around a claim, and that only the root may write. This is the
belt to the settings key's braces: the setting stops the mechanism, the block tells
the agent why.

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

**`claim-guard`** is the only place claims are truly enforced. It reads `session_id`,
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

**`root-gate`** denies the mutating `mcp__stigmergy__*` tools when the payload shows
a subagent indicator:

> `stigmergy: <tool> may only be called by a root session, and this call comes from a subagent (<field>). Report your findings to your root and let it record them: memories, claims and mail are the root's to write.`

Which field identifies a subagent is **not documented by the host**. The code scans
a candidate set (`subagent_type`, `agent_type`, `is_subagent`, `parent_tool_use_id`,
and camelCase variants) and treats *absence of any indicator as "root"*, on purpose:
guessing "subagent" would lock a root out of its own tools. **Whether any such field
exists at all is unverified.** If none does, this gate is advisory and the enforced
explorer path is a read-only agent definition. Settle it with `stigmergy hook dump`:
capture a root's payload and a subagent's, and diff them.

**`session-end`** ends the root and releases its claims. It emits nothing and
swallows every error: it is a courtesy, not a guarantee. The real safety net is the
root TTL, which frees the claims of any root that goes quiet for an hour whether or
not this hook ever ran.

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
| `PreToolUse` | `Edit\|Write` | `stigmergy hook codex-claim-warn` |
| `PostToolUse` | `Edit\|Write` | `stigmergy hook codex-claim-stop` |

`apply_patch` is addressable as `Edit|Write`.

### `AGENTS.md`

The same marker block as `CLAUDE.md`, plus two things Codex agents need and Claude
agents don't: that claims **cannot be enforced before an edit here**, and that
native subagents are not a boundary (use `stigmergy explore` instead).

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
codex exec --sandbox read-only --ask-for-approval never --ephemeral \
  -c mcp_servers.stigmergy.enabled=false [--json] -- "PROMPT"
```

Every flag is load-bearing:

| flag | why |
|---|---|
| `--sandbox read-only` | cannot write the tree, so cannot dodge the claim guard |
| `--ask-for-approval never` | fails closed on escalation instead of prompting into a void |
| `--ephemeral` | no session state carried between explorations |
| `-c mcp_servers.stigmergy.enabled=false` | the explorer cannot even *see* stigmergy's tools — it cannot register a root, take a claim, or write a memory |

The explorer reports back; the root decides what to record. Memories and claims stay
the root's to write.

On Claude Code, Task subagents already flow through `claim-guard` and `root-gate`,
so no separate explorer mechanism is needed.

---

## Probing the hosts

Several details of both hosts' hook payloads are documented poorly or not at all.
`stigmergy hook dump --tag <label>` is the instrument: wire it up as a hook, and it
appends the raw stdin JSON to `$XDG_STATE_HOME/stigmergy/probe.jsonl` (mode `0600`)
as `{"tag": …, "at": …, "payload": …}`.

Use it to settle the assumptions this document flags as unverified — in particular
the subagent indicator: capture a root's payload and a Task subagent's, and diff them.
