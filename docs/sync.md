# Cross-machine memory sync

How to move memories between two machines belonging to the same developer.
[architecture.md](architecture.md) §3 explains why this is a deliberate act
rather than a side effect of `git push`: a project database lives inside
`.git/`, is never committed, and a project can have several developers, each
with private memories. This document is the user guide for the deliberate
act; [sync-model.md](sync-model.md) is the design behind it, for whoever is
extending this rather than driving it.

**Stage A**, described here, ships first and stands on its own: `sync export`
writes this machine's memories to a plain directory, and `sync import` reads
another machine's export back in. Move the directory between machines
however is convenient — a USB stick, a Syncthing folder, a private git
repository driven by hand. A git-backed transport that fetches and pushes
automatically is a later stage; see [What is not here yet](#what-is-not-here-yet).

- [What syncs, and what does not](#what-syncs-and-what-does-not)
- [What the exported tree exposes](#what-the-exported-tree-exposes)
- [Do not point a file syncer at the database](#do-not-point-a-file-syncer-at-the-database)
- [First-time setup](#first-time-setup)
- [The daily shape](#the-daily-shape)
- [Conflicts](#conflicts)
- [Choosing what syncs](#choosing-what-syncs)
- [Command reference](#command-reference)
- [Troubleshooting](#troubleshooting)
- [What is not here yet](#what-is-not-here-yet)

---

## What syncs, and what does not

A memory syncs when its meaning does not depend on which machine it is on.
Project memories sync by default, because the project's identity has already
been confirmed by `sync enable` (below). Global memories do **not** sync by
default: a global memory can be a fact about the physical machine it was
written on — this repository's own global scope holds one such memory
alongside a portable one — and only the person who wrote it knows which. Use
[`sync share`](#command-reference) to opt a global memory in, or
[`sync unshare`](#command-reference) to opt a project memory out.

Nothing else moves. Claims, roots, mail, and the audit log are all facts
about one machine or one live session, and importing any of them would either
do nothing useful or actively mislead the machine that received them — see
sync-model.md §1 for the full table and the reasoning behind each row.
Deletes do propagate: deleting a memory here and syncing is expected to
delete it on the other machine too, the next time it imports.

---

## What the exported tree exposes

An exported directory holds **plaintext**: one markdown file per memory,
readable in any editor, plus a couple of JSON Lines files for links and
deletions. Nothing is encrypted. If the transport carrying this directory is
a private repository on a hosted git service, the operator of that service
can read every memory, every description, and the timing of every sync. A
bare repository on a machine only the developer controls means nobody else
can. Encryption at rest is deliberately out of scope for now — see
sync-model.md §2.1 for why encrypting the tree would cost the one thing
plain text buys, and what to do instead in the meantime (a private remote
plus full-disk encryption on both machines).

## Do not point a file syncer at the database

**Never let Dropbox, Syncthing, iCloud, or any other file syncer touch
`stigmergy.sqlite3` directly**, and never `rsync` or `scp` it between
machines. The database journals in WAL mode over a single connection; a file
syncer copies the main file and its `-wal`/`-shm` companions at different
moments, and what lands on the other end is a database whose WAL does not
belong to it. This does not fail loudly. It presents as the claim guard
failing closed in a repository nobody was even touching, days later, far from
whatever actually broke it.

`stigmergy sync export`/`import` refuse outright if the directory they are
given contains anything that looks like a SQLite file — this is the one
mistake the tooling actively checks for, because it is also the one a
developer is most likely to make by hand.

---

## First-time setup

Run once per machine:

```sh
stigmergy doctor
```

Sync reuses the same project and global databases `doctor` already sets up,
and refuses to run against one it has not — see
[Troubleshooting](#troubleshooting).

Run once per project, per machine:

```sh
stigmergy sync enable
```

This proposes a name for the project — a short prefix of a fingerprint taken
over every root commit in its git history — and records both the name and the
full fingerprint. Running the same command on another clone of the same
repository proposes the same name, so in the ordinary case nothing has to be
typed twice. The name is what identifies the project in the exported tree; the
full fingerprint is what a mismatch would be detected against. Pass `--as <name>` to choose the
name explicitly — required for a shallow clone (a fingerprint cannot be
derived from one reliably) or a project spanning several repositories (a
fingerprint over one member's history would not describe the whole project).

A project's global scope needs no `enable` step; it always exists.

---

## The daily shape

Configure a private transport once on each machine:

```sh
stigmergy sync init --remote git@github.com:me/stigmergy-memories.git
```

Then run the complete pass whenever memories should move:

```sh
stigmergy sync
```

The command fetches, reconciles every enabled project and the global scope,
commits, and pushes. Its private working copy lives under
`$XDG_DATA_HOME/stigmergy/sync/` at mode 0700. The first run names the remote
and performs a dry-run warning on first use; re-run with `--confirm` because it receives plaintext memories. A remote
equal to any project's `origin` is refused. A rejected push is fetched,
reconciled, and retried once; sync never forces a push. `dir:/path` selects a
directory transport for an already-managed private directory.

`export` and `import` remain explicit primitives, useful when moving memories
without git:

```sh
stigmergy sync export /path/to/somewhere      # write this machine's state out
# move /path/to/somewhere to the other machine, however is convenient
stigmergy sync import /path/to/somewhere      # apply it there
```

Both commands cover the global scope and, when run from inside a
sync-enabled project, that project's scope in the same pass. Running `export`
again later — after `import` has been run on the other side and the
resulting directory brought back — reconciles against whatever is already in
the directory, so a memory the other machine has since changed is never
silently overwritten: that key is left for [`sync
resolve`](#conflicts) instead. Keeping one directory around and re-running
both commands against it, rather than exporting to a fresh directory each
time, is what makes each run cheap and its plan accurate.

`import` only ever writes to this machine's databases; it never modifies the
directory it reads. `export` only ever writes to the directory; it never
modifies this machine's databases beyond recording which digest was last
agreed on for each key. Running one without the other is a legitimate use —
`export` alone is "back up my memories to a USB stick"; `import` alone is
"bring in what somebody else already exported."

---

## Conflicts

Nothing is ever merged automatically. If both machines changed the same
memory since they last agreed, `sync import` stages it rather than writing
either side: the remote's version is saved separately, and the memory is
left exactly as it was locally. Every other key in the run still goes
through — one contested memory does not stop the rest.

```sh
stigmergy sync status
```

lists every staged conflict for the current scope, with both descriptions,
so it is clear at a glance what is contested and what it is about. Settle
one with:

```sh
stigmergy sync resolve <key> --mine      # keep the local body
stigmergy sync resolve <key> --theirs    # adopt the remote body
stigmergy sync resolve <key> --edit      # open $EDITOR on both, merge by hand
```

`--edit` writes both bodies into a temporary file with `<<<<<<< mine` /
`=======` / `>>>>>>> theirs` markers, opens `$EDITOR` (falling back to `vi`)
on it, and takes whatever is left once the markers are gone as the resolved
body. This is an offer a human confirms, not an automatic merge: conflict
markers are ordinary text and would otherwise end up stored as the memory's
own content, read by the next agent as instructions.

Resolving a conflict writes an ordinary version-bumped update, attributed to
`sync:<device-label>`. The next `sync export` picks it up like any other
change.

---

## Choosing what syncs

```sh
stigmergy sync share <key>      # opt a memory in (default scope: global)
stigmergy sync unshare <key>    # opt a memory out (default scope: project)
```

Both take `--scope project` or `--scope global` to override the default.
Since project memories sync unless excluded and global memories do not sync
unless included, `share` is mainly useful in the global scope and `unshare`
mainly in the project scope — but either works in both, for the times the
default is not what is wanted.

---

## Command reference

| Command | What it does |
|---|---|
| `stigmergy sync enable [--as <name>]` | Declare this project's sync identity, once per project per machine. |
| `stigmergy sync export <dir>` | Write this machine's syncable memories to `<dir>`, reconciling against whatever is already there. |
| `stigmergy sync import <dir>` | Apply `<dir>`'s content into this machine's databases. |
| `stigmergy sync status` | Show sync bookkeeping (never-exported count, tombstone count) and list staged conflicts, per scope. |
| `stigmergy sync share <key> [--scope]` | Opt one memory into sync. |
| `stigmergy sync unshare <key> [--scope]` | Opt one memory out of sync. |
| `stigmergy sync resolve <key> --mine\|--theirs\|--edit [--scope]` | Settle a staged conflict. |

None of this is reachable from an agent. Sync stays a command a person types:
a conflict needs a human to decide which of two beliefs survives, and an
agent that could push could push private memories to wherever the transport
points on a model's say-so alone. See sync-model.md §6.3 for the full
reasoning.

---

## Troubleshooting

**"the project/global database is at schema version N but this stigmergy
expects M — run `stigmergy doctor`."** Sync never migrates a database itself
(a v13 machine and a v14 machine can still exchange memories with each
other, because the wire format is defined in fields rather than tables — but
each machine's own database still has to match its own binary). Run
`stigmergy doctor` in the project, or `stigmergy doctor --all` to cover every
project this machine knows about, then retry.

**"no global stigmergy database found — run `stigmergy doctor` first."**
Nothing has adopted stigmergy on this machine yet. `stigmergy doctor` creates
the global database as a side effect of checking it.

**"... contains a database file ... stigmergy sync refuses to run against
it."** See [Do not point a file syncer at the database](#do-not-point-a-file-syncer-at-the-database).
Point sync at a plain directory instead.

**"project: not exported/imported — run `stigmergy sync enable` first."**
The current directory is inside an adopted project, but that project has
never declared a sync name. Run `stigmergy sync enable`.

**A record is skipped during import with a note about a missing reference.**
A link whose other endpoint does not exist locally is skipped and counted
rather than allowed to fail the whole run — see the summary line `sync
import` prints. Nothing else in the run is affected.

---

## What is not here yet

Some capabilities remain deferred to later stages:

- **Episodes, verifications, and evidence policies do not sync.** Only
  memories and links do, in this stage. The append-only history tables need
  a stable cross-machine identity of their own first (sync-model.md §4.3).
- **Encryption at rest on the transport** is deferred; see [What the
  exported tree exposes](#what-the-exported-tree-exposes).
- **No `stigmergy sync clone`.** A project that exists only on the other
  machine is not adopted automatically, and will not be even in a later
  stage without an explicit command for it.
