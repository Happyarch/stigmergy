-- Episodes: history, not state (docs/association-model.md §4). `memories` is
-- "the state of the system, not its history" (gc.go); this is the other half,
-- finally first-class. "Session N tried approach X; it failed because Y" is
-- actor-bound, time-bound, and true forever AS A RECORD OF WHAT HAPPENED —
-- even if the reasoning it records later turns out wrong.
--
-- IMMUTABLE. No update tool exists, ever, at the Go layer. A wrong episode is
-- corrected by a NEW episode linked 'corrects', never by rewriting this one —
-- what was believed stays written, and what is now believed is a later
-- record. This is the episodic analogue of never inferring a verification
-- from a write (memory-model §7).
--
-- Project scope only. Episodes are session-bound events in a repo; the
-- global database has no roots and no sessions to bind them to. A
-- machine-wide lesson worth keeping is, by definition, already semantic —
-- promote a memory instead.
CREATE TABLE episodes (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  title      TEXT NOT NULL,          -- ValidateLine
  body       TEXT NOT NULL,          -- ValidateBlock; immutable once written
  actor      TEXT NOT NULL,
  agent_kind TEXT NOT NULL,
  at         TEXT NOT NULL
);

-- Provenance: this episode grounds that memory. Gives a semantic claim a
-- citation — this is where the lesson came from — and is what GC (§10.3)
-- checks before reclaiming an episode: distilled-and-cited history stays.
CREATE TABLE episode_memory (
  episode_id INTEGER NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
  key        TEXT NOT NULL REFERENCES memories(key) ON DELETE CASCADE,
  note       TEXT NOT NULL,
  PRIMARY KEY (episode_id, key)
);

-- Correction and continuation chains. A successor always surfaces alongside
-- the episode it points from — superseded reasoning is never read without
-- its correction (the §5 push principle, applied to episodes) — so an
-- immutable record plus an explicit chain gives the record of belief over
-- time that an edit tool would otherwise have to reconstruct from nothing.
CREATE TABLE episode_links (
  episode_id   INTEGER NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
  successor_id INTEGER NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
  kind         TEXT NOT NULL CHECK (kind IN ('corrects','continues')),
  PRIMARY KEY (episode_id, successor_id),
  CHECK (episode_id != successor_id)
);

-- A NEW virtual table. memories_fts and its triggers are untouched by this
-- migration; the FTS law (docs/association-model.md §7) is that the schema
-- of `memories` is frozen to this design, not that episodes may not have
-- their own index. episode_list(query) is the episodic search surface —
-- memory_search stays memories-only, the same reason it never grew time
-- filters: an extra result class interacting with MaxSearchHits truncates
-- differently than an agent expects.
CREATE VIRTUAL TABLE episodes_fts USING fts5(
  title, body,
  content='episodes', content_rowid='id',
  tokenize='porter unicode61'
);

-- Cloned from memories_fts's trigger pattern (migrations/project/0001_init.sql)
-- rather than sharing code with it: the two content tables have different
-- columns, and the 'delete' form of INSERT INTO ...(fts_table, ...) is the
-- required incantation for a content-table FTS5 index — a plain DELETE would
-- corrupt it rather than update it.
CREATE TRIGGER episodes_ai AFTER INSERT ON episodes BEGIN
  INSERT INTO episodes_fts(rowid, title, body)
  VALUES (new.id, new.title, new.body);
END;

CREATE TRIGGER episodes_ad AFTER DELETE ON episodes BEGIN
  INSERT INTO episodes_fts(episodes_fts, rowid, title, body)
  VALUES ('delete', old.id, old.title, old.body);
END;

CREATE TRIGGER episodes_au AFTER UPDATE ON episodes BEGIN
  INSERT INTO episodes_fts(episodes_fts, rowid, title, body)
  VALUES ('delete', old.id, old.title, old.body);
  INSERT INTO episodes_fts(rowid, title, body)
  VALUES (new.id, new.title, new.body);
END;
