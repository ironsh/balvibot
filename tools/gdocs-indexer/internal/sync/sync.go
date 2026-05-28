// Package sync runs one cycle of the Drive -> SQLite sync. Each cycle:
//  1. Increments a monotonic cycle counter.
//  2. For each active grantee, walks every grantee_sources entry, fetches
//     matching Google Docs, and upserts them into the docs table.
//  3. Scans everything else shared with the SA into unregistered_docs.
//  4. Marks any previously-active doc that wasn't seen in this cycle stale.
//
// Per-doc errors are recorded on the row but do not fail the cycle.
package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ironcd/philanthropy-os/tools/gdocs-indexer/internal/drive"
	"github.com/ironcd/philanthropy-os/tools/gdocs-indexer/internal/store"
)

// DriveAPI is the subset of *drive.Client used by the sync loop. Mocked in
// tests via an in-memory implementation.
type DriveAPI interface {
	ListFolder(ctx context.Context, folderID, pageToken string) (*drive.FileList, error)
	ListSharedWithMe(ctx context.Context, pageToken string) (*drive.FileList, error)
	GetFile(ctx context.Context, fileID, fields string) (*drive.File, error)
	ExportMarkdown(ctx context.Context, fileID string) (string, error)
}

// Stats summarizes one cycle for logging / metrics.
type Stats struct {
	CycleID          int64
	DocsSynced       int // freshly written or rewritten
	DocsUnchanged    int // unchanged-modifiedTime touches
	DocsSkipped      int // mime mismatch
	DocsFailed       int // export errors that landed status=error
	DocsMarkedStale  int // 403/404 + unseen-this-cycle
	UnregisteredSeen int
}

// Run executes one full sync cycle and returns Stats. The cycle never aborts
// on per-doc errors; only context cancellation or unrecoverable DB errors
// short-circuit it.
func Run(ctx context.Context, st *store.Store, d DriveAPI, logger *slog.Logger) (*Stats, error) {
	cycleID, err := st.NextCycleID(ctx)
	if err != nil {
		return nil, fmt.Errorf("bump cycle id: %w", err)
	}
	stats := &Stats{CycleID: cycleID}
	logger = logger.With("cycle_id", cycleID)
	logger.Info("sync cycle start")

	grantees, err := st.ListActiveGrantees(ctx)
	if err != nil {
		return stats, fmt.Errorf("list grantees: %w", err)
	}

	for i := range grantees {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		g := grantees[i]
		if err := syncGrantee(ctx, st, d, &g, cycleID, stats, logger); err != nil {
			logger.Error("grantee sync failed", "grantee_id", g.GranteeID, "err", err)
			// continue to next grantee
		}
	}

	if err := scanUnregistered(ctx, st, d, stats, logger); err != nil {
		logger.Error("unregistered scan failed", "err", err)
	}

	marked, err := st.MarkUnseenStale(ctx, cycleID)
	if err != nil {
		return stats, fmt.Errorf("mark unseen stale: %w", err)
	}
	stats.DocsMarkedStale += int(marked)

	if err := st.SetLastSuccessfulSync(ctx, time.Now()); err != nil {
		return stats, fmt.Errorf("record last_successful_sync: %w", err)
	}

	logger.Info("sync cycle done",
		"synced", stats.DocsSynced,
		"unchanged", stats.DocsUnchanged,
		"skipped", stats.DocsSkipped,
		"failed", stats.DocsFailed,
		"stale", stats.DocsMarkedStale,
		"unregistered_seen", stats.UnregisteredSeen,
	)
	return stats, nil
}

func syncGrantee(ctx context.Context, st *store.Store, d DriveAPI, g *store.Grantee, cycleID int64, stats *Stats, logger *slog.Logger) error {
	srcs, err := st.ListSourcesForGrantee(ctx, g.GranteeID)
	if err != nil {
		return fmt.Errorf("list sources: %w", err)
	}
	gLog := logger.With("grantee_id", g.GranteeID)
	for i := range srcs {
		if err := ctx.Err(); err != nil {
			return err
		}
		src := srcs[i]
		switch src.SourceType {
		case store.SourceTypeFolder:
			if err := walkFolder(ctx, st, d, g, &src, src.DriveID, cycleID, stats, gLog); err != nil {
				gLog.Error("walk folder failed", "drive_id", src.DriveID, "err", err)
			}
		case store.SourceTypeDoc:
			file, err := d.GetFile(ctx, src.DriveID, "")
			if err != nil {
				gLog.Error("doc source get failed", "drive_id", src.DriveID, "err", err)
				continue
			}
			processDoc(ctx, st, d, g, &src, file, cycleID, stats, gLog)
		default:
			gLog.Warn("unknown source_type", "source_type", src.SourceType, "drive_id", src.DriveID)
		}
	}
	return nil
}

// walkFolder recursively pages a folder's contents. Subfolders recurse;
// Doc-mime files go through processDoc. Anything else is skipped.
func walkFolder(ctx context.Context, st *store.Store, d DriveAPI, g *store.Grantee, src *store.Source, folderID string, cycleID int64, stats *Stats, logger *slog.Logger) error {
	pageToken := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := d.ListFolder(ctx, folderID, pageToken)
		if err != nil {
			return fmt.Errorf("list folder %s: %w", folderID, err)
		}
		for i := range page.Files {
			f := page.Files[i]
			switch f.MimeType {
			case drive.MimeFolder:
				if err := walkFolder(ctx, st, d, g, src, f.ID, cycleID, stats, logger); err != nil {
					logger.Error("recurse folder failed", "drive_id", f.ID, "err", err)
				}
			case drive.MimeDoc:
				processDoc(ctx, st, d, g, src, &f, cycleID, stats, logger)
			default:
				logger.Debug("skipping non-doc mime", "drive_id", f.ID, "mime_type", f.MimeType)
			}
		}
		if page.NextPageToken == "" {
			return nil
		}
		pageToken = page.NextPageToken
	}
}

// processDoc handles a single Drive file: change detection, export, and
// upsert. It never returns an error; per-doc failures are logged + recorded
// on the docs row. The trust boundary is the source itself (an allowlisted
// folder or doc id); per-doc owner is recorded as audit metadata only.
func processDoc(ctx context.Context, st *store.Store, d DriveAPI, g *store.Grantee, src *store.Source, f *drive.File, cycleID int64, stats *Stats, logger *slog.Logger) {
	docLog := logger.With("doc_id", f.ID, "title", f.Name)

	if f.MimeType != drive.MimeDoc {
		docLog.Warn("mime mismatch (direct doc source)", "mime_type", f.MimeType)
		stats.DocsSkipped++
		return
	}

	owner := strings.ToLower(strings.TrimSpace(f.PrimaryOwnerEmail()))

	modifiedAt, err := parseDriveTime(f.ModifiedTime)
	if err != nil {
		docLog.Error("invalid modifiedTime", "got", f.ModifiedTime, "err", err)
		stats.DocsFailed++
		return
	}

	existing, err := st.GetDocByID(ctx, f.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		docLog.Error("doc lookup failed", "err", err)
		stats.DocsFailed++
		return
	}

	// Acceptance criterion #4: same modifiedTime is a no-op.
	if existing != nil && existing.ModifiedAt.Equal(modifiedAt) {
		if err := st.TouchDocSeen(ctx, f.ID, cycleID); err != nil {
			docLog.Error("touch unchanged doc failed", "err", err)
			stats.DocsFailed++
			return
		}
		stats.DocsUnchanged++
		return
	}

	markdown, err := d.ExportMarkdown(ctx, f.ID)
	if err != nil {
		if drive.IsNotFoundOrForbidden(err) {
			docLog.Warn("export 403/404, marking stale", "err", err)
			if existing != nil {
				if e := st.MarkDocStale(ctx, f.ID, err.Error()); e != nil {
					docLog.Error("mark stale failed", "err", e)
				}
				stats.DocsMarkedStale++
			} else {
				stats.DocsSkipped++
			}
			return
		}
		docLog.Error("export failed", "err", err)
		if existing != nil {
			if e := st.MarkDocError(ctx, f.ID, err.Error()); e != nil {
				docLog.Error("mark error failed", "err", e)
			}
		}
		stats.DocsFailed++
		return
	}

	doc := store.Doc{
		DocID:           f.ID,
		GranteeID:       g.GranteeID,
		Title:           f.Name,
		OwnerEmail:      owner,
		ContentMarkdown: markdown,
		ModifiedAt:      modifiedAt,
		SourceType:      src.SourceType,
		SourceDriveID:   src.DriveID,
		HadImages:       detectImages(markdown),
		HadComments:     false, // Drive markdown export omits comments; we don't currently probe revisions.list.
		Status:          store.StatusActive,
	}
	if err := st.UpsertDoc(ctx, doc, cycleID); err != nil {
		docLog.Error("upsert doc failed", "err", err)
		stats.DocsFailed++
		return
	}
	stats.DocsSynced++
}

// scanUnregistered records every file shared with the SA that is NOT already
// registered as a grantee source. Status is preserved if the row exists.
func scanUnregistered(ctx context.Context, st *store.Store, d DriveAPI, stats *Stats, logger *slog.Logger) error {
	registered, err := st.AllSourceDriveIDs(ctx)
	if err != nil {
		return fmt.Errorf("list registered source ids: %w", err)
	}
	pageToken := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := d.ListSharedWithMe(ctx, pageToken)
		if err != nil {
			return fmt.Errorf("list sharedWithMe: %w", err)
		}
		for i := range page.Files {
			f := page.Files[i]
			if registered[f.ID] {
				continue
			}
			seen, err := parseDriveTime(f.SharedWithMeTime)
			if err != nil || seen.IsZero() {
				seen = time.Now()
			}
			u := store.UnregisteredDoc{
				DocID:      f.ID,
				OwnerEmail: f.PrimaryOwnerEmail(),
				Title:      f.Name,
				MimeType:   f.MimeType,
				FirstSeen:  seen,
				LastSeen:   time.Now(),
			}
			if err := st.UpsertUnregisteredDoc(ctx, u); err != nil {
				logger.Error("upsert unregistered failed", "doc_id", f.ID, "err", err)
				continue
			}
			stats.UnregisteredSeen++
		}
		if page.NextPageToken == "" {
			return nil
		}
		pageToken = page.NextPageToken
	}
}

func parseDriveTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse drive time %q: %w", s, err)
	}
	return t.UTC(), nil
}

// detectImages is a best-effort heuristic: Drive's markdown export emits
// `![…](…)` for embedded images. We don't preserve images in v1, so this
// flag exists purely to tell agents "there was visual content here we
// dropped". False negatives are fine for v1.
func detectImages(md string) bool {
	return strings.Contains(md, "![")
}
