# Usage

How to install stigmergy, enable it in a repository, work with it day to day, and
what to do when something looks wrong.

- [Install](#install)
- [Enable it in a repository](#enable-it-in-a-repository)
- [The daily loop](#the-daily-loop)
- [When a claim blocks you](#when-a-claim-blocks-you)
- [CLI reference](#cli-reference)
- [Troubleshooting](#troubleshooting)
- [Several repositories, one project](#several-repositories-one-project)
- [After upgrading stigmergy](#after-upgrading-stigmergy)

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

That creates the project database, registers the MCP server with every supported
host, and installs the hooks. Existing configuration is merged, never overwritten.
Use `--host claude`, `--host codex`, `--host antigravity` or `--host opencode` for
just one.

It does not touch `CLAUDE.md` or `AGENTS.md`. It used to write a block into them,
and that block is now delivered instead by the MCP server (which every host shows
its model) and the session hooks (which are the only thing that knows an agent's
session id). If you have a leftover block from an older version, `stigmergy doctor`
says so; `stigmergy init --remove` takes it out.

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
with it inside fifteen minutes. Nothing leaks.

Your mail is delivered to you, not left for you to find. On Claude Code you cannot end
a turn while a message you have never been shown is waiting: the turn is blocked, the
message is put in front of you, and you deal with it. On Codex it arrives as the turn
begins and after each edit. Either way, answering is not optional politeness — the
agent that wrote to you is usually blocked on your reply.

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

### Write to the agent that is actually there

The `to_root` in that `mailbox_send` is the whole game, and it is easy to get wrong.
The conflict names the root that holds the path *and* says whether it is still live —
"live (last seen 20s ago)", "quiet (last seen 9m ago; lapses in 6m if it stays
silent)". Use that id. Do not reconstruct one from earlier in the session, from a
memory, or from a message you read twenty minutes ago: a root id remembered wrongly is
not an error, it is an address, and the mail goes cheerfully to an agent that no longer
exists while the one blocking you is never asked.

If you are unsure who is here, `root_list_active` tells you: every agent working in the
repository right now, how recently each was heard from, and what each of them holds.

If the owner turns out to be gone, `mailbox_send` refuses — and that is good news, not
a dead end. A dead root's claims have lapsed with it, so the path you wanted is already
free. The refusal says so, and hands you the list of agents who *are* here in case you
still need one of them.

And if you asked for a file and no answer ever comes, check `mailbox_threads`. A thread
whose counterparty has died is marked as such: nobody is thinking about your request,
and the claim you were waiting on is gone. Stop waiting, take the claim, and close the
thread with `mailbox_resolve(abandoned=true)`.

### Saying yes is not releasing

If you agree to hand a path over, `claim_release` is what actually hands it over.
Agreeing in a message and then keeping the claim leaves the other agent exactly as
blocked as before, and now waiting on a promise as well.

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
| `--host` | `all` | which hosts to configure: `all`, `claude`, `codex`, `antigravity`, or `opencode` |
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
| `--gc` | also prune old audit records, resolved mail, and stale episodes |
| `--all` | check and upgrade every project on this machine, not just this one |

It checks the whole project, not the directory it was run from. In a project
spanning several repositories, that means it resolves the shared database through
the pointer in this repository's git common dir, lists every member with its
worktree path, and marks which one you are standing in. Two things it can only
report from that whole-project view:

- **Host configuration is checked per member**, each line prefixed with the
  repository it is about. This is the failure that hides: one member fully wired
  and another with no hooks at all looks entirely healthy from inside the working
  one, and the project coordinates in one direction only.
- **A member whose worktree has gone missing is a FAIL.** Its claims can no longer
  be resolved, so nothing can release them; the fix is
  `stigmergy project remove <repo>`.

A missing database is also treated more seriously in a multi-repository project.
Elsewhere it just means stigmergy was never enabled, but a repository holding a
pointer to a database that is not there has a claim guard that fails closed, so
every edit in it is blocked until the database is restored or the pointer removed.

`--all` is a different job: it walks the registry of known projects and opens each
one read-write, which is what applies a pending schema migration. A project
spanning several repositories appears there once, as the one database it has — so
`--all` never performs the per-member wiring checks above. See
[After upgrading stigmergy](#after-upgrading-stigmergy).

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

The hook handlers: `claim-guard`, `root-gate`, `subagent-stop`, `session-start`,
`session-end`, `mail-gate`, `mail-notify`, `codex-session-start`, `codex-claim-warn`,
`codex-claim-stop`, and the Antigravity and opencode equivalents. They read a host's
JSON payload on stdin and write a decision on stdout. `init` wires them up; you do
not run them yourself.

`stigmergy hook dump --tag <label>` is the exception, and it is a debugging
instrument: point a host hook at it and it appends the raw payload to
`$XDG_STATE_HOME/stigmergy/probe.jsonl`. It is how the undocumented corners of the
two hosts' hook payloads get settled — see [hosts.md](hosts.md#probing-the-hosts).

### `stigmergy project …`

`create`, `add`, `remove`, `list` — group several repositories into one project so
they share a database, a roster and a mailbox. Nothing here is needed for a project
that is one repository, which is almost all of them. See
[Several repositories, one project](#several-repositories-one-project) below.

### `stigmergy deliberate`

Put a specification through agents that take turns attacking it: draft, interrogate,
revise, tear down, judge, rotate, repeat. Each runs at the real repository path inside
a bwrap overlay whose writes land in RAM, so your working tree is never touched and
does not need to be clean. It is a separate subsystem with its own document —
[deliberation.md](deliberation.md) — and is not part of the memory/claims loop above.

### `stigmergy sync …`

Carry your memories between your own machines. `enable` joins a project to the sync
set, `export <dir>` writes this machine's memories to a directory, `import <dir>` reads
another machine's export back in, `status` shows what is pending or contested, and
`resolve <key>` settles a memory that changed on both machines.

This is for **one developer's own machines**, not for a team. A project can have several
developers with private memories, so nothing here ever travels through the project's
repository — moving the exported directory is a separate, deliberate act.

There is no transport yet: move the directory however you like, including with a file
syncer. Move only the *exported directory* that way, never `stigmergy.sqlite3` itself —
[operations.md](operations.md) explains why that corrupts databases. Full guide:
[sync.md](sync.md).

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

It will free itself. A root that has not been heard from for fifteen minutes stops
holding claims, and claims expire on their own within 30 minutes by default.

Check first rather than guessing: `root_list_active` shows who is actually working
here and how recently each was heard from, and a claim conflict says the same thing
about the owner in the same breath as it names them.

If you want to confirm, `mailbox_send` to the owner: if the root is dead you get
`recipient_inactive`, which tells you in as many words that its claims have lapsed
and the paths are free — and names the agents that *are* active, so you can write to
the right one instead.

Do not wait on a dead agent. If you have already written to one and are waiting for a
reply, `mailbox_threads` marks the thread: the counterparty is gone, and no answer is
coming.

To force the issue: `stigmergy doctor --gc` reaps long-silent roots.

### Search returns nothing for a word that is definitely in a memory

Search is FTS5 with a `porter unicode61` tokenizer — stemmed, and **all words must
appear**. Try fewer words. If search fails entirely with `unsupported_search`, run
`doctor`: it probes FTS5 and will tell you if the build lacks it.

### I want to see who did what

The audit log records it, kept 90 days. It is a table (`audit_log`) in the project
database; there is no CLI reporting for it yet — query the SQLite file directly.

## Several repositories, one project

Most projects are one repository and need none of this. When several are one
piece of work — a client and its service, with separate remotes, whose changes
cross between them — group them:

```
stigmergy project create --add ./naviamp --add ./naviamp-sidecar --name naviamp
```

They share one database, so memories, the roster and the mailbox reach across all
of them, and a claim in one blocks an agent editing it from another. Scopes are
spelled `repo:path`; a bare path means the repository you are in.

The repositories need share no parent directory and may sit on different disks.

```
stigmergy project list          every project on this machine
stigmergy project add <path>    add a repository to the project you are in
stigmergy project remove <r>    drop one; its claims are released
```

### Where to start an agent session

**Inside one of the member repositories** — never in a directory above them.

A parent directory holding both of them is not a git repository, and stigmergy
resolves a project by walking *up* from the working directory to the nearest
`.git` and reading the pointer file it finds there. From a parent there is no
`.git` to find, so there is no project, no database, and no coordination. Nor is
the configuration there: `init` writes `.mcp.json`, `.claude/`, `.codex/` and
`opencode.json` into each member's own worktree, so a host started a level up
sees no MCP server and no hooks. Worse is a parent that happens to be a git
repository itself, because then everything resolves — to a different project that
has never been adopted, where the hooks correctly stay out of the way and nothing
announces that the session is uncoordinated.

Picking one member costs no reach. The claim guard resolves the *project* from the
working directory, but decides which repository governs each edit from the edited
file's own path, so a session started in `naviamp` can edit a file in
`naviamp-sidecar` by absolute path and the claim is still scoped
`naviamp-sidecar:…` — and still blocks an agent working from the other side.
Memories, mail, roots and claims live in the one shared database whichever member
the session began in. Start in whichever repository most of the work is, since
that is what relative paths resolve against.

Antigravity and opencode can hold several workspaces in one session, and the claim
guard groups edits by the workspace each file came from. Where a host offers that,
adding both repositories is a good fit for a project like this — but each added
workspace must itself be a member repository, not the directory above them.

## After upgrading stigmergy

```
stigmergy doctor --all
```

A new binary carrying a schema migration blocks edits in every adopted project
until each database is upgraded — the claim guard fails closed when it cannot
verify claims. `--all` upgrades every project this machine knows about in one
pass. Run it immediately after installing.
