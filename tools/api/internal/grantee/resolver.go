package grantee

import (
	"context"

	"github.com/ironsh/balvibot/tools/api/internal/store"
)

type Resolver struct {
	store *store.Store
}

func NewResolver(s *store.Store) *Resolver {
	return &Resolver{store: s}
}

// Result reports how a message's grantee was determined.
type Result struct {
	GranteeID  *string
	FromSender bool
	FromThread bool
}

// Resolve returns the grantee_id for a message given its sender and the thread
// it belongs to. Order: sender lookup -> thread inheritance -> nil.
func (r *Resolver) Resolve(ctx context.Context, fromEmail string, threadID int64) (Result, error) {
	if fromEmail != "" {
		if id, ok, err := r.store.LookupGranteeByEmail(ctx, fromEmail); err != nil {
			return Result{}, err
		} else if ok {
			return Result{GranteeID: &id, FromSender: true}, nil
		}
	}
	if threadID != 0 {
		t, err := r.store.GetThread(ctx, threadID)
		if err != nil {
			return Result{}, err
		}
		if t.GranteeID != nil {
			id := *t.GranteeID
			return Result{GranteeID: &id, FromThread: true}, nil
		}
	}
	return Result{}, nil
}

// PromoteThread sets the thread's grantee_id (if currently NULL) and back-fills
// any prior NULL-grantee messages on the thread. Safe to call repeatedly.
func (r *Resolver) PromoteThread(ctx context.Context, threadID int64, granteeID string) error {
	return r.store.SetThreadGrantee(ctx, threadID, granteeID)
}
