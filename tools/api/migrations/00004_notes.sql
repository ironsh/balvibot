-- +goose Up
-- +goose StatementBegin

-- ---------- notes ----------
-- Free-text notes the agent keeps about a grantee: durable memory it can write
-- when a user asks it to remember something, and read back later. Unlike the
-- approval-gated actions, notes are written directly (they are additive,
-- low-risk memory, not side effects on the outside world).
--
-- kind classifies the note. supersedes_id lets a newer note mark an older one
-- as out of date without deleting it, so history is preserved. signal_number is
-- request metadata: the Signal phone number that asked for the note, if any.
-- Timestamps are unix seconds (BIGINT), matching the rest of the schema.
CREATE TABLE notes (
  id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  grantee_id    TEXT   NOT NULL REFERENCES grantees(grantee_id) ON DELETE CASCADE,
  kind          TEXT   NOT NULL DEFAULT 'note'
                  CHECK (kind IN ('note','fact','preference','status','contact')),
  content       TEXT   NOT NULL,
  signal_number TEXT,
  supersedes_id BIGINT REFERENCES notes(id) ON DELETE SET NULL,
  created_at    BIGINT NOT NULL
);
CREATE INDEX idx_notes_grantee    ON notes(grantee_id);
CREATE INDEX idx_notes_created    ON notes(created_at);
CREATE INDEX idx_notes_supersedes ON notes(supersedes_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS notes;
-- +goose StatementEnd
