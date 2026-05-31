-- +goose Up
-- +goose StatementBegin

-- Case-insensitive text, used for every email-ish column. Replaces SQLite's
-- COLLATE NOCASE. The DB role must be allowed to create extensions; the
-- in-cluster Postgres superuser handles this on first migrate.
CREATE EXTENSION IF NOT EXISTS citext;

-- ---------- grantees (unified) ----------
-- Single source of truth for the grantee concept, shared by mail + docs.
-- Adopts the gdocs naming (grantee_id/display_name/status) and grafts on the
-- mail side's email mapping (grantee_emails). All timestamps are unix seconds
-- stored as BIGINT, matching the Go store's time.Unix()/.Unix() conversions.
CREATE TABLE grantees (
  grantee_id   TEXT PRIMARY KEY,
  display_name TEXT,
  status       TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','paused')),
  created_at   BIGINT NOT NULL
);

-- Sender email -> grantee mapping for mail attribution.
CREATE TABLE grantee_emails (
  email      CITEXT PRIMARY KEY,
  grantee_id TEXT NOT NULL REFERENCES grantees(grantee_id) ON DELETE CASCADE
);
CREATE INDEX idx_grantee_emails_grantee ON grantee_emails(grantee_id);

-- Authorized Drive folders/docs for a grantee (docs side).
CREATE TABLE grantee_sources (
  id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  grantee_id  TEXT NOT NULL REFERENCES grantees(grantee_id) ON DELETE CASCADE,
  source_type TEXT NOT NULL CHECK (source_type IN ('folder','doc')),
  drive_id    TEXT NOT NULL,
  added_at    BIGINT NOT NULL,
  UNIQUE(grantee_id, drive_id)
);
CREATE INDEX idx_sources_grantee ON grantee_sources(grantee_id);

-- ---------- mail ----------
CREATE TABLE threads (
  id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  root_message_id TEXT UNIQUE NOT NULL,
  subject_norm    TEXT,
  grantee_id      TEXT REFERENCES grantees(grantee_id) ON DELETE SET NULL,
  first_seen_at   BIGINT NOT NULL,
  last_seen_at    BIGINT NOT NULL
);
CREATE INDEX idx_threads_grantee ON threads(grantee_id);
CREATE INDEX idx_threads_subject ON threads(subject_norm);

CREATE TABLE messages (
  id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  message_id     TEXT UNIQUE NOT NULL,
  thread_id      BIGINT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  grantee_id     TEXT REFERENCES grantees(grantee_id) ON DELETE SET NULL,
  folder         TEXT NOT NULL,
  uid            BIGINT NOT NULL,
  uid_validity   BIGINT NOT NULL,
  in_reply_to    TEXT,
  references_raw TEXT,
  from_addr      CITEXT NOT NULL,
  from_name      TEXT,
  to_addrs       TEXT NOT NULL,
  cc_addrs       TEXT,
  subject        TEXT,
  date           BIGINT NOT NULL,
  body_text      TEXT,
  body_html      TEXT,
  size_bytes     BIGINT NOT NULL,
  indexed_at     BIGINT NOT NULL,
  UNIQUE(folder, uid_validity, uid)
);
CREATE INDEX idx_messages_from    ON messages(from_addr);
CREATE INDEX idx_messages_subject ON messages(subject);
CREATE INDEX idx_messages_thread  ON messages(thread_id);
CREATE INDEX idx_messages_grantee ON messages(grantee_id);
CREATE INDEX idx_messages_date    ON messages(date);

CREATE TABLE message_references (
  message_id BIGINT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  ref        TEXT NOT NULL,
  PRIMARY KEY (message_id, ref)
);
CREATE INDEX idx_message_references_ref ON message_references(ref);

CREATE TABLE message_recipients (
  message_id BIGINT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL,
  email      CITEXT NOT NULL,
  name       TEXT,
  PRIMARY KEY (message_id, kind, email)
);
CREATE INDEX idx_recipients_email ON message_recipients(email);

CREATE TABLE attachments (
  id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  message_id BIGINT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  filename   TEXT,
  mime_type  TEXT,
  size_bytes BIGINT NOT NULL,
  sha256     TEXT NOT NULL,
  path       TEXT NOT NULL
);
CREATE INDEX idx_attachments_sha256 ON attachments(sha256);
CREATE INDEX idx_attachments_message ON attachments(message_id);

CREATE TABLE mailbox_state (
  folder       TEXT PRIMARY KEY,
  uid_validity BIGINT NOT NULL,
  last_uid     BIGINT NOT NULL,
  updated_at   BIGINT NOT NULL
);

-- ---------- docs ----------
CREATE TABLE docs (
  doc_id            TEXT PRIMARY KEY,
  grantee_id        TEXT NOT NULL REFERENCES grantees(grantee_id) ON DELETE CASCADE,
  title             TEXT NOT NULL,
  owner_email       CITEXT NOT NULL,
  content_markdown  TEXT NOT NULL,
  modified_at       BIGINT NOT NULL,
  synced_at         BIGINT NOT NULL,
  source_type       TEXT NOT NULL,
  source_drive_id   TEXT NOT NULL,
  had_images        BOOLEAN NOT NULL DEFAULT FALSE,
  had_comments      BOOLEAN NOT NULL DEFAULT FALSE,
  status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','stale','error')),
  last_error        TEXT,
  last_seen_cycle   BIGINT
);
CREATE INDEX idx_docs_grantee ON docs(grantee_id);
CREATE INDEX idx_docs_status  ON docs(status);

CREATE TABLE unregistered_docs (
  doc_id      TEXT PRIMARY KEY,
  owner_email CITEXT,
  title       TEXT,
  mime_type   TEXT,
  first_seen  BIGINT NOT NULL,
  last_seen   BIGINT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','ignored','blocked','registered'))
);

-- Forward-compat placeholder for the future shadow-inbox MCP.
CREATE TABLE blocked_owners (
  owner_email CITEXT PRIMARY KEY,
  blocked_at  BIGINT NOT NULL,
  reason      TEXT
);

CREATE TABLE sync_state (
  k TEXT PRIMARY KEY,
  v TEXT
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS sync_state;
DROP TABLE IF EXISTS blocked_owners;
DROP TABLE IF EXISTS unregistered_docs;
DROP TABLE IF EXISTS docs;
DROP TABLE IF EXISTS mailbox_state;
DROP TABLE IF EXISTS attachments;
DROP TABLE IF EXISTS message_recipients;
DROP TABLE IF EXISTS message_references;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS threads;
DROP TABLE IF EXISTS grantee_sources;
DROP TABLE IF EXISTS grantee_emails;
DROP TABLE IF EXISTS grantees;
-- +goose StatementEnd
