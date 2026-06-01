package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	reg.Register(ActionAddApprovalUser, func(ctx context.Context, raw json.RawMessage) error {
		var a AddApprovalUserArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		a.Email = strings.TrimSpace(a.Email)
		if a.Email == "" {
			return errors.New("email required")
		}
		a.PublicKey = strings.TrimSpace(a.PublicKey)
		if a.PublicKey == "" {
			return errors.New("ssh_public_key required")
		}
		fingerprint, err := approval.Fingerprint(a.PublicKey)
		if err != nil {
			return err
		}
		return st.UpsertApprovalUser(ctx, a.Email, a.PublicKey, fingerprint)
	})

	reg.Register(ActionWhitelistDoc, func(ctx context.Context, raw json.RawMessage) error {
		var a WhitelistDocArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		a.GranteeID = strings.TrimSpace(a.GranteeID)
		if a.GranteeID == "" {
			return errors.New("grantee_id required")
		}
		a.DriveID = strings.TrimSpace(a.DriveID)
		if a.DriveID == "" {
			return errors.New("drive_id required")
		}
		// SourceType is resolved from the Drive MIME type by the enqueueing
		// MCP tool; the executor only sanity-checks it (the approval service
		// has no Drive access of its own).
		if a.SourceType != store.SourceTypeFolder && a.SourceType != store.SourceTypeDoc {
			return fmt.Errorf("invalid source_type %q", a.SourceType)
		}
		// The grantee must already exist; whitelist_doc never creates one
		// (use add_grantee for that). This stops a typo'd slug from attaching
		// a source to a grantee nobody meant to create.
		if _, err := st.GetGrantee(ctx, a.GranteeID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("grantee %q does not exist", a.GranteeID)
			}
			return err
		}
		_, err := st.UpsertSource(ctx, store.Source{
			GranteeID:  a.GranteeID,
			SourceType: a.SourceType,
			DriveID:    a.DriveID,
		})
		return err
	})
}
