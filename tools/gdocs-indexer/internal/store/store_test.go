package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSchemaIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s1, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, s1.Close())
	s2, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, s2.Close())
}

func TestUpsertGrantee(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	require.NoError(t, s.UpsertGrantee(ctx, Grantee{
		GranteeID: "acme", DisplayName: "Acme",
	}))

	g, err := s.GetGrantee(ctx, "acme")
	require.NoError(t, err)
	require.Equal(t, "acme", g.GranteeID)
	require.Equal(t, "Acme", g.DisplayName)
	require.Equal(t, StatusActive, g.Status)

	// Update display_name; same id.
	require.NoError(t, s.UpsertGrantee(ctx, Grantee{
		GranteeID: "acme", DisplayName: "Acme Foundation",
	}))
	g, err = s.GetGrantee(ctx, "acme")
	require.NoError(t, err)
	require.Equal(t, "Acme Foundation", g.DisplayName)

	// SetGranteeStatus
	require.NoError(t, s.SetGranteeStatus(ctx, "acme", StatusPaused))
	g, err = s.GetGrantee(ctx, "acme")
	require.NoError(t, err)
	require.Equal(t, StatusPaused, g.Status)

	// ListActiveGrantees skips paused.
	active, err := s.ListActiveGrantees(ctx)
	require.NoError(t, err)
	require.Empty(t, active)
	all, err := s.ListGrantees(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
}

func TestUpsertSourceDuplicateNoError(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	require.NoError(t, s.UpsertGrantee(ctx, Grantee{GranteeID: "acme"}))

	id1, err := s.UpsertSource(ctx, Source{GranteeID: "acme", SourceType: SourceTypeFolder, DriveID: "folder-1"})
	require.NoError(t, err)
	require.NotZero(t, id1)

	// Same (grantee, drive_id): upsert no-ops, returns same id.
	id2, err := s.UpsertSource(ctx, Source{GranteeID: "acme", SourceType: SourceTypeFolder, DriveID: "folder-1"})
	require.NoError(t, err)
	require.Equal(t, id1, id2)

	srcs, err := s.ListSourcesForGrantee(ctx, "acme")
	require.NoError(t, err)
	require.Len(t, srcs, 1)
}

func TestDocUpsertAndUnseenStale(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	require.NoError(t, s.UpsertGrantee(ctx, Grantee{GranteeID: "acme"}))
	_, err := s.UpsertSource(ctx, Source{GranteeID: "acme", SourceType: SourceTypeFolder, DriveID: "folder-1"})
	require.NoError(t, err)

	cycle, err := s.NextCycleID(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), cycle)

	mod := time.Unix(1_700_000_000, 0).UTC()
	require.NoError(t, s.UpsertDoc(ctx, Doc{
		DocID: "doc-1", GranteeID: "acme", Title: "Hello",
		OwnerEmail: "o@a.org", ContentMarkdown: "# hi",
		ModifiedAt: mod, SourceType: SourceTypeFolder, SourceDriveID: "folder-1",
	}, cycle))

	d, err := s.GetDocByID(ctx, "doc-1")
	require.NoError(t, err)
	require.Equal(t, "# hi", d.ContentMarkdown)
	require.Equal(t, StatusActive, d.Status)
	require.Equal(t, mod, d.ModifiedAt)

	// Touch as unchanged in same cycle: status stays active.
	require.NoError(t, s.TouchDocSeen(ctx, "doc-1", cycle))

	// Bump cycle without re-touching the doc: it should land in MarkUnseenStale.
	cycle2, err := s.NextCycleID(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), cycle2)

	n, err := s.MarkUnseenStale(ctx, cycle2)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	d, err = s.GetDocByID(ctx, "doc-1")
	require.NoError(t, err)
	require.Equal(t, StatusStale, d.Status)
	require.NotEmpty(t, d.ContentMarkdown, "markdown must be preserved when going stale")

	// Re-seeing the doc in cycle 3 flips it back to active.
	cycle3, err := s.NextCycleID(ctx)
	require.NoError(t, err)
	require.NoError(t, s.TouchDocSeen(ctx, "doc-1", cycle3))
	d, err = s.GetDocByID(ctx, "doc-1")
	require.NoError(t, err)
	require.Equal(t, StatusActive, d.Status)
}

func TestGetDocForGranteeCrossGranteeRefused(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	require.NoError(t, s.UpsertGrantee(ctx, Grantee{GranteeID: "a"}))
	require.NoError(t, s.UpsertGrantee(ctx, Grantee{GranteeID: "b"}))
	cycle, err := s.NextCycleID(ctx)
	require.NoError(t, err)
	require.NoError(t, s.UpsertDoc(ctx, Doc{
		DocID: "doc-a", GranteeID: "a", Title: "A",
		OwnerEmail: "o@a.org", ContentMarkdown: "x",
		ModifiedAt: time.Unix(1, 0), SourceType: SourceTypeDoc, SourceDriveID: "doc-a",
	}, cycle))

	_, err = s.GetDocForGrantee(ctx, "b", "doc-a")
	require.ErrorIs(t, err, ErrNotFound)
	d, err := s.GetDocForGrantee(ctx, "a", "doc-a")
	require.NoError(t, err)
	require.Equal(t, "doc-a", d.DocID)
}

func TestUpsertUnregisteredDocPreservesStatus(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	require.NoError(t, s.UpsertUnregisteredDoc(ctx, UnregisteredDoc{
		DocID: "unreg-1", Title: "Sus Doc", OwnerEmail: "stranger@example.org",
		MimeType: "application/vnd.google-apps.document",
	}))
	// Human reviewer flips to ignored. We don't expose a setter, so set via raw SQL.
	_, err := s.DB().ExecContext(ctx, `UPDATE unregistered_docs SET status = 'ignored' WHERE doc_id = ?`, "unreg-1")
	require.NoError(t, err)

	// Second sight should not overwrite status.
	require.NoError(t, s.UpsertUnregisteredDoc(ctx, UnregisteredDoc{
		DocID: "unreg-1", Title: "Sus Doc Renamed", OwnerEmail: "stranger@example.org",
		MimeType: "application/vnd.google-apps.document",
	}))
	var status, title string
	require.NoError(t, s.DB().QueryRowContext(ctx,
		`SELECT status, title FROM unregistered_docs WHERE doc_id = ?`, "unreg-1",
	).Scan(&status, &title))
	require.Equal(t, "ignored", status)
	require.Equal(t, "Sus Doc Renamed", title)
}

func TestNextCycleIDMonotonic(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	c1, err := s.NextCycleID(ctx)
	require.NoError(t, err)
	c2, err := s.NextCycleID(ctx)
	require.NoError(t, err)
	c3, err := s.NextCycleID(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), c1)
	require.Equal(t, int64(2), c2)
	require.Equal(t, int64(3), c3)
}

func TestListDocsForGranteePagination(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	require.NoError(t, s.UpsertGrantee(ctx, Grantee{GranteeID: "acme"}))
	cycle, err := s.NextCycleID(ctx)
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		require.NoError(t, s.UpsertDoc(ctx, Doc{
			DocID: fakeID(i), GranteeID: "acme", Title: "t",
			OwnerEmail: "o@a.org", ContentMarkdown: "x",
			ModifiedAt: time.Unix(int64(1000+i), 0),
			SourceType: SourceTypeDoc, SourceDriveID: fakeID(i),
		}, cycle))
	}
	docs, next, err := s.ListDocsForGrantee(ctx, "acme", 2, 0)
	require.NoError(t, err)
	require.Len(t, docs, 2)
	require.NotZero(t, next)
	docs2, _, err := s.ListDocsForGrantee(ctx, "acme", 100, next)
	require.NoError(t, err)
	require.Len(t, docs2, 3, "rest of the docs returned with cursor")
}

func fakeID(i int) string {
	const ids = "abcdefghij"
	return "doc-" + string(ids[i])
}
