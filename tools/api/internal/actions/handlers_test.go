package actions_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ironsh/balvibot/tools/api/internal/actions"
	"github.com/ironsh/balvibot/tools/api/internal/approval"
	"github.com/ironsh/balvibot/tools/api/internal/db"
	"github.com/ironsh/balvibot/tools/api/internal/store"
)

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
		         attachments, mailbox_state, docs, sync_state,
		         approval_actions, approval_users RESTART IDENTITY CASCADE
	`)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	return store.New(pool), ctx
}

// TestWhitelistDocExecutor exercises the post-approval handler: it trusts the
// already-resolved source_type (the MCP tool classified it via Drive), requires
// the grantee to exist, and writes the source.
func TestWhitelistDocExecutor(t *testing.T) {
	st, ctx := openTestStore(t)
	require.NoError(t, st.EnsureGrantee(ctx, store.Grantee{GranteeID: "acme", Status: store.StatusActive}))

	reg := approval.NewRegistry()
	actions.Register(reg, st)

	dispatch := func(a actions.WhitelistDocArgs) error {
		raw, err := json.Marshal(a)
		require.NoError(t, err)
		return reg.Dispatch(ctx, actions.ActionWhitelistDoc, raw)
	}

	// A folder source and a doc source are both written.
	require.NoError(t, dispatch(actions.WhitelistDocArgs{GranteeID: "acme", DriveID: "folder1", SourceType: store.SourceTypeFolder}))
	require.NoError(t, dispatch(actions.WhitelistDocArgs{GranteeID: "acme", DriveID: "doc1", SourceType: store.SourceTypeDoc}))

	srcs, err := st.ListSourcesForGrantee(ctx, "acme")
	require.NoError(t, err)
	byID := map[string]string{}
	for _, s := range srcs {
		byID[s.DriveID] = s.SourceType
	}
	require.Equal(t, store.SourceTypeFolder, byID["folder1"])
	require.Equal(t, store.SourceTypeDoc, byID["doc1"])

	// An invalid source_type is rejected.
	require.Error(t, dispatch(actions.WhitelistDocArgs{GranteeID: "acme", DriveID: "x", SourceType: "spreadsheet"}))
	// An unknown grantee is rejected.
	require.Error(t, dispatch(actions.WhitelistDocArgs{GranteeID: "ghost", DriveID: "doc1", SourceType: store.SourceTypeDoc}))

	srcs, err = st.ListSourcesForGrantee(ctx, "acme")
	require.NoError(t, err)
	require.Len(t, srcs, 2)
}
