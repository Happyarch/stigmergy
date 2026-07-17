-- Stop constraining agent_kind in the schema. Go is the validator now.
--
-- This is the last rebuild of this table, which is the point of it.
--
-- The CHECK enumerated the hosts, so every new host needed a migration to widen
-- it, and SQLite can only change a CHECK by rebuilding the table: rename,
-- recreate, copy, drop. That is not a cheap ceremony. It cost us both roots
-- indexes in 0003 — they follow the table through the rename and die with the
-- DROP, while every row survives and the migration reports success, so nothing
-- looks wrong until a claim-guard lookup that must answer in 250ms starts doing
-- a full table scan. 0004 put them back. Doing that dance again for every host
-- someone wants to add is a standing invitation to repeat it.
--
-- And it bought nothing. No agent ever reached the constraint: every write goes
-- through Registration.validate first, which checks store.AgentKinds and returns
-- a message naming the kinds it accepts. The CHECK could only fire on a bug that
-- had already bypassed that, and its failure mode — a raw SQLite constraint
-- error — is worse than the one it would be shadowing. Two declarations of the
-- same closed set, in two languages, kept in step by whoever remembered both.
--
-- So the set is declared once, in internal/hosts, and adding a host is now a Go
-- edit with no schema change: no rebuild, no index risk, and no window where the
-- repository is read-only while a binary catches up with its database.
--
-- This mirrors what 0005 already decided for the model column — nullable, no
-- CHECK, unvalidated, because "model names are somebody else's release
-- schedule". Host names are the same kind of thing.
--
-- The Go migrate function runs PRAGMA legacy_alter_table = ON before this, so
-- the child tables (claims, mailbox_threads) keep their foreign keys pointing at
-- the new roots table rather than being rewritten to roots_old.
--
-- Columns are listed explicitly rather than SELECT *: model was appended by 0005
-- via ADD COLUMN, so it sits last, and a future reader adding another column
-- deserves to fail loudly here rather than silently shift every value one place
-- to the left.

ALTER TABLE roots RENAME TO roots_old;

CREATE TABLE roots (
  root_id       TEXT PRIMARY KEY,
  agent_kind    TEXT NOT NULL,
  session_label TEXT,
  worktree      TEXT NOT NULL,
  branch        TEXT,
  registered_at TEXT NOT NULL,
  last_seen_at  TEXT NOT NULL,
  ended_at      TEXT,
  model         TEXT
);

INSERT INTO roots (
  root_id, agent_kind, session_label, worktree, branch,
  registered_at, last_seen_at, ended_at, model
)
SELECT
  root_id, agent_kind, session_label, worktree, branch,
  registered_at, last_seen_at, ended_at, model
FROM roots_old;

DROP TABLE roots_old;

-- Recreate what the rename carried off. This is the step 0003 forgot.
CREATE INDEX IF NOT EXISTS idx_roots_session ON roots(session_label) WHERE ended_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_roots_active  ON roots(last_seen_at)  WHERE ended_at IS NULL;
