-- Restore the two roots indexes that 0003 destroyed.
--
-- 0003 changed the agent_kind CHECK constraint the only way SQLite allows:
-- rename, recreate, copy, drop. What it missed is that indexes follow their
-- table through the rename. ALTER TABLE roots RENAME TO roots_old carried
-- idx_roots_session and idx_roots_active onto roots_old, the recreated roots
-- table had none, and DROP TABLE roots_old took both with it. The migration
-- reported success and every row survived, so nothing looked wrong.
--
-- The cost is not academic. idx_roots_session serves RootBySession, which the
-- claim guard calls on every edit under a 250ms budget; without it that lookup
-- is a full table scan. idx_roots_active serves the active-root sweep.
--
-- IF NOT EXISTS because both kinds of database arrive here: one created before
-- 0003 shipped has already lost the indexes, while a fresh one rebuilt them in
-- 0003 itself. Either way this leaves exactly the indexes 0001 declared.

CREATE INDEX IF NOT EXISTS idx_roots_session ON roots(session_label) WHERE ended_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_roots_active  ON roots(last_seen_at)  WHERE ended_at IS NULL;
