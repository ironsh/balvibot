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
)

const defaultLimit = 50
const maxLimit = 200
const snippetLen = 200

// emailSep is the separator used to pack a grantee's emails into a single
// string_agg result, matching the split below. ASCII Unit Separator (0x1F)
// can't appear in an email address.
const emailSep = "\x1F"

func clampLimit(n int) int {
	if n <= 0 {
		return defaultLimit
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}

// granteeWhere produces a SQL fragment and arg for filtering by grantee_id.
// The empty string means "no filter". `_unassigned` selects rows where
// grantee_id IS NULL.
func granteeWhere(col, granteeID string) (string, []any) {
	switch granteeID {
	case "":
		return "", nil
	case UnassignedGrantee:
		return fmt.Sprintf("%s IS NULL", col), nil
	default:
		return fmt.Sprintf("%s = ?", col), []any{granteeID}
	}
}

func splitEmails(blob string) []string {
	if blob == "" {
		return []string{}
	}
	return strings.Split(blob, emailSep)
}

// ListGrantees returns every grantee with its associated emails (mail side) and
// status. Used by the MCP list_grantees tool and the grantee-admin CLI.
func (s *Store) ListGrantees(ctx context.Context) ([]Grantee, error) {
	rows, err := s.query(ctx, `
		SELECT g.grantee_id, COALESCE(g.display_name,''), g.status, g.created_at,
		       COALESCE(string_agg(e.email::text, E'\x1F'), '')
		FROM grantees g
		LEFT JOIN grantee_emails e ON e.grantee_id = g.grantee_id
		GROUP BY g.grantee_id, g.display_name, g.status, g.created_at
		ORDER BY g.grantee_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Grantee
	for rows.Next() {
		var g Grantee
		var created int64
		var emailsBlob string
		if err := rows.Scan(&g.GranteeID, &g.DisplayName, &g.Status, &created, &emailsBlob); err != nil {
			return nil, err
		}
		g.CreatedAt = time.Unix(created, 0).UTC()
		g.Emails = splitEmails(emailsBlob)
		out = append(out, g)
	}
	return out, rows.Err()
}

// GranteeSummaries returns grantees plus their emails and active-doc count.
func (s *Store) GranteeSummaries(ctx context.Context) ([]GranteeSummary, error) {
	rows, err := s.query(ctx, `
		SELECT g.grantee_id, COALESCE(g.display_name,''), g.status,
		  COALESCE(string_agg(DISTINCT e.email::text, E'\x1F'), ''),
		  (SELECT COUNT(*) FROM docs d WHERE d.grantee_id = g.grantee_id)
		FROM grantees g
		LEFT JOIN grantee_emails e ON e.grantee_id = g.grantee_id
		GROUP BY g.grantee_id, g.display_name, g.status
		ORDER BY g.grantee_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GranteeSummary
	for rows.Next() {
		var gs GranteeSummary
		var emailsBlob string
		if err := rows.Scan(&gs.GranteeID, &gs.DisplayName, &gs.Status, &emailsBlob, &gs.DocumentCount); err != nil {
			return nil, err
		}
		gs.Emails = splitEmails(emailsBlob)
		out = append(out, gs)
	}
	return out, rows.Err()
}

// ListMessagesByGrantee returns message summaries ordered by (date DESC, id DESC).
func (s *Store) ListMessagesByGrantee(ctx context.Context, granteeID string, since, until *time.Time, limit int, cursorID int64) ([]MessageSummary, int64, error) {
	limit = clampLimit(limit)

	var where []string
	var args []any
	if gw, ga := granteeWhere("m.grantee_id", granteeID); gw != "" {
		where = append(where, gw)
		args = append(args, ga...)
	} else if granteeID == "" {
		return nil, 0, errors.New("granteeID is required")
	}
	if since != nil {
		where = append(where, "m.date >= ?")
		args = append(args, since.Unix())
	}
	if until != nil {
		where = append(where, "m.date <= ?")
		args = append(args, until.Unix())
	}
	if cursorID > 0 {
		where = append(where, "m.id < ?")
		args = append(args, cursorID)
	}

	q := `
		SELECT m.id, m.message_id, m.thread_id, m.grantee_id, m.folder,
		       m.from_addr, COALESCE(m.from_name, ''),
		       COALESCE(m.subject, ''), m.date,
		       EXISTS(SELECT 1 FROM attachments a WHERE a.message_id = m.id),
		       COALESCE(SUBSTR(m.body_text, 1, ?), '')
		FROM messages m
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY m.date DESC, m.id DESC
		LIMIT ?
	`
	args = append([]any{snippetLen}, args...)
	args = append(args, limit+1)

	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	return scanMessageSummaries(rows, limit)
}

func scanMessageSummaries(rows *sql.Rows, limit int) ([]MessageSummary, int64, error) {
	var out []MessageSummary
	for rows.Next() {
		var m MessageSummary
		var fromName, snippet string
		var date int64
		if err := rows.Scan(&m.ID, &m.MessageID, &m.ThreadID, &m.GranteeID, &m.Folder,
			&m.From.Email, &fromName,
			&m.Subject, &date,
			&m.HasAttachments,
			&snippet,
		); err != nil {
			return nil, 0, err
		}
		m.From.Name = fromName
		m.Date = time.Unix(date, 0)
		m.Snippet = strings.TrimSpace(snippet)
		out = append(out, m)
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

// GetMessage looks up a message by either its numeric id (as a string) or by
// RFC 5322 Message-ID. Returns the message, its attachments, and recipients.
func (s *Store) GetMessage(ctx context.Context, idOrMessageID string) (*Message, []Attachment, []Recipient, error) {
	if idOrMessageID == "" {
		return nil, nil, nil, errors.New("id or message_id required")
	}

	var (
		row *sql.Row
		m   Message
	)
	if n, err := strconv.ParseInt(idOrMessageID, 10, 64); err == nil {
		row = s.queryRow(ctx, messageSelectByID, n)
	} else {
		row = s.queryRow(ctx, messageSelectByMessageID, idOrMessageID)
	}

	var fromName, inReplyTo, subject, bodyText, bodyHTML, references, toBlob, ccBlob string
	var date int64
	if err := row.Scan(
		&m.ID, &m.MessageID, &m.ThreadID, &m.GranteeID, &m.Folder, &m.UID, &m.UIDValidity,
		&inReplyTo, &references,
		&m.From.Email, &fromName,
		&toBlob, &ccBlob,
		&subject, &date,
		&bodyText, &bodyHTML,
		&m.SizeBytes,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, err
	}
	m.From.Name = fromName
	m.InReplyTo = inReplyTo
	if references != "" {
		m.References = strings.Fields(references)
	}
	m.Subject = subject
	m.Date = time.Unix(date, 0)
	m.BodyText = bodyText
	m.BodyHTML = bodyHTML
	if toBlob != "" {
		_ = json.Unmarshal([]byte(toBlob), &m.To)
	}
	if ccBlob != "" {
		_ = json.Unmarshal([]byte(ccBlob), &m.Cc)
	}

	atts, err := s.ListAttachmentsByMessage(ctx, m.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	recs, err := s.listRecipients(ctx, m.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	return &m, atts, recs, nil
}

const messageColumns = `
	m.id, m.message_id, m.thread_id, m.grantee_id, m.folder, m.uid, m.uid_validity,
	COALESCE(m.in_reply_to, ''), COALESCE(m.references_raw, ''),
	m.from_addr, COALESCE(m.from_name, ''),
	m.to_addrs, COALESCE(m.cc_addrs, ''),
	COALESCE(m.subject, ''), m.date,
	COALESCE(m.body_text, ''), COALESCE(m.body_html, ''),
	m.size_bytes
`

var (
	messageSelectByID        = `SELECT ` + messageColumns + ` FROM messages m WHERE m.id = ?`
	messageSelectByMessageID = `SELECT ` + messageColumns + ` FROM messages m WHERE m.message_id = ?`
)

func (s *Store) listRecipients(ctx context.Context, messageID int64) ([]Recipient, error) {
	rows, err := s.query(ctx,
		`SELECT kind, email, COALESCE(name, '') FROM message_recipients WHERE message_id = ? ORDER BY kind, email`,
		messageID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Recipient
	for rows.Next() {
		var r Recipient
		if err := rows.Scan(&r.Kind, &r.Email, &r.Name); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) ListAttachmentsByMessage(ctx context.Context, messageID int64) ([]Attachment, error) {
	rows, err := s.query(ctx, `
		SELECT id, COALESCE(filename, ''), COALESCE(mime_type, ''), size_bytes, sha256, path
		FROM attachments WHERE message_id = ? ORDER BY id
	`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Attachment
	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.ID, &a.Filename, &a.MimeType, &a.SizeBytes, &a.SHA256, &a.Path); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) ListThreadsByGrantee(ctx context.Context, granteeID string, since, until *time.Time, limit int, cursorID int64) ([]ThreadSummary, int64, error) {
	limit = clampLimit(limit)

	var where []string
	var args []any
	if gw, ga := granteeWhere("t.grantee_id", granteeID); gw != "" {
		where = append(where, gw)
		args = append(args, ga...)
	} else if granteeID == "" {
		return nil, 0, errors.New("granteeID is required")
	}
	if since != nil {
		where = append(where, "t.last_seen_at >= ?")
		args = append(args, since.Unix())
	}
	if until != nil {
		where = append(where, "t.first_seen_at <= ?")
		args = append(args, until.Unix())
	}
	if cursorID > 0 {
		where = append(where, "t.id < ?")
		args = append(args, cursorID)
	}

	q := `
		SELECT t.id, t.root_message_id, COALESCE(t.subject_norm, ''), t.grantee_id,
		       t.first_seen_at, t.last_seen_at,
		       (SELECT COUNT(*) FROM messages m WHERE m.thread_id = t.id) AS msg_count
		FROM threads t
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY t.last_seen_at DESC, t.id DESC
		LIMIT ?
	`
	args = append(args, limit+1)

	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []ThreadSummary
	for rows.Next() {
		var t ThreadSummary
		var first, last int64
		if err := rows.Scan(&t.ID, &t.RootMessageID, &t.SubjectNorm, &t.GranteeID, &first, &last, &t.MessageCount); err != nil {
			return nil, 0, err
		}
		t.FirstSeenAt = time.Unix(first, 0)
		t.LastSeenAt = time.Unix(last, 0)
		out = append(out, t)
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

// GetThreadMessages returns the thread record and its messages in chronological
// order. Returns (nil, nil, nil) when the thread does not exist.
func (s *Store) GetThreadMessages(ctx context.Context, threadID int64) (*Thread, []MessageSummary, error) {
	t, err := s.GetThread(ctx, threadID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	rows, err := s.query(ctx, `
		SELECT m.id, m.message_id, m.thread_id, m.grantee_id, m.folder,
		       m.from_addr, COALESCE(m.from_name, ''),
		       COALESCE(m.subject, ''), m.date,
		       EXISTS(SELECT 1 FROM attachments a WHERE a.message_id = m.id),
		       COALESCE(SUBSTR(m.body_text, 1, ?), '')
		FROM messages m
		WHERE m.thread_id = ?
		ORDER BY m.date ASC, m.id ASC
	`, snippetLen, threadID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	msgs, _, err := scanMessageSummaries(rows, maxLimit*100)
	if err != nil {
		return nil, nil, err
	}
	return t, msgs, nil
}

func (s *Store) SearchMessages(ctx context.Context, p SearchParams) ([]MessageSummary, int64, error) {
	limit := clampLimit(p.Limit)

	var where []string
	var args []any

	if gw, ga := granteeWhere("m.grantee_id", p.GranteeID); gw != "" {
		where = append(where, gw)
		args = append(args, ga...)
	}
	if p.From != "" {
		where = append(where, "m.from_addr ILIKE ?")
		args = append(args, "%"+p.From+"%")
	}
	if p.Subject != "" {
		where = append(where, "m.subject ILIKE ?")
		args = append(args, "%"+p.Subject+"%")
	}
	if p.Body != "" {
		where = append(where, "m.body_text ILIKE ?")
		args = append(args, "%"+p.Body+"%")
	}
	if p.Since != nil {
		where = append(where, "m.date >= ?")
		args = append(args, p.Since.Unix())
	}
	if p.Until != nil {
		where = append(where, "m.date <= ?")
		args = append(args, p.Until.Unix())
	}
	if p.Cursor > 0 {
		where = append(where, "m.id < ?")
		args = append(args, p.Cursor)
	}

	if len(where) == 0 {
		return nil, 0, errors.New("at least one search filter is required")
	}

	q := `
		SELECT m.id, m.message_id, m.thread_id, m.grantee_id, m.folder,
		       m.from_addr, COALESCE(m.from_name, ''),
		       COALESCE(m.subject, ''), m.date,
		       EXISTS(SELECT 1 FROM attachments a WHERE a.message_id = m.id),
		       COALESCE(SUBSTR(m.body_text, 1, ?), '')
		FROM messages m
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY m.date DESC, m.id DESC
		LIMIT ?
	`
	args = append([]any{snippetLen}, args...)
	args = append(args, limit+1)

	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	return scanMessageSummaries(rows, limit)
}
