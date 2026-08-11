-- Identical DDL to project's 0014_sync.sql — see that file for the full
-- rationale on each table, in particular why sync_tombstone carries no foreign
-- key and why the two link_sync_base/memory_sync_base pairs cascade off their
-- parent rows. Four new tables, nothing existing touched: no ADD COLUMN, no
-- rebuild, so none of the existing indexes in this database are at risk.
--
-- Only one thing differs between the two migrations, and it lives in
-- application code rather than in this file: internal/store.SyncableMemories()
-- reads an ABSENT sync_policy row as "syncs" in the project database and as
-- "does not sync" in this one (docs/sync-model.md §6.4, D6). A project memory
-- is about the repository, and the fingerprint exchanged at `sync enable` has
-- already proven both machines mean the same one — so a project memory syncs
-- unless a human opts it out. A global memory is about this machine or this
-- person, and only the human who wrote it knows which; the live global set on
-- the machine this design was written on holds two memories that are facts
-- about one physical box and only one that is portable, spread across three
-- different `type` values (memory-model.md §8's "type is an enum nothing
-- branches on", refuted yet again by the live data) — so a global memory stays
-- private unless a human opts it in. The schema below cannot express that
-- asymmetry and does not try to; it is identical in both databases on purpose,
-- so the only place the rule can be wrong is the one function that reads it.
CREATE TABLE memory_sync_base (
  key       TEXT PRIMARY KEY REFERENCES memories(key) ON DELETE CASCADE,
  digest    TEXT NOT NULL,
  device_id TEXT NOT NULL,
  at        TEXT NOT NULL
);

-- Reserved for a future stage, exactly as in the project database — Stage A
-- does not read or write it. See 0014_sync.sql's comment on the same table.
CREATE TABLE link_sync_base (
  key_a     TEXT NOT NULL,
  key_b     TEXT NOT NULL,
  digest    TEXT NOT NULL,
  device_id TEXT NOT NULL,
  at        TEXT NOT NULL,
  PRIMARY KEY (key_a, key_b),
  FOREIGN KEY (key_a, key_b) REFERENCES memory_links(key_a, key_b) ON DELETE CASCADE
);

-- No foreign key, by construction: a tombstone exists precisely because the
-- row it names no longer does, in this database exactly as in the project one.
-- Retention is forever; doctor --gc reports the count and prunes nothing.
CREATE TABLE sync_tombstone (
  kind      TEXT NOT NULL CHECK (kind IN ('memory','link')),
  ident     TEXT NOT NULL,
  digest    TEXT NOT NULL,
  device_id TEXT NOT NULL,
  at        TEXT NOT NULL,
  PRIMARY KEY (kind, ident)
);

-- Per-key opt-in/opt-out. The DDL is byte-for-byte the same CHECK and columns
-- as the project database's sync_policy — see the comment at the top of this
-- file for where the two scopes actually diverge (they do not diverge here).
CREATE TABLE sync_policy (
  key  TEXT PRIMARY KEY REFERENCES memories(key) ON DELETE CASCADE,
  mode TEXT NOT NULL CHECK (mode IN ('include','exclude')),
  at   TEXT NOT NULL
);

-- No timestamp canonicalisation to run here: every table above is new, with no
-- legacy rows, and every stamp it will ever hold is written by
-- internal/store/sync.go through store.Now().
