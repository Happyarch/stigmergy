-- An evidence policy says where to LOOK when assessing a memory. It does not
-- say what the memory is about.
--
-- That distinction is the whole design. A field claiming "this memory is about
-- the code" asserts something about the world that can be wrong with nothing
-- able to detect it — which is exactly what `type` already is, an enum nothing
-- branches on and which therefore means whatever each writer hoped. What is
-- stored here instead is operational: *when assessing this memory, observe
-- changes in these repositories and these paths, starting from these commits.*
-- Every result echoes that boundary back, so a reader can see what was and was
-- not looked at. It still cannot tell that the declared scope was the WRONG
-- scope; nothing can. Inspectable is the achievable property, not correct.
--
-- Project scope only. There is no global equivalent and there must not be: git
-- evidence is undefined for a memory about this machine, and the global scope
-- has no repository to measure. memory_evidence_set rejects the global scope
-- outright rather than accepting it and quietly doing nothing.

CREATE TABLE memory_evidence_policy (
  key        TEXT PRIMARY KEY REFERENCES memories(key) ON DELETE CASCADE,
  version    INTEGER NOT NULL DEFAULT 1,
  updated_by TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

-- One row per repository the policy observes, carrying the commit that
-- observation starts from.
--
-- base_oid is compared by REACHABILITY, never by date. `%ct` is the committer
-- date, not the time a commit arrived here: rebase, cherry-pick and fast-forward
-- all land commits after this baseline carrying dates from before it, so any
-- --since window silently misses real change. A stored object id has no such
-- failure mode, and it is the reason this table exists at all rather than the
-- design getting by with a timestamp and no migration.
--
-- The repos FK is load-bearing. `stigmergy project remove` deletes a member, and
-- its evidence must go with it — otherwise the policy keeps a row nobody can
-- resolve, which reports missing_worktree forever and reads like a broken
-- repository rather than a departed one.
CREATE TABLE memory_evidence_member (
  key         TEXT NOT NULL REFERENCES memory_evidence_policy(key) ON DELETE CASCADE,
  repo_id     TEXT NOT NULL REFERENCES repos(repo_id) ON DELETE CASCADE,
  base_oid    TEXT NOT NULL,
  captured_at TEXT NOT NULL,
  PRIMARY KEY (key, repo_id)
);

-- Optional path narrowing, one row per pattern.
--
-- ZERO ROWS FOR A MEMBER MEANS THE WHOLE MEMBER, and that is the conservative
-- default rather than an unconfigured state. A too-narrow anchor undercounts
-- silently and reads as plausibly clean, which is the worst failure available
-- here: it produces confident quiet. Whole-repository observation over-reports
-- instead, which is visible and arguable.
--
-- Separate rows rather than a delimited TEXT column because git filenames may
-- legally contain newlines — there is no safe delimiter — and because a column
-- of joined text cannot distinguish a literal from a glob, cannot be validated
-- per pattern, and cannot deduplicate.
--
-- kind is CHECKed here, which is affordable precisely because this table is new:
-- the standing lesson of this directory is that changing a CHECK later means
-- rebuilding the table, and 0003 lost both of roots' indexes doing exactly that
-- while reporting success.
CREATE TABLE memory_evidence_path (
  key     TEXT NOT NULL,
  repo_id TEXT NOT NULL,
  kind    TEXT NOT NULL CHECK (kind IN ('literal','glob')),
  pattern TEXT NOT NULL,
  PRIMARY KEY (key, repo_id, kind, pattern),
  FOREIGN KEY (key, repo_id) REFERENCES memory_evidence_member(key, repo_id) ON DELETE CASCADE
);

-- No ON UPDATE CASCADE anywhere, deliberately. Declaring it on the policy table
-- alone would cascade one level and then fail at the next, and no operation
-- renames a memory key in the first place. A promise kept halfway is worse than
-- one never made: it reads as covered.
--
-- No timestamp canonicalisation here either — see docs/memory-model.md §14.5.
-- The known bad row is in the GLOBAL database, which this migration cannot
-- reach, and robust RFC3339 normalisation is not a job for static embedded SQL.
-- `stigmergy doctor` does it, in both scopes.
--
-- memories itself is untouched, so memories_fts and its three triggers are not
-- involved in any of this.
