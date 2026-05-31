package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ironsh/balvibot/tools/api/internal/store"
)

func TestApprovalActionRoundTrip(t *testing.T) {
	st, ctx := openTestStore(t)

	id, err := st.EnqueueAction(ctx, "send_email",
		json.RawMessage(`{"to":"a@b.com"}`), json.RawMessage(`{"src":"agent"}`), "hermes")
	require.NoError(t, err)
	require.NotZero(t, id)

	got, err := st.GetAction(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "send_email", got.Action)
	require.Equal(t, store.ApprovalPending, got.Status)
	require.Equal(t, "hermes", got.RequestedBy)
	require.JSONEq(t, `{"to":"a@b.com"}`, string(got.Args))

	pending, err := st.ListActionsByStatus(ctx, store.ApprovalPending)
	require.NoError(t, err)
	require.Len(t, pending, 1)

	require.NoError(t, st.MarkActionExecuted(ctx, id, "op@example.com"))
	got, err = st.GetAction(ctx, id)
	require.NoError(t, err)
	require.Equal(t, store.ApprovalExecuted, got.Status)
	require.Equal(t, "op@example.com", got.ApprovedBy)
	require.NotNil(t, got.ExecutedAt)

	// A second mark must not match (no longer pending).
	require.ErrorIs(t, st.MarkActionExecuted(ctx, id, "op@example.com"), store.ErrNotFound)
}

func TestApprovalActionFailure(t *testing.T) {
	st, ctx := openTestStore(t)

	id, err := st.EnqueueAction(ctx, "noop", nil, nil, "")
	require.NoError(t, err)
	require.JSONEq(t, `{}`, mustArgs(t, st, ctx, id))

	require.NoError(t, st.MarkActionFailed(ctx, id, "op@example.com", "boom"))
	got, err := st.GetAction(ctx, id)
	require.NoError(t, err)
	require.Equal(t, store.ApprovalFailed, got.Status)
	require.Equal(t, "boom", got.LastError)
}

func TestApprovalUserRoundTrip(t *testing.T) {
	st, ctx := openTestStore(t)

	const pub = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKeyDataForTesting op@example.com"
	require.NoError(t, st.UpsertApprovalUser(ctx, "op@example.com", pub, "SHA256:abc"))

	u, err := st.GetApprovalUser(ctx, "op@example.com")
	require.NoError(t, err)
	require.Equal(t, pub, u.PublicKey)
	require.Equal(t, "SHA256:abc", u.Fingerprint)

	// CITEXT email is case-insensitive.
	u2, err := st.GetApprovalUser(ctx, "OP@example.com")
	require.NoError(t, err)
	require.Equal(t, "op@example.com", u2.Email)

	users, err := st.ListApprovalUsers(ctx)
	require.NoError(t, err)
	require.Len(t, users, 1)

	require.NoError(t, st.RemoveApprovalUser(ctx, "op@example.com"))
	_, err = st.GetApprovalUser(ctx, "op@example.com")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func mustArgs(t *testing.T, st *store.Store, ctx context.Context, id int64) string {
	t.Helper()
	a, err := st.GetAction(ctx, id)
	require.NoError(t, err)
	return string(a.Args)
}
