package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed schema.sql
var schemaSQL string

var (
	ErrNotFound = errors.New("not found")
)

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

// ---------- grantees ----------

// EnsureGrantee inserts a grantee row if missing, but never overwrites an
// existing one. Use this from the `authorize` flow, where display_name is
// only meaningful at first creation.
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
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO grantees(grantee_id, display_name, status, created_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(grantee_id) DO NOTHING
	`, g.GranteeID, nullStr(g.DisplayName), g.Status, createdAt)
	return err
}

// UpsertGrantee inserts or updates a grantee row by id. Returns ErrNotFound
// only on read-after-write inconsistency; normally never errors with that.
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
	_, err := s.db.ExecContext(ctx, `
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
	res, err := s.db.ExecContext(ctx, `UPDATE grantees SET status = ? WHERE grantee_id = ?`, status, granteeID)
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
	row := s.db.QueryRowContext(ctx, `
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

func (s *Store) ListGrantees(ctx context.Context) ([]Grantee, error) {
	return s.listGrantees(ctx, "ORDER BY grantee_id")
}

func (s *Store) listGrantees(ctx context.Context, whereOrderClause string) ([]Grantee, error) {
	q := `SELECT grantee_id, COALESCE(display_name,''), status, created_at FROM grantees ` + whereOrderClause
	rows, err := s.db.QueryContext(ctx, q)
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

// GranteeSummaries returns grantees plus their active-doc count. Used by the
// MCP list_grantees tool.
func (s *Store) GranteeSummaries(ctx context.Context) ([]GranteeSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT g.grantee_id, COALESCE(g.display_name,''),
		  (SELECT COUNT(*) FROM docs d WHERE d.grantee_id = g.grantee_id)
		FROM grantees g
		ORDER BY g.grantee_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GranteeSummary
	for rows.Next() {
		var s GranteeSummary
		if err := rows.Scan(&s.GranteeID, &s.DisplayName, &s.DocumentCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
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
	// SQLite ON CONFLICT DO UPDATE returns RETURNING id reliably from 3.35+.
	var id int64
	err := s.db.QueryRowContext(ctx, `
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
	res, err := s.db.ExecContext(ctx,
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
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, grantee_id, source_type, drive_id, added_at
		FROM grantee_sources WHERE grantee_id = ? ORDER BY id
	`, granteeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		var s Source
		var addedAt int64
		if err := rows.Scan(&s.ID, &s.GranteeID, &s.SourceType, &s.DriveID, &addedAt); err != nil {
			return nil, err
		}
		s.AddedAt = time.Unix(addedAt, 0).UTC()
		out = append(out, s)
	}
	return out, rows.Err()
}

// AllSourceDriveIDs returns every drive_id currently registered as a source.
// Used by the unregistered-scan to filter out doc sources that are already
// registered (so they don't appear in the shadow inbox).
func (s *Store) AllSourceDriveIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT drive_id FROM grantee_sources`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ---------- docs ----------

func (s *Store) GetDocByID(ctx context.Context, docID string) (*Doc, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT doc_id, grantee_id, title, owner_email, content_markdown,
		  modified_at, synced_at, source_type, source_drive_id,
		  had_images, had_comments, status, COALESCE(last_error,'')
		FROM docs WHERE doc_id = ?
	`, docID)
	return scanDoc(row)
}

func (s *Store) GetDocForGrantee(ctx context.Context, granteeID, docID string) (*Doc, error) {
	row := s.db.QueryRowContext(ctx, `
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
	var hadImages, hadComments int
	if err := row.Scan(
		&d.DocID, &d.GranteeID, &d.Title, &d.OwnerEmail, &d.ContentMarkdown,
		&modifiedAt, &syncedAt, &d.SourceType, &d.SourceDriveID,
		&hadImages, &hadComments, &d.Status, &d.LastError,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	d.ModifiedAt = time.Unix(modifiedAt, 0).UTC()
	d.SyncedAt = time.Unix(syncedAt, 0).UTC()
	d.HadImages = hadImages != 0
	d.HadComments = hadComments != 0
	return &d, nil
}

// UpsertDoc writes the full doc row and stamps last_seen_cycle = cycleID.
// Always overwrites content_markdown. Use TouchDocSeen for unchanged docs.
func (s *Store) UpsertDoc(ctx context.Context, d Doc, cycleID int64) error {
	if d.DocID == "" || d.GranteeID == "" {
		return errors.New("doc_id and grantee_id required")
	}
	now := time.Now().Unix()
	if d.Status == "" {
		d.Status = StatusActive
	}
	_, err := s.db.ExecContext(ctx, `
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
		boolInt(d.HadImages), boolInt(d.HadComments), d.Status, nullStr(d.LastError), cycleID,
	)
	return err
}

// TouchDocSeen bumps last_seen_cycle and synced_at for an unchanged doc and
// clears any stale/error status (the doc is alive and unchanged this cycle).
func (s *Store) TouchDocSeen(ctx context.Context, docID string, cycleID int64) error {
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx, `
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
	_, err := s.db.ExecContext(ctx,
		`UPDATE docs SET status = 'stale', last_error = ? WHERE doc_id = ?`,
		nullStr(lastErr), docID,
	)
	return err
}

func (s *Store) MarkDocError(ctx context.Context, docID, lastErr string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE docs SET status = 'error', last_error = ? WHERE doc_id = ?`,
		nullStr(lastErr), docID,
	)
	return err
}

// MarkUnseenStale flips status='active' rows whose source still exists in the
// active registry but whose last_seen_cycle predates cycleID. Returns the
// number of rows marked stale.
func (s *Store) MarkUnseenStale(ctx context.Context, cycleID int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
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
	// Cursor is modified_at unix seconds; results are returned in
	// descending modified_at order. cursor == 0 means "from the start".
	q := `
		SELECT doc_id, title, modified_at, synced_at, had_images, had_comments, status
		FROM docs WHERE grantee_id = ?
		  AND (? = 0 OR modified_at < ?)
		ORDER BY modified_at DESC, doc_id DESC LIMIT ?
	`
	rows, err := s.db.QueryContext(ctx, q, granteeID, cursor, cursor, limit+1)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []DocSummary
	for rows.Next() {
		var d DocSummary
		var modifiedAt, syncedAt int64
		var hadImages, hadComments int
		if err := rows.Scan(&d.DocID, &d.Title, &modifiedAt, &syncedAt, &hadImages, &hadComments, &d.Status); err != nil {
			return nil, 0, err
		}
		d.ModifiedAt = time.Unix(modifiedAt, 0).UTC()
		d.SyncedAt = time.Unix(syncedAt, 0).UTC()
		d.HadImages = hadImages != 0
		d.HadComments = hadComments != 0
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

// ---------- unregistered ----------

// UpsertUnregisteredDoc inserts a new row at status='pending' on first sight,
// or updates last_seen / title / mime_type / owner_email on subsequent sights.
// Status is NEVER overwritten (human reviewers own it).
func (s *Store) UpsertUnregisteredDoc(ctx context.Context, u UnregisteredDoc) error {
	if u.DocID == "" {
		return errors.New("doc_id required")
	}
	now := time.Now().Unix()
	first := u.FirstSeen.Unix()
	if u.FirstSeen.IsZero() {
		first = now
	}
	last := u.LastSeen.Unix()
	if u.LastSeen.IsZero() {
		last = now
	}
	if u.Status == "" {
		u.Status = UnregisteredPending
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO unregistered_docs(doc_id, owner_email, title, mime_type, first_seen, last_seen, status)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(doc_id) DO UPDATE SET
		  owner_email = excluded.owner_email,
		  title = excluded.title,
		  mime_type = excluded.mime_type,
		  last_seen = excluded.last_seen
	`, u.DocID, nullStr(u.OwnerEmail), nullStr(u.Title), nullStr(u.MimeType), first, last, u.Status)
	return err
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
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sync_state(k, v) VALUES('cycle_counter', ?)
		ON CONFLICT(k) DO UPDATE SET v = excluded.v
	`, strconv.FormatInt(next, 10)); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return next, nil
}

func (s *Store) SetLastSuccessfulSync(ctx context.Context, t time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sync_state(k, v) VALUES('last_successful_sync', ?)
		ON CONFLICT(k) DO UPDATE SET v = excluded.v
	`, strconv.FormatInt(t.Unix(), 10))
	return err
}

func (s *Store) LastSuccessfulSync(ctx context.Context) (time.Time, error) {
	var raw sql.NullString
	err := s.db.QueryRowContext(ctx,
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

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
