package grantee

import (
	"context"

	"github.com/ironcd/philanthropy-os/tools/mail-indexer/internal/store"
)

type Resolver struct {
	store *store.Store
}

func NewResolver(s *store.Store) *Resolver {
	return &Resolver{store: s}
}

// Resolve returns the grantee_id for a message given its sender and the thread it belongs to.
// Order: sender lookup -> thread inheritance -> nil.
// If a sender match is found and the thread has no grantee yet, the caller should call
// PromoteThread to back-fill the thread and any prior NULL-grantee messages on it.
type Result struct {
	GranteeID    *string
	FromSender   bool
	FromThread   bool
}

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

// PromoteThread sets the thread's grantee_id (if currently NULL) and back-fills any prior
// messages on the thread that have a NULL grantee_id. Safe to call repeatedly.
func (r *Resolver) PromoteThread(ctx context.Context, threadID int64, granteeID string) error {
	return r.store.SetThreadGrantee(ctx, threadID, granteeID)
}
