package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ironcd/philanthropy-os/tools/gdocs-indexer/internal/drive"
	"github.com/ironcd/philanthropy-os/tools/gdocs-indexer/internal/store"
)

// fakeDrive is an in-memory implementation of DriveAPI for tests.
type fakeDrive struct {
	folders     map[string][]drive.File // folderID -> children
	files       map[string]drive.File   // fileID -> metadata for GetFile / mid-folder lookup
	exports     map[string]string       // fileID -> markdown body
	exportErr   map[string]error
	sharedWith  []drive.File
	listFolderC int
	exportC     int
}

func newFakeDrive() *fakeDrive {
	return &fakeDrive{
		folders:   map[string][]drive.File{},
		files:     map[string]drive.File{},
		exports:   map[string]string{},
		exportErr: map[string]error{},
	}
}

func (f *fakeDrive) ListFolder(_ context.Context, folderID, _ string) (*drive.FileList, error) {
	f.listFolderC++
	return &drive.FileList{Files: f.folders[folderID]}, nil
}

func (f *fakeDrive) ListSharedWithMe(_ context.Context, _ string) (*drive.FileList, error) {
	return &drive.FileList{Files: f.sharedWith}, nil
}

func (f *fakeDrive) GetFile(_ context.Context, fileID, _ string) (*drive.File, error) {
	file, ok := f.files[fileID]
	if !ok {
		return nil, &drive.StatusError{Status: http.StatusNotFound, URL: "fake://" + fileID}
	}
	return &file, nil
}

func (f *fakeDrive) ExportMarkdown(_ context.Context, fileID string) (string, error) {
	f.exportC++
	if err, ok := f.exportErr[fileID]; ok {
		return "", err
	}
	body, ok := f.exports[fileID]
	if !ok {
		return "", &drive.StatusError{Status: http.StatusNotFound, URL: "fake://" + fileID + "/export"}
	}
	return body, nil
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func makeFile(id, name, mime, owner, modified string) drive.File {
	return drive.File{
		ID: id, Name: name, MimeType: mime, ModifiedTime: modified,
		Owners: []drive.Owner{{EmailAddress: owner}},
	}
}

func TestSyncIngestsEveryDocInFolderRegardlessOfOwner(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	require.NoError(t, st.UpsertGrantee(ctx, store.Grantee{GranteeID: "acme"}))
	_, err := st.UpsertSource(ctx, store.Source{GranteeID: "acme", SourceType: store.SourceTypeFolder, DriveID: "folder-1"})
	require.NoError(t, err)

	// Folder-as-trust-boundary: docs added by a grantee staffer (different
	// Drive owner than anyone we registered) ingest just like any other
	// folder content. Non-doc mimes are dropped at the folder walk and never
	// reach processDoc.
	d := newFakeDrive()
	d.folders["folder-1"] = []drive.File{
		makeFile("doc-balvi", "From Balvi", drive.MimeDoc, "balvi-admin@balvi.org", "2026-01-02T03:04:05Z"),
		makeFile("doc-staff", "From grantee staffer", drive.MimeDoc, "alice@grantee.org", "2026-01-02T03:04:05Z"),
		makeFile("doc-bin", "Spreadsheet", "application/vnd.google-apps.spreadsheet", "balvi-admin@balvi.org", "2026-01-02T03:04:05Z"),
	}
	d.exports["doc-balvi"] = "# balvi"
	d.exports["doc-staff"] = "# staff"

	stats, err := Run(ctx, st, d, newDiscardLogger())
	require.NoError(t, err)
	require.Equal(t, 2, stats.DocsSynced)
	require.Equal(t, 0, stats.DocsSkipped)

	staff, err := st.GetDocByID(ctx, "doc-staff")
	require.NoError(t, err)
	require.Equal(t, "# staff", staff.ContentMarkdown)
	require.Equal(t, "acme", staff.GranteeID)
	require.Equal(t, "alice@grantee.org", staff.OwnerEmail, "audit metadata preserved")
}

func TestSyncUnchangedDocIsNoop(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	require.NoError(t, st.UpsertGrantee(ctx, store.Grantee{GranteeID: "acme"}))
	_, err := st.UpsertSource(ctx, store.Source{GranteeID: "acme", SourceType: store.SourceTypeFolder, DriveID: "folder-1"})
	require.NoError(t, err)

	d := newFakeDrive()
	d.folders["folder-1"] = []drive.File{
		makeFile("doc-1", "A", drive.MimeDoc, "owner@acme.org", "2026-01-02T03:04:05Z"),
	}
	d.exports["doc-1"] = "# content"

	_, err = Run(ctx, st, d, newDiscardLogger())
	require.NoError(t, err)
	exportsAfterFirst := d.exportC
	require.Equal(t, 1, exportsAfterFirst)

	// Second cycle: same modifiedTime -> should not re-export.
	stats, err := Run(ctx, st, d, newDiscardLogger())
	require.NoError(t, err)
	require.Equal(t, exportsAfterFirst, d.exportC, "unchanged docs must not re-export (acceptance criterion #4)")
	require.Equal(t, 0, stats.DocsSynced)
	require.Equal(t, 1, stats.DocsUnchanged)
}

func TestSyncRevisesContentWhenModifiedTimeChanges(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	require.NoError(t, st.UpsertGrantee(ctx, store.Grantee{GranteeID: "acme"}))
	_, err := st.UpsertSource(ctx, store.Source{GranteeID: "acme", SourceType: store.SourceTypeFolder, DriveID: "folder-1"})
	require.NoError(t, err)

	d := newFakeDrive()
	d.folders["folder-1"] = []drive.File{
		makeFile("doc-1", "A", drive.MimeDoc, "o@a.org", "2026-01-01T00:00:00Z"),
	}
	d.exports["doc-1"] = "v1"

	_, err = Run(ctx, st, d, newDiscardLogger())
	require.NoError(t, err)
	got, err := st.GetDocByID(ctx, "doc-1")
	require.NoError(t, err)
	require.Equal(t, "v1", got.ContentMarkdown)

	// Bump modifiedTime + body.
	d.folders["folder-1"] = []drive.File{
		makeFile("doc-1", "A renamed", drive.MimeDoc, "o@a.org", "2026-01-02T00:00:00Z"),
	}
	d.exports["doc-1"] = "v2"

	stats, err := Run(ctx, st, d, newDiscardLogger())
	require.NoError(t, err)
	require.Equal(t, 1, stats.DocsSynced)
	got, err = st.GetDocByID(ctx, "doc-1")
	require.NoError(t, err)
	require.Equal(t, "v2", got.ContentMarkdown)
	require.Equal(t, "A renamed", got.Title)
}

func TestSyncUnregisteredScan(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	require.NoError(t, st.UpsertGrantee(ctx, store.Grantee{GranteeID: "acme"}))
	_, err := st.UpsertSource(ctx, store.Source{GranteeID: "acme", SourceType: store.SourceTypeFolder, DriveID: "folder-1"})
	require.NoError(t, err)

	d := newFakeDrive()
	d.folders["folder-1"] = []drive.File{} // empty registered folder
	// One file shared with the SA by a stranger.
	d.sharedWith = []drive.File{
		makeFile("unreg-1", "Sus", drive.MimeDoc, "stranger@example.org", "2026-01-02T03:04:05Z"),
		// Add a file that IS already registered as a doc source — should be skipped.
		makeFile("doc-direct", "Direct", drive.MimeDoc, "o@a.org", "2026-01-02T03:04:05Z"),
	}
	// And register that "direct" doc as a source so it's not unregistered.
	_, err = st.UpsertSource(ctx, store.Source{GranteeID: "acme", SourceType: store.SourceTypeDoc, DriveID: "doc-direct"})
	require.NoError(t, err)
	d.files["doc-direct"] = makeFile("doc-direct", "Direct", drive.MimeDoc, "o@a.org", "2026-01-02T03:04:05Z")
	d.exports["doc-direct"] = "direct content"

	stats, err := Run(ctx, st, d, newDiscardLogger())
	require.NoError(t, err)
	require.Equal(t, 1, stats.UnregisteredSeen)

	// docs table should contain only doc-direct.
	doc, err := st.GetDocByID(ctx, "doc-direct")
	require.NoError(t, err)
	require.Equal(t, "doc-direct", doc.DocID)

	// unregistered_docs should contain only unreg-1.
	var n int
	require.NoError(t, st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM unregistered_docs WHERE doc_id = ?`, "unreg-1",
	).Scan(&n))
	require.Equal(t, 1, n)
	require.NoError(t, st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM unregistered_docs WHERE doc_id = ?`, "doc-direct",
	).Scan(&n))
	require.Equal(t, 0, n)
}

func TestSyncMarksStaleOnExport404(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	require.NoError(t, st.UpsertGrantee(ctx, store.Grantee{GranteeID: "acme"}))
	_, err := st.UpsertSource(ctx, store.Source{GranteeID: "acme", SourceType: store.SourceTypeFolder, DriveID: "folder-1"})
	require.NoError(t, err)

	d := newFakeDrive()
	d.folders["folder-1"] = []drive.File{
		makeFile("doc-1", "A", drive.MimeDoc, "o@a.org", "2026-01-01T00:00:00Z"),
	}
	d.exports["doc-1"] = "v1"
	_, err = Run(ctx, st, d, newDiscardLogger())
	require.NoError(t, err)

	// Second cycle: modifiedTime advances so we'd re-export, but the SA
	// has lost access.
	d.folders["folder-1"] = []drive.File{
		makeFile("doc-1", "A", drive.MimeDoc, "o@a.org", "2026-01-02T00:00:00Z"),
	}
	d.exports = map[string]string{}
	d.exportErr["doc-1"] = &drive.StatusError{Status: http.StatusForbidden, URL: "fake"}

	stats, err := Run(ctx, st, d, newDiscardLogger())
	require.NoError(t, err)
	require.GreaterOrEqual(t, stats.DocsMarkedStale, 1)

	got, err := st.GetDocByID(ctx, "doc-1")
	require.NoError(t, err)
	require.Equal(t, store.StatusStale, got.Status)
	require.Equal(t, "v1", got.ContentMarkdown, "stale must preserve cached content")
}

func TestSyncMarksStaleOnDisappearance(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	require.NoError(t, st.UpsertGrantee(ctx, store.Grantee{GranteeID: "acme"}))
	_, err := st.UpsertSource(ctx, store.Source{GranteeID: "acme", SourceType: store.SourceTypeFolder, DriveID: "folder-1"})
	require.NoError(t, err)

	d := newFakeDrive()
	d.folders["folder-1"] = []drive.File{
		makeFile("doc-1", "A", drive.MimeDoc, "o@a.org", "2026-01-01T00:00:00Z"),
	}
	d.exports["doc-1"] = "v1"
	_, err = Run(ctx, st, d, newDiscardLogger())
	require.NoError(t, err)

	// Doc vanishes from the folder listing entirely with no HTTP error.
	d.folders["folder-1"] = nil
	stats, err := Run(ctx, st, d, newDiscardLogger())
	require.NoError(t, err)
	require.GreaterOrEqual(t, stats.DocsMarkedStale, 1, "unseen-in-cycle must also flip to stale")

	got, err := st.GetDocByID(ctx, "doc-1")
	require.NoError(t, err)
	require.Equal(t, store.StatusStale, got.Status)
}

func TestSyncSubfolderRecursion(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	require.NoError(t, st.UpsertGrantee(ctx, store.Grantee{GranteeID: "acme"}))
	_, err := st.UpsertSource(ctx, store.Source{GranteeID: "acme", SourceType: store.SourceTypeFolder, DriveID: "folder-1"})
	require.NoError(t, err)

	d := newFakeDrive()
	d.folders["folder-1"] = []drive.File{
		{ID: "folder-nested", Name: "Sub", MimeType: drive.MimeFolder},
		makeFile("doc-top", "Top", drive.MimeDoc, "o@a.org", "2026-01-01T00:00:00Z"),
	}
	d.folders["folder-nested"] = []drive.File{
		makeFile("doc-deep", "Deep", drive.MimeDoc, "o@a.org", "2026-01-01T00:00:00Z"),
	}
	d.exports["doc-top"] = "top"
	d.exports["doc-deep"] = "deep"

	stats, err := Run(ctx, st, d, newDiscardLogger())
	require.NoError(t, err)
	require.Equal(t, 2, stats.DocsSynced)

	_, err = st.GetDocByID(ctx, "doc-deep")
	require.NoError(t, err)
}

func TestPausedGranteeNotSynced(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	require.NoError(t, st.UpsertGrantee(ctx, store.Grantee{GranteeID: "acme"}))
	_, err := st.UpsertSource(ctx, store.Source{GranteeID: "acme", SourceType: store.SourceTypeFolder, DriveID: "folder-1"})
	require.NoError(t, err)
	require.NoError(t, st.SetGranteeStatus(ctx, "acme", store.StatusPaused))

	d := newFakeDrive()
	d.folders["folder-1"] = []drive.File{
		makeFile("doc-1", "A", drive.MimeDoc, "o@a.org", "2026-01-01T00:00:00Z"),
	}
	d.exports["doc-1"] = "v1"

	stats, err := Run(ctx, st, d, newDiscardLogger())
	require.NoError(t, err)
	require.Equal(t, 0, stats.DocsSynced)
	require.Equal(t, 0, d.listFolderC, "paused grantee must not trigger any Drive calls")
}

func TestRunPropagatesContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	st := openTestStore(t)
	require.NoError(t, st.UpsertGrantee(ctx, store.Grantee{GranteeID: "acme"}))
	_, err := st.UpsertSource(ctx, store.Source{GranteeID: "acme", SourceType: store.SourceTypeFolder, DriveID: "folder-1"})
	require.NoError(t, err)
	cancel()

	d := newFakeDrive()
	_, err = Run(ctx, st, d, newDiscardLogger())
	require.ErrorIs(t, err, context.Canceled)
}

// Exercise the failure path that surfaces non-403/404 errors as DocsFailed.
func TestSyncRecordsTransientExportError(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	require.NoError(t, st.UpsertGrantee(ctx, store.Grantee{GranteeID: "acme"}))
	_, err := st.UpsertSource(ctx, store.Source{GranteeID: "acme", SourceType: store.SourceTypeFolder, DriveID: "folder-1"})
	require.NoError(t, err)
	d := newFakeDrive()
	d.folders["folder-1"] = []drive.File{
		makeFile("doc-1", "A", drive.MimeDoc, "o@a.org", "2026-01-01T00:00:00Z"),
	}
	d.exportErr["doc-1"] = errors.New("transient: connection reset")
	stats, err := Run(ctx, st, d, newDiscardLogger())
	require.NoError(t, err)
	require.Equal(t, 1, stats.DocsFailed)
	// Doc not previously known, so no row to mark.
	_, err = st.GetDocByID(ctx, "doc-1")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestParseDriveTimeAccepted(t *testing.T) {
	tt, err := parseDriveTime("2026-01-02T03:04:05.123456789Z")
	require.NoError(t, err)
	require.False(t, tt.IsZero())
	tt, err = parseDriveTime("2026-01-02T03:04:05Z")
	require.NoError(t, err)
	require.Equal(t, time.UTC, tt.Location())
	_, err = parseDriveTime("not a time")
	require.Error(t, err)
}

// Helps test maintenance: keep the discard logger reachable from outside.
var _ = fmt.Sprintf
