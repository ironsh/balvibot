// Package store is the unified Postgres data layer shared by the mail indexer,
// the gdocs sync loop, the grantee-admin CLI, and the MCP server. It speaks
// database/sql over the pgx stdlib driver. All timestamps are stored as unix
// seconds (BIGINT).
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
}

// New wraps an already-open *sql.DB (migrations are run separately via the db
// package / `api migrate up`).
func New(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) DB() *sql.DB { return s.db }

// rebind converts SQLite-style `?` placeholders to Postgres `$N` positional
// placeholders, in order. None of our query literals contain a literal `?`
// other than a bind placeholder, so a straight scan is safe.
func rebind(q string) string {
	var b strings.Builder
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(q[i])
	}
	return b.String()
}

func (s *Store) exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, rebind(q), args...)
}

func (s *Store) query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, rebind(q), args...)
}

func (s *Store) queryRow(ctx context.Context, q string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, rebind(q), args...)
}

// IsUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505). Replaces the SQLite "UNIQUE" string check.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

// ---------- grantees ----------

// EnsureGrantee inserts a grantee row if missing, but never overwrites an
// existing one. Used by the `authorize` flow, where display_name is only
// meaningful at first creation.
func (s *Store) EnsureGrantee(ctx context.Context, g Grantee) error {
	if g.GranteeID == "" {
		return errors.New("grantee_id required")
	}
	if g.Status == "" {
		g.Status = StatusActive
	}
	createdAt := g.CreatedAt.Unix()
	if g.CreatedAt.IsZero() {
		createdAt = time.Now().Unix()
	}
	_, err := s.exec(ctx, `
		INSERT INTO grantees(grantee_id, display_name, status, created_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(grantee_id) DO NOTHING
	`, g.GranteeID, nullStr(g.DisplayName), g.Status, createdAt)
	return err
}

// UpsertGrantee inserts or updates a grantee row by id.
func (s *Store) UpsertGrantee(ctx context.Context, g Grantee) error {
	if g.GranteeID == "" {
		return errors.New("grantee_id required")
	}
	if g.Status == "" {
		g.Status = StatusActive
	}
	createdAt := g.CreatedAt.Unix()
	if g.CreatedAt.IsZero() {
		createdAt = time.Now().Unix()
	}
	_, err := s.exec(ctx, `
		INSERT INTO grantees(grantee_id, display_name, status, created_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(grantee_id) DO UPDATE SET
		  display_name = excluded.display_name,
		  status = excluded.status
	`, g.GranteeID, nullStr(g.DisplayName), g.Status, createdAt)
	return err
}

func (s *Store) SetGranteeStatus(ctx context.Context, granteeID, status string) error {
	if status != StatusActive && status != StatusPaused {
		return fmt.Errorf("invalid grantee status %q", status)
	}
	res, err := s.exec(ctx, `UPDATE grantees SET status = ? WHERE grantee_id = ?`, status, granteeID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetGrantee(ctx context.Context, granteeID string) (*Grantee, error) {
	row := s.queryRow(ctx, `
		SELECT grantee_id, COALESCE(display_name,''), status, created_at
		FROM grantees WHERE grantee_id = ?
	`, granteeID)
	var g Grantee
	var createdAt int64
	if err := row.Scan(&g.GranteeID, &g.DisplayName, &g.Status, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	g.CreatedAt = time.Unix(createdAt, 0).UTC()
	return &g, nil
}

func (s *Store) ListActiveGrantees(ctx context.Context) ([]Grantee, error) {
	return s.listGrantees(ctx, "WHERE status = 'active' ORDER BY grantee_id")
}

func (s *Store) listGrantees(ctx context.Context, whereOrderClause string) ([]Grantee, error) {
	q := `SELECT grantee_id, COALESCE(display_name,''), status, created_at FROM grantees ` + whereOrderClause
	rows, err := s.query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Grantee
	for rows.Next() {
		var g Grantee
		var createdAt int64
		if err := rows.Scan(&g.GranteeID, &g.DisplayName, &g.Status, &createdAt); err != nil {
			return nil, err
		}
		g.CreatedAt = time.Unix(createdAt, 0).UTC()
		out = append(out, g)
	}
	return out, rows.Err()
}

// ---------- grantee emails ----------

func (s *Store) AddGranteeEmail(ctx context.Context, granteeID, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if granteeID == "" || email == "" {
		return errors.New("grantee_id and email required")
	}
	_, err := s.exec(ctx, `
		INSERT INTO grantee_emails(email, grantee_id) VALUES(?, ?)
		ON CONFLICT(email) DO UPDATE SET grantee_id = excluded.grantee_id
	`, email, granteeID)
	return err
}

func (s *Store) RemoveGranteeEmail(ctx context.Context, granteeID, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	res, err := s.exec(ctx, `DELETE FROM grantee_emails WHERE email = ? AND grantee_id = ?`, email, granteeID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) LookupGranteeByEmail(ctx context.Context, email string) (string, bool, error) {
	var id string
	err := s.queryRow(ctx,
		`SELECT grantee_id FROM grantee_emails WHERE email = ?`,
		strings.ToLower(email),
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

// ---------- sources ----------

func (s *Store) UpsertSource(ctx context.Context, src Source) (int64, error) {
	if src.GranteeID == "" {
		return 0, errors.New("grantee_id required")
	}
	if src.SourceType != SourceTypeFolder && src.SourceType != SourceTypeDoc {
		return 0, fmt.Errorf("invalid source_type %q", src.SourceType)
	}
	if src.DriveID == "" {
		return 0, errors.New("drive_id required")
	}
	addedAt := src.AddedAt.Unix()
	if src.AddedAt.IsZero() {
		addedAt = time.Now().Unix()
	}
	var id int64
	err := s.queryRow(ctx, `
		INSERT INTO grantee_sources(grantee_id, source_type, drive_id, added_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(grantee_id, drive_id) DO UPDATE SET source_type = excluded.source_type
		RETURNING id
	`, src.GranteeID, src.SourceType, src.DriveID, addedAt).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) DeleteSource(ctx context.Context, granteeID, driveID string) error {
	res, err := s.exec(ctx,
		`DELETE FROM grantee_sources WHERE grantee_id = ? AND drive_id = ?`,
		granteeID, driveID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListSourcesForGrantee(ctx context.Context, granteeID string) ([]Source, error) {
	rows, err := s.query(ctx, `
		SELECT id, grantee_id, source_type, drive_id, added_at
		FROM grantee_sources WHERE grantee_id = ? ORDER BY id
	`, granteeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		var src Source
		var addedAt int64
		if err := rows.Scan(&src.ID, &src.GranteeID, &src.SourceType, &src.DriveID, &addedAt); err != nil {
			return nil, err
		}
		src.AddedAt = time.Unix(addedAt, 0).UTC()
		out = append(out, src)
	}
	return out, rows.Err()
}

// ---------- mailbox state ----------

func (s *Store) GetMailboxState(ctx context.Context, folder string) (*MailboxState, error) {
	var st MailboxState
	err := s.queryRow(ctx,
		`SELECT folder, uid_validity, last_uid FROM mailbox_state WHERE folder = ?`,
		folder,
	).Scan(&st.Folder, &st.UIDValidity, &st.LastUID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *Store) SaveMailboxState(ctx context.Context, st *MailboxState) error {
	_, err := s.exec(ctx, `
		INSERT INTO mailbox_state(folder, uid_validity, last_uid, updated_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(folder) DO UPDATE SET
		  uid_validity=excluded.uid_validity,
		  last_uid=excluded.last_uid,
		  updated_at=excluded.updated_at
	`, st.Folder, st.UIDValidity, st.LastUID, time.Now().Unix())
	return err
}

// ---------- threads + messages ----------

func (s *Store) MessageExistsByID(ctx context.Context, messageID string) (bool, error) {
	var n int
	err := s.queryRow(ctx, `SELECT 1 FROM messages WHERE message_id = ? LIMIT 1`, messageID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// FindThreadByRefs returns the thread_id whose root_message_id is any of `refs`,
// or which contains any message whose message_id is in `refs`. First match wins.
func (s *Store) FindThreadByRefs(ctx context.Context, refs []string) (int64, error) {
	if len(refs) == 0 {
		return 0, nil
	}
	placeholders := strings.Repeat("?,", len(refs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(refs))
	for i, r := range refs {
		args[i] = r
	}
	q := fmt.Sprintf(`SELECT id FROM threads WHERE root_message_id IN (%s) LIMIT 1`, placeholders)
	var id int64
	err := s.queryRow(ctx, q, args...).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	q2 := fmt.Sprintf(`SELECT thread_id FROM messages WHERE message_id IN (%s) LIMIT 1`, placeholders)
	err = s.queryRow(ctx, q2, args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) CreateThread(ctx context.Context, rootMessageID, subjectNorm string, granteeID *string, t time.Time) (int64, error) {
	var id int64
	err := s.queryRow(ctx, `
		INSERT INTO threads(root_message_id, subject_norm, grantee_id, first_seen_at, last_seen_at)
		VALUES(?, ?, ?, ?, ?)
		RETURNING id
	`, rootMessageID, subjectNorm, granteeID, t.Unix(), t.Unix()).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) GetThread(ctx context.Context, id int64) (*Thread, error) {
	var t Thread
	var firstSeen, lastSeen int64
	err := s.queryRow(ctx, `
		SELECT id, root_message_id, COALESCE(subject_norm,''), grantee_id, first_seen_at, last_seen_at
		FROM threads WHERE id = ?
	`, id).Scan(&t.ID, &t.RootMessageID, &t.SubjectNorm, &t.GranteeID, &firstSeen, &lastSeen)
	if err != nil {
		return nil, err
	}
	t.FirstSeenAt = time.Unix(firstSeen, 0)
	t.LastSeenAt = time.Unix(lastSeen, 0)
	return &t, nil
}

// SetThreadGrantee sets the thread's grantee_id (if NULL) and back-fills any
// prior NULL-grantee messages on it. Safe to call repeatedly.
func (s *Store) SetThreadGrantee(ctx context.Context, threadID int64, granteeID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		rebind(`UPDATE threads SET grantee_id = ? WHERE id = ? AND grantee_id IS NULL`),
		granteeID, threadID,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		rebind(`UPDATE messages SET grantee_id = ? WHERE thread_id = ? AND grantee_id IS NULL`),
		granteeID, threadID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) TouchThread(ctx context.Context, threadID int64, t time.Time) error {
	_, err := s.exec(ctx,
		`UPDATE threads SET last_seen_at = GREATEST(last_seen_at, ?) WHERE id = ?`,
		t.Unix(), threadID,
	)
	return err
}

// InsertMessage inserts the message and its child rows (recipients, refs,
// attachments). Returns (newID, true) on insert; (existingID, false) if a row
// with the same message_id already exists.
func (s *Store) InsertMessage(ctx context.Context, m *Message) (int64, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	to, _ := json.Marshal(m.To)
	cc, _ := json.Marshal(m.Cc)

	var msgID int64
	err = tx.QueryRowContext(ctx, rebind(`
		INSERT INTO messages(
		  message_id, thread_id, grantee_id, folder, uid, uid_validity,
		  in_reply_to, references_raw, from_addr, from_name,
		  to_addrs, cc_addrs, subject, date, body_text, body_html,
		  size_bytes, indexed_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(message_id) DO NOTHING
		RETURNING id
	`),
		m.MessageID, m.ThreadID, m.GranteeID, m.Folder, m.UID, m.UIDValidity,
		nullStr(m.InReplyTo), nullStr(strings.Join(m.References, " ")),
		m.From.Email, nullStr(m.From.Name),
		string(to), nullStr(string(cc)),
		nullStr(m.Subject), m.Date.Unix(),
		nullStr(m.BodyText), nullStr(m.BodyHTML),
		m.SizeBytes, time.Now().Unix(),
	).Scan(&msgID)
	if errors.Is(err, sql.ErrNoRows) {
		// Conflict: the message already exists. Fetch its id and report no insert.
		var id int64
		if err := tx.QueryRowContext(ctx, rebind(`SELECT id FROM messages WHERE message_id = ?`), m.MessageID).Scan(&id); err != nil {
			return 0, false, err
		}
		if err := tx.Commit(); err != nil {
			return 0, false, err
		}
		return id, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("insert message: %w", err)
	}

	for _, ref := range m.References {
		if ref == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			rebind(`INSERT INTO message_references(message_id, ref) VALUES(?, ?) ON CONFLICT DO NOTHING`),
			msgID, ref,
		); err != nil {
			return 0, false, err
		}
	}
	insertRecipients := func(kind string, addrs []Address) error {
		for _, a := range addrs {
			if a.Email == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx,
				rebind(`INSERT INTO message_recipients(message_id, kind, email, name) VALUES(?, ?, ?, ?) ON CONFLICT DO NOTHING`),
				msgID, kind, strings.ToLower(a.Email), nullStr(a.Name),
			); err != nil {
				return err
			}
		}
		return nil
	}
	if err := insertRecipients("to", m.To); err != nil {
		return 0, false, err
	}
	if err := insertRecipients("cc", m.Cc); err != nil {
		return 0, false, err
	}
	if err := insertRecipients("bcc", m.Bcc); err != nil {
		return 0, false, err
	}

	for _, a := range m.Attachments {
		if _, err := tx.ExecContext(ctx, rebind(`
			INSERT INTO attachments(message_id, filename, mime_type, size_bytes, sha256, path)
			VALUES(?, ?, ?, ?, ?, ?)
		`), msgID, nullStr(a.Filename), nullStr(a.MimeType), a.SizeBytes, a.SHA256, a.Path); err != nil {
			return 0, false, fmt.Errorf("insert attachment: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return msgID, true, nil
}

// ---------- docs ----------

func (s *Store) GetDocByID(ctx context.Context, docID string) (*Doc, error) {
	row := s.queryRow(ctx, `
		SELECT doc_id, grantee_id, title, owner_email, content_markdown,
		  modified_at, synced_at, source_type, source_drive_id,
		  had_images, had_comments, status, COALESCE(last_error,'')
		FROM docs WHERE doc_id = ?
	`, docID)
	return scanDoc(row)
}

func (s *Store) GetDocForGrantee(ctx context.Context, granteeID, docID string) (*Doc, error) {
	row := s.queryRow(ctx, `
		SELECT doc_id, grantee_id, title, owner_email, content_markdown,
		  modified_at, synced_at, source_type, source_drive_id,
		  had_images, had_comments, status, COALESCE(last_error,'')
		FROM docs WHERE doc_id = ? AND grantee_id = ?
	`, docID, granteeID)
	return scanDoc(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanDoc(row rowScanner) (*Doc, error) {
	var d Doc
	var modifiedAt, syncedAt int64
	if err := row.Scan(
		&d.DocID, &d.GranteeID, &d.Title, &d.OwnerEmail, &d.ContentMarkdown,
		&modifiedAt, &syncedAt, &d.SourceType, &d.SourceDriveID,
		&d.HadImages, &d.HadComments, &d.Status, &d.LastError,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	d.ModifiedAt = time.Unix(modifiedAt, 0).UTC()
	d.SyncedAt = time.Unix(syncedAt, 0).UTC()
	return &d, nil
}

// UpsertDoc writes the full doc row and stamps last_seen_cycle = cycleID.
func (s *Store) UpsertDoc(ctx context.Context, d Doc, cycleID int64) error {
	if d.DocID == "" || d.GranteeID == "" {
		return errors.New("doc_id and grantee_id required")
	}
	now := time.Now().Unix()
	if d.Status == "" {
		d.Status = StatusActive
	}
	_, err := s.exec(ctx, `
		INSERT INTO docs(
		  doc_id, grantee_id, title, owner_email, content_markdown,
		  modified_at, synced_at, source_type, source_drive_id,
		  had_images, had_comments, status, last_error, last_seen_cycle
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(doc_id) DO UPDATE SET
		  grantee_id = excluded.grantee_id,
		  title = excluded.title,
		  owner_email = excluded.owner_email,
		  content_markdown = excluded.content_markdown,
		  modified_at = excluded.modified_at,
		  synced_at = excluded.synced_at,
		  source_type = excluded.source_type,
		  source_drive_id = excluded.source_drive_id,
		  had_images = excluded.had_images,
		  had_comments = excluded.had_comments,
		  status = excluded.status,
		  last_error = excluded.last_error,
		  last_seen_cycle = excluded.last_seen_cycle
	`,
		d.DocID, d.GranteeID, d.Title, strings.ToLower(d.OwnerEmail), d.ContentMarkdown,
		d.ModifiedAt.Unix(), now, d.SourceType, d.SourceDriveID,
		d.HadImages, d.HadComments, d.Status, nullStr(d.LastError), cycleID,
	)
	return err
}

// TouchDocSeen bumps last_seen_cycle and synced_at for an unchanged doc and
// clears any stale/error status.
func (s *Store) TouchDocSeen(ctx context.Context, docID string, cycleID int64) error {
	now := time.Now().Unix()
	res, err := s.exec(ctx, `
		UPDATE docs
		SET last_seen_cycle = ?, synced_at = ?, status = 'active', last_error = NULL
		WHERE doc_id = ?
	`, cycleID, now, docID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) MarkDocStale(ctx context.Context, docID, lastErr string) error {
	_, err := s.exec(ctx,
		`UPDATE docs SET status = 'stale', last_error = ? WHERE doc_id = ?`,
		nullStr(lastErr), docID,
	)
	return err
}

func (s *Store) MarkDocError(ctx context.Context, docID, lastErr string) error {
	_, err := s.exec(ctx,
		`UPDATE docs SET status = 'error', last_error = ? WHERE doc_id = ?`,
		nullStr(lastErr), docID,
	)
	return err
}

// MarkUnseenStale flips active rows whose source still exists but whose
// last_seen_cycle predates cycleID. Returns the number of rows marked stale.
func (s *Store) MarkUnseenStale(ctx context.Context, cycleID int64) (int64, error) {
	res, err := s.exec(ctx, `
		UPDATE docs SET status = 'stale', last_error = 'not seen in last sync cycle'
		WHERE status = 'active'
		  AND (last_seen_cycle IS NULL OR last_seen_cycle < ?)
		  AND grantee_id IN (SELECT grantee_id FROM grantees WHERE status = 'active')
		  AND source_drive_id IN (SELECT drive_id FROM grantee_sources)
	`, cycleID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) ListDocsForGrantee(ctx context.Context, granteeID string, limit int, cursor int64) ([]DocSummary, int64, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	// Cursor is modified_at unix seconds; results are descending by modified_at.
	q := `
		SELECT doc_id, title, modified_at, synced_at, had_images, had_comments, status
		FROM docs WHERE grantee_id = ?
		  AND (? = 0 OR modified_at < ?)
		ORDER BY modified_at DESC, doc_id DESC LIMIT ?
	`
	rows, err := s.query(ctx, q, granteeID, cursor, cursor, limit+1)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []DocSummary
	for rows.Next() {
		var d DocSummary
		var modifiedAt, syncedAt int64
		if err := rows.Scan(&d.DocID, &d.Title, &modifiedAt, &syncedAt, &d.HadImages, &d.HadComments, &d.Status); err != nil {
			return nil, 0, err
		}
		d.ModifiedAt = time.Unix(modifiedAt, 0).UTC()
		d.SyncedAt = time.Unix(syncedAt, 0).UTC()
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var next int64
	if len(out) > limit {
		next = out[limit-1].ModifiedAt.Unix()
		out = out[:limit]
	}
	return out, next, nil
}

// ---------- sync_state ----------

// NextCycleID atomically increments the monotonic cycle counter and returns
// the new value.
func (s *Store) NextCycleID(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var raw sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT v FROM sync_state WHERE k = 'cycle_counter'`).Scan(&raw); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	var current int64
	if raw.Valid {
		n, err := strconv.ParseInt(raw.String, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse cycle_counter %q: %w", raw.String, err)
		}
		current = n
	}
	next := current + 1
	if _, err := tx.ExecContext(ctx, rebind(`
		INSERT INTO sync_state(k, v) VALUES('cycle_counter', ?)
		ON CONFLICT(k) DO UPDATE SET v = excluded.v
	`), strconv.FormatInt(next, 10)); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return next, nil
}

func (s *Store) SetLastSuccessfulSync(ctx context.Context, t time.Time) error {
	_, err := s.exec(ctx, `
		INSERT INTO sync_state(k, v) VALUES('last_successful_sync', ?)
		ON CONFLICT(k) DO UPDATE SET v = excluded.v
	`, strconv.FormatInt(t.Unix(), 10))
	return err
}

func (s *Store) LastSuccessfulSync(ctx context.Context) (time.Time, error) {
	var raw sql.NullString
	err := s.queryRow(ctx,
		`SELECT v FROM sync_state WHERE k = 'last_successful_sync'`,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || !raw.Valid {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	n, err := strconv.ParseInt(raw.String, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse last_successful_sync %q: %w", raw.String, err)
	}
	return time.Unix(n, 0).UTC(), nil
}

// ---------- helpers ----------

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
