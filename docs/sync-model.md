# Cross-machine memory sync

How one developer's memories move between that developer's own machines, and
why the mechanism is shaped the way it is.

[architecture.md](architecture.md) §3 states the starting position as a
decision rather than an omission: project memories live in `.git/`, are never
committed or pushed, and *"sharing them across machines is a deliberate act,
not a side effect of `git push`."* This document specifies that deliberate act.
It does not walk the sentence back — it builds the act.

The constraint that shapes everything: **this is one developer's memories
moving between that developer's own machines.** A project can have several
developers, each with private memories, so the project repository is not a
channel. A design that commits memories into the project's tree, or pushes them
to `origin`, is refused up front and appears only in Appendix A.

[memory-model.md](memory-model.md) and [association-model.md](association-model.md)
bind this design: the FTS freeze on `memories`, fixed-width UTC timestamps
compared as TEXT, `type` as an enum nothing branches on, verification never
inferred from a write, and the doctrine that memories are state while episodes
are history. Nothing below relitigates them.

---

## 0. Decisions

Settled before implementation began, each with the alternative rejected and the
reason. A reversal costs a re-read of the named section, not a re-derivation of
the design.

| # | Decision | Rejected | Why |
|---|---|---|---|
| D1 | **Transport is a user-owned private git repository of text files**, with a plain directory as a second transport behind the same format (§2) | Syncthing/Dropbox as primary; a ref on a private remote; a self-hosted server; rsync | git is the one tool a developer with two machines already has on both, already authenticated. Divergence is git's competence, and its history is the diagnostic. The format is a directory, so the directory transport falls out for free. |
| D2 | **`version` never travels** (§3.1) | A Lamport counter or vector clock in `memories` | Redefining `version` redefines what every agent's `expected_version` means, and the FTS freeze forbids a new column on `memories`. Causality buys nothing when a real divergence needs a human anyway. |
| D3 | **Snapshot compare against a per-key base**, not an oplog (§3.10) | A per-device append-only log | No oplog exists; building one is a write-path change on every table. Log truncation is what kills the month-old-laptop case, which a snapshot handles for free. |
| D4 | **Nothing is auto-merged. Conflicts are staged and resolved by a human** (§3.5) | diff3 of memory bodies; last-writer-wins by timestamp | Conflict markers pass `ValidateBlock`, so an automatic text merge would store `<<<<<<<` inside a memory body and hand it to the next agent as instructions. Timestamp ordering decides content by comparing two machines' clocks. |
| D5 | **Deletes propagate via durable tombstones, retained forever** (§3.7) | No propagation; time-bounded tombstones | Without tombstones every delete returns on the next sync and the user learns delete does not work. A tombstone is a few dozen bytes and a laptop can be off for a year. |
| D6 | **Project scope syncs by default; global scope is opt-in per key** (§6.4) | One rule for both; deriving the rule from `type` | A project memory is about the repository, and the fingerprint has proved it is the same repository. A global memory may be about *this physical box* — the live global set holds two such memories and one portable one, spread across three different `type` values. |
| D7 | **Project identity is a declared name, proposed from a root-commit fingerprint** (§4.2) | Root-commit OID alone; remote URL; a bare label | A bare label lets a typo silently join two unrelated memory sets. The fingerprint makes that detectable and refusable while the human still types nothing in the common case. |
| D8 | **Sync never migrates anything, and the wire format is defined in fields, not tables** (§5, §7.1) | Gating the exchange on equal schema versions | A v13 machine and a v14 machine must still exchange memories. Gating would propagate architecture §5.9's lockout across machines. |
| D9 | **No daemon, no sync from a hook, no MCP tool** (§6.2, §6.3) | Background sync; a Stop-hook sync; an agent-callable tool | A daemon would be the first component here that can fail silently while appearing to work. Hooks run per edit under a 250ms budget and never spawn subprocesses. An agent that can push can push private memories on a model's say-so. |
| D10 | **Encryption at rest on the transport is deferred, and the exposure is documented instead** (§2.1) | Encrypting the tree now | Per-file encryption destroys the textual mergeability that is the whole argument for D1. If it ever happens it is a transport wrapper, never a format change. |
| D11 | **Settings live in SQLite `meta` plus a policy table** (§6.4) | A configuration file | No such concept exists in this project, and adding one means precedence rules, a parser, a `doctor` check and a second source of truth. |
| D12 | **This document is the design; `docs/sync.md` is the user guide** | One combined document | The rationale is long and the reader running `stigmergy sync` does not need it. The split matches `memory-model.md` against `usage.md`. |
| D13 | **Stage A ships first and alone**: format, merger, `export`/`import` against a directory | Shipping A+B together | Stage A is independently useful — export to a USB stick, import on the other machine — and it is testable with no network at all, which is what makes the merge rules provable. |
| D14 | **The hook ban lands on `internal/syncgit`, not on `internal/syncx`** (§6.2) | Banning `internal/syncx` from the hook path, as first drafted | `internal/store` returns syncx's plain types, and every hook opens a database, so the original ban was unsatisfiable. It was also aimed at the wrong thing: the hazard is a git subprocess and network I/O blocking on a credential prompt, not a struct. Stage B's transport therefore gets its own package, and the imports test bans that — exactly as it bans `internal/drift`. Taken while writing the test that would have failed. |
| D15 | **`sync enable` proposes a 12-character fingerprint prefix as the join name**, not the full 64-character hash (§4.2) | Using the whole fingerprint, as first drafted | The name is a directory in the sync tree and appears in every line of output; a full hash makes it unreadable for no gain. The prefix still derives identically on both machines, and the FULL fingerprint is what `project.json` records and what a mismatch is detected against. The name identifies; the fingerprint proves. Not derived from the directory name — the same repository is `~/work/stig` on one machine and `~/code/stigmergy` on the other. |
| D16 | **`sync status` reports both descriptions, both digests and the staged path — but prints no diff of its own** (§3.5) | Implementing a unified diff inside stigmergy, as §3.5 first specified | Two descriptions are usually identical while the bodies differ, so the digests are what actually name the dispute. The remote copy is already staged as a file and the operator has a better diff tool than any built here; `--edit` remains the guided path. A diff algorithm is code to maintain for a display concern. |

Two general biases, stated so they are visible rather than discovered: prefer
the option that fails visibly over the one that fails quietly, and prefer the
one a developer can inspect and repair by hand.

---

## 1. What is synced, and what is not

The rule that generates the tables below: **a row syncs when its meaning is
independent of the machine it is on.** A row about *this box*, *this checkout*,
*this session* or *this database* does not sync, and importing one is not merely
useless — several are actively destructive, because live predicates read them.

### 1.1 Project database (schema v13)

| Table | Synced | Reason |
|---|---|---|
| `memories` | **yes** | The point. CAS-versioned; §3 is how. |
| `memories_fts` | **never** | External-content FTS5 holding no text of its own; three triggers maintain it from `memories`. Copying a shadow table produces an index that disagrees with its content table, and a plain `DELETE` against it corrupts rather than updates. Every import goes through `memories` so the triggers fire. |
| `memory_links` | **yes** | Identity is `(key_a, key_b)`, machine-independent by construction. Merge is union (§3.6). |
| `memory_verification` | **yes**, Stage C | Append-only and never GC'd. Needs two new columns first: `memory_version` is a *local* counter, so an imported row cites a number that means nothing here (§3.6). |
| `memory_evidence_policy` / `_member` / `_path` | **yes**, Stage C, with a refusal | `base_oid` ports only when the receiver has the commit, and a missing object is already a defined outcome (`missing_base`). The blocker is the FK to `repos`: `repo_id` is a human-chosen name and *is* portable, the `repos` rows are not. The policy joins by name and is skipped and reported when no member of that name exists. |
| `episodes`, `episode_memory`, `episode_links` | **yes**, Stage C | Immutable and append-only, so the merge is trivial — once they have an identity that is not `AUTOINCREMENT` (§4.3). Interacts with GC (§3.8). |
| `roots` | **never** | `worktree` is a path on the writing machine. Worse, a root imported with a live-looking `last_seen_at` satisfies the active-claim predicate — a corpse that blocks edits and cannot be negotiated with. |
| `claims` | **never** | A live reservation on files on one machine, held by a root that does not exist on the other. Importing one puts a claim into force without the overlap check that grants one. |
| `mailbox_threads`, `mailbox_messages` | **never** | Mail is addressed to the living. Every `to_root` would name a root this database has never heard of. |
| `priming_delivered` | **never** | Dedup state for one session's Stop hook, FK'd to `roots`. |
| `caller_tickets` | **never** | Lives for the milliseconds between a hook and a tool call. |
| `repos` | **never as rows** | `common_dir` and `worktree` are `NOT NULL UNIQUE` paths *on this machine*. Only `repo_id` travels, as a join key. |
| `audit_log` | **never** | Actors are local root ids, and it is GC'd at 90 days independently on each machine, so an import resurrects what the other machine reclaimed (§3.8). |
| `meta` | **never as a whole** | `created_common_dir` is this machine's path. The `sync_*` keys this design adds are per-machine settings and must never be exported. |
| `schema_migrations` | **never** | This database's own history. Migration is `doctor`'s job, never sync's (§7.1). |

### 1.2 Global database (schema v3)

| Table | Synced | Reason |
|---|---|---|
| `memories` | **opt-in, per key** | The live data settles this rather than merely suggesting it. Of three global memories on this machine, `machine-navi31-hard-locks` is a fact about one physical box and `spongebob-restore-chain-hardlock-context` is a job running on it; only `adversarial-review-round-cap-is-not-a-rejection` is portable. §6.4 gives the mechanism; A.11 says why the rule is not derived from `type`. |
| `memory_links` | **yes**, when both endpoints are opted in | Otherwise the FK refuses the insert. The importer pre-resolves and skips (§7.9). |
| `known_projects` | **never** | Local database paths. |
| `audit_log`, `meta` | **never** | As above. |

### 1.3 The consequence nobody expects

`DeleteMemory` cascades: `memory_links`, `memory_evidence_*`,
`memory_verification`, `episode_memory` and `priming_delivered` all declare
`ON DELETE CASCADE`, and foreign keys are on at runtime. So deleting a memory
destroys its verification history — the one table migration 0009 promises GC
will never prune. That is existing behaviour, not something sync introduces,
but sync makes it visible across machines: a delete on one machine propagates
and takes the other machine's verification rows with it. Named here so it reads
as a known consequence rather than a sync bug.

---

## 2. Transport

Five candidates, judged on six axes. "Fails badly when misused" carries weight,
because the misuse is not hypothetical: the thing a developer will otherwise do
is point Dropbox at `stigmergy.sqlite3`, and A.1 says what that costs.

| | works offline | no server | survives three-way divergence | month-old laptop | easy | worst misuse |
|---|---|---|---|---|---|---|
| **A. Private git repo of text files** | yes | yes | yes, with history to diagnose it | trivial — full-state compare | already installed and authenticated | pushing to the wrong remote — detectable and refusable |
| **B. A ref on a non-origin remote** | yes | yes | yes | yes | one extra remote to explain | the ref reaches `origin` and a colleague fetches it |
| **C. A synced directory** | yes | depends | partly — no atomic multi-file commit; the syncer invents `.sync-conflict-` copies | yes | very | the user extends the sync to the `.sqlite3` |
| **D. Self-hosted server** | no | no | yes, authoritatively | yes | no | a service to patch that holds every private note |
| **E. SSH / rsync** | no | yes | no — last writer wins, silently | fails; divergence is what it destroys | one command | `rsync` of the database file |

**Chosen: A, with C supported behind the same format** (D1).

1. **git is the one tool both machines demonstrably have, already
   authenticated.** The hardest part of any sync — credentials and reachability
   — is already solved by whatever the user pushes code with.
   `stigmergy sync init --remote git@…:me/stigmergy-memories.git` is the entire
   setup, and there is no account to make.
2. **Divergence is git's actual competence**, and the history is the
   diagnostic: `git log -p` over one's own memories, which no other option
   offers.
3. **The format is a directory of files; git is one way to move a directory.**
   That decoupling is what makes the merger testable with no network, and what
   gets transport C for free as `--remote dir:/path/to/Syncthing/stigmergy`.

Three things the design does **not** delegate to git:

- **git never merges a memory body.** `stigmergy sync` fetches and reconciles
  against the fetched tree itself, then writes a commit with both parents. A
  textual three-way merge would insert `<<<<<<<` markers into a memory body —
  and those markers pass `ValidateBlock`, so they would be stored as memory
  content and handed to the next agent as instructions. See A.6.
- **git never runs the user's hooks or identity.** Every invocation carries
  `-c core.hooksPath=/dev/null -c user.name=stigmergy
  -c user.email=stigmergy@localhost`, following the `-c` precedent in
  `internal/explore`. A memories repository inheriting a work email address is
  a leak; a checked-out repository running hooks is a foothold.
- **`--force` never appears.** A rejected push means the other machine pushed;
  the answer is fetch, re-reconcile, retry once, then report.

**What would change the choice.** Two machines on one LAN that are never both
online, for a user already running Syncthing: C becomes the default and git the
optional history layer. Memories on a phone or in a browser: D, and then the
model changes, because a server can be authoritative and §3's three-way merge
collapses to a two-way one. Team sharing: also D — see §9.5.

### 2.1 What the transport exposes

Stated plainly, because these are private notes. The git host sees the **full
plaintext** of every synced memory, every description, every episode body,
every commit message, and the timing of every sync. A private repository on a
hosted service means the operator can read them; a bare repository on the
user's own machine means nobody else can. `stigmergy sync` cannot verify that a
remote is private and does not pretend to — it prints the remote and says what
it is about to send, on first use, and requires confirmation.

**Encryption at rest on the transport is out of scope for Stages A–C** (D10),
and the reason is a tension rather than laziness: per-file encryption destroys
the textual mergeability that is the entire argument for A, turning git into a
dumb blob store and losing the diff, the history and the conflict handling. The
recommendation is a private remote plus full-disk encryption on both machines;
a user who needs more can point transport C at an encrypted volume, which the
format supports unchanged. If this is revisited it is a wrapper (`age` over the
exported directory), never a change to the record format.

---

## 3. Merge semantics

### 3.1 `version` is a CAS token, and it stays one

`memories.version` is a local counter incremented by `WriteMemory`. It is not a
causality token and must not become one:

- Every agent's `expected_version` is this number, and `casCheck` is the single
  place the CAS matrix is decided. Redefining what the number counts redefines
  what an agent's retry means.
- A vector clock or per-device counter would want a column in `memories`, and
  the FTS freeze forbids one: the three triggers enumerate columns literally,
  and the search snippet addresses `body` by column index.
- The payoff — automatic causality — buys nothing, because a genuine divergence
  is resolved by a human either way (§3.5).

So **`version` never travels** (D2). The wire record carries the origin's
version as provenance for a reader; the importer never sets it. This is
enforced by the FTS law rather than by discipline: because every import goes
through the `memories` table so the triggers fire, and the write path computes
`next = cur.Version + 1`, an imported memory necessarily takes the local
counter's next value. The one constraint the design could not bend is the one
that keeps it honest.

A sync is therefore just another writer. An agent holding `expected_version=4`
from before a sync gets a `cas_conflict` after it, with the current entry
attached, and re-reads — the behaviour it already has for a concurrent agent,
reached by a different route.

### 3.2 Causality lives in a satellite table

Divergence detection needs a **base**: the content both sides last agreed on.

```sql
CREATE TABLE memory_sync_base (
  key       TEXT PRIMARY KEY REFERENCES memories(key) ON DELETE CASCADE,
  digest    TEXT NOT NULL,     -- sha256 over the synced fields; see 3.3
  device_id TEXT NOT NULL,
  at        TEXT NOT NULL
);
```

The `ON DELETE CASCADE` is deliberate: a locally deleted memory loses its base
row and gains a tombstone (§3.7), and the rule set is written for that pair of
states.

### 3.3 The digest

`syncx.Digest` is sha256, hex, over a canonical serialization of exactly the
synced fields: `key`, `type`, `description`, `body`. Not `version`, not
`updated_by`, not the timestamps. Two machines on which the user independently
typed the same correction must **converge, not conflict** — and they do,
because the digests match. `store.BodyHash` already establishes sha256-hex as
the hashing convention; this reuses its shape.

### 3.4 The rule set

For each key, with `base` (possibly absent), `mine` and `theirs`:

| base | mine | theirs | outcome |
|---|---|---|---|
| — | present | absent | **push** — this machine created it |
| — | absent | present | **pull** — adopt it |
| — | present | present, equal | **agree** — record the base, write nothing |
| — | present | present, differ | **conflict** — both created the same key independently |
| = mine | any | ≠ base | **pull** — only the remote moved |
| ≠ base | — | = base | **push** — only this machine moved |
| ≠ base | ≠ base, ≠ theirs | ≠ base | **conflict** — both moved |
| ≠ base | ≠ base, = theirs | ≠ base | **agree** — both moved the same way; record the new base |

A **pull** applies through `store.ImportMemory` (§3.9) with `ExpectedVersion`
set to the current local version, inside its own transaction. A CAS failure
there means an agent wrote while the sync ran; the record is reported as moved
and left for the next run. A **push** writes the record into the export tree.
An **agree** updates only `memory_sync_base`.

### 3.5 Conflicts, and where the human enters

**Nothing is auto-merged, ever** (D4). This inherits `internal/importer`'s two
rules verbatim: never modify the source, and never overwrite an existing memory,
*"because the alternative is silently destroying whichever version happened to
lose."*

A sync that only reports and never converges is a sync that stops working, so a
conflict is materialized and actionable rather than merely printed:

- Neither side is written. The key joins the run's conflict set, and the
  remote's version is staged at
  `$XDG_STATE_HOME/stigmergy/sync/conflicts/<scope>/<key>.theirs.md`.
- `stigmergy sync status` lists conflicts with both descriptions and a unified
  diff of the two bodies.
- `stigmergy sync resolve <key> --mine | --theirs | --edit` settles one.
  `--edit` opens `$EDITOR` on a diff3-style draft — an *offer*, confirmed by a
  human, which is a different thing from an automatic merge.
- A resolution is an ordinary CAS write with `updated_by = "sync:<device-label>"`,
  and the next run pushes it. The base advances to the resolved digest, so the
  conflict does not recur.
- Until it is resolved that one key is excluded; every other key syncs. A single
  contested memory must not wedge the mechanism.

Conflicts should be rare — one developer, two machines, usually working in
sequence. Rare and loud is the correct trade.

### 3.6 Append-only tables merge by identity

**Links.** Identity is `(key_a, key_b)`. Union. When both sides hold the pair
with different `reason`, the merger takes the earlier `created_at`, tie-broken
by device id, and reports the discarded reason in the run summary. Migration
0011 authorizes the cheapness: *"A link is cheap, attributed, and repairable by
unlink."* This is the only place a decision compares two machines' clocks, and
a wrong answer costs one prose string on one edge (§7.4).

Link deletes get tombstones in the same mechanism. `DeleteMemory` does **not**
emit them for cascade-severed links — it already reads `severedNeighborKeysTx`
before the delete, so it knows what the cascade will take, and the memory's own
tombstone covers them.

**Verifications.** Union by content digest, after two ADD COLUMNs (§8.3):

- `origin_device` — whose counter `memory_version` refers to. Without it an
  imported row asserts a version number in a namespace it does not belong to,
  which is worse than no row.
- `memory_digest` — the content actually assessed. This is what makes the row
  interpretable after a merge has renumbered versions on both sides, and it is
  a strictly better identity than the version number ever was. NULL on existing
  rows reads as *"recorded when there was only one counter"*.

**Episodes.** Union by uid (§4.3), with `episode_memory` and `episode_links`
remapped through the uid→local-id table. Immutable, so there is no update case.
Subject to the GC horizon (§3.8).

### 3.7 Deletes, and the resurrection loop

Without tombstones a delete on machine A is indistinguishable from a create on
machine B, and the next sync resurrects what the user deleted. So **deletes
propagate, via durable tombstones** (D5).

```sql
CREATE TABLE sync_tombstone (
  kind      TEXT NOT NULL CHECK (kind IN ('memory','link')),
  ident     TEXT NOT NULL,     -- the key, or "<key_a> <key_b>"; no space is legal in a key
  digest    TEXT NOT NULL,
  device_id TEXT NOT NULL,
  at        TEXT NOT NULL,
  PRIMARY KEY (kind, ident)
);
```

No foreign key, by construction: a tombstone exists precisely because the row
does not. `DeleteMemory` and `DeleteLink` write one in the same transaction as
the delete — the only change this design makes to an existing write path.

- A tombstone beats an **unchanged** remote: the digests match, so the delete
  propagates.
- A tombstone loses to a remote that **changed after** the tombstoned digest.
  That is delete/edit divergence, surfaced as a conflict, default **keep**.
  Never silently destroy beats converge.
- A machine that sees a tombstone records it, so it can never re-emit the
  memory. That closes the loop.
- **A key that is written again has its tombstone cleared**, in the same
  transaction as the write. "Retained forever" governs how long a tombstone
  survives the passage of time, not whether it survives its subject coming
  back: a tombstone naming a live memory asserts something false. This is not
  housekeeping, and it was found the hard way — the first implementation
  omitted it. Because the tombstone rules above run BEFORE the base comparison
  in §3.4, a stale tombstone shadows its own memory in both directions, and
  says nothing either time. Against a machine still holding the old content the
  plan reads delete-remote, destroying that machine's copy of a memory this one
  has re-created. Against a machine that has never seen the key the plan reads
  "nothing to do", so the re-created memory never syncs, on any run, ever.
  Every write path clears, unconditionally rather than only on create, so an
  ordinary write repairs a database that somehow acquired one. The merger
  defends the same invariant from the other side: a live record beats a
  same-side tombstone, for the hand-repaired database this rule cannot reach.
- **Retention: forever.** A tombstone is a few dozen bytes and a laptop can be
  off for a year; `memory_verification`'s *"NOT PRUNED BY GC, deliberately"* is
  the precedent. `doctor --gc` reports the count and prunes nothing.

**The first sync of a device is the dangerous one**, because it is the only run
where a tombstone can delete something the local user has never seen deleted.
So the first run against a remote is dry-run by default, prints every delete it
intends, and requires confirmation.

### 3.8 GC must never be undone by an import

`internal/store/gc.go` reclaims audit rows at 90 days, resolved mail at 30, and
episodes at 180 *when they ground no memory and sit in no chain*. Audit and mail
do not sync, so only episodes are exposed. The law:

> **The importer never inserts a row that its own GC would immediately reclaim.**

For episodes that is the GC predicate applied at the door: an inbound episode
older than `now - EpisodeRetention` that arrives grounding nothing and chained
to nothing is skipped and counted. An old episode that *does* ground a memory is
imported, because the local GC would keep it too.

### 3.9 Timestamps

`WriteMemory` stamps `Now()` unconditionally. Using it for imports would make
every imported memory's `updated_at` the import time, destroying the
last-mutation semantics that `memory_list order_by:"recent"` sorts on — a freshly
synced machine would show every memory as identically new.

So Stage A adds a sibling:

```go
type MemoryImport struct {
    Key, Type, Description, Body string
    CreatedAt, UpdatedAt         string // from the wire, canonicalised on the way in
    UpdatedBy                    string // "sync:<device-label>"
    ExpectedVersion              *int
}

func (d *DB) ImportMemory(m MemoryImport, agentKind string) (*WriteResult, error)
```

It shares `casCheck` — architecture §5.5 permits exactly one place to decide
CAS and this is not a second one — writes through the `memories` table so the
three triggers fire, and runs both stamps through `CanonicalStamp` on the way
in. Architecture §5.4 already anticipates this caller by name.

One consequence, stated rather than hidden: `updated_at` no longer moves
monotonically with `version` on a machine that has imported. Nothing compares
them, and `updated_at` is documented as last-mutation-anywhere rather than
last-mutation-here.

### 3.10 Snapshot, not an oplog

A per-device append-only oplog is the textbook answer and is wrong here (D3):

- **There is no oplog and building one is a write-path change on every table.**
  `audit_log` looks like one and is not — pruned at 90 days, free-text `detail`
  — and association-model A.10 already rejected deriving structure from it.
- **The month-old laptop is what kills log-based sync** (truncation, missing
  segments, compaction) and costs a snapshot compare nothing.
- **There is nothing to optimize.** The live databases hold 24 project memories
  and 3 global ones. A full compare is microseconds.
- **The intermediate states an oplog preserves are not wanted.** Memories are
  *"the state of the system, not its history"*, and the history layer already
  exists and is already append-only: episodes.

---

## 4. Identity

### 4.1 Device

Minted once per machine, stored in the global database's `meta` as
`sync_device_id`: `"d-"` plus 8 random bytes, from a new `ids.NewDeviceID()`
beside `NewRootID` and `NewProjectID`, with the same comment — random, not a
secret, uniqueness only. A human-editable `sync_device_label` (defaulting to the
hostname) is what appears in `updated_by` and in output.

Not derived from hostname or MAC: hostnames collide and change, and a derivation
makes two machines cloned from one disk image indistinguishable. The id lives in
the database, so a *restored backup* legitimately carries the same identity —
right for the common case, and handled as a rollback in §7.5.

### 4.2 Project

Nothing today identifies "the same project on another machine".
`ids.NewProjectID()` is random per machine and deliberately not path-derived.

| Candidate | Fails when |
|---|---|
| Root-commit OID | shallow clone (`rev-list --max-parents=0` returns the shallow boundary, silently wrong); rewritten history; several root commits from subtree merges or orphan branches; two machines that each ran `git init` |
| Remote URL | no remote; several remotes; ssh and https spelling one repo two ways; a fork; a host rename |
| Declared label | a typo silently forks the sync — or worse, silently joins two unrelated projects |

**Chosen: a declared name, proposed from a root-commit fingerprint, confirmed
once per machine** (D7). `stigmergy sync enable` hashes every root commit,
sorted, and proposes the result as the join name; the human accepts or supplies
`--as <name>`. In practice the human types nothing and both machines land on the
same name.

The fingerprint then does what a name alone cannot: it is recorded in the
transport, and a machine whose fingerprint disagrees with the remote's record
for that name is **refused**, not merged, unless the human passes
`--force-adopt`. That catches the typo a bare label cannot detect and cannot
undo.

Two refusals fall out:

- **A shallow clone cannot derive a fingerprint.** Detect it at
  `CommonDir/shallow` — a `.git` is a *file* in a linked worktree, which is why
  `internal/drift` already checks the common dir — and require an explicit
  `--as`.
- **A multi-repository project must declare its name.** `project.ID` is random
  per machine, and "which repositories are one project" is a human construct
  there by definition. Architecture §3's *"membership is stated, never
  inferred"* is the same rule one level up.

### 4.3 Episode

`episodes.id` is `AUTOINCREMENT`, referenced by `episode_memory` and
`episode_links`, **and** it is `episodes_fts`'s `content_rowid`. It cannot be
renumbered and the table must not be rebuilt.

So: `ALTER TABLE episodes ADD COLUMN uid TEXT`, plus
`CREATE UNIQUE INDEX idx_episodes_uid ON episodes(uid) WHERE uid IS NOT NULL`.
ADD COLUMN leaves the table, its indexes and its triggers alone — *"prefer a
column to a rebuild"* is this directory's standing lesson — and the
`episodes_fts` triggers name `title` and `body` literally, so a new column is
invisible to them. The partial index lets pre-migration rows keep a NULL uid
without colliding; `doctor` backfills them, which is cheap (this repository's
project database holds zero episodes today).

`RecordEpisode` mints the uid in the same transaction as the insert. The
importer keeps a uid→local-id map for the run and remaps both child tables
through it.

A content digest over `(title, body, actor, at)` was considered instead; a
minted uid is honest, cannot collide between two genuinely distinct episodes,
and does not make dedup depend on byte-identity of prose.

### 4.4 Attribution

`memories.updated_by` holds a machine-local root id in practice, and sometimes a
free string (`importer`, `phase-9-docs`). There is no foreign key, so an
imported root id would not error; it would simply be a lie, naming a session
that never existed here and that an agent could try to `mailbox_send` to.

So **root ids do not travel.** An imported memory gets
`updated_by = "sync:<device-label>"`, following `importer` as the established
non-root value. The original `updated_by`, the origin device and the origin
version ride in the wire record as provenance a human can read, and the audit
row records them.

---

## 5. Wire format

A directory tree of files, one per record. Human-readable, diffable, and
textually mergeable where merging is safe.

```
FORMAT                                   -- "stigmergy-sync 1", one line
devices/<device-id>.json                 -- label, seq, last push, schema versions written
global/
  memories/<key>.md
  links.jsonl
  tombstones.jsonl
projects/<name>/
  project.json                           -- fingerprint, project schema version
  memories/<key>.md
  links.jsonl
  verifications.jsonl
  episodes/<uid>.md
  episode-links.jsonl
  evidence.jsonl
  tombstones.jsonl
```

**A memory is markdown with YAML frontmatter**, because `internal/importer`
already parses exactly that shape — the format is not new to this project, a
human can open one in an editor, and a `git diff` of a memory is a diff of
prose, which is the whole reason to prefer text over a blob.

```markdown
---
key: shipping-a-migration-locks-the-repo
type: project
description: A new migration blocks every adopted project until doctor runs.
digest: sha256:1f0c…
created_at: 2026-07-21T09:14:02.113004212Z
updated_at: 2026-08-02T15:00:06.547664879Z
origin_device: d-3f2a9c81b4de7a05
origin_updated_by: r-e226af35bd46
origin_version: 6
---
The hook that guards edits refuses to run against a database whose schema…
```

**The append-only sets are JSONL**, one record per line, sorted
deterministically (links by `key_a,key_b`; verifications by `at,digest`; episode
links by uid pair). They are records, not prose, and one line per record is what
lets git merge two machines' disjoint additions cleanly at the text level — even
though the merger reconciles them semantically afterwards, a transport that
conflicts on every sync is a transport nobody uses.

**Format version.** The `FORMAT` file carries one integer, and the refusal is
asymmetric: a reader finding a **higher** version refuses the entire run and
names the stigmergy to install. A lower version is read and rewritten at the
current version. Record files carry no version of their own, which keeps diffs
clean and puts the refusal in one place.

**Schema version is separate from format version, and that separation is the
point** (D8). `project.json` records the project schema version the writer was
at, but the receiver does not gate on equality — the wire format is defined in
terms of *fields*, not tables, so a v13 machine and a v14 machine exchange
memories without either migrating. This is what stops architecture §5.9's
footgun from travelling: sync never applies a migration and never asks another
machine to.

The corollary is a hard requirement on the writer: **unknown fields are
preserved, never dropped.** An older binary rewriting a record written by a
newer one must round-trip fields it does not understand, or one sync from an old
laptop silently strips the new machine's data.

**Nothing in the tree is a SQLite file.** The exporter never writes one, and
`stigmergy sync` refuses to run against a tree containing one — because somebody
will copy `stigmergy.sqlite3` into the sync folder, and A.1 says what that costs.

---

## 6. CLI surface

### 6.1 What a developer types

```
stigmergy sync init --remote git@github.com:me/stigmergy-memories.git   # once per machine
stigmergy sync enable [--as <name>]                                     # once per project, per machine
stigmergy sync                                                          # everything, both scopes
```

After that, `stigmergy sync` with no arguments is the whole interface: fetch,
reconcile every enabled project and the global scope, commit, push, print a
summary. It mirrors `doctor --all` — one command reaching every project the
machine knows about — and reuses `known_projects` to find them.

```
stigmergy sync --dry-run              # print the plan, touch nothing
stigmergy sync status                 # per scope: pending, conflicts, last run
stigmergy sync resolve <key> --mine|--theirs|--edit
stigmergy sync share <key>            # opt a global memory in
stigmergy sync unshare <key>
stigmergy sync export <dir>           # the primitives; git is a wrapper over these
stigmergy sync import <dir>
stigmergy sync clone <name>           # adopt a project that exists only on the other machine (Stage C)
```

### 6.2 Manual, hook-driven, or a daemon

**No daemon.** `internal/cli/watch.go` is not one — it is a read-only viewer
with a `--follow` redraw loop whose comment says it *"never writes"*.
Architecture §5.1 is proud of the absence: *"Nothing runs in the background, so
nothing can fail to run in the background."* A sync daemon would hold
credentials, do network I/O, and be the first thing here that can fail silently
while appearing to work.

**No sync from a hook.** Hooks run as a fresh process on every edit under a
250ms lock budget, never spawn subprocesses (`TestResolveNeverSpawnsASubprocess`
and `TestHooksDoNotReachTheDriftPackage` pin it), and open the database
read-only or `NoMigrate`. A sync spawns git, does network I/O, and can block
indefinitely on an SSH passphrase prompt — inside an agent's turn.

**One cheap thing a hook may do**, in Stage C: the Stop path may *notice* that a
project has unsynced changes and add one line to the note it is already
composing — a single indexed count over `memory_sync_base`, no network, no
subprocess. That is the mail-gate pattern, and it is what makes "near-zero
ceremony" true without a daemon.

The import boundary that enforces the rest is on the **transport**, not on the
model (D14). `internal/syncx` is plain data and a pure merger, and
`internal/store` returns its types, so every hook reaches it transitively the
moment it opens a database — banning it would be banning a struct. The git
subprocess wrapper lives in `internal/syncgit` instead, and
`TestHooksDoNotReachTheSyncTransport` bans *that*, for the same reason
`TestHooksDoNotReachTheDriftPackage` bans `internal/drift`: a subprocess that
can block on a credential prompt has no business on a path with a 250ms lock
budget.

**stigmergy ships no scheduler.** A user who wants a timer has cron, a systemd
timer or a shell alias, and the documentation says so rather than growing a
third answer.

### 6.3 No MCP tool

Sync stays a human CLI (D9):

1. It is a machine-administration act involving network credentials, and the
   agent-facing surface is deliberately memory, claims and mail.
2. A conflict needs a human. An agent resolving one would be silently choosing
   which of the user's two beliefs survives — the act `internal/importer`
   refuses on the user's behalf.
3. An agent that can push can push private memories to a remote on a model's
   say-so. stigmergy is not a security boundary, but it need not hand out the
   trigger.
4. Hosts restart the MCP server mid-session, and a multi-second network
   operation inside a tool call has no cancellation story here.

The one thing agents would genuinely benefit from is knowing memories may be
stale. That is a read, not an action, and it is deferred to §9.8.

### 6.4 Where settings live

**No configuration file** (D11). Settings live where settings already live:
`known_projects` is the precedent for machine-wide registry state, and
`SetMeta`/`Meta` for project-level facts.

- **Global `meta`**: `sync_device_id`, `sync_device_label`, `sync_remote`,
  `sync_seq`.
- **Project `meta`**: `sync_project`, `sync_fingerprint`.
- **Env overrides**, for CI and tests only: `STIGMERGY_SYNC_REMOTE`,
  `STIGMERGY_SYNC_HOME`.

One trap: `SetMeta` runs `NormalizeText` and `ValidateLine(value,
MaxLineLength)`, so a value must be one line under 500 characters. A remote URL
is comfortably both, but nothing structured may be stuffed in there. A URL with
an embedded token would sit in plaintext in the global database — the
documentation says to use an SSH key or git's credential helper, which is also
what makes the no-server story work.

Per-key sync policy needs a table, in both scopes:

```sql
CREATE TABLE sync_policy (
  key  TEXT PRIMARY KEY REFERENCES memories(key) ON DELETE CASCADE,
  mode TEXT NOT NULL CHECK (mode IN ('include','exclude')),
  at   TEXT NOT NULL
);
```

Same DDL, opposite defaults, and the asymmetry is the design (D6):

- **Project scope: everything syncs unless excluded.** A project memory is about
  the repository, and the fingerprint has proved it is the same repository.
- **Global scope: nothing syncs unless included.** A global memory is about
  *this machine* or *this person*, and only the human knows which.

---

## 7. Failure modes and refusals

**7.1 Schema-version skew.** Two hazards. Locally, `stigmergy sync` refuses to
run against a project whose schema version differs from the binary's and says
`run stigmergy doctor` — the failure the hook already reports, reached from a
command that can afford to explain it. Across machines, the format/schema
separation in §5 handles it. **Sync never migrates anything**, and the refusal
never suggests that it could.

**7.2 A half-finished run.** Each record is its own transaction, so a crash
leaves a prefix applied and a re-run is idempotent because the plan is
recomputed from digests. A single long transaction was rejected: with
`SetMaxOpenConns(1)` and `_txlock=immediate`, holding the write lock across a
whole sync blocks every agent in the project. Remotely the commit is atomic; a
crash before it leaves the remote untouched, and after it leaves a local commit
the next run pushes.

**7.3 An interrupted push.** A rejected push means the other machine pushed
first: fetch, re-reconcile, retry once, then report and stop. Never `--force`,
and never `--force-with-lease` either — the second is safe against a race but
not against the other machine having pushed something the human wanted.

**7.4 Clock skew.** The law: **nothing in the merge is decided by comparing two
machines' clocks.** Divergence is decided by digests against a base, deletes by
digests against a tombstone, episode import by a local horizon against a stored
`at`. The single exception is the link reason tie-break (§3.6), costing one
prose string on one edge. A machine whose clock is badly wrong may also skip
importing recent episodes at the GC horizon; that is reported, not silent.

**7.5 A device restored from an old backup.** Its base table and its memories
are both old, so every key reads as locally unchanged and fast-forwards to the
remote — correct — and its stale tombstones are corrected by the remote's. The
dangerous case is the restored device **pushing a rollback**. Detect it:
`devices/<id>.json` carries a monotone `seq`, and a device pushing a `seq` at or
below the remote's record for itself is refused with an explanation naming both
possibilities — a restored backup, or two machines cloned from one image. The
escapes are `stigmergy sync --accept-rollback` and
`stigmergy sync adopt-identity --new`, the second being right for the clone.

**7.6 A project that exists on one machine only.** Do not auto-create. Creating
a project database means choosing where it lives and wiring the hosts, which is
`init`'s job. `sync` lists the unknown projects and stops; `stigmergy sync clone
<name>` is the explicit act. This is `checkJoinable`'s doctrine applied to sync.

**7.7 The remote is the project's origin.** Refuse at `sync init` and again at
every run: the configured remote is compared against every remote of every
adopted repository. This is off the hook path, so shelling out to `git remote`
is fine.

**7.8 Agents writing while sync runs.** Sync is another writer and CAS covers
it. Each record's import re-checks CAS inside its own transaction; a key that
moved is reported and picked up next run.

**7.9 A record whose references are absent.** `memory_links` FKs both endpoints,
`memory_evidence_member` FKs `repos`, `episode_memory` FKs `memories`. With
runtime foreign keys on, an unresolved reference is a driver error mid-run — the
worst way to learn about it. The law: **the importer resolves every reference
before it writes, and a record whose references are absent is skipped and
reported, never allowed to reach a foreign-key error.**

**7.10 Text an older binary would refuse.** Inbound text goes through the same
`NormalizeText`/`ValidateBlock`/`ValidateLine` rules as anything else — free,
because `ImportMemory` shares `WriteMemory`'s validation — and a record that
fails is reported per record rather than aborting the run. `doctor` already
reports unreadable rows and repairs nothing; sync does not become a second
repair path.

**7.11 No git, no network.** `export`/`import` against a plain directory are the
primitives and always work. This is also why the merger is testable with no
network at all.

**7.12 A transport tree containing a database file.** Refuse, and say why (A.1).

---

## 8. Staged implementation

### Stage A — the format and the merger, no transport

Independently shippable: a developer can `export` to a USB stick and `import` on
the other machine. That is the feature; Stage B is the convenience.

**New package `internal/syncx`** — named to avoid shadowing the standard
library's `sync`, which is imported widely enough that a package named `sync`
here would force an alias at every call site and read as a mistake. It owns the
record types, the canonical serializer, `Digest`, and the merger. **No git, no
network, no `internal/store` write calls** — the merger takes two record sets
and a base and returns a plan, which is what makes §3.4 testable as pure
functions, the way `internal/claims` is.

**Migrations** — `project/0014_sync.sql`, `global/0004_sync.sql`:
`memory_sync_base`, `link_sync_base`, `sync_tombstone`, `sync_policy`. Each with
the comment style of 0008 and 0011, stating why there is no FK on the tombstone
table and why the two scopes' `sync_policy` defaults differ.

| File | Change |
|---|---|
| `internal/syncx/*.go` | new: record types, serializer, digest, merger, plan |
| `internal/store/sync.go` | new: base/tombstone/policy DAO, `ImportMemory` |
| `internal/store/memories.go` | `DeleteMemory` writes a memory tombstone in its transaction |
| `internal/store/links.go` | `DeleteLink` writes a link tombstone; `DeleteMemory` suppresses them for cascade-severed links |
| `internal/ids/ids.go` | `NewDeviceID` |
| `internal/cli/sync.go` | new: `sync export\|import\|status\|enable\|share\|unshare\|resolve` |
| `internal/cli/root.go` | one line |
| `docs/sync.md` | new: the user guide |
| `docs/architecture.md` | §3 gains a pointer; the "deliberate act" sentence stands and now names the act |
| `docs/TODO.md` | the cross-machine entry goes; what remains deferred is stated |

**Tests** (written by the session root, not the implementer):

- `internal/syncx/merge_test.go` — the full §3.4 matrix as a table, plus
  delete/edit divergence both ways, plus tombstone-vs-unchanged.
- `internal/store/sync_test.go` — a delete leaves a tombstone and removes the
  base row; `ImportMemory` preserves both stamps through `CanonicalStamp`; **an
  imported memory is findable by `memory_search` and the snippet still
  highlights `body`**, which is the FTS-trigger proof; `ImportMemory` shares
  `casCheck`.
- Round-trip: export → import into an empty database → export produces
  byte-identical files.
- Unknown-field round-trip: a record carrying a field this binary does not know
  survives a rewrite.
- `internal/store/migrate_test.go` — undo statements for project 0014 and global
  0004 in `TestRestoringIndexesOnADatabaseThatAlreadyLostThem`. This is the cost
  that test charges each migration, and it is the point.

### Stage B — git and directory transports

`internal/syncx/transport.go`, `internal/syncgit/git.go` (a subprocess wrapper in
the shape of `internal/gitx`'s fallback, carrying the same "never on the hook
path" warning), `internal/cli/sync.go`, `docs/sync.md`, `docs/usage.md`,
`docs/operations.md`, `README.md`. No migration.

Adds `sync init --remote`, the bare `stigmergy sync`, the sync working copy
under `$XDG_DATA_HOME/stigmergy/sync/` at mode 0700, rollback detection,
push-reject retry, the origin refusal, the first-run confirmation, and the
`dir:` transport.

**Tests** — against real local bare repositories in `t.TempDir()`, with real
commits and no `exec` mocking, matching how `internal/gitx` and `internal/drift`
already test git:

- Two clones diverge on one key and each machine wins once; a third case where
  both moved reports a conflict without writing.
- A non-fast-forward push is retried once and succeeds.
- A device that has not synced for a simulated month converges in one run.
- A rollback push is refused and names both explanations.
- The remote-is-origin check refuses.
- No invocation of git omits `core.hooksPath` or the identity overrides.
- The transport interface is exercised identically by `dir:` and `git:`.

### Stage C — episodes, verifications, evidence, and the nudge

**Migration `project/0015_sync_identity.sql`**: `episodes.uid` plus the partial
unique index; `memory_verification.origin_device`; `memory_verification.memory_digest`.
Three ADD COLUMNs and one index — no rebuild, and the file says why. `doctor`
backfills episode uids.

`internal/store/episodes.go` (mint the uid in `RecordEpisode`),
`internal/store/verification.go` (record origin and digest),
`internal/store/evidence.go` (import by repo name, refuse when absent),
`internal/syncx/*`, `internal/cli/doctor.go` (uid backfill), the Stop path (the
one-line unsynced nudge), `internal/cli/sync.go` (`sync clone`).

**Tests**

- Episode uid remap: `episode_memory` and `episode_links` land on the right
  local ids; a re-import is a no-op.
- The GC-horizon law: an old, ungrounded, unchained episode is skipped and
  counted; an old but *cited* one is imported.
- An evidence policy naming an absent repo is skipped and reported, and **no
  foreign-key error is ever produced** — asserting `PRAGMA foreign_keys` is ON
  in the test first, because the evidence work's own lesson is that a
  consistency check does not prove enforcement.
- An imported verification row carries `origin_device` and `memory_digest`, and
  `VerificationHistory` renders it without claiming its `memory_version` is
  local.
- `internal/hooks` does not import `internal/syncx` — a transitive-import test in
  the shape of `TestHooksDoNotReachTheDriftPackage`.
- The nudge is emitted once per session and costs one query.

### Live verification

The project's standing distinction applies. **Harness-confirmed** is two
databases in `t.TempDir()` and a local bare repository. **Confirmed** is two real
machines, a real remote and a real week of use. The list to settle live:

1. A memory written on machine A appears on B, keeps its `created_at`, and is
   findable by `memory_search` on B.
2. A memory edited on both machines produces a conflict, and neither body is
   touched until `sync resolve`.
3. A delete on A removes it on B, and B does not re-emit it on the next run.
4. A laptop left off for a month converges in one run with no manual step.
5. Machine A on a newer project schema and machine B on an older one exchange
   memories, and neither is blocked.
6. No adopted project's claim guard ever fails closed as a result of a sync.

---

## 9. Open questions, and how each was settled

1. **Do project memories default to sync-all?** **Yes** (D6) — the fingerprint
   has proved it is the same repository, and an opt-in default means the feature
   quietly does nothing for weeks.
2. **Tombstone retention.** **Forever** (D5), on `memory_verification`'s
   precedent. Pruning once every known device has acknowledged needs
   acknowledgement state and buys back kilobytes.
3. **Encryption at rest on the transport.** **Deferred explicitly** (D10), with
   §2.1's reasoning written into the user documentation rather than left
   implicit.
4. **Do episodes sync at all?** **Yes, in Stage C.** They are the history layer
   and are half-useless if each machine remembers a different half. They are
   also the largest volume and the only GC-pruned thing carried, which is why
   they are last and why §3.8 exists.
5. **Team-shared memories, later?** A different problem with a different shape,
   and this design does not become it by relaxing a flag. Team sharing needs an
   authoritative store, an identity per person rather than per device, a review
   step before a memory becomes everyone's, and a rule for what happens when one
   person's private memory contradicts the team's. The one thing this design
   hands a future team feature is the wire format: a directory of readable
   records with a version, which is what a server would ingest. Keep the format
   free of anything device-shaped for that reason, and no more than that.
6. **Should `sync` create a project that exists only on the other machine?**
   **Yes, as an explicit `sync clone`** (§7.6), never as a side effect of a run.
7. **Multi-repo fingerprint — declared name, or an anchor member?**
   **Declared.** An anchor member makes one repository special, and architecture
   §3 says *"No member is special"* for a reason.
8. **Should `context_open` tell an agent when the project last synced?** **Not
   in Stage A.** It is one more line in a response agents already read past, and
   the value is unproven. Revisit once a real stale-memory incident exists to
   point at.

---

## Appendix A — Rejected alternatives

Kept because each prevents a specific, likely regression.

**A.1 Syncing the SQLite file itself** (Dropbox, Syncthing, iCloud, rsync of
`stigmergy.sqlite3`). This is what a user will otherwise try, so it is refused
loudly and in the documentation. Journalling is WAL and the pool is
`SetMaxOpenConns(1)`; a file syncer copies the main database and the `-wal` and
`-shm` files at different moments and cannot copy them atomically, so what lands
is a database with a WAL that does not belong to it. Architecture §3 already
restricts stigmergy to local filesystems because *"SQLite locking over NFS or a
network share is not trustworthy, and the whole enforcement story rests on that
locking."* And the failure does not present as a sync failure: it presents as
the claim guard failing closed in a repository the user is not standing in.

**A.2 Committing memories into the project repository.** A project can have
several developers with private memories. Also refused by architecture §3, which
puts the database inside `.git/` precisely so it is never committed.

**A.3 A ref (`refs/stigmergy/*`) pushed to `origin`.** Same exposure as A.2 by
another route: a colleague's `git fetch --all`, a `git push --mirror`, a fork, a
CI checkout with `fetch-depth: 0`. The privacy of a ref rests on nobody looking,
which is not a property.

**A.4 `version` as a Lamport counter or a vector clock.** It would redefine the
token `casCheck` and every agent's `expected_version` rest on; a vector clock
wants a column in `memories`, which the FTS freeze forbids; and its payoff is
automatic causality, which buys nothing when a real divergence needs a human.

**A.5 A per-device append-only oplog.** No oplog exists, and building one is a
write-path change on every table. `audit_log` is not one — pruned at 90 days,
free-text `detail` — and association-model A.10 already rejected deriving
structure from it. A snapshot compare over dozens of rows costs nothing and
survives the month-old laptop that log truncation destroys.

**A.6 Three-way text merge (diff3) of memory bodies, or letting git merge
them.** Conflict markers pass `ValidateBlock` — valid UTF-8, real newlines, no
control characters — so they would be stored as memory content and read by
agents as instructions. And a successful automatic merge is indistinguishable
from a correct one. A diff3 *draft offered to a human in `$EDITOR`* is a
different thing, and is what §3.5 does.

**A.7 Last-writer-wins by `updated_at`.** Decides content by comparing two
machines' clocks. Architecture §5.4's timestamp doctrine exists because a wrong
time comparison produces *"claims that expire at subtly wrong moments, which you
would probably never notice and never be able to reproduce"* — the same failure
shape, applied to which of two memories survives.

**A.8 Deletes that do not propagate.** Simpler, and wrong in a way that
compounds: every deleted memory returns on the next sync, so the user learns
that delete does not work and stops using it.

**A.9 Renumbering episode ids on import.** `episodes.id` is `episodes_fts`'s
`content_rowid` and the target of two foreign keys. Renumbering means rebuilding
the table, which this directory's history says takes the indexes with it (0003,
repaired by 0004).

**A.10 A `synced` boolean column on `memories`.** The FTS freeze: satellite
tables only. It would also store a per-remote fact in a table that knows nothing
about remotes.

**A.11 Choosing which global memories sync from `type` or from scope.**
memory-model §8: `type` is *"an enum nothing branches on"*. The live global set
refutes it again — `machine-navi31-hard-locks` is `reference`,
`spongebob-restore-chain-hardlock-context` is `project`, and neither should
travel, while `adversarial-review-round-cap-is-not-a-rejection` is `feedback` and
should. Per-key opt-in asks the only actor who knows.

**A.12 A daemon.** Architecture §5.1: lazy expiry exists so that *"nothing runs
in the background, so nothing can fail to run in the background."*

**A.13 Syncing from a hook.** Hooks run per edit under a 250ms lock budget,
never spawn subprocesses, and open the database read-only or `NoMigrate`. A sync
spawns git, does network I/O, and can block on a credential prompt inside an
agent's turn.

**A.14 An MCP `sync` tool.** §6.3. The short form: a conflict needs a human, and
an agent that can push can push private memories on a model's say-so.

**A.15 A configuration file.** No such concept exists here, and adding one means
precedence rules, a parser, a `doctor` check and a second source of truth.

**A.16 Deriving project identity from the remote URL.** A repository may have no
remote, several, or a rewritten one; ssh and https spell the same repository two
ways; a fork spells it a third.

**A.17 Deriving device identity from hostname or MAC address.** Hostnames
collide and change, and a derivation makes two machines cloned from one image
indistinguishable — which is exactly the case §7.5 has to detect.

**A.18 Syncing `audit_log`.** Actors are local root ids, and the log is pruned
at 90 days independently on each machine, so an import resurrects precisely what
the other machine reclaimed.

**A.19 rsync or `scp` between machines.** Requires both machines up at once, has
no merge at all, and destroys a month of divergence silently. Its only virtue —
one command — the chosen design also has.
