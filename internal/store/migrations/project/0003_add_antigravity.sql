-- Add 'antigravity' as a valid agent_kind.
--
-- SQLite cannot alter an existing CHECK constraint with ALTER TABLE, so we
-- rename the table, recreate it with the updated constraint, copy the data,
-- then drop the old table. All existing rows remain: the new constraint is a
-- superset of the old one.
--
-- The Go migrate function runs PRAGMA legacy_alter_table = ON before this runs,
-- so child tables (claims, mailbox_threads) will retain their foreign keys
-- pointing to the new roots table instead of being rewritten to roots_old.
--
-- A rebuild must also recreate the table's indexes: they follow the table
-- through the rename onto roots_old, and the DROP below takes them with it.
-- Databases that ran an earlier version of this migration lost both and get
-- them back in 0004.

ALTER TABLE roots RENAME TO roots_old;

CREATE TABLE roots (
  root_id       TEXT PRIMARY KEY,
  agent_kind    TEXT NOT NULL CHECK (agent_kind IN ('claude-code','codex','antigravity')),
  session_label TEXT,
  worktree      TEXT NOT NULL,
  branch        TEXT,
  registered_at TEXT NOT NULL,
  last_seen_at  TEXT NOT NULL,
  ended_at      TEXT
);

INSERT INTO roots SELECT * FROM roots_old;

DROP TABLE roots_old;

CREATE INDEX IF NOT EXISTS idx_roots_session ON roots(session_label) WHERE ended_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_roots_active  ON roots(last_seen_at)  WHERE ended_at IS NULL;
