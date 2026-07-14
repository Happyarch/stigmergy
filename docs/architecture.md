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

Claude Code and Codex each keep their own memory, per host and per session. Run
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

**Root.** A top-level agent session — one Claude Code or Codex conversation. A
root is the only thing that may register, claim, write memory, or send mail.
Subagents and explorers are *not* roots: they read, and they report findings back
to their root, which decides what to record. This keeps writes serialized through
one accountable actor per session rather than through an unbounded fan-out of
subagents that don't know about each other.

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

**Project DB** — `<git-common-dir>/stigmergy.sqlite3`.

The *common* dir, not the worktree, and that choice carries real weight. Linked
worktrees (`git worktree add`) all share one common dir, so they share one
database: two agents in two worktrees of the same repository can see and block
each other, which is exactly what you want, because they are working on the same
codebase ([§5.2](#52-claims-are-repo-wide-not-worktree-scoped) explains why that is
worth the false positives it costs). Separate clones have separate common dirs and
stay fully isolated, which is also what you want, because they are not.

A consequence worth stating outright, because it surprises people: the database is
inside `.git/`. It is **not** committed, cloned, or pushed. Project memories are
local to your machine. Sharing them across machines is a deliberate act, not a
side effect of `git push`.

**Global DB** — `$XDG_DATA_HOME/stigmergy/global.sqlite3` (default
`~/.local/share/stigmergy/global.sqlite3`, directory mode `0700`). Memories and an
audit log only — no roots, claims, or mailbox, because coordination is inherently
per-repository.

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
  worktree    TEXT NOT NULL,
  branch      TEXT,
  reason      TEXT NOT NULL,   -- other agents read this when you block them
  created_at  TEXT NOT NULL,
  expires_at  TEXT NOT NULL,
  released_at TEXT
);

CREATE INDEX idx_claims_open ON claims(expires_at) WHERE released_at IS NULL;
```

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
```

`idx_audit_at` exists for GC, which deletes by age.

`notified_at` and `read_at` look redundant and are not. `notified_at` is *stigmergy's*
record — we put this message in front of the agent — and `read_at` is the *agent's* —
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

Separate *clones* are a different matter entirely: they have different git common
dirs, so they have different databases and never see each other at all.

Pinned by `TestClaimsBlockAcrossWorktrees`.

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

`RegisterRoot` matches an existing live root on `(agent_kind, worktree,
session_label)` and returns it, same `root_id`, claims intact.

This is not an optimization. Hosts restart the MCP server process mid-session —
Claude on `/clear`, Codex on compact. If each restart minted a fresh `root_id`, the
previous root's claims would be **stranded**: live, owned by nobody reachable,
unreleasable, and blocking every other agent in the repository until they timed
out. Resume reconnects a session to the claims it already holds.

An empty `session_label` cannot be matched, so it always mints a new root. Which
leads directly to:

### 5.7 Identity is advisory, and an unregistered session owns nothing

`session_label` is the host's session id, supplied *by the model* when it calls
`root_register`. A model could lie. We accept that, because we are not a security
boundary (§1) and lying gains an honest participant nothing.

The consequence to understand: the claim guard resolves "who am I?" by looking up
the session label. **If a session has not registered, `selfRoot` is empty, and
every claim in the repository is foreign to it — including claims it made itself
in a previous life.** So an unregistered agent blocks itself. That is not a bug;
it is what makes registration self-enforcing without any enforcement code. It is
also the answer to the support question "why is the agent blocking its own edits?"
— it registered with the wrong session label, or not at all.

### 5.8 Failure directions are chosen per-failure, not globally

The claim guard does not have "a" failure mode. It has two, pointing opposite ways:

- **Unparseable hook payload → allow.** Our own parser broke, or the host changed
  its schema. Blocking every edit in the user's repository because *we* have a bug
  is far worse than the collision we are guarding against.
- **Database unreadable / schema mismatch → deny** (on Claude). Here we know
  stigmergy is meant to be active and we *cannot verify* claims. Allowing the edit
  risks silently destroying another agent's work. So we refuse, and say so:
  the message states plainly that this is not a claim conflict and there is nobody
  to negotiate with — run `stigmergy doctor`.

That second case is why `doctor` reports a broken project DB as "Every edit is
currently BLOCKED".

---

## 6. Package map

`internal/`, one line each on what it owns and why it is separate.

| package | owns | why it's its own package |
|---|---|---|
| `store` | all SQLite: open, migrate, memories, roots, claims, mailbox, audit, GC | one place that knows SQL; the DAO is the contract |
| `claims` | the overlap rule — pure functions, no I/O | the rule the whole enforcement layer rests on, so it must be testable with no database in sight (§5.3) |
| `paths` | absolute/relative → repo-relative POSIX; symlinks; worktree-escape | agents write files that *don't exist yet*, so it resolves the deepest existing ancestor and re-appends the missing tail |
| `gitx` | worktree root + git common dir, pure Go, with a subprocess fallback | the hook path uses the pure-Go path only — shelling out to `git` on every edit would blow the latency budget |
| `mcpserver` | the MCP server: session state machine, 21 tools, instructions | |
| `hooks` | the host hook protocols and the shared `Guard` fast path | Claude and Codex differ in *protocol*, not in *decision* — one guard, two renderings |
| `hostcfg` | writing/removing host config, idempotently, without clobbering | merging into someone else's config file is fiddly and deserves its own tests |
| `importer` | legacy Claude markdown memory import | |
| `explore` | the sandboxed `codex exec` explorer | |
| `serr` | the closed set of protocol error codes | agents branch on these, so they are an API |
| `xdg`, `ids`, `cli` | XDG paths; root id generation; cobra wiring | |

Two structural rules worth preserving: `cli` stays thin (cobra wiring only — logic
lives in the packages it calls), and `claims` never grows a database dependency.

---

## 7. The asymmetry is inherent

Claude Code's `PreToolUse` hook can **deny** a tool call before it runs. Codex's
cannot: it may only surface a `systemMessage`, and the edit proceeds regardless
(returning `continue:false` there marks the hook as *failed* and the call goes
ahead anyway). This is a property of the hosts, not a shortcut we took.

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

## 8. Subagents, explorers, and the root-gate

Only roots may mutate. Enforcing that differs by host:

- **Claude**: a `root-gate` PreToolUse hook matches the mutating
  `mcp__stigmergy__*` tools and denies them when the payload shows a subagent
  indicator. **If no indicator is present, the caller is treated as a root** — on
  purpose. Guessing "subagent" on an ambiguous payload would lock a root out of its
  own tools, which is a far worse failure than a subagent sneaking a write. Which
  field actually identifies a subagent — or whether any field does — is **unverified**;
  `stigmergy hook dump` exists to settle it. If none does, this gate is advisory.
- **Codex**: native subagents inherit the parent's sandbox and permissions, so they
  are *not* an isolation boundary. That is why `stigmergy explore` exists: it runs
  `codex exec --sandbox read-only --ask-for-approval never --ephemeral -c
  mcp_servers.stigmergy.enabled=false`. The explorer cannot write the tree and
  cannot even *see* stigmergy's tools — it has no way to register, claim, or write
  a memory. It reports back; the root decides what to record.

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
