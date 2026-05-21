CREATE TABLE IF NOT EXISTS grantees (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  created_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS grantee_emails (
  email        TEXT PRIMARY KEY COLLATE NOCASE,
  grantee_id   TEXT NOT NULL REFERENCES grantees(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS threads (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  root_message_id TEXT UNIQUE NOT NULL,
  subject_norm    TEXT,
  grantee_id      TEXT REFERENCES grantees(id) ON DELETE SET NULL,
  first_seen_at   INTEGER NOT NULL,
  last_seen_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_threads_grantee ON threads(grantee_id);
CREATE INDEX IF NOT EXISTS idx_threads_subject ON threads(subject_norm);

CREATE TABLE IF NOT EXISTS messages (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  message_id     TEXT UNIQUE NOT NULL,
  thread_id      INTEGER NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  grantee_id     TEXT REFERENCES grantees(id) ON DELETE SET NULL,
  folder         TEXT NOT NULL,
  uid            INTEGER NOT NULL,
  uid_validity   INTEGER NOT NULL,
  in_reply_to    TEXT,
  references_raw TEXT,
  from_addr      TEXT NOT NULL COLLATE NOCASE,
  from_name      TEXT,
  to_addrs       TEXT NOT NULL,
  cc_addrs       TEXT,
  subject        TEXT,
  date           INTEGER NOT NULL,
  body_text      TEXT,
  body_html      TEXT,
  size_bytes     INTEGER NOT NULL,
  indexed_at     INTEGER NOT NULL,
  UNIQUE(folder, uid_validity, uid)
);
CREATE INDEX IF NOT EXISTS idx_messages_from    ON messages(from_addr);
CREATE INDEX IF NOT EXISTS idx_messages_subject ON messages(subject);
CREATE INDEX IF NOT EXISTS idx_messages_thread  ON messages(thread_id);
CREATE INDEX IF NOT EXISTS idx_messages_grantee ON messages(grantee_id);
CREATE INDEX IF NOT EXISTS idx_messages_date    ON messages(date);

CREATE TABLE IF NOT EXISTS message_references (
  message_id   INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  ref          TEXT NOT NULL,
  PRIMARY KEY (message_id, ref)
);
CREATE INDEX IF NOT EXISTS idx_message_references_ref ON message_references(ref);

CREATE TABLE IF NOT EXISTS message_recipients (
  message_id   INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  kind         TEXT NOT NULL,
  email        TEXT NOT NULL COLLATE NOCASE,
  name         TEXT,
  PRIMARY KEY (message_id, kind, email)
);
CREATE INDEX IF NOT EXISTS idx_recipients_email ON message_recipients(email);

CREATE TABLE IF NOT EXISTS attachments (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  message_id   INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  filename     TEXT,
  mime_type    TEXT,
  size_bytes   INTEGER NOT NULL,
  sha256       TEXT NOT NULL,
  path         TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_attachments_sha256 ON attachments(sha256);
CREATE INDEX IF NOT EXISTS idx_attachments_message ON attachments(message_id);

CREATE TABLE IF NOT EXISTS mailbox_state (
  folder       TEXT PRIMARY KEY,
  uid_validity INTEGER NOT NULL,
  last_uid     INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);
