package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CreateNote inserts a note and returns it with its assigned id and created_at.
// content and grantee_id are required; kind defaults to NoteKindNote. The
// caller is expected to have validated that the grantee (and any supersedes_id)
// exist; the foreign keys enforce it regardless.
func (s *Store) CreateNote(ctx context.Context, n Note) (*Note, error) {
	n.GranteeID = strings.TrimSpace(n.GranteeID)
	n.Content = cleanText(strings.TrimSpace(n.Content))
	if n.GranteeID == "" {
		return nil, errors.New("grantee_id required")
	}
	if n.Content == "" {
		return nil, errors.New("content required")
	}
	if n.Kind == "" {
		n.Kind = NoteKindNote
	}
	if !ValidNoteKind(n.Kind) {
		return nil, fmt.Errorf("invalid note kind %q", n.Kind)
	}
	createdAt := n.CreatedAt.Unix()
	if n.CreatedAt.IsZero() {
		createdAt = time.Now().Unix()
	}
	var id int64
	err := s.queryRow(ctx, `
		INSERT INTO notes(grantee_id, kind, content, signal_number, supersedes_id, created_at)
		VALUES(?, ?, ?, ?, ?, ?)
		RETURNING id
	`, n.GranteeID, n.Kind, n.Content, nullStr(strings.TrimSpace(n.SignalNumber)), n.SupersedesID, createdAt).Scan(&id)
	if err != nil {
		return nil, err
	}
	n.ID = id
	n.CreatedAt = time.Unix(createdAt, 0).UTC()
	return &n, nil
}

// GetNote returns a single note by id, or ErrNotFound.
func (s *Store) GetNote(ctx context.Context, id int64) (*Note, error) {
	row := s.queryRow(ctx, `
		SELECT n.id, n.grantee_id, n.kind, n.content, COALESCE(n.signal_number,''),
		       n.supersedes_id, n.created_at,
		       (SELECT s.id FROM notes s WHERE s.supersedes_id = n.id ORDER BY s.id LIMIT 1)
		FROM notes n WHERE n.id = ?
	`, id)
	n, err := scanNote(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return n, nil
}

// ListNotes returns notes for a grantee, newest first. Superseded notes (those
// pointed at by another note's supersedes_id) are excluded unless
// f.IncludeSuperseded is set. Returns the page and a next-page cursor (0 when
// exhausted); the cursor is the id to pass back as f.Cursor.
func (s *Store) ListNotes(ctx context.Context, f NoteFilter) ([]Note, int64, error) {
	if strings.TrimSpace(f.GranteeID) == "" {
		return nil, 0, errors.New("grantee_id required")
	}
	if f.Kind != "" && !ValidNoteKind(f.Kind) {
		return nil, 0, fmt.Errorf("invalid note kind %q", f.Kind)
	}
	limit := clampLimit(f.Limit)

	where := []string{"n.grantee_id = ?"}
	args := []any{f.GranteeID}
	if f.Kind != "" {
		where = append(where, "n.kind = ?")
		args = append(args, f.Kind)
	}
	if f.Since != nil {
		where = append(where, "n.created_at >= ?")
		args = append(args, f.Since.Unix())
	}
	if f.Until != nil {
		where = append(where, "n.created_at <= ?")
		args = append(args, f.Until.Unix())
	}
	if f.Cursor > 0 {
		where = append(where, "n.id < ?")
		args = append(args, f.Cursor)
	}
	if !f.IncludeSuperseded {
		where = append(where, "NOT EXISTS(SELECT 1 FROM notes s WHERE s.supersedes_id = n.id)")
	}

	q := `
		SELECT n.id, n.grantee_id, n.kind, n.content, COALESCE(n.signal_number,''),
		       n.supersedes_id, n.created_at,
		       (SELECT s.id FROM notes s WHERE s.supersedes_id = n.id ORDER BY s.id LIMIT 1)
		FROM notes n
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY n.id DESC
		LIMIT ?
	`
	args = append(args, limit+1)

	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *n)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var next int64
	if len(out) > limit {
		next = out[limit-1].ID
		out = out[:limit]
	}
	return out, next, nil
}

func scanNote(sc scanner) (*Note, error) {
	var (
		n              Note
		supersedesID   sql.NullInt64
		supersededByID sql.NullInt64
		createdAt      int64
	)
	if err := sc.Scan(&n.ID, &n.GranteeID, &n.Kind, &n.Content, &n.SignalNumber,
		&supersedesID, &createdAt, &supersededByID); err != nil {
		return nil, err
	}
	if supersedesID.Valid {
		n.SupersedesID = &supersedesID.Int64
	}
	if supersededByID.Valid {
		n.SupersededByID = &supersededByID.Int64
	}
	n.CreatedAt = time.Unix(createdAt, 0).UTC()
	return &n, nil
}
