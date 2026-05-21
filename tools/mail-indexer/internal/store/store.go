package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/ironcd/philanthropy-os/tools/mail-indexer/internal/config"
)

//go:embed schema.sql
var schemaSQL string

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	dsn := path + "?_journal_mode=WAL&_synchronous=NORMAL&_foreign_keys=on&_busy_timeout=5000"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) ReconcileGrantees(ctx context.Context, grantees []config.Grantee) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	wantIDs := map[string]bool{}
	wantEmails := map[string]string{}
	for _, g := range grantees {
		wantIDs[g.ID] = true
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO grantees(id, name, created_at) VALUES(?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name
		`, g.ID, g.Name, now); err != nil {
			return fmt.Errorf("upsert grantee %s: %w", g.ID, err)
		}
		for _, e := range g.Emails {
			el := strings.ToLower(strings.TrimSpace(e))
			if el == "" {
				continue
			}
			wantEmails[el] = g.ID
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO grantee_emails(email, grantee_id) VALUES(?, ?)
				ON CONFLICT(email) DO UPDATE SET grantee_id=excluded.grantee_id
			`, el, g.ID); err != nil {
				return fmt.Errorf("upsert grantee_email %s: %w", el, err)
			}
		}
	}

	rows, err := tx.QueryContext(ctx, `SELECT email FROM grantee_emails`)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			rows.Close()
			return err
		}
		if _, ok := wantEmails[e]; !ok {
			stale = append(stale, e)
		}
	}
	rows.Close()
	for _, e := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM grantee_emails WHERE email = ?`, e); err != nil {
			return err
		}
	}

	gRows, err := tx.QueryContext(ctx, `SELECT id FROM grantees`)
	if err != nil {
		return err
	}
	var staleGrantees []string
	for gRows.Next() {
		var id string
		if err := gRows.Scan(&id); err != nil {
			gRows.Close()
			return err
		}
		if !wantIDs[id] {
			staleGrantees = append(staleGrantees, id)
		}
	}
	gRows.Close()
	for _, id := range staleGrantees {
		if _, err := tx.ExecContext(ctx, `DELETE FROM grantees WHERE id = ?`, id); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Store) LookupGranteeByEmail(ctx context.Context, email string) (string, bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT grantee_id FROM grantee_emails WHERE email = ? COLLATE NOCASE`,
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

func (s *Store) GetMailboxState(ctx context.Context, folder string) (*MailboxState, error) {
	var st MailboxState
	err := s.db.QueryRowContext(ctx,
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
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO mailbox_state(folder, uid_validity, last_uid, updated_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(folder) DO UPDATE SET
		  uid_validity=excluded.uid_validity,
		  last_uid=excluded.last_uid,
		  updated_at=excluded.updated_at
	`, st.Folder, st.UIDValidity, st.LastUID, time.Now().Unix())
	return err
}

func (s *Store) MessageExistsByID(ctx context.Context, messageID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM messages WHERE message_id = ? LIMIT 1`, messageID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// FindThreadByRefs returns the thread_id whose root_message_id is any of `refs`,
// or which contains any message whose message_id is in `refs`. The first match wins.
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
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	q2 := fmt.Sprintf(`SELECT thread_id FROM messages WHERE message_id IN (%s) LIMIT 1`, placeholders)
	err = s.db.QueryRowContext(ctx, q2, args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) CreateThread(ctx context.Context, rootMessageID, subjectNorm string, granteeID *string, t time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO threads(root_message_id, subject_norm, grantee_id, first_seen_at, last_seen_at)
		VALUES(?, ?, ?, ?, ?)
	`, rootMessageID, subjectNorm, granteeID, t.Unix(), t.Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) GetThread(ctx context.Context, id int64) (*Thread, error) {
	var t Thread
	var firstSeen, lastSeen int64
	err := s.db.QueryRowContext(ctx, `
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

func (s *Store) SetThreadGrantee(ctx context.Context, threadID int64, granteeID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE threads SET grantee_id = ? WHERE id = ? AND grantee_id IS NULL`,
		granteeID, threadID,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE messages SET grantee_id = ? WHERE thread_id = ? AND grantee_id IS NULL`,
		granteeID, threadID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) TouchThread(ctx context.Context, threadID int64, t time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE threads SET last_seen_at = MAX(last_seen_at, ?) WHERE id = ?`,
		t.Unix(), threadID,
	)
	return err
}

// InsertMessage inserts the message and its child rows (recipients, refs, attachments).
// Returns (newID, true) on insert; (existingID, false) if a row with the same message_id already exists.
func (s *Store) InsertMessage(ctx context.Context, m *Message) (int64, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	to, _ := json.Marshal(m.To)
	cc, _ := json.Marshal(m.Cc)

	res, err := tx.ExecContext(ctx, `
		INSERT INTO messages(
		  message_id, thread_id, grantee_id, folder, uid, uid_validity,
		  in_reply_to, references_raw, from_addr, from_name,
		  to_addrs, cc_addrs, subject, date, body_text, body_html,
		  size_bytes, indexed_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(message_id) DO NOTHING
	`,
		m.MessageID, m.ThreadID, m.GranteeID, m.Folder, m.UID, m.UIDValidity,
		nullStr(m.InReplyTo), nullStr(strings.Join(m.References, " ")),
		m.From.Email, nullStr(m.From.Name),
		string(to), nullStr(string(cc)),
		nullStr(m.Subject), m.Date.Unix(),
		nullStr(m.BodyText), nullStr(m.BodyHTML),
		m.SizeBytes, time.Now().Unix(),
	)
	if err != nil {
		return 0, false, fmt.Errorf("insert message: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM messages WHERE message_id = ?`, m.MessageID).Scan(&id); err != nil {
			return 0, false, err
		}
		if err := tx.Commit(); err != nil {
			return 0, false, err
		}
		return id, false, nil
	}
	msgID, err := res.LastInsertId()
	if err != nil {
		return 0, false, err
	}

	for _, ref := range m.References {
		if ref == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO message_references(message_id, ref) VALUES(?, ?)`,
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
				`INSERT OR IGNORE INTO message_recipients(message_id, kind, email, name) VALUES(?, ?, ?, ?)`,
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
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO attachments(message_id, filename, mime_type, size_bytes, sha256, path)
			VALUES(?, ?, ?, ?, ?, ?)
		`, msgID, nullStr(a.Filename), nullStr(a.MimeType), a.SizeBytes, a.SHA256, a.Path); err != nil {
			return 0, false, fmt.Errorf("insert attachment: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return msgID, true, nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
