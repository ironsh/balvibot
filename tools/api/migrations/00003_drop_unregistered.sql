-- +goose Up
-- +goose StatementBegin

-- Shared-but-not-whitelisted docs are now simply ignored: the sync loop only
-- fetches sources explicitly registered in grantee_sources. Drop the discovery
-- table and the unused shadow-inbox placeholder it was meant to feed.
DROP TABLE IF EXISTS unregistered_docs;
DROP TABLE IF EXISTS blocked_owners;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

CREATE TABLE unregistered_docs (
  doc_id      TEXT PRIMARY KEY,
  owner_email CITEXT,
  title       TEXT,
  mime_type   TEXT,
  first_seen  BIGINT NOT NULL,
  last_seen   BIGINT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','ignored','blocked','registered'))
);

CREATE TABLE blocked_owners (
  owner_email CITEXT PRIMARY KEY,
  blocked_at  BIGINT NOT NULL,
  reason      TEXT
);

-- +goose StatementEnd
