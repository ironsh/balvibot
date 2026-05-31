-- +goose Up
-- +goose StatementBegin

-- ---------- approvals ----------
-- The approval service is a separate workload (`api approve-serve`) that lets
-- the agent enqueue side-effecting actions for human approval. An operator
-- approves via the offline `balvi-approve` CLI, which signs (id, action, args)
-- with their SSH key; the service verifies against approval_users before
-- dispatching the action. Timestamps are unix seconds (BIGINT), matching the
-- rest of the schema. args/metadata are JSONB.

-- Operators authorized to approve actions. The public key is stored in
-- authorized_keys format; fingerprint is the SHA256:... form for display/lookup.
CREATE TABLE approval_users (
  email          CITEXT PRIMARY KEY,
  ssh_public_key TEXT   NOT NULL,
  fingerprint    TEXT   NOT NULL,
  created_at     BIGINT NOT NULL
);
CREATE INDEX idx_approval_users_fingerprint ON approval_users(fingerprint);

-- Queued actions awaiting (or having received) approval.
CREATE TABLE approval_actions (
  id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  action       TEXT   NOT NULL,
  args         JSONB  NOT NULL DEFAULT '{}'::jsonb,
  metadata     JSONB  NOT NULL DEFAULT '{}'::jsonb,
  status       TEXT   NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending','executed','failed','rejected')),
  requested_by TEXT,
  approved_by  CITEXT,
  created_at   BIGINT NOT NULL,
  approved_at  BIGINT,
  executed_at  BIGINT,
  last_error   TEXT
);
CREATE INDEX idx_approval_actions_status ON approval_actions(status);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS approval_actions;
DROP TABLE IF EXISTS approval_users;
-- +goose StatementEnd
