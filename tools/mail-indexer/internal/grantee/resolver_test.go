package grantee

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ironcd/philanthropy-os/tools/mail-indexer/internal/config"
	"github.com/ironcd/philanthropy-os/tools/mail-indexer/internal/store"
)

func setup(t *testing.T) (*store.Store, *Resolver, int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "g.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if err := s.ReconcileGrantees(ctx, []config.Grantee{
		{ID: "acme", Name: "Acme", Emails: []string{"jane@acme.org"}},
	}); err != nil {
		t.Fatal(err)
	}
	tid, err := s.CreateThread(ctx, "root@x", "hello", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return s, NewResolver(s), tid
}

func TestResolveSenderMatch(t *testing.T) {
	_, r, tid := setup(t)
	res, err := r.Resolve(context.Background(), "jane@acme.org", tid)
	if err != nil {
		t.Fatal(err)
	}
	if res.GranteeID == nil || *res.GranteeID != "acme" || !res.FromSender {
		t.Fatalf("got %+v", res)
	}
}

func TestResolveThreadInheritance(t *testing.T) {
	s, r, tid := setup(t)
	if err := s.SetThreadGrantee(context.Background(), tid, "acme"); err != nil {
		t.Fatal(err)
	}
	res, err := r.Resolve(context.Background(), "stranger@unknown", tid)
	if err != nil {
		t.Fatal(err)
	}
	if res.GranteeID == nil || *res.GranteeID != "acme" || !res.FromThread {
		t.Fatalf("got %+v", res)
	}
}

func TestResolveNoMatch(t *testing.T) {
	_, r, tid := setup(t)
	res, err := r.Resolve(context.Background(), "stranger@unknown", tid)
	if err != nil {
		t.Fatal(err)
	}
	if res.GranteeID != nil {
		t.Fatalf("expected nil grantee, got %v", *res.GranteeID)
	}
}
