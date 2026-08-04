-- An explicit, untyped, symmetric association between two memories in this
-- scope: "these are related, and here is why." See docs/association-model.md
-- Part I §3 for the reasoning; this migration only records the decisions.
--
-- UNTYPED. A vocabulary of relation kinds (supersedes, part-of, refutes...) is
-- only defensible once something branches on each kind, and nothing here does
-- — it would be the `type` column's failure repeated with more enum values.
-- The mandatory free-text reason carries the semantics instead, for an LLM
-- reader that can interpret prose far richer than any enum.
--
-- SYMMETRIC, stored once. CHECK (key_a < key_b) is total under the key
-- grammar (^[a-z0-9][a-z0-9-]{0,127}$, a well-defined byte order), so it makes
-- symmetry structural: exactly one row per pair, a self-link is impossible by
-- construction, the primary key prefix serves key_a lookups and the extra
-- index below serves key_b.
--
-- NO WEIGHTS, NO DECAY, NO USE-COUNTERS. memory-model.md Appendix A.1 rejected
-- base-level activation for memories themselves; an edge strength column would
-- reintroduce exactly that one level up, with no portable unit to define it
-- (memory-model.md §11).
--
-- NO CAS. A link is cheap, attributed, and repairable by unlink — demanding
-- expected_version for both endpoints would tax exactly the behavior this is
-- meant to encourage. A duplicate pair is rejected rather than silently
-- ignored, and the rejection carries the existing edge so the agent can merge
-- reasons (unlink + relink) instead of losing one.
--
-- Same-scope only: project links reference project memories, global links
-- reference global memories. The two databases cannot share a transaction or
-- a foreign key (see PromoteMemory's ordering comment in memories.go), and the
-- governing principle — the agent's weights already ARE the general semantic
-- network — removes the motivation for a cross-scope edge in the first place.
--
-- No ON UPDATE CASCADE: no operation renames a memory key, so the promise
-- would be kept one level deep and nowhere else.
CREATE TABLE memory_links (
  key_a      TEXT NOT NULL REFERENCES memories(key) ON DELETE CASCADE,
  key_b      TEXT NOT NULL REFERENCES memories(key) ON DELETE CASCADE,
  reason     TEXT NOT NULL,     -- the encoding context; never empty
  created_by TEXT NOT NULL,
  agent_kind TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (key_a, key_b),
  CHECK (key_a < key_b)
);

CREATE INDEX idx_memory_links_b ON memory_links(key_b);

-- memories itself is untouched, so memories_fts and its three triggers are not
-- involved in any of this.
