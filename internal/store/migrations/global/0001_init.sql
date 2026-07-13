CREATE TABLE meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE memories (
  key         TEXT PRIMARY KEY,
  type        TEXT NOT NULL CHECK (type IN ('user','feedback','project','reference')),
  description TEXT NOT NULL,
  body        TEXT NOT NULL,
  version     INTEGER NOT NULL DEFAULT 1,
  updated_by  TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

CREATE VIRTUAL TABLE memories_fts USING fts5(
  key, description, body,
  content='memories', content_rowid='rowid',
  tokenize='porter unicode61'
);

CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
  INSERT INTO memories_fts(rowid, key, description, body)
  VALUES (new.rowid, new.key, new.description, new.body);
END;

CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
  INSERT INTO memories_fts(memories_fts, rowid, key, description, body)
  VALUES ('delete', old.rowid, old.key, old.description, old.body);
END;

CREATE TRIGGER memories_au AFTER UPDATE ON memories BEGIN
  INSERT INTO memories_fts(memories_fts, rowid, key, description, body)
  VALUES ('delete', old.rowid, old.key, old.description, old.body);
  INSERT INTO memories_fts(rowid, key, description, body)
  VALUES (new.rowid, new.key, new.description, new.body);
END;

CREATE TABLE audit_log (
  id         INTEGER PRIMARY KEY,
  at         TEXT NOT NULL,
  actor      TEXT,
  agent_kind TEXT,
  action     TEXT NOT NULL,
  target     TEXT,
  detail     TEXT
);

CREATE INDEX idx_audit_at ON audit_log(at);
