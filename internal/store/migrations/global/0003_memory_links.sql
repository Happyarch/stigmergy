-- Identical to project's 0011_memory_links.sql — see that file's comments for
-- the reasoning. Links are same-scope only (docs/association-model.md §1), so
-- each database carries its own table rather than there being one shared
-- notion of a link that could ever cross the project/global boundary.
CREATE TABLE memory_links (
  key_a      TEXT NOT NULL REFERENCES memories(key) ON DELETE CASCADE,
  key_b      TEXT NOT NULL REFERENCES memories(key) ON DELETE CASCADE,
  reason     TEXT NOT NULL,
  created_by TEXT NOT NULL,
  agent_kind TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (key_a, key_b),
  CHECK (key_a < key_b)
);

CREATE INDEX idx_memory_links_b ON memory_links(key_b);
