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

CREATE TABLE roots (
  root_id       TEXT PRIMARY KEY,
  agent_kind    TEXT NOT NULL CHECK (agent_kind IN ('claude-code','codex')),
  session_label TEXT,
  worktree      TEXT NOT NULL,
  branch        TEXT,
  registered_at TEXT NOT NULL,
  last_seen_at  TEXT NOT NULL,
  ended_at      TEXT
);

CREATE INDEX idx_roots_session ON roots(session_label) WHERE ended_at IS NULL;
CREATE INDEX idx_roots_active  ON roots(last_seen_at)  WHERE ended_at IS NULL;

CREATE TABLE claims (
  id          INTEGER PRIMARY KEY,
  scope_path  TEXT NOT NULL,
  recursive   INTEGER NOT NULL CHECK (recursive IN (0,1)),
  root_id     TEXT NOT NULL REFERENCES roots(root_id),
  worktree    TEXT NOT NULL,
  branch      TEXT,
  reason      TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  expires_at  TEXT NOT NULL,
  released_at TEXT
);

CREATE INDEX idx_claims_open ON claims(expires_at) WHERE released_at IS NULL;

CREATE TABLE mailbox_threads (
  id         INTEGER PRIMARY KEY,
  claim_id   INTEGER REFERENCES claims(id),
  created_by TEXT NOT NULL REFERENCES roots(root_id),
  state      TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open','resolved','abandoned')),
  resolution TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE mailbox_messages (
  id        INTEGER PRIMARY KEY,
  thread_id INTEGER NOT NULL REFERENCES mailbox_threads(id),
  from_root TEXT NOT NULL,
  to_root   TEXT NOT NULL,
  subject   TEXT NOT NULL,
  body      TEXT NOT NULL,
  sent_at   TEXT NOT NULL,
  read_at   TEXT
);

CREATE INDEX idx_msgs_inbox ON mailbox_messages(to_root) WHERE read_at IS NULL;

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
