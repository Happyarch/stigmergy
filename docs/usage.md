# Usage

How to install stigmergy, enable it in a repository, work with it day to day, and
what to do when something looks wrong.

- [Install](#install)
- [Enable it in a repository](#enable-it-in-a-repository)
- [The daily loop](#the-daily-loop)
- [When a claim blocks you](#when-a-claim-blocks-you)
- [CLI reference](#cli-reference)
- [Troubleshooting](#troubleshooting)

---

## Install

Linux, Go 1.26+:

```sh
make build
sudo install -m755 bin/stigmergy /usr/local/bin/
```

`make install` does the same thing (`DESTDIR` respected). If `~/.local/bin` is on
your `PATH`, `install -Dm755 bin/stigmergy ~/.local/bin/stigmergy` needs no `sudo`.

Confirm it took with `stigmergy --version`. A build from a tree with no git tag
reports `dev`; that is the version string, not a failure.

The binary is static (`CGO_ENABLED=0`), so there is nothing else to install — no
libsqlite3 to match, no runtime.

**It must be on `PATH`.** Host configurations invoke `stigmergy` by bare name, so a
binary the hosts cannot find fails *invisibly*: the MCP server never starts, and the
claim hooks silently never run — everything looks fine and nothing is enforced.
`stigmergy doctor` checks this first.

---

## Enable it in a repository

```sh
cd your-repo
stigmergy init
```

That creates the project database, registers the MCP server with both hosts,
installs the hooks, and adds a marked block to `CLAUDE.md` / `AGENTS.md` telling
agents to use it. Existing configuration is merged, never overwritten. Use
`--host claude` or `--host codex` for just one.

Exactly what gets written, per host: [hosts.md](hosts.md).

If `init` finds an existing Claude Code markdown memory directory for the project,
it says so and offers to import it. The source files are never modified, and an
existing memory is never overwritten — a file whose content differs from a memory of
the same name is reported as a conflict for you to reconcile. `--no-input` prints
the command to run later instead of prompting.

**Import before you restart.** `init` also sets `autoMemoryEnabled: false`, which
takes effect at the next session start. Between adopting the project and importing,
anything already in the old markdown store is stranded: Claude will no longer read
it, and stigmergy does not yet have it. Nothing is deleted, so this is recoverable —
run the import at any time — but a session restarted in the gap begins with no
memory at all. If you declined the prompt or used `--no-input`, run the command
`init` printed before restarting anything:

```sh
stigmergy import claude-memory --source ~/.claude/projects/<slug>/memory
```

Then **restart any running agent sessions** so they pick up the new configuration,
and check it took:

```sh
stigmergy doctor
```

### If you use Codex

Codex loads project config and hooks **only for trusted projects**. Start Codex in
the repository, trust it when prompted, and review the hooks with `/hooks`. Until
you do, none of this exists as far as Codex is concerned.

---

## The daily loop

Mostly you don't drive stigmergy — the agents do, because `init` told them to. The
loop each agent follows:

1. **Register.** `context_open`, then `root_register` with the host session id as
   `session_label`. The session-start hook tells the agent to do this and gives it
   the exact value to use.
2. **Search before working.** `memory_search` — what does this project already know
   about the thing I'm about to do?
3. **Claim before editing.** `claim_acquire` on the narrowest scope that covers the
   work, with a reason other agents can act on.
4. **Work.**
5. **Release.** `claim_release`, as soon as it's done — someone may be waiting.
   `claim_renew` if it's taking longer than the TTL.
6. **Record.** `memory_write` for durable, non-obvious facts: conventions,
   decisions, constraints. Not things the code or git history already say.

Claims expire on their own (30 minutes by default), and a crashed agent's claims die
with it inside an hour. Nothing leaks.

---

## When a claim blocks you

On **Claude Code** the edit is refused outright, and the reason names the owner, the
reason, the worktree, the branch, and the expiry. There are exactly three legitimate
responses:

- **Negotiate** — `mailbox_send(to_root, subject, body)` to the owner. Say what you
  need and what you propose; the other agent has to be able to act on it. This is the
  intended path.
- **Wait** — the claim has an expiry, and it is in the message.
- **Work elsewhere.**

There is no fourth option. Do not edit around the claim, and do not re-file the same
work under a different path.

If the owner turns out to be gone, `mailbox_send` tells you so — and that is good
news, because a dead root's claims have lapsed with it and the paths it held are
already free.

On **Codex** the edit is *not* refused. You get a warning first and the turn is
halted after the edit lands, which means undoing work. Check claims yourself with
`claim_check`; nothing else will stop you. See
[hosts.md](hosts.md#what-codex-enforcement-is-and-is-not).

---

## CLI reference

### `stigmergy init`

Enable stigmergy in this repository.

| Flag | Default | Meaning |
|---|---|---|
| `--host` | `all` | which hosts to configure: `all`, `claude`, or `codex` |
| `--remove` | | remove stigmergy's configuration from this repository |
| `--no-input` | | never prompt; print what to run instead |
| `--purge-db` | | with `--remove`: **also delete the project database and every memory in it** |
| `--yes` | | with `--purge-db`: skip the confirmation prompt |

`--remove` keeps the database. Disabling stigmergy and destroying a project's
accumulated memory are different intentions, and only one of them is reversible —
re-running `init` picks the memories back up. `--purge-db` does the destructive
thing, and asks first. See [operations.md](operations.md#removing-and-purging).

### `stigmergy watch`

See what the agents are doing: who is working here, what they have claimed, and what
they are saying to each other. This is the human's window onto a mesh that otherwise
only agents can see — it reads, never writes, and never marks anything read.

```
ROOTS (2 active)
  r-96ef3e4adebb claude-code  main           sess-abc     seen 0s ago
  r-4fbc71ae4f79 codex        feature-x      sess-def     seen 4s ago

CLAIMS (2)
  src/api/**               r-96ef3e4adebb "reshaping the request types"  expires in 29m
  docs/api.md              r-96ef3e4adebb "documenting the new shape"    expires in 29m

MAILBOX (1 threads)

  #1 OPEN
     r-4fbc71ae4f79 -> r-96ef3e4adebb  2m ago  UNREAD
       blocked on src/api/handlers.go
         I need to add a /healthz endpoint. Can you release the claim, or
         tell me what is changing so I do not build on the old shape?
```

| Flag | Default | Meaning |
|---|---|---|
| `-f`, `--follow` | | keep watching, redrawing as things change |
| `--interval` | `2s` | how often to refresh with `--follow` |
| `--json` | | emit the snapshot as JSON, for scripting |
| `--threads` | `10` | how many recent conversations to show |

The three sections are meant to be read together: a negotiation only makes sense
next to the claim it is about, and a claim only makes sense next to the agent
holding it.

### `stigmergy doctor`

Diagnose the installation and this project. Run it first whenever something is not
working. Exits non-zero if anything FAILs.

| Flag | Meaning |
|---|---|
| `--gc` | also prune old audit records and resolved mail |

### `stigmergy import claude-memory`

Import a Claude Code markdown memory directory.

| Flag | Default | Meaning |
|---|---|---|
| `--source` | detected | the memory directory to import |
| `--scope` | `project` | import into `project` or `global` |
| `--dry-run` | | report what would happen, write nothing |
| `--report` | | also write a JSON report to this path |

Idempotent: unchanged files are skipped, differing ones are reported as conflicts and
**never overwritten**, and source files are never modified. A `--dry-run` will not
even create a database.

### `stigmergy explore "PROMPT"`

Run a read-only exploration in a confined Codex process — it cannot write the tree
and cannot see stigmergy's tools. Use this instead of a native Codex subagent, which
inherits your sandbox and is not a boundary.

| Flag | Default | Meaning |
|---|---|---|
| `--worktree` | current repo | directory to explore |
| `--timeout` | `10m` | give up after this long |
| `--json` | | stream Codex's JSON events instead of plain output |

The child's exit code is propagated.

### `stigmergy mcp`

Run the MCP server on stdio. This is what the host configurations invoke; it is not
meant to be run by hand.

### `stigmergy hook …`

The hook handlers: `claim-guard`, `root-gate`, `session-start`, `session-end`,
`codex-session-start`, `codex-claim-warn`, `codex-claim-stop`. They read a host's
JSON payload on stdin and write a decision on stdout. `init` wires them up; you do
not run them yourself.

`stigmergy hook dump --tag <label>` is the exception, and it is a debugging
instrument: point a host hook at it and it appends the raw payload to
`$XDG_STATE_HOME/stigmergy/probe.jsonl`. It is how the undocumented corners of the
two hosts' hook payloads get settled — see [hosts.md](hosts.md#probing-the-hosts).

### `stigmergy db path` / `stigmergy db migrate`

Print the database paths, or create/migrate them. Debugging aids.

---

## Troubleshooting

### Every edit is blocked, and the reason mentions `stigmergy doctor`

The claim guard **fails closed**: when it cannot verify claims, it refuses the edit
rather than risk overwriting another agent's work. The message says so explicitly —
it is not a claim conflict, and there is nobody to negotiate with.

Run `stigmergy doctor`. It is almost always one of:

- the project database cannot be opened (permissions, corruption, a full disk)
- the database is at a *newer* schema version than this binary — a newer stigmergy
  wrote it. Upgrade the binary.

### Claims are not being enforced at all

Nothing is blocked, no warnings appear, and agents happily edit each other's files.
In order of likelihood:

1. **`stigmergy` is not on `PATH`.** The hosts invoke it by name; if they can't find
   it, the hooks never run and the MCP server never starts. `doctor` reports this as
   a FAIL.
2. **The project was never adopted** — no `stigmergy init` here. The hooks
   deliberately allow everything in a project with no database, so that stigmergy
   stays out of the way of repositories that don't use it.
3. **The agent sessions predate `init`.** Restart them.
4. **Codex: the project is not trusted.** Codex loads project hooks only for trusted
   projects. Trust it, and review the hooks with `/hooks`.
5. **Codex, before the edit:** it is *supposed* to be a warning. Codex cannot block a
   write in advance. This one is not a bug.

### An agent is blocking its own edits

It registered with the wrong `session_label`, or did not register at all.

The claim guard identifies "you" by matching the host session id you passed to
`root_register`. If it can't match you to a root, then **every claim in the
repository is foreign to you — including the ones you made yourself**. So you block
yourself.

Fix: call `root_register` with `session_label` set to exactly the session id the
session-start hook gave the agent. (This is also what makes registration
self-enforcing: an unregistered agent quickly discovers it needs to register.)

### The agent has lost everything it used to remember about this project

You restarted the session before importing. `init` turns Claude's auto memory off,
so the old markdown store is no longer read — and if it was never imported, stigmergy
has nothing to read *instead*. `doctor` says `0 project memories`.

Nothing was deleted. The files are still in `~/.claude/projects/<slug>/memory/`:

```sh
stigmergy import claude-memory --source ~/.claude/projects/<slug>/memory
```

Then restart the session again.

### `memory_write` keeps returning `cas_conflict`

Another agent changed the memory between your read and your write. The error carries
their current version in `current`. Re-read it, merge your change into theirs, and
retry with the new `expected_version`.

Do **not** work around it by writing to a new key. That leaves the project with two
half-true memories and no way to tell which one is current.

### A claim is held by an agent that is clearly gone

It will free itself. A root that has not been heard from for an hour stops holding
claims, and claims expire on their own within 30 minutes by default.

If you want to confirm, `mailbox_send` to the owner: if the root is dead you get
`recipient_inactive`, which tells you in as many words that its claims have lapsed
and the paths are free.

To force the issue: `stigmergy doctor --gc` reaps long-silent roots.

### Search returns nothing for a word that is definitely in a memory

Search is FTS5 with a `porter unicode61` tokenizer — stemmed, and **all words must
appear**. Try fewer words. If search fails entirely with `unsupported_search`, run
`doctor`: it probes FTS5 and will tell you if the build lacks it.

### I want to see who did what

The audit log records it, kept 90 days. It is a table (`audit_log`) in the project
database; there is no CLI reporting for it yet — query the SQLite file directly.
