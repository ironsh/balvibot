package actions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ironsh/balvibot/tools/api/internal/approval"
	"github.com/ironsh/balvibot/tools/api/internal/store"
)

// Register wires every approval-gated action's executor handler into reg. These
// run only after an operator's signature has been verified, so they perform the
// real, side-effecting mutation.
func Register(reg *approval.Registry, st *store.Store) {
	reg.Register(ActionAddGrantee, func(ctx context.Context, raw json.RawMessage) error {
		var a AddGranteeArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		a.Slug = strings.TrimSpace(a.Slug)
		if a.Slug == "" {
			return errors.New("slug required")
		}
		return st.EnsureGrantee(ctx, store.Grantee{
			GranteeID:   a.Slug,
			DisplayName: a.DisplayName,
			Status:      store.StatusActive,
		})
	})
}
