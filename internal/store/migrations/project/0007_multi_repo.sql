-- A project may span several git repositories.
--
-- The claim is the only thing in this schema whose meaning depends on which one.
-- A scope_path is repo-relative, so "README.md" names a different file in each
-- member, and without a repo the two would be the same claim: an agent editing
-- the client's README would be blocked by someone editing the service's.
--
-- Nothing else needs the dimension, and adding it anywhere else would cost more
-- than it bought:
--
--   memories  project-wide sharing is the POINT of a multi-repo project — the
--             whole reason to have one is that what you learn about the client
--             applies while you are in the service. It is also the table it is
--             most dangerous to touch: memories_fts is an external-content FTS5
--             index whose three triggers enumerate columns literally, and the
--             search snippet addresses body by column INDEX.
--   roots     a root is an agent, not a directory. Registration already resolves
--             a session to one root wherever it is standing (see RegisterRoot);
--             a repo column would re-introduce exactly the split that fixed.
--   mailbox   addressed root to root, and roots are project-wide.
--   audit_log target is free text. Spell it "repo:path" and the trail reads
--             correctly with no schema change at all.
--
-- ADD COLUMN only. Nothing here rebuilds a table, so idx_claims_open and both
-- roots indexes survive untouched — 0003 rebuilt roots to widen a CHECK and
-- silently took both its indexes with it, and 0004 had to put them back. That is
-- the standing lesson of this directory: prefer a column to a rebuild.
--
-- No new index either. The covering test does not run in SQL: ClaimsCovering
-- loads every active claim and filters in Go, because overlap is component-wise
-- and expressing that as a LIKE pattern is easy to get subtly wrong. The active
-- set is a handful of rows, so an index on repo_id would buy nothing and carry
-- the rebuild risk above for it.

CREATE TABLE repos (
  repo_id    TEXT PRIMARY KEY,      -- the name agents type in a "repo:path" scope
  common_dir TEXT NOT NULL UNIQUE,  -- git common dir, on THIS machine
  worktree   TEXT NOT NULL,         -- main worktree root, on THIS machine
  added_at   TEXT NOT NULL
);

-- '' is not a placeholder waiting to be tidied away. It is the stored spelling
-- of "the sole repository of a single-repo project", and every read maps it to
-- whichever repo the database was opened from.
--
-- That mapping is what makes the upgrade safe. A v6 database that has been
-- migrated but whose backfill has not yet run still enforces its claims exactly
-- as before — so the window during which a project is broken by a new binary is
-- the time it takes to install one, not the time it takes to get round to
-- running `stigmergy doctor` in it.
ALTER TABLE claims ADD COLUMN repo_id TEXT NOT NULL DEFAULT '';
