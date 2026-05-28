CREATE TABLE IF NOT EXISTS grantees (
  grantee_id   TEXT PRIMARY KEY,
  owner_email  TEXT NOT NULL COLLATE NOCASE,
  display_name TEXT,
  status       TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','paused')),
  created_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_grantees_owner ON grantees(owner_email);

CREATE TABLE IF NOT EXISTS grantee_sources (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  grantee_id  TEXT NOT NULL REFERENCES grantees(grantee_id) ON DELETE CASCADE,
  source_type TEXT NOT NULL CHECK (source_type IN ('folder','doc')),
  drive_id    TEXT NOT NULL,
  added_at    INTEGER NOT NULL,
  UNIQUE(grantee_id, drive_id)
);
CREATE INDEX IF NOT EXISTS idx_sources_grantee ON grantee_sources(grantee_id);

CREATE TABLE IF NOT EXISTS docs (
  doc_id            TEXT PRIMARY KEY,
  grantee_id        TEXT NOT NULL REFERENCES grantees(grantee_id) ON DELETE CASCADE,
  title             TEXT NOT NULL,
  owner_email       TEXT NOT NULL COLLATE NOCASE,
  content_markdown  TEXT NOT NULL,
  modified_at       INTEGER NOT NULL,
  synced_at         INTEGER NOT NULL,
  source_type       TEXT NOT NULL,
  source_drive_id   TEXT NOT NULL,
  had_images        INTEGER NOT NULL DEFAULT 0,
  had_comments      INTEGER NOT NULL DEFAULT 0,
  status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','stale','error')),
  last_error        TEXT,
  last_seen_cycle   INTEGER
);
CREATE INDEX IF NOT EXISTS idx_docs_grantee ON docs(grantee_id);
CREATE INDEX IF NOT EXISTS idx_docs_status  ON docs(status);

CREATE TABLE IF NOT EXISTS unregistered_docs (
  doc_id      TEXT PRIMARY KEY,
  owner_email TEXT,
  title       TEXT,
  mime_type   TEXT,
  first_seen  INTEGER NOT NULL,
  last_seen   INTEGER NOT NULL,
  status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','ignored','blocked','registered'))
);

-- Forward-compat placeholder. v1 sync does not read this; the future
-- shadow-inbox MCP will write to it.
CREATE TABLE IF NOT EXISTS blocked_owners (
  owner_email TEXT PRIMARY KEY COLLATE NOCASE,
  blocked_at  INTEGER NOT NULL,
  reason      TEXT
);

CREATE TABLE IF NOT EXISTS sync_state (
  k TEXT PRIMARY KEY,
  v TEXT
);
