package actions_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ironsh/balvibot/tools/api/internal/actions"
	"github.com/ironsh/balvibot/tools/api/internal/approval"
)

// TestRegisterWiresKnownActions verifies the executor registry has a handler
// for each action name the MCP tools enqueue. A real DB-backed end-to-end test
// (enqueue -> approve -> grantee created) lives with the store tests.
func TestRegisterWiresKnownActions(t *testing.T) {
	reg := approval.NewRegistry()
	actions.Register(reg, nil) // handlers aren't invoked here, only registered.
	require.True(t, reg.Has(actions.ActionAddGrantee))
}

// TestAddGranteeArgsRoundTrip locks the wire shape shared by the producer
// (MCP tool) and consumer (executor handler).
func TestAddGranteeArgsRoundTrip(t *testing.T) {
	b, err := json.Marshal(actions.AddGranteeArgs{Slug: "acme", DisplayName: "Acme Foundation"})
	require.NoError(t, err)
	require.JSONEq(t, `{"slug":"acme","display_name":"Acme Foundation"}`, string(b))

	var got actions.AddGranteeArgs
	require.NoError(t, json.Unmarshal(b, &got))
	require.Equal(t, "acme", got.Slug)
}

// TestUnknownActionRejected confirms an unregistered action is not dispatchable.
func TestUnknownActionRejected(t *testing.T) {
	reg := approval.NewRegistry()
	actions.Register(reg, nil)
	require.ErrorIs(t, reg.Dispatch(context.Background(), "delete_everything", nil), approval.ErrUnknownAction)
}
