package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ironcd/philanthropy-os/tools/mail-indexer/internal/config"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSchemaIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s1.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("re-open failed: %v", err)
	}
	s2.Close()
}

func TestReconcileGrantees(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	gs := []config.Grantee{
		{ID: "acme", Name: "Acme", Emails: []string{"a@acme.org", "b@acme.org"}},
		{ID: "beta", Name: "Beta", Emails: []string{"hello@beta.example"}},
	}
	if err := s.ReconcileGrantees(ctx, gs); err != nil {
		t.Fatal(err)
	}
	id, ok, err := s.LookupGranteeByEmail(ctx, "A@acme.org")
	if err != nil || !ok || id != "acme" {
		t.Fatalf("lookup a@acme.org: id=%q ok=%v err=%v", id, ok, err)
	}

	// Remove an email and a grantee; reconcile should clean them out.
	gs2 := []config.Grantee{
		{ID: "acme", Name: "Acme", Emails: []string{"a@acme.org"}},
	}
	if err := s.ReconcileGrantees(ctx, gs2); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.LookupGranteeByEmail(ctx, "b@acme.org"); ok {
		t.Fatal("b@acme.org should be removed")
	}
	if _, ok, _ := s.LookupGranteeByEmail(ctx, "hello@beta.example"); ok {
		t.Fatal("beta should be removed")
	}
}

func TestThreadCreateAndPromote(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.ReconcileGrantees(ctx, []config.Grantee{
		{ID: "acme", Name: "Acme", Emails: []string{"jane@acme.org"}},
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Unix(1700000000, 0)
	tid, err := s.CreateThread(ctx, "root@x", "hello", nil, now)
	if err != nil {
		t.Fatal(err)
	}

	// Insert a message tied to that thread with no grantee.
	m := &Message{
		MessageID:   "m1@x",
		ThreadID:    tid,
		Folder:      "INBOX",
		UID:         1,
		UIDValidity: 1,
		From:        Address{Email: "unknown@x"},
		To:          []Address{{Email: "us@here"}},
		Subject:     "hello",
		Date:        now,
		SizeBytes:   100,
	}
	if _, _, err := s.InsertMessage(ctx, m); err != nil {
		t.Fatal(err)
	}

	// Now promote thread to acme.
	if err := s.SetThreadGrantee(ctx, tid, "acme"); err != nil {
		t.Fatal(err)
	}
	th, err := s.GetThread(ctx, tid)
	if err != nil {
		t.Fatal(err)
	}
	if th.GranteeID == nil || *th.GranteeID != "acme" {
		t.Fatalf("thread grantee = %v", th.GranteeID)
	}

	// Earlier NULL-grantee message should now be back-filled to acme.
	var got string
	err = s.db.QueryRowContext(ctx,
		`SELECT grantee_id FROM messages WHERE message_id = ?`, "m1@x",
	).Scan(&got)
	if err != nil || got != "acme" {
		t.Fatalf("back-fill grantee_id=%q err=%v", got, err)
	}
}

func TestInsertMessageIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	tid, err := s.CreateThread(ctx, "root@x", "hello", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	m := &Message{
		MessageID:   "dup@x",
		ThreadID:    tid,
		Folder:      "INBOX",
		UID:         1,
		UIDValidity: 1,
		From:        Address{Email: "x@y"},
		To:          []Address{{Email: "us@here"}},
		Date:        time.Now(),
		SizeBytes:   1,
	}
	id1, inserted, err := s.InsertMessage(ctx, m)
	if err != nil || !inserted {
		t.Fatalf("first insert: id=%d inserted=%v err=%v", id1, inserted, err)
	}
	m.UID = 2 // pretend different UID (e.g. seen via Sent folder later)
	id2, inserted2, err := s.InsertMessage(ctx, m)
	if err != nil || inserted2 || id2 != id1 {
		t.Fatalf("second insert should return existing id: id=%d inserted=%v err=%v", id2, inserted2, err)
	}
}
