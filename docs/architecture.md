# Architecture

This document explains what stigmergy is made of and, more importantly, *why each
piece is the way it is*. Most of the non-obvious code in this repository is a
decision, not a mechanism — and a decision that isn't written down is a decision
the next contributor will cheerfully undo.

Companion documents: [usage.md](usage.md) (how to drive it),
[mcp-tools.md](mcp-tools.md) (the tool reference), [hosts.md](hosts.md) (the host
integration contract, and what about the hosts is still assumed rather than
observed), [operations.md](operations.md) (running it).

---

## 1. The problem

Claude Code, Codex, Antigravity and opencode each keep their own memory, per host and per session. Run
two agents on one repository and they diverge immediately: they learn different
things, forget them separately, and overwrite each other's files without ever
knowing the other was there.

stigmergy gives them one place to remember and one way to stay out of each
other's way. The name is the mechanism: *stigmergy* is coordination through
traces left in a shared environment — how ants route around one another with no
supervisor. There is no daemon, no scheduler, and no coordinating agent. There is
a database that agents write to and read from, and hooks that make them look
before they leap.

### The non-goal, stated once and hard

**stigmergy is cooperative. It is not a security boundary.**

It coordinates agents that participate. A shell command, a stray script, an agent
without the hooks installed, or a sufficiently determined model can write a
claimed file and nothing will stop it. Claims prevent *accidents between
cooperating agents*. They defend against nothing.

Every design decision below follows from taking that sentence seriously. When you
are not trying to stop an adversary, you can accept model-supplied identity, you
can put enforcement in an advisory hook, and you can spend your complexity budget
on being *useful* rather than on being airtight. If a future change starts
treating stigmergy as a security mechanism, that change is wrong — either fix the
threat model deliberately, or drop the idea.

---

## 2. The model

**Root.** One agent, addressable and answerable — usually a top-level session, one
Claude Code or Codex conversation. On a host that identifies the agents running
inside a session, each of them is a root too, with its own claims (§8); on every
other host a root is the session and nothing else.

Memory and mail stay with the session root everywhere, so what is written down stays
serialized through one accountable actor rather than an unbounded fan-out of
short-lived agents that don't know about each other. Explorers are not roots at all:
they read, and report back to the root that sent them.

**Memory.** A durable note with a key, a type, a one-line description, and a body.
Two scopes:

- **project** — lives in this repository, shared by every agent and every linked
  worktree on it.
- **global** — lives on this machine, shared across every repository.

Memories are versioned and written under compare-and-swap, so two agents editing
the same memory get a visible conflict instead of a silent overwrite.

**Claim.** A reservation on a file or a directory subtree, held by a root, with a
stated reason and a TTL. While it is held, another root's edit inside that scope
is refused (Claude Code) or warned about and then halted after the fact (Codex —
see [§7](#7-the-asymmetry-is-inherent)).

**Mailbox.** Root-to-root messages, threaded, with a resolution. This exists so a
blocked agent has something to *do* other than wait or barge through: it can
negotiate with the root that holds the claim.

A mailbox nobody reads is not a coordination mechanism, so two properties are part of
the design rather than the etiquette. **Mail is delivered**: the host hooks put unread
messages in front of the agent — on Claude Code by refusing to let a turn end while a
message has never been shown — because an agent deep in its own work will not think to
call `mailbox_inbox`, and telling it to try harder does not change that. And **mail is
addressed to the living**: a conflict names the owner *and* its liveness, `root_list_active`
lists who is here, and a send to a root that has died is refused with the roster of
those that have not. An agent reconstructing a root id from memory is how a message
ends up delivered, perfectly, to nobody.

**Audit log.** Who did what, kept 90 days. In particular it is the only record
that a claim was actually *violated* (on Codex) rather than merely enforced.

---

## 3. Storage

Two SQLite databases, both pure-Go (`modernc.org/sqlite`, no cgo, static binary).

**Project DB** — `<git-common-dir>/stigmergy.sqlite3`, for a project that is one
repository. Which is almost all of them, and nothing below changes for those.

The *common* dir, not the worktree, and that choice carries real weight. Linked
worktrees (`git worktree add`) all share one common dir, so they share one
database: two agents in two worktrees of the same repository can see and block
each other, which is exactly what you want, because they are working on the same
codebase ([§5.2](#52-claims-are-repo-wide-not-worktree-scoped) explains why that is
worth the false positives it costs).

A consequence worth stating outright, because it surprises people: the database is
inside `.git/`. It is **not** committed, cloned, or pushed. Project memories are
local to your machine. Sharing them across machines is a deliberate act, not a
side effect of `git push` — the act is `stigmergy sync`, and
[sync.md](sync.md) is its guide ([sync-model.md](sync-model.md) is the design
behind it).

**A project may span several repositories.** A client and its service, with
separate remotes, whose changes cross between them are one piece of work even
though git has no word for it. Those members share **one** database, kept outside
all of them at `$XDG_DATA_HOME/stigmergy/projects/<project-id>/stigmergy.sqlite3`,
and each member's git common dir holds a pointer file naming it:

```json
{"project": "p-3f2a9c81b4de7a05", "repo": "naviamp-sidecar", "created_at": "…"}
```

Three properties are load-bearing:

- **Membership is stated, never inferred.** Resolution reads the pointer in the
  repository's own common dir and stops. It does not walk up looking for a
  project and does not consult siblings, so a stray file high in a directory tree
  cannot quietly adopt everything beneath it. `stigmergy project add` is the only
  thing that writes a pointer, and it refuses a repository that already has one.
- **Members share no root.** They may sit under different parents, on different
  filesystems. Antigravity mounts unrelated directories as one workspace
  routinely, and the project id is opaque rather than derived from a path
  precisely so nothing depends on where the repositories happen to live.
- **No member is special.** The database is outside all of them, so removing one
  repository does not strand the rest — which is also why the id is random rather
  than "whichever repository was first".

Separate clones that are *not* members of a common project still have separate
databases and never see each other, exactly as before. What changed is that
"separate clone" and "different project" are no longer the same statement.

**Global DB** — `$XDG_DATA_HOME/stigmergy/global.sqlite3` (default
`~/.local/share/stigmergy/global.sqlite3`, directory mode `0700`). Memories, an
audit log, and `known_projects` — no roots, claims, or mailbox, because
coordination is inherently per-project.

`known_projects` is the registry of every project database this machine has been
told about. It exists so `stigmergy doctor --all` can reach all of them at once;
see [§5.9](#59-a-migration-blocks-every-adopted-project-at-once).

### Connection settings, and why

```
journal_mode=WAL       several processes touch this at once: the MCP server, the
                       hooks (one process per edit!), and the CLI
busy_timeout=5000      server/CLI. The hook path uses 250ms instead — see below
foreign_keys=1
synchronous=NORMAL
_txlock=immediate      every transaction is BEGIN IMMEDIATE
SetMaxOpenConns(1)
```

`_txlock=immediate` is not a performance tweak; it is a correctness one. Almost
every write in this codebase is a read-then-write (check the CAS version, then
update; check for an overlapping claim, then insert). With a deferred transaction
SQLite would upgrade the lock only at the write, and a concurrent writer that
committed in between produces `SQLITE_BUSY_SNAPSHOT` — a failure you get *after*
you have done the reading, and one that is easy to mistake for a lock timeout.
Taking the write lock up front makes every such sequence atomic by construction.

Local filesystems only. SQLite locking over NFS or a network share is not
trustworthy, and the whole enforcement story rests on that locking.

---

## 4. The schema

From `internal/store/migrations/project/0001_init.sql`. Annotated; the SQL itself
is authoritative.

### Memories (both databases)

```sql
CREATE TABLE memories (
  key         TEXT PRIMARY KEY,   -- ^[a-z0-9][a-z0-9-]{0,127}$
  type        TEXT NOT NULL CHECK (type IN ('user','feedback','project','reference')),
  description TEXT NOT NULL,      -- one line; what other agents see when listing
  body        TEXT NOT NULL,
  version     INTEGER NOT NULL DEFAULT 1,   -- the CAS token
  updated_by  TEXT NOT NULL,      -- root_id, or 'importer'
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

CREATE VIRTUAL TABLE memories_fts USING fts5(
  key, description, body,
  content='memories', content_rowid='rowid',
  tokenize='porter unicode61'
);
```

The FTS5 table is a **content table** (`content='memories'`), meaning it stores no
copy of the text — it indexes rows that live in `memories`. That keeps one source
of truth, but it also means the index does not maintain itself: three triggers
(`memories_ai`, `memories_ad`, `memories_au`) do. The delete/update triggers use
the `INSERT INTO memories_fts(memories_fts, ...) VALUES('delete', ...)` form,
which is the required incantation for content tables — a plain `DELETE` would
corrupt the index rather than update it. If you add a column to `memories`, you
must touch the virtual table and all three triggers together or search silently
goes stale.

`tokenize='porter unicode61'` gives stemming, so a search for "authenticate"
finds "authentication". Ranking is bm25, with `snippet()` for the excerpt.

### Links (both databases)

```sql
CREATE TABLE memory_links (    -- 0011 project / 0003 global
  key_a      TEXT NOT NULL REFERENCES memories(key) ON DELETE CASCADE,
  key_b      TEXT NOT NULL REFERENCES memories(key) ON DELETE CASCADE,
  reason     TEXT NOT NULL,     -- mandatory; the encoding context
  created_by TEXT NOT NULL,
  agent_kind TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (key_a, key_b),
  CHECK (key_a < key_b)
);

CREATE INDEX idx_memory_links_b ON memory_links(key_b);
```

An explicit, untyped, symmetric association between two memories in the same
scope — see [docs/association-model.md](association-model.md) for the full
design. `CHECK (key_a < key_b)` is total under the key grammar, which is what
makes symmetry structural rather than something application code has to
maintain: one row per pair, no self-links, and the primary key prefix and the
extra index cover lookups from either endpoint. No CAS, no weights, no
relation types — a link is cheap and attributed, and a wrong one is fixed by
`memory_unlink` rather than negotiated. Links are same-scope only: the two
databases cannot share a transaction or a foreign key (see `PromoteMemory`'s
ordering comment below), and there is no cross-scope motivation once general
knowledge is understood to live in the agent's weights rather than in a node
here. `memories` and `memories_fts` are untouched by this table.

### Episodes (project only)

```sql
CREATE TABLE episodes (         -- 0013; history, not state
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  title      TEXT NOT NULL,
  body       TEXT NOT NULL,     -- immutable once written
  actor      TEXT NOT NULL,
  agent_kind TEXT NOT NULL,
  at         TEXT NOT NULL
);

CREATE TABLE episode_memory (   -- provenance: this episode grounds that memory
  episode_id INTEGER NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
  key        TEXT NOT NULL REFERENCES memories(key) ON DELETE CASCADE,
  note       TEXT NOT NULL,
  PRIMARY KEY (episode_id, key)
);

CREATE TABLE episode_links (    -- correction/continuation chains
  episode_id   INTEGER NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
  successor_id INTEGER NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
  kind         TEXT NOT NULL CHECK (kind IN ('corrects','continues')),
  PRIMARY KEY (episode_id, successor_id),
  CHECK (episode_id != successor_id)
);

CREATE VIRTUAL TABLE episodes_fts USING fts5(
  title, body,
  content='episodes', content_rowid='id',
  tokenize='porter unicode61'
);
```

A `memories`-shaped record for what *happened* rather than what is true — see
[docs/association-model.md](association-model.md) §4. IMMUTABLE: no update
tool exists at the Go layer, ever; a wrong episode is corrected by a new one
chained on top with `episode_links.kind = 'corrects'`, and `episode_read`
walks the full chain from any episode in it, so superseded reasoning is never
read alone. Project scope only — episodes are session-bound, and the global
database has no roots or sessions. `episodes_fts` is a **separate** virtual
table cloned from `memories_fts`'s trigger pattern (0001_init.sql); it does
not touch `memories_fts`, whose schema this whole design keeps frozen.
GC (`gc.go`) reclaims an episode only once it is old, ungrounded (no
`episode_memory` row) and unchained (no `episode_links` row on either side) —
consolidation as retention policy, not decay: distilled-and-cited history is
provenance and stays.

### Roots (project only)

```sql
CREATE TABLE roots (
  root_id       TEXT PRIMARY KEY,   -- "r-" + 12 hex, crypto/rand
  agent_kind    TEXT NOT NULL CHECK (agent_kind IN ('claude-code','codex')),
  session_label TEXT,               -- the host's session id, model-supplied
  worktree      TEXT NOT NULL,
  branch        TEXT,
  registered_at TEXT NOT NULL,
  last_seen_at  TEXT NOT NULL,      -- refreshed by every tool call
  ended_at      TEXT
);

CREATE INDEX idx_roots_session ON roots(session_label) WHERE ended_at IS NULL;
CREATE INDEX idx_roots_active  ON roots(last_seen_at)  WHERE ended_at IS NULL;
```

`session_label` is the identity of an **agent**, which is not always the identity of
a host session. Where a host runs several agents in one session and says which is
which — Claude Code, today — a peer's label is `"<session>#<type>:<id>"` and it gets
a root of its own, while the main thread keeps the bare session id. See §8. That is
why identity was added as a label convention and not as a column: `RegisterRoot` and
`RootBySession` already key on this string, so every path that resolves an owner —
the claim guard included — got peers for free, and a database written before it
still reads correctly.

Both indexes are **partial** (`WHERE ended_at IS NULL`). Every query that matters
asks about live roots; ended ones are history. The partial index keeps the hot
lookup — "which root is this session?", run on the hook path, on every single edit
— proportional to the number of *live* roots rather than to every root that has
ever existed in the repository.

### Claims (project only)

```sql
CREATE TABLE claims (
  id          INTEGER PRIMARY KEY,
  scope_path  TEXT NOT NULL,   -- repo-relative POSIX; "." is the whole repo
  recursive   INTEGER NOT NULL CHECK (recursive IN (0,1)),
  root_id     TEXT NOT NULL REFERENCES roots(root_id),
  repo_id     TEXT NOT NULL DEFAULT '',  -- 0007; which member. '' = the only one
  worktree    TEXT NOT NULL,
  branch      TEXT,
  reason      TEXT NOT NULL,   -- other agents read this when you block them
  created_at  TEXT NOT NULL,
  expires_at  TEXT NOT NULL,
  released_at TEXT
);

CREATE INDEX idx_claims_open ON claims(expires_at) WHERE released_at IS NULL;

CREATE TABLE repos (            -- 0007; the project's member repositories
  repo_id    TEXT PRIMARY KEY,      -- the name agents type in a "repo:path" scope
  common_dir TEXT NOT NULL UNIQUE,  -- on THIS machine
  worktree   TEXT NOT NULL,         -- on THIS machine
  added_at   TEXT NOT NULL
);

CREATE TABLE caller_tickets (   -- 0010; which agent is about to make one MCP call
  tool_use_id   TEXT PRIMARY KEY,   -- the host's own id for the call; the join key
  session_label TEXT NOT NULL,      -- the caller's agent label, already composed
  agent_kind    TEXT NOT NULL,
  worktree      TEXT NOT NULL,
  created_at    TEXT NOT NULL
);

CREATE INDEX idx_caller_tickets_age ON caller_tickets(created_at);
```

`caller_tickets` is the one table here that holds no history: a row lives for the
milliseconds between the hook that writes it and the tool call that consumes it (60s
TTL, swept by the same transaction that reads one). It exists because the MCP server
cannot see which agent in a session is calling it, and the hook can — see §8.

`claims.repo_id` is the only place the repository dimension appears, and the
absences are deliberate. `memories` does not get one because project-wide sharing
is the *point* of a multi-repo project — and because `memories_fts` is an
external-content index whose three triggers enumerate columns literally and whose
`snippet()` addresses `body` by column *index*, so it is the table it is most
expensive to be wrong about. `roots` does not get one because a root is an agent,
not a directory ([§5.6](#56-root-registration-resumes)). The mailbox does not,
because roots are project-wide. `audit_log` does not: `target` is free text, so
spelling it `repo:path` makes the trail read correctly with no schema change.

`''` is not a placeholder awaiting cleanup. It is the stored spelling of "the
sole repository of a single-repo project", and every read maps it to whichever
repository the database was opened from. That is what makes the upgrade safe: a
v6 database migrated to v7 whose backfill has not run *still enforces its claims
exactly as before*, so the window in which a project is broken by a new binary is
the time it takes to install one, not the time it takes to get round to running
`doctor` in it.

`reason` is `NOT NULL` and rejected when empty for a human reason: a claim without
a reason is a lock with no way to negotiate around it. The blocked agent is shown
this string, and it is what it has to reason about.

### Mailbox and audit (project only)

```sql
CREATE TABLE mailbox_threads (
  id         INTEGER PRIMARY KEY,
  claim_id   INTEGER REFERENCES claims(id),
  created_by TEXT NOT NULL REFERENCES roots(root_id),
  state      TEXT NOT NULL DEFAULT 'open'
             CHECK (state IN ('open','resolved','abandoned')),
  resolution TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE mailbox_messages (
  id          INTEGER PRIMARY KEY,
  thread_id   INTEGER NOT NULL REFERENCES mailbox_threads(id),
  from_root   TEXT NOT NULL,
  to_root     TEXT NOT NULL,
  subject     TEXT NOT NULL,
  body        TEXT NOT NULL,
  sent_at     TEXT NOT NULL,
  read_at     TEXT,
  notified_at TEXT              -- 0002: when stigmergy put this in front of the agent
);

CREATE INDEX idx_msgs_inbox      ON mailbox_messages(to_root) WHERE read_at IS NULL;
CREATE INDEX idx_msgs_unnotified ON mailbox_messages(to_root)
  WHERE notified_at IS NULL AND read_at IS NULL;

CREATE TABLE audit_log (
  id         INTEGER PRIMARY KEY,
  at         TEXT NOT NULL,
  actor      TEXT,        -- root_id
  agent_kind TEXT,
  action     TEXT NOT NULL,
  target     TEXT,
  detail     TEXT
);

CREATE INDEX idx_audit_at ON audit_log(at);

CREATE TABLE priming_delivered (   -- 0012; docs/association-model.md §9
  root_id TEXT NOT NULL REFERENCES roots(root_id) ON DELETE CASCADE,
  key     TEXT NOT NULL REFERENCES memories(key) ON DELETE CASCADE,
  at      TEXT NOT NULL,
  PRIMARY KEY (root_id, key)
);

-- 0014 project / 0004 global; docs/sync-model.md §3.2, §3.7, §6.4.
-- All four are satellites: nothing about sync is a column on `memories`,
-- because the FTS triggers there enumerate columns literally (§4.2).

CREATE TABLE memory_sync_base (    -- the content both machines last agreed on
  key       TEXT PRIMARY KEY REFERENCES memories(key) ON DELETE CASCADE,
  digest    TEXT NOT NULL,         -- sha256 over key, type, description, body
  device_id TEXT NOT NULL,
  at        TEXT NOT NULL
);

CREATE TABLE sync_tombstone (      -- what was deleted, so a delete propagates
  kind      TEXT NOT NULL CHECK (kind IN ('memory','link')),
  ident     TEXT NOT NULL,         -- the key, or "<key_a> <key_b>"
  digest    TEXT NOT NULL,
  device_id TEXT NOT NULL,
  at        TEXT NOT NULL,
  PRIMARY KEY (kind, ident)
);

CREATE TABLE sync_policy (         -- per-key override; the DEFAULT differs by scope
  key  TEXT PRIMARY KEY REFERENCES memories(key) ON DELETE CASCADE,
  mode TEXT NOT NULL CHECK (mode IN ('include','exclude')),
  at   TEXT NOT NULL
);
```

`idx_audit_at` exists for GC, which deletes by age.

**The sync tables.** `memory_sync_base` is what makes divergence detectable: with a
recorded base, "both machines changed this" is distinguishable from "only one did",
which is the whole reason `version` never has to travel (sync-model.md §3.1).

`sync_tombstone` carries no foreign key, and cannot: it exists precisely because the
row it names does not. It is also the one table here whose absence would be silently
wrong rather than loudly broken — with no tombstone, a delete on one machine is
indistinguishable from a create on the other, so every deleted memory returns on the
next sync. The mirror of that rule is less obvious and is enforced in the write paths:
a key that is written again has its tombstone **cleared**, because a tombstone naming a
live memory shadows it in both directions and says nothing while it does.

`sync_policy` has identical DDL in both scopes and opposite defaults — project
memories sync unless excluded, global memories sync only when included. A global
memory may be about *this physical box*, and only a human knows which; the rule is
deliberately not derived from `type`, which is an enum nothing branches on
(memory-model.md §8).

`link_sync_base` is created by the same migration and is not yet read by anything. It
is the link-side counterpart reserved for the stage that merges link reasons rather
than merely their existence; the migration says so rather than leaving a future reader
to wonder.

`priming_delivered` is dedup for the end-of-turn priming nudge, not a
mailbox: it rides the same Stop hook as mail (`stigmergy hook mail-gate`) so
a session's held claims — its own and any agent's that ran inside it —
surface memories whose declared evidence overlaps them, plus one hop of
their linked neighbors, at most once per (root, memory). Keyed on the
session's own root, since only the session's main thread ever sees a Stop
hook.

`notified_at` and `read_at` look redundant and are not. `notified_at` is *stigmergy's*
record — stigmergy put this message in front of the agent — and `read_at` is the *agent's* —
it looked. Only the delivery hooks set the first; there is no tool for it, because an
agent able to suppress its own notifications eventually would.

Collapsing the two is what made the mailbox pull-only for so long: an unread message
was indistinguishable from an undelivered one, so nothing in the system could tell
whether an agent had ignored its mail or had simply never been told it had any. With
them apart, the Stop hook can interrupt exactly once per message — it blocks on what
has never been announced, stamps it, and lets the agent go. An agent that reads its
mail and presses on regardless is not trapped in a loop; a message that arrives
mid-turn still gets its one interruption.

---

## 5. Invariants

These are the things a change must not break. Each one is here because breaking it
produces a bug that does not look like a bug.

### 5.1 The active-claim predicate

A claim is in force if and only if:

```sql
c.released_at IS NULL
AND c.expires_at > :now
AND r.ended_at IS NULL
AND r.last_seen_at > :cutoff     -- cutoff = now - RootTTL (15 minutes)
```

Read the last two lines again: **a claim is only alive while the root that holds it
is alive.** That is what makes the whole system safe to crash. Kill an agent
mid-edit — `SIGKILL`, closed laptop, OOM — and its claims stop blocking anyone
within the root TTL, with no cleanup process, no lease renewal daemon, and no
stale-lock recovery path. The expiry is *lazy*: it is folded into the query
predicate, evaluated on read. Nothing runs in the background, so nothing can fail
to run in the background.

This predicate is the reason there is no daemon. Do not replace it with a
`status` column that something has to remember to update.

### 5.1.1 Why the TTL is fifteen minutes, and what it costs

The TTL is not a guess at how long an agent might idle. It is **how long a dead agent
goes on looking alive**, and everything bad follows from that window: its claims keep
blocking, and mail addressed to it is accepted and never read. It was an hour, and an
hour is far too long to be blocked by a corpse.

Shortening it was only possible because liveness stopped depending on an agent's
goodwill. It used to be refreshed solely by MCP calls, so an agent heads-down in a long
stretch of editing looked exactly like an agent that had crashed — which forced the TTL
to cover the longest plausible silence. The hooks (`claim-guard`, `mail-gate`,
`mail-notify`) now heartbeat on the agent's own activity: every edit, every turn. A
working agent proves it is alive as a side effect of working, and silence finally means
what it says.

The price is paid at `heartbeat()`, and it is worth understanding. A root that has
lapsed has had its claims declared free — the predicate above already ignores them, and
another agent may have claimed the same path and started editing. If the original then
returns and heartbeats, a bare `UPDATE roots SET last_seen_at` would bring those claims
back to life, and two agents would each have been told the same file was theirs. So
**coming back from the dead costs you your claims**: they are released, and the root
must re-acquire — which is exactly the moment the overlap check runs and it learns the
path is spoken for. The agent is welcome back. Its promises are not.

### 5.2 Claims are repo-wide, not worktree-scoped

The conflict test compares `scope_path` and nothing else. A claim's `worktree` is
recorded and reported, but it is deliberately **not** part of the decision — so a
claim taken in one worktree blocks an agent in *another* worktree of the same
repository.

This looks wrong at first, and the wrong conclusion is easy to reach: linked
worktrees are separate checkouts, usually on separate branches, so two agents
editing `src/api/handlers.go` in two worktrees are touching two different files on
disk and cannot possibly clobber each other. Surely the `worktree` column belongs
in the overlap test?

No — because **the point of a claim is not to protect bytes.** It is to stop the
second agent from writing a pile of code against assumptions the first agent is in
the middle of invalidating. Nothing is corrupted on disk; the damage shows up at
merge time, when one of the two piles of work has to be rewritten. Blocking the
second agent costs one conversation. Not blocking it costs a rewrite.

That is why the conflict message names the holder's **worktree, branch and
reason**: the blocked agent is meant to recognize this as a cross-branch
conversation and go have it, via `mailbox_send`.

The accepted cost is a real false positive: an agent doing an unrelated hotfix on a
release branch can be blocked by a refactor claim on `main`. The escape hatch is
the negotiation path, which is the same one you would want anyway — the owner
releases, narrows the claim, or tells the hotfixer what is changing under them.

Pinned by `TestClaimsBlockAcrossWorktrees`.

#### Amended: the conflict test now compares the repository too

The sentence that used to close this section — "separate *clones* have different
databases and never see each other at all" — stopped being true when a project
gained the ability to span repositories. Two members share one database, so the
overlap test has to say which repository a path is in:

```go
func overlaps(a, b RepoScope) bool {
    return sameRepo(a.Repo, b.Repo) && claims.Overlaps(a.Scope, b.Scope)
}
```

This does **not** walk back the reasoning above, and the distinction is the whole
point:

- Two linked worktrees of one repository share a `repo_id`, so §5.2's behaviour is
  preserved exactly and its test passes unchanged. A claim on `main` still blocks
  a hotfix on a release branch, and it still should.
- Two *members* are different codebases with different remotes. Without the
  dimension, a claim on the client's `README.md` would block an agent editing the
  service's — a false positive with no shared assumption behind it. §5.2 accepts
  false positives that buy something; that one buys nothing.

The repository dimension deliberately does **not** live in `internal/claims`.
That package is pure path algebra, and a `Scope` with a repo field would have a
zero value naming no repository — every construction site that forgot it would
compare equal to every one that did, and the claim guard would fail *open* with
no error anywhere. So it lives one level up in `store.RepoScope`, whose only two
constructors take the repository and the path together, and `claims.Covers` —
which built its operand from a partial literal — was **deleted** rather than
extended. Forgetting the repository is now a compile error.

An empty `repo_id` matches anything, which is correct for a single-repo project
and fails *safe* in a multi-repo one: it blocks more rather than less. A false
conflict costs one conversation; a missed one costs somebody's work.

### 5.3 Overlap is component-wise, never a string prefix

`internal/claims/overlap.go`, pure logic, no database, deliberately isolated so
the rule is trivially testable:

```go
func covers(dir, path string) bool {
	if dir == path { return true }
	if dir == "." { return true }               // the repo root contains everything
	return strings.HasPrefix(path, dir+"/")     // note the "/"
}
```

The `dir+"/"` is the entire point. A naive `strings.HasPrefix(path, dir)` would
have a claim on `foo` silently block every edit to `foobar`, `foo.go`, and
`foosball/` — files with no relationship to the claim beyond a shared prefix. It
would look like it worked, right up until it blocked something baffling.

The `dir == "."` case is here because a test caught it: the repo root is spelled
`"."`, but the paths under it are spelled `src/main.go`, not `./src/main.go`, so
the generic prefix test never matches and a whole-repo claim covers *nothing*.

### 5.4 Timestamps are fixed-width UTC

```go
const TimeLayout = "2006-01-02T15:04:05.000000000Z"
```

Every timestamp in both databases is TEXT in this format. This is load-bearing:
the active-claim predicate compares timestamps with `>`, and SQLite compares TEXT
lexicographically. The obvious choice — `time.RFC3339Nano` — **trims trailing
zeros**, so `…:00.5Z` and `…:00Z` have different widths and sort against each
other wrongly. The result would not be a crash; it would be claims that expire at
subtly wrong moments, which you would probably never notice and never be able to
reproduce.

Fixed width, always UTC, always the same number of fractional digits. `store.Now()`
and `store.Stamp()` are the only ways to produce one, and `store.SetClock()` lets
tests pin it.

**Every timestamp, not only the interesting ones.** A single short value skews its
own comparisons — `…:00Z` sorts *above* `…:00.000000000Z`, because `Z` beats `.` —
so a row written earlier reads as newer. For most columns that is cosmetic; for two
it decides behaviour. `claims.expires_at` reading as newer than it is means a claim
that never expires and blocks everyone forever, and `roots.last_seen_at` reading
newer means a dead root that goes on holding its claims and accepting mail. Any
value arriving from outside — an import, a repaired row, an older binary — goes
through `CanonicalStamp` on the way in. Do not add a column that stores a time
without it.

### 5.5 CAS is decided in exactly one place

`expected_version` semantics:

| value | meaning | failure |
|---|---|---|
| omitted / null | create if absent | `cas_conflict` if the key exists |
| integer | update that exact version | `cas_conflict` if the current version differs, or the key is gone |

Every conflict returns the **current entry** in the error's `current` field, so the
agent can merge and retry rather than guess. One function (`casCheck`, in
`internal/store/memories.go`) decides the whole matrix. Keep it that way — a second
place that decides "may this write proceed" is a second place to get it wrong.

The instructions tell agents in as many words: on a conflict, re-read, merge,
retry — and never work around it by inventing a new key, because that is how you
get two half-true memories instead of one true one.

### 5.6 Root registration *resumes*

`RegisterRoot` matches an existing live root on `(agent_kind, session_label)` and
returns it, same `root_id`, claims intact.

This is not an optimization. Hosts restart the MCP server process mid-session —
Claude on `/clear`, Codex on compact. If each restart minted a fresh `root_id`, the
previous root's claims would be **stranded**: live, owned by nobody reachable,
unreleasable, and blocking every other agent in the repository until they timed
out. Resume reconnects a session to the claims it already holds.

**`worktree` used to be part of that key, and its removal fixed a bug that
predates multi-repo.** `RootBySession` — the *read* path, the one the claim guard
uses to decide whether a claim is your own — has always matched on `(agent_kind,
session_label)` alone. Only the write path included `worktree`, so the two sides
disagreed about what identifies a root: registering from a second worktree minted
a second root for one session while the guard went on resolving whichever was
most recent. The consequences all point the same way — the abandoned root goes
silent, lapses at `RootTTL`, and [§5.1.1](#511-why-the-ttl-is-fifteen-minutes-and-what-it-costs)'s
"coming back from the dead costs you your claims" then fires on a root that never
went anywhere, while the agent, still working, believes it holds them.

A root is an agent, not a directory. One host session is one root, wherever it
happens to be standing. `worktree` is still recorded and still shown — it is what
a blocked agent reads in a conflict — and a resume updates it, because a stale
answer there is worse than none.

An empty `session_label` cannot be matched, so it always mints a new root. Which
leads directly to:

### 5.7 Identity is advisory, and an unregistered session owns nothing

`session_label` is the host's session id, supplied *by the model* when it calls
`root_register`. A model could lie. That is accepted, because stigmergy is not a
security boundary (§1) and lying gains an honest participant nothing.

The consequence to understand: the claim guard resolves "who am I?" by looking up
the session label. **If a session has not registered, `selfRoot` is empty, and
every claim in the repository is foreign to it — including claims it made itself
in a previous life.** So an unregistered agent blocks itself. That is not a bug;
it is what makes registration self-enforcing without any enforcement code. It is
also the answer to the support question "why is the agent blocking its own edits?"
— it registered with the wrong session label, or not at all.

### 5.8 Failure directions are chosen per-failure, not globally

The claim guard does not have "a" failure mode. It has two, pointing opposite ways:

- **Unparseable hook payload → allow.** Stigmergy's own parser broke, or the host
  changed its schema. Blocking every edit in the user's repository over a bug in
  *this* code is far worse than the collision the guard exists to prevent.
- **Database unreadable / schema mismatch → deny** (on Claude). Here stigmergy is
  known to be meant to be active, and claims *cannot be verified*. Allowing the edit
  risks silently destroying another agent's work. So the guard refuses, and says so:
  the message states plainly that this is not a claim conflict and there is nobody
  to negotiate with — run `stigmergy doctor`.

That second case is why `doctor` reports a broken project DB as "Every edit is
currently BLOCKED".

### 5.9 A migration blocks every adopted project at once

The claim guard fails closed when a project database's schema version differs
from the version compiled into the binary ([§5.8](#58-failure-directions-are-chosen-per-failure-not-globally)),
while the MCP server migrates on open and does not version-gate. Both are right
on their own. Together they mean that the moment a binary carrying a new
migration lands on `PATH`, **every** adopted project on the machine blocks every
edit — including the one you are standing in, and including the agents working in
repositories you had forgotten were adopted.

This has cost real work. The recovery used to be a manual walk: run `stigmergy
doctor` in each project, from inside it, with nothing anywhere listing them.

So the global database keeps `known_projects`, written whenever a project
announces itself — `init`, `doctor`, `project create/add`, or an agent opening it
over MCP — and `stigmergy doctor --all` opens and upgrades all of them in one
pass. It is deliberately a record of projects that *said so*, never the result of
scanning the filesystem for databases: a tool that went looking would eventually
find one it should not have touched.

The hook path never writes to it. Hooks run per-edit under a 250ms lock budget
and open the project read-only; a registry write there would put a second
database in the critical section of the thing §9 exists to keep fast.

### 5.10 A claim that has stopped binding never comes back

A claim stops binding in four ways: its root goes silent past the TTL, its
`expires_at` passes, it is released, or its root ends.

> **Nothing may return a dead claim to life without re-running the check that grants
> one in the first place.**

The trap is that `released_at IS NULL` answers "does this row still exist", which is
not the same question as "does this still bind" — and code that asks only the first
will resurrect something. Renewing an expired claim is the sharp case: by then another
agent may legitimately hold the path, and extending the old one leaves two roots each
told the same file is theirs.

Two rules keep it closed. The liveness sweep runs at the **top** of an acquire or
renew transaction, never after the write, so the sweep, the overlap check and the
write are one atomic step. And an operation on a claim that has stopped binding is not
simply refused — it re-runs the overlap check an acquire would: path still free, let
it through; somebody took it, report the conflict and name them. Refusing outright
fails safe operations for no reason the agent can act on.

If you add a fifth way for a claim to end — a project-level pause, a manual override,
removing a repository from a project — assume it has this bug until a test says
otherwise, and write the "somebody else took it meanwhile" case first.

### 5.11 Text an agent writes is validated before it is stored

Every agent-authored string — memory descriptions and bodies, mail subjects and
bodies, claim reasons, branches, worktrees, session labels, models, verification
reasons, evidence patterns — goes through `ValidateLine`/`ValidateBlock` and
`NormalizeText`. Not for tidiness. This text is *displayed to other agents and to
people*, and two of the things it can contain are attacks on the reader rather than
mistakes: an ANSI escape stored in a memory executes in the next reader's terminal,
and a bidi override (Trojan Source) makes text render in an order it is not written
in — in a system whose entire content is instructions other agents act on.

The three-way split is the design:

- **Normalised** where there is one obvious meaning: BOM, CRLF/CR → LF, trim.
- **Rejected** where the input is ambiguous or destructive: C0 controls and DEL
  except tab and newline, invalid UTF-8, bidi overrides, a newline in a one-line
  field, over-length.
- **Defused** for the one case where refusing would be wrong: a lone carriage return
  is *converted*, not refused. `"real\rfake"` displays in a terminal as only
  `"fake"` — the first half is overwritten and never seen — but refusing every CR
  would turn "your editor saved this file" into an error.

Two rules are easy to get wrong in the obvious direction. **Literal `\n` is not
banned**, because both populations are real: bodies mangled by an agent JSON-encoding
text that was already going to be encoded, and perfectly good bodies containing printf
formats or regexes. What separates them is the *absence of real line breaks*, not the
presence of escapes. And **normalisation runs to a fixed point**, not one pass —
stripping one leading BOM exposes the next, and no ordering of the steps avoids it.

`doctor` reports rows that would be refused today and never repairs them. Un-escaping
means guessing what the author meant, and a legacy row stays readable so it stays
fixable by whoever knows.

---

## 6. Package map

`internal/`, one line each on what it owns and why it is separate.

| package | owns | why it's its own package |
|---|---|---|
| `store` | all SQLite: open, migrate, memories, roots, claims, mailbox, audit, GC | one place that knows SQL; the DAO is the contract |
| `claims` | the overlap rule — pure functions, no I/O | the rule the whole enforcement layer rests on, so it must be testable with no database in sight (§5.3) |
| `paths` | absolute/relative → repo-relative POSIX; symlinks; worktree-escape | agents write files that *don't exist yet*, so it resolves the deepest existing ancestor and re-appends the missing tail |
| `gitx` | worktree root + git common dir, pure Go, with a subprocess fallback | the hook path uses the pure-Go path only — shelling out to `git` on every edit would blow the latency budget |
| `project` | which project governs a directory, and which of its repositories a path is in | resolution runs on the hook path, so it is filesystem-only by construction: one `gitx` walk and one small file read, no subprocess and no query |
| `mcpserver` | the MCP server: session state machine, 31 tools, instructions | |
| `hooks` | the host hook protocols and the shared `Guard` fast path | Claude and Codex differ in *protocol*, not in *decision* — one guard, two renderings |
| `hosts` | what each host can enforce, declared once along the axes that vary | the per-host copies of that text used to contradict each other; every rule an agent reads is now rendered from here |
| `drift` | what has CHANGED in a memory's declared scope since a recorded commit | it measures change and never truth — keeping it out of `store` keeps that distinction structural (see memory-model.md) |
| `deliberate` | the adversarial specification pipeline and its bwrap sandbox | a separate subsystem that merely *uses* stigmergy; nothing in the memory/claims path may depend on it |
| `hostcfg` | writing/removing host config, idempotently, without clobbering | merging into someone else's config file is fiddly and deserves its own tests |
| `importer` | legacy Claude markdown memory import | |
| `syncx` | the cross-machine wire format and the pure merge rules | the merger takes two exported views and a base and returns a plan, so every rule in sync-model.md §3.4 is testable with no database and no network — the same reason `claims` is separate. Named `syncx` because a package called `sync` would shadow the standard library's at every call site |
| `explore` | the sandboxed `codex exec` explorer | |
| `serr` | the closed set of protocol error codes | agents branch on these, so they are an API |
| `xdg`, `ids`, `cli` | XDG paths; root id generation; cobra wiring | |

Two structural rules worth preserving: `cli` stays thin (cobra wiring only — logic
lives in the packages it calls), and `claims` never grows a database dependency.
A third, newer: `project.Resolve` never grows a subprocess or a query — §9 is why,
and `TestResolveNeverSpawnsASubprocess` is what notices.

---

## 7. The asymmetry is inherent

Claude Code's `PreToolUse` hook can **deny** a tool call before it runs. Codex's
cannot: it may only surface a `systemMessage`, and the edit proceeds regardless
(returning `continue:false` there marks the hook as *failed* and the call goes
ahead anyway). This is a property of the hosts, not a shortcut stigmergy took.

So enforcement is genuinely different on each side:

- **Claude Code**: claim guard denies the edit. Prevention.
- **Codex**: PreToolUse warns; PostToolUse halts the turn *after* the edit has
  landed, and audits `codex_post_edit_conflict`. Damage limitation.

Two things follow, and both are enforced in the code and in the tests:

1. **No user-facing text may claim an edit was blocked when it wasn't.** The Codex
   warning says, in as many words, "this is a warning, not a block". There is a
   test asserting it never says "is blocked by". The `ConflictDetail` renderer is
   shared, but the lead-in sentence is per-host, precisely so this can't drift.
2. **Fail-closed is a Claude-only concept in practice.** `Guard` fails closed on a
   DB error, but `codex-claim-warn` only emits when there are actual conflicts —
   so on Codex a broken database produces *silence*. That looks like a bug and is
   not: Codex could not have blocked the edit anyway, so there is nothing useful to
   say. Do not "fix" it into a warning that promises protection it cannot deliver.

`AGENTS.md` tells Codex agents this plainly: check claims yourself, because nothing
else will stop you.

---

## 8. Agents inside a session, explorers, and the root-gate

A host session is not an agent. Several agents can run inside one, sharing its id
and — because there is one `stigmergy mcp` process per host process — its single MCP
connection. Every request from them arrives looking identical, which is a problem
about *identity* long before it is a problem about permission.

- **Claude**: identified, and each agent gets a root. The host describes each call
  twice: to the `PreToolUse` hook, which is told `agent_id` and `agent_type`, and to
  the MCP server, which is told `_meta["claudecode/toolUseId"]`. Both carry the same
  host-minted tool-use id, so the hook writes a **caller ticket** under it
  (`caller_tickets`, migration 0010) and `Session.actingRoot` consumes it to act as
  the agent that actually called. The claim guard resolves the same label, so a
  peer's claim blocks its neighbours exactly as a stranger's would.

  This is enforcement rather than an honour system, and the indirection is what
  makes it so: a model never handles a tool-use id, cannot mint one, and cannot
  write a ticket. An "which agent are you" argument on the tool would have been one
  line and worth nothing.

  Every failure — no `_meta`, no ticket, no hooks installed, another host — resolves
  to the session root, which is exactly how this behaved before the mechanism
  existed. That is the compatibility argument, and it is why the identity could be
  added without a flag.

  What the gate still refuses to a peer is chosen by **lifetime, not rank**: memory
  writes and the whole mailbox stay with the session root, because a peer ends in
  minutes and cannot read a reply that arrives afterwards, nor answer for a memory
  someone reads next year. Claims are deliberately not on that list — a claim is a
  thing you borrow and give back, which is precisely what a short-lived agent can do
  and precisely what it needs.

  `SubagentStop` ends an agent's root and releases its claims immediately;
  `SessionEnd` sweeps the session and every agent under it. Without those a peer's
  claim would outlive it by the full root TTL, held by an agent that cannot be
  negotiated with — which would make granting peers claims a worse trade than
  refusing them.

  The payload fields this rests on are **undocumented**, and were read off a live
  2.1.220 session with `stigmergy hook dump` and a `tee` on the MCP stdio rather
  than taken from a doc page (which, checked the same day, denies they exist).
  Tests pin the observed shapes so a host that changes them fails loudly instead of
  silently collapsing every agent back into one identity.
- **Codex**: native subagents inherit the parent's sandbox and permissions, so they
  are *not* an isolation boundary. That is why `stigmergy explore` exists: it runs
  `codex exec --sandbox read-only -c approval_policy="never" --ephemeral -c
  mcp_servers.stigmergy.enabled=false`. The explorer cannot write the tree and
  cannot even *see* stigmergy's tools — it has no way to register, claim, or write
  a memory. It reports back; the root decides what to record.

  The approval policy goes through `-c` because `--ask-for-approval` is a
  top-level flag that `codex exec` does not accept — it is an unknown argument
  there, and the explorer dies on argument parsing before the model is reached.
  It was written the obvious way for months and nothing noticed, because a flag
  list is not the kind of thing anyone tests and codex does not run in CI. See
  [hosts.md](hosts.md) for the full trap list; `internal/explore` now has tests.

---

## 9. Latency, and why it is an architectural concern

The claim guard runs as a **fresh process on every single Edit and Write**. If it
were slow, stigmergy would be intolerable to work with, and the correct response
from any user would be to uninstall it. So the budget is real and the design bends
around it:

- static Go binary, no runtime, no cgo — process start is ~2–5ms
- the hook path never shells out to `git` (pure-Go common-dir resolution)
- it opens the database **read-only** and never migrates
- `busy_timeout` drops from 5000ms to **250ms**: on the hook path, waiting a long
  time for a lock is itself the failure
- audit writes get a 100ms budget and are abandoned if they exceed it — an audit
  record must never be able to flip or delay a decision
- resolving which project governs a directory is a `gitx` walk plus one ~100-byte
  file read. The member roster costs one query on a connection that is being
  opened anyway. Measured after multi-repo landed: ~19ms mean end to end,
  unchanged

Measured: ~17ms median, ~19ms p95, and that includes the test harness's own
subprocess overhead, so the real figure is lower.

---

## 10. Protocol surface

Errors returned to agents carry a **stable, closed set of codes** (`internal/serr`):
`wrong_state`, `invalid_input`, `not_a_repo`, `cas_conflict`, `claim_conflict`,
`not_owner`, `recipient_inactive`, `unsupported_search`, `internal`. Agents branch
on these, so they are an API: never rename one, never repurpose one.

The wire shape is a JSON body carried in the tool error text, with any structured
context merged in at the top level of the `error` object:

```json
{"error":{"code":"claim_conflict","message":"…","conflict":{…}}}
```

An unrecognized internal error is coerced to `internal` rather than leaking a raw
driver message with an unstable code. Full reference: [mcp-tools.md](mcp-tools.md).
