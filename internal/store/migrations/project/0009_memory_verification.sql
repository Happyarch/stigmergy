-- When someone actually CHECKED a memory, and what they concluded.
--
-- This is the assertion time the model has been missing from the start.
-- memories.updated_at is last MUTATION — a typo fix moves it exactly as far as a
-- rewrite does — so nothing in the schema has ever recorded that a human or an
-- agent looked at a proposition and found it still true. This table does, and
-- only ever because someone said so explicitly.
--
-- NEVER INFERRED. Not from a write, not from byte equality, not from a read. An
-- identical rewrite is not proof anyone verified anything, and a changed body may
-- be a typo fix, a reformat, or an elaboration. The one thing that lands a row
-- here is an agent explicitly stating an outcome.
--
-- It is also why the sampling is biased and must be treated as such: verification
-- is SELECTED, never random. Agents check what they are already suspicious of, so
-- the refuted rate here is not the refuted rate in the world. Anyone who later
-- calibrates against this has to model that, and this comment is the warning.

CREATE TABLE memory_verification (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  key            TEXT NOT NULL REFERENCES memories(key) ON DELETE CASCADE,
  outcome        TEXT NOT NULL CHECK (outcome IN ('reaffirmed','revised','refuted')),
  -- The version that was ASSESSED. Without it a row says "someone approved of
  -- this memory" without saying which text they were looking at, and a later
  -- rewrite silently inherits the approval.
  memory_version INTEGER NOT NULL,
  -- The evidence policy in force at the time, if any. NULL means the memory had
  -- none — the judgement was made without change evidence, which is a legitimate
  -- and common way to verify something and must stay distinguishable from having
  -- had evidence that showed nothing.
  policy_version INTEGER,
  -- The evidence record exactly as it stood when the call was made, or NULL.
  --
  -- Snapshotted rather than recomputed later, and this is the point of the whole
  -- table: an outcome is only calibratable against what the observer could
  -- actually see. Re-running the measurement next year answers a different
  -- question, because HEAD has moved and the counts have all changed.
  evidence       TEXT,
  reason         TEXT,
  actor          TEXT NOT NULL,
  agent_kind     TEXT NOT NULL,
  at             TEXT NOT NULL
);

-- Every read is "the history of one memory, in order".
CREATE INDEX idx_memory_verification_key ON memory_verification(key, at);

-- NOT PRUNED BY GC, deliberately, unlike audit_log and resolved mail.
--
-- This table IS the thing Stage 3 exists to accumulate. Ranking, thresholds and
-- any notion of which memories are worth re-checking are all undecidable until
-- there is enough history here to calibrate against, and a retention window
-- would quietly cap that history at the exact moment it started being useful.
-- It is a handful of rows per memory per year; there is nothing to reclaim.
--
-- No ON UPDATE CASCADE, for the same reason as 0008: nothing renames a memory
-- key, and a promise kept one level deep reads as covered while failing at the
-- next.
