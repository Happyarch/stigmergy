# Operations

Running stigmergy over time: the constants that govern its behavior, what
housekeeping does, how to back it up, and how to take it apart.

---

## Constants

Every tunable in one place, with why it is what it is. Change one and you are
changing a design decision, not a setting.

| Constant | Value | Where | Why |
|---|---|---|---|
| `DefaultClaimTTL` | 30 min | `store/claims.go` | long enough for a real unit of work; short enough that a forgotten claim is an annoyance, not an outage |
| `MinClaimTTL` | 60 s | | below this a claim expires before anyone can react to it |
| `MaxClaimTTL` | 24 h | | a claim that never expires is a deadlock waiting to happen |
| `RootTTL` | 15 min | `store/roots.go` | a root unheard-from this long stops holding claims, and can no longer be sent mail. **This is the crash-safety net** — see below |
| `StaleRootSilence` | 5 min | | a root still inside the TTL but quiet long enough to say so when it is named. Changes no decision; it is what turns "live" into "quiet (last seen 9m ago; lapses in 6m)" |
| `StaleRootAge` | 7 d | | when a silent root gets `ended_at` stamped by `ReapStaleRoots` |
| heartbeat budget | 100 ms | `hooks/mail.go` | a heartbeat must never cost the agent its edit |
| `AuditRetention` | 90 d | `store/gc.go` | long enough to investigate anything anyone still remembers |
| `MailRetention` | 30 d | | resolved threads only; a settled conversation stops being worth re-reading |
| `MaxSearchHits` | 20 | `store/memories.go` | per scope |
| `SuggestionLimit` | 3 | `mcpserver/tools_memory.go` | near-duplicate hints on a create |
| `PriorityBudget` | 512 chars | `mcpserver/instructions.go` | what Codex prioritizes from the server instructions; **test-enforced** |
| `HookBusyTimeout` | 250 ms | `hooks/claimguard.go` | on the hook path, waiting a long time for a lock *is* the failure |
| audit budget | 100 ms | `hooks/session.go` | an audit write must never be able to flip or delay a decision |
| default busy timeout | 5000 ms | `store/open.go` | server and CLI, where waiting is fine |
| explore timeout | 10 min | `explore/codexexec.go` | |
| Codex MCP startup / tool timeout | 10 s / 60 s | `hostcfg/codex.go` | |

TTLs out of range are **rejected, not clamped**. Silently giving an agent a
different TTL than it asked for is how you get an agent that believes it still
holds a claim it doesn't.

### Why the root TTL is the important one

A claim is only in force while the root holding it is alive
([architecture.md §5.1](architecture.md#51-the-active-claim-predicate)). Kill an
agent however you like — `SIGKILL`, closed laptop, OOM — and within `RootTTL` its
claims stop blocking anyone. No cleanup process, no lease renewal, no stale-lock
recovery.

That is why there is no daemon. Expiry is lazy, folded into the query predicate and
evaluated on read. Nothing runs in the background, so nothing can fail to run in the
background.

Any tool call refreshes a root's liveness — and so does any *edit*, and any turn,
through the hooks. An agent that is doing anything at all stays alive without thinking
about it, which is precisely what lets the TTL be fifteen minutes rather than an hour:
the TTL is how long a *dead* agent goes on blocking the living, and there is no longer
any reason to pad it against the possibility that a live one simply had nothing to say.

The price is at the other end. A root that lapses and then comes back **loses its
claims** rather than resurrecting them: the paths had been declared free, someone may
already have taken one, and two agents each believing they hold the same file is the
one thing this whole design exists to prevent. It re-acquires, and finds out. You will
see this in the audit log as `root_lapsed_claims_released`.

Fifteen minutes is not sacred, but it is a trade, not a preference. Raise it and a
crashed agent blocks others for longer. Lower it and an agent that legitimately goes
quiet — a very long tool call, a human staring at a diff — starts losing claims it is
still using.

---

## Housekeeping

```sh
stigmergy doctor --gc
```

Two things happen:

**`ReapStaleRoots`** stamps `ended_at` on roots that have been silent for 7 days.
Cosmetic — they already stopped holding claims fifteen minutes in.

**`GC`** prunes:

- audit records older than **90 days**
- messages in **resolved or abandoned** threads older than **30 days**, and then any
  thread left empty

What GC **never touches**, and must never touch:

- **memories** — these are the state of the system, not its history. Nothing but an
  explicit `memory_delete` should ever remove one.
- **open claims** — obviously.
- **messages in threads that are still open.** An old message in an unsettled thread
  is precisely the context someone needs in order to settle it. Age is not the test;
  resolution is.

There is nothing to schedule. Run it when you feel like it, or never — the system
works fine with an unpruned audit log; it just gets larger.

---

## Backup

The project database is a single SQLite file:

```
<git-common-dir>/stigmergy.sqlite3
```

Copy it (plus `-wal` and `-shm`, or checkpoint the WAL first with
`sqlite3 stigmergy.sqlite3 'PRAGMA wal_checkpoint(TRUNCATE);'`). The global database
is at `$XDG_DATA_HOME/stigmergy/global.sqlite3`.

**The project database lives inside `.git/`, which means it is not committed, not
cloned, and not pushed.** Project memories are local to your machine. This surprises
people, so it is worth saying twice: `git push` does not share your memories with
anyone, and `git clone` on another machine gets you an empty stigmergy.

If a fact is true everywhere, `memory_promote` it into the global scope — but that
is still per-machine. Syncing memories across machines is not something stigmergy
does today.

### Worktrees and clones

The database sits in the git **common** dir, so:

- linked worktrees (`git worktree add`) **share one database** — two agents in two
  worktrees of the same repository see and block each other, which is what you want,
  because they are editing the same codebase
- separate clones are **fully isolated**, which is also what you want, because they
  are not

---

## Removing and purging

```sh
stigmergy init --remove              # unhook it; keep the memories
stigmergy init --remove --purge-db   # delete the database too (asks first)
```

`--remove` takes back exactly what `init` added — the MCP registration, the hook
entries (identified by their `stigmergy hook` command prefix), and the marker blocks
— and **keeps the database**. Disabling a tool and destroying a project's
accumulated memory are different intentions, and only one of them is reversible.
Re-running `init` picks the memories back up.

`--purge-db` is the one genuinely destructive operation in stigmergy, and it behaves
accordingly. It tells you exactly what you are about to lose:

```
This will permanently delete /repo/.git/stigmergy.sqlite3,
including 47 project memories, and every claim and message in this repository.
This cannot be undone. Type 'delete' to confirm:
```

Counts, not just a filename — "delete the database?" and "delete 47 memories?" are
different questions. Nothing but the literal word `delete` proceeds.

With no terminal to ask on, it **refuses** rather than guessing:

> `refusing to delete 47 memories without confirmation — re-run with --yes if you mean it`

`--yes` skips the prompt, for when you genuinely mean it in a script. Global memories
are never touched by a project purge.

---

## Concurrency

Several processes touch the project database at once: the MCP server (one per agent
session), the CLI, and the hooks — *one fresh process per edit*. WAL mode handles
this, and every write takes the lock up front (`BEGIN IMMEDIATE`), so read-then-write
sequences are atomic rather than racy.

**Local filesystems only.** SQLite locking over NFS or a network share is not
trustworthy, and every enforcement guarantee here rests on that locking.

---

## Failure modes, and which way they fail

| Situation | Claude Code, Antigravity | opencode | Codex |
|---|---|---|---|
| No project database (not adopted) | allow — stays out of the way | allow | allow |
| Not a git repository | allow | allow | allow |
| Unparseable hook payload | **allow** — our bug must not block the user's work | **allow** | allow |
| stigmergy binary missing or crashed | allow (the hook never runs) | **allow** — the plugin turns every failure into a null | allow |
| Database unreadable / schema mismatch | **deny**, and say to run `doctor` | **deny**, and say to run `doctor` | **silent** — see below |
| Claim held by another root | **deny** | **deny** | warn, then halt the turn after the edit lands |

opencode's row for a missing binary is worth stating out loud, because it is the one
host where stigmergy is code running inside the agent rather than a hook the host
calls. The plugin throws to block an edit, and a throw is exactly what opencode turns
into a failed tool call — so a plugin that threw on its own errors would convert a
missing `stigmergy` binary into a repository where nothing can be written. It returns
null on every failure instead, and only a genuine deny throws.

The Codex cell in that fourth row is deliberate. `Guard` fails closed, but
`codex-claim-warn` only speaks when there are real conflicts — and a fail-closed
decision has none. Codex could not have blocked the edit under any circumstances, so
a warning there would promise protection that does not exist. It looks like a bug and
is not.

A newer stigmergy having written the database is a *hard* fail on Claude: every edit
stays blocked until you upgrade the binary. `doctor` says so in as many words, because
"all my edits are blocked" with no explanation is the worst experience this tool can
inflict.

### Upgrading: migrate the database and the binary together

This is the sharp edge of the rule above, and it is easy to walk into.

Running a *newer* `stigmergy` anywhere — `init`, `doctor`, an MCP server — migrates the
project database. From that moment, the older binary still on `$PATH` fails closed on
every edit, in every session, because it cannot read the schema and refuses to guess
about claims. The system is behaving exactly as designed and the experience is that all
work stops.

So: install the new binary, *then* migrate. In practice:

```sh
make install                 # or: cp bin/stigmergy ~/.local/bin/stigmergy
stigmergy doctor             # confirms the schema version it can actually read
```

If the copy fails with `Text file busy`, a running MCP server is holding the old binary
open. `mv` it aside and copy the new one into place — the running process keeps its
inode, and the next session picks up the new file.

Restart agent sessions afterwards. A session's MCP server is the binary it was launched
with: until it restarts, that session keeps the old tool set and the old constants, even
though its hooks (which are fresh processes each time) already have the new ones.
