package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ironsh/balvibot/tools/api/internal/db"
	"github.com/ironsh/balvibot/tools/api/internal/store"
)

// openTestStore connects to the Postgres pointed at by DATABASE_URL, applies
// migrations, and truncates all tables for a clean slate. Tests are skipped
// when DATABASE_URL is unset (e.g. in CI without a DB).
func openTestStore(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, dsn)
	require.NoError(t, err)
	require.NoError(t, db.MigrateUp(pool))
	_, err = pool.ExecContext(ctx, `
		TRUNCATE grantees, grantee_emails, grantee_sources,
		         threads, messages, message_references, message_recipients,
		         attachments, mailbox_state, docs, unregistered_docs,
		         blocked_owners, sync_state RESTART IDENTITY CASCADE
	`)
	require.NoError(t, err)
	st := store.New(pool)
	t.Cleanup(func() { _ = st.Close() })
	return st, ctx
}

func TestGranteesAndEmails(t *testing.T) {
	st, ctx := openTestStore(t)

	require.NoError(t, st.EnsureGrantee(ctx, store.Grantee{GranteeID: "acme", DisplayName: "Acme"}))
	// EnsureGrantee must not clobber an existing display name.
	require.NoError(t, st.EnsureGrantee(ctx, store.Grantee{GranteeID: "acme", DisplayName: "Other"}))
	require.NoError(t, st.AddGranteeEmail(ctx, "acme", "Dev@Acme.org"))

	// citext: lookup is case-insensitive and id comes back.
	id, ok, err := st.LookupGranteeByEmail(ctx, "dev@acme.ORG")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "acme", id)

	grantees, err := st.ListGrantees(ctx)
	require.NoError(t, err)
	require.Len(t, grantees, 1)
	require.Equal(t, "Acme", grantees[0].DisplayName)
	require.Equal(t, []string{"dev@acme.org"}, grantees[0].Emails)
	require.Equal(t, store.StatusActive, grantees[0].Status)
}

func TestThreadAndMessageDedup(t *testing.T) {
	st, ctx := openTestStore(t)
	require.NoError(t, st.EnsureGrantee(ctx, store.Grantee{GranteeID: "acme"}))
	require.NoError(t, st.AddGranteeEmail(ctx, "acme", "sender@acme.org"))

	now := time.Now()
	threadID, err := st.CreateThread(ctx, "root@x", "hello", nil, now)
	require.NoError(t, err)
	require.NotZero(t, threadID)

	gid := "acme"
	msg := &store.Message{
		MessageID: "msg-1@x", ThreadID: threadID, GranteeID: &gid,
		Folder: "INBOX", UID: 10, UIDValidity: 1,
		From:    store.Address{Email: "sender@acme.org", Name: "Sender"},
		To:      []store.Address{{Email: "us@org.test"}},
		Subject: "Hello there", Date: now, BodyText: "the body text",
		SizeBytes: 123,
	}
	id1, inserted1, err := st.InsertMessage(ctx, msg)
	require.NoError(t, err)
	require.True(t, inserted1)
	require.NotZero(t, id1)

	// Re-inserting the same message_id must dedup (RETURNING/ON CONFLICT path).
	id2, inserted2, err := st.InsertMessage(ctx, msg)
	require.NoError(t, err)
	require.False(t, inserted2)
	require.Equal(t, id1, id2)

	// search_emails ILIKE path.
	res, _, err := st.SearchMessages(ctx, store.SearchParams{Body: "BODY"})
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.Equal(t, "msg-1@x", res[0].MessageID)

	full, _, recs, err := st.GetMessage(ctx, "msg-1@x")
	require.NoError(t, err)
	require.NotNil(t, full)
	require.Equal(t, "Hello there", full.Subject)
	require.Len(t, recs, 1)
	require.Equal(t, "us@org.test", recs[0].Email)
}

func TestDocsUpsertAndGet(t *testing.T) {
	st, ctx := openTestStore(t)
	require.NoError(t, st.EnsureGrantee(ctx, store.Grantee{GranteeID: "acme"}))

	doc := store.Doc{
		DocID: "doc-1", GranteeID: "acme", Title: "Plan", OwnerEmail: "Owner@Acme.org",
		ContentMarkdown: "# Plan\n", ModifiedAt: time.Now(), SourceType: store.SourceTypeFolder,
		SourceDriveID: "folder-1", HadImages: true,
	}
	require.NoError(t, st.UpsertDoc(ctx, doc, 1))

	got, err := st.GetDocForGrantee(ctx, "acme", "doc-1")
	require.NoError(t, err)
	require.Equal(t, "Plan", got.Title)
	require.True(t, got.HadImages)
	require.Equal(t, "owner@acme.org", got.OwnerEmail) // lowercased on write

	summaries, err := st.GranteeSummaries(ctx)
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	require.Equal(t, 1, summaries[0].DocumentCount)

	_, err = st.GetDocForGrantee(ctx, "acme", "missing")
	require.ErrorIs(t, err, store.ErrNotFound)
}
