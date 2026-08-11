-- Cross-machine memory sync, Stage A (docs/sync-model.md). Four new tables,
-- nothing existing touched: no ADD COLUMN, no rebuild, no index at risk. This
-- directory's standing lesson is why that restraint matters here in
-- particular — 0003 rebuilt `roots` to change a CHECK constraint and took both
-- of its indexes with it, silently, while reporting success; 0004 existed only
-- to put them back (see `shipping-a-migration-locks-the-repo`). A migration
-- that adds four tables and rebuilds none of the existing ones cannot repeat
-- that mistake, so Stage A is written to stay exactly that shape even though a
-- rebuild was never on the table for any of the four.
--
-- `memories` and `memories_fts` are untouched by every table below. The FTS5
-- freeze (docs/architecture.md §4) is absolute: the three triggers enumerate
-- `memories`' columns literally and `snippet()` addresses `body` by column
-- index, so nothing in this file may add a column there. Sync imports go
-- through `memories` itself (internal/store/sync.go's ImportMemory shares
-- WriteMemory's write path), which is what keeps the triggers firing without
-- this migration having to know they exist.

-- The causality signal (docs/sync-model.md §3.1-3.2). `memories.version` is a
-- local CAS counter and stays one — redefining what it counts would redefine
-- every agent's `expected_version`, and the FTS freeze forbids the vector-clock
-- column that would need anyway (D2). So divergence is detected against a
-- separate per-key base instead: the content both machines last agreed on.
--
-- digest is sha256 over exactly (key, type, description, body) — see
-- syncx.Digest. Not version, not updated_by, not either timestamp: two
-- machines on which a developer independently typed the same correction must
-- converge, not conflict, and comparing only the synced content is what makes
-- that true.
--
-- ON DELETE CASCADE is deliberate, not incidental. A locally deleted memory
-- must lose its base row in the same instant it gains a row in sync_tombstone
-- below — Reconcile (internal/syncx/merge.go) is written for exactly that pair
-- of states, "has a tombstone, has no base" — and a stray base row surviving a
-- delete would let a later sync mistake "deleted" for "still at this digest".
CREATE TABLE memory_sync_base (
  key       TEXT PRIMARY KEY REFERENCES memories(key) ON DELETE CASCADE,
  digest    TEXT NOT NULL,
  device_id TEXT NOT NULL,
  at        TEXT NOT NULL
);

-- The link-table twin of the row above, created now for schema symmetry and so
-- a later stage does not need a second migration to add it — but genuinely
-- UNUSED by Stage A. No Go code in this stage reads or writes it, and that is
-- not an oversight: link merge (docs/sync-model.md §3.6) is union by identity
-- `(key_a, key_b)`, which needs no stored base to detect divergence the way a
-- CAS-versioned memory does. Two machines either both have a pair or do not;
-- the only decision Stage A's link handling makes is the `reason` tie-break
-- when both sides hold the pair with a different reason, and that is decided
-- fresh from `created_at` on every run, not from anything recorded here. This
-- table is reserved for a future stage that wants to compare a link's own
-- content (its reason, say) the way memory_sync_base compares a memory's.
--
-- The composite foreign key mirrors memory_evidence_path's precedent
-- (migration 0008) for referencing a composite primary key: memory_links' key
-- is (key_a, key_b), not a single column, so link_sync_base's key must be too.
-- ON DELETE CASCADE keeps the same promise as memory_sync_base's: a severed
-- link takes its base row with it.
CREATE TABLE link_sync_base (
  key_a     TEXT NOT NULL,
  key_b     TEXT NOT NULL,
  digest    TEXT NOT NULL,
  device_id TEXT NOT NULL,
  at        TEXT NOT NULL,
  PRIMARY KEY (key_a, key_b),
  FOREIGN KEY (key_a, key_b) REFERENCES memory_links(key_a, key_b) ON DELETE CASCADE
);

-- Deletes propagate via durable tombstones, retained forever (D5,
-- docs/sync-model.md §3.7). Without one, a delete on one machine is
-- indistinguishable from a create on another, and the next sync resurrects
-- exactly what the user deleted — the reason this table exists at all.
--
-- NO FOREIGN KEY, by construction, and that absence is the point rather than
-- an omission: a tombstone exists precisely BECAUSE the row it names no longer
-- does. A REFERENCES clause here would either refuse the very insert this
-- table is for (the memory or link is already gone by the time DeleteMemory or
-- DeleteLink writes the tombstone in the same transaction) or, worse, cascade
-- the tombstone away the moment something with that identity is later
-- recreated — which would silently reopen the resurrection loop this whole
-- table exists to close.
--
-- kind distinguishes the two identity shapes sync ever deletes: `ident` is a
-- memory key for kind='memory', or "<key_a> <key_b>" (space-joined; no space
-- is legal in the key grammar, ^[a-z0-9][a-z0-9-]{0,127}$, so the join can
-- never collide with a real key) for kind='link'.
--
-- Retention: forever, on memory_verification's precedent ("NOT PRUNED BY GC,
-- deliberately") — a tombstone is a few dozen bytes and a laptop can be off
-- for a year. `doctor --gc` reports the count here and prunes nothing.
CREATE TABLE sync_tombstone (
  kind      TEXT NOT NULL CHECK (kind IN ('memory','link')),
  ident     TEXT NOT NULL,
  digest    TEXT NOT NULL,
  device_id TEXT NOT NULL,
  at        TEXT NOT NULL,
  PRIMARY KEY (kind, ident)
);

-- Per-key opt-in/opt-out for what actually leaves this machine
-- (docs/sync-model.md §6.4, D6, D11). The identical DDL is created again in
-- migrations/global/0004_sync.sql, and the two tables' CHECK and columns are
-- intentionally byte-for-byte the same — what differs between the scopes is
-- never the schema, only how internal/store.SyncableMemories() interprets an
-- ABSENT row for a given key, which is an application-layer default this
-- migration does not and cannot encode:
--
--   project scope: absent means "syncs" (mode defaults to include). A project
--   memory is about the repository, and the fingerprint exchanged at
--   `sync enable` has already proven both machines mean the same one, so
--   there is nothing left for a human to decide per key. A row here is only
--   ever an EXCLUDE, opting a specific memory back out.
--
--   global scope: absent means "does not sync" (mode defaults to exclude). A
--   global memory is about this machine or this person — the live global set
--   on the machine this design was written on holds two memories that are
--   true of one physical box and only one that is portable, spread across
--   three different `type` values, which is why the opt-in cannot be derived
--   from `type` either (memory-model.md §8: "an enum nothing branches on").
--   Only the human who wrote a global memory knows which kind it is.
--
-- Reusing one table shape for an asymmetric default, rather than two
-- differently-shaped tables, is deliberate: the asymmetry belongs to
-- SyncableMemories' default, which is exactly one `if scope == project` away
-- from being wrong in a way a schema difference could not catch, whereas a
-- misread default is one bug in one function.
CREATE TABLE sync_policy (
  key  TEXT PRIMARY KEY REFERENCES memories(key) ON DELETE CASCADE,
  mode TEXT NOT NULL CHECK (mode IN ('include','exclude')),
  at   TEXT NOT NULL
);

-- No timestamp canonicalisation to run here, unlike migration 0008's note
-- about the one pre-existing bad row in the global database: every table
-- above is new, so there are no legacy rows to repair. Every `at`/`created_at`
-- this migration's tables will ever hold is written by internal/store/sync.go
-- through store.Now(), which has produced TimeLayout-canonical text since
-- before this migration existed.
