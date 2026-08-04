-- Dedup for the end-of-turn priming nudge (docs/association-model.md §9): a
-- session hears about a memory exactly once, not once per Stop hook it hits
-- while still touching the same claimed files.
--
-- Keyed on the SESSION's own root, not any agent that ran inside it —
-- priming rides the Stop hook, which only ever fires for the session's main
-- thread (see internal/cli/hook.go's mail-gate, whose dedup this mirrors).
-- The cue set that produces a note can span the session's agents' claims
-- too (RootsOfSession), but only the session is ever shown the note, so only
-- the session's root needs to remember having seen it.
--
-- Marked AFTER the note text is composed, never before — the notified_at
-- philosophy: a crash between composing and delivering must not eat the
-- note by having already recorded it as delivered.
CREATE TABLE priming_delivered (
  root_id TEXT NOT NULL REFERENCES roots(root_id) ON DELETE CASCADE,
  key     TEXT NOT NULL REFERENCES memories(key) ON DELETE CASCADE,
  at      TEXT NOT NULL,
  PRIMARY KEY (root_id, key)
);

-- Project scope only: priming walks claims and evidence policies, both of
-- which are project-only concepts. The global database has no roots, no
-- claims, and no evidence to prime from.
