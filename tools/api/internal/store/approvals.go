package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Approval action statuses.
const (
	ApprovalPending  = "pending"
	ApprovalExecuted = "executed"
	ApprovalFailed   = "failed"
	ApprovalRejected = "rejected"
)

// ApprovalUser is an operator authorized to approve queued actions. The public
// key is stored in authorized_keys format; fingerprint is its SHA256:... form.
type ApprovalUser struct {
	Email       string    `json:"email"`
	PublicKey   string    `json:"ssh_public_key"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"created_at"`
}

// ApprovalAction is a queued, side-effecting action awaiting (or having
// received) human approval. Args and Metadata are raw JSON (JSONB columns).
type ApprovalAction struct {
	ID          int64           `json:"id"`
	Action      string          `json:"action"`
	Args        json.RawMessage `json:"args"`
	Metadata    json.RawMessage `json:"metadata"`
	Status      string          `json:"status"`
	RequestedBy string          `json:"requested_by,omitempty"`
	ApprovedBy  string          `json:"approved_by,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	ApprovedAt  *time.Time      `json:"approved_at,omitempty"`
	ExecutedAt  *time.Time      `json:"executed_at,omitempty"`
	LastError   string          `json:"last_error,omitempty"`
}

// EnqueueAction inserts a new pending action and returns its id. args/metadata
// must be valid JSON; empty values default to "{}".
func (s *Store) EnqueueAction(ctx context.Context, action string, args, metadata json.RawMessage, requestedBy string) (int64, error) {
	if action == "" {
		return 0, errors.New("action required")
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	var id int64
	err := s.queryRow(ctx, `
		INSERT INTO approval_actions(action, args, metadata, status, requested_by, created_at)
		VALUES(?, ?, ?, ?, ?, ?)
		RETURNING id
	`, action, []byte(args), []byte(metadata), ApprovalPending, nullStr(requestedBy), time.Now().Unix()).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// GetAction returns a single action by id, or ErrNotFound.
func (s *Store) GetAction(ctx context.Context, id int64) (*ApprovalAction, error) {
	row := s.queryRow(ctx, `
		SELECT id, action, args, metadata, status, requested_by, approved_by,
		       created_at, approved_at, executed_at, last_error
		FROM approval_actions
		WHERE id = ?
	`, id)
	a, err := scanAction(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

// ListActionsByStatus returns actions with the given status, newest first.
func (s *Store) ListActionsByStatus(ctx context.Context, status string) ([]ApprovalAction, error) {
	rows, err := s.query(ctx, `
		SELECT id, action, args, metadata, status, requested_by, approved_by,
		       created_at, approved_at, executed_at, last_error
		FROM approval_actions
		WHERE status = ?
		ORDER BY id DESC
	`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ApprovalAction
	for rows.Next() {
		a, err := scanAction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// MarkActionExecuted records a successful approval+dispatch.
func (s *Store) MarkActionExecuted(ctx context.Context, id int64, approvedBy string) error {
	now := time.Now().Unix()
	res, err := s.exec(ctx, `
		UPDATE approval_actions
		SET status = ?, approved_by = ?, approved_at = ?, executed_at = ?, last_error = NULL
		WHERE id = ? AND status = ?
	`, ApprovalExecuted, approvedBy, now, now, id, ApprovalPending)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// MarkActionFailed records an approval whose dispatch failed.
func (s *Store) MarkActionFailed(ctx context.Context, id int64, approvedBy, errMsg string) error {
	now := time.Now().Unix()
	res, err := s.exec(ctx, `
		UPDATE approval_actions
		SET status = ?, approved_by = ?, approved_at = ?, last_error = ?
		WHERE id = ? AND status = ?
	`, ApprovalFailed, approvedBy, now, nullStr(errMsg), id, ApprovalPending)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// UpsertApprovalUser inserts or updates an operator's authorized key.
func (s *Store) UpsertApprovalUser(ctx context.Context, email, pubKey, fingerprint string) error {
	if email == "" || pubKey == "" || fingerprint == "" {
		return errors.New("email, public key, and fingerprint required")
	}
	_, err := s.exec(ctx, `
		INSERT INTO approval_users(email, ssh_public_key, fingerprint, created_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(email) DO UPDATE SET
		  ssh_public_key = excluded.ssh_public_key,
		  fingerprint = excluded.fingerprint
	`, email, pubKey, fingerprint, time.Now().Unix())
	return err
}

// GetApprovalUser returns an operator by email, or ErrNotFound.
func (s *Store) GetApprovalUser(ctx context.Context, email string) (*ApprovalUser, error) {
	row := s.queryRow(ctx, `
		SELECT email, ssh_public_key, fingerprint, created_at
		FROM approval_users
		WHERE email = ?
	`, email)
	var u ApprovalUser
	var createdAt int64
	err := row.Scan(&u.Email, &u.PublicKey, &u.Fingerprint, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt = time.Unix(createdAt, 0).UTC()
	return &u, nil
}

// ListApprovalUsers returns all authorized operators, ordered by email.
func (s *Store) ListApprovalUsers(ctx context.Context) ([]ApprovalUser, error) {
	rows, err := s.query(ctx, `
		SELECT email, ssh_public_key, fingerprint, created_at
		FROM approval_users
		ORDER BY email
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ApprovalUser
	for rows.Next() {
		var u ApprovalUser
		var createdAt int64
		if err := rows.Scan(&u.Email, &u.PublicKey, &u.Fingerprint, &createdAt); err != nil {
			return nil, err
		}
		u.CreatedAt = time.Unix(createdAt, 0).UTC()
		out = append(out, u)
	}
	return out, rows.Err()
}

// RemoveApprovalUser deletes an operator by email.
func (s *Store) RemoveApprovalUser(ctx context.Context, email string) error {
	res, err := s.exec(ctx, `DELETE FROM approval_users WHERE email = ?`, email)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanAction(sc scanner) (*ApprovalAction, error) {
	var (
		a           ApprovalAction
		args        []byte
		metadata    []byte
		requestedBy sql.NullString
		approvedBy  sql.NullString
		createdAt   int64
		approvedAt  sql.NullInt64
		executedAt  sql.NullInt64
		lastErr     sql.NullString
	)
	if err := sc.Scan(&a.ID, &a.Action, &args, &metadata, &a.Status, &requestedBy,
		&approvedBy, &createdAt, &approvedAt, &executedAt, &lastErr); err != nil {
		return nil, err
	}
	a.Args = json.RawMessage(args)
	a.Metadata = json.RawMessage(metadata)
	a.RequestedBy = requestedBy.String
	a.ApprovedBy = approvedBy.String
	a.CreatedAt = time.Unix(createdAt, 0).UTC()
	if approvedAt.Valid {
		t := time.Unix(approvedAt.Int64, 0).UTC()
		a.ApprovedAt = &t
	}
	if executedAt.Valid {
		t := time.Unix(executedAt.Int64, 0).UTC()
		a.ExecutedAt = &t
	}
	a.LastError = lastErr.String
	return &a, nil
}

// requireOneRow returns ErrNotFound when an UPDATE/DELETE matched no rows.
func requireOneRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
