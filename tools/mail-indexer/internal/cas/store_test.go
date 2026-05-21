package cas

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestPutDedupAndLayout(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	payload := []byte("hello world")
	want := sha256.Sum256(payload)
	wantHex := hex.EncodeToString(want[:])

	sum, rel, n, err := s.Put(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if sum != wantHex {
		t.Fatalf("sum=%s want=%s", sum, wantHex)
	}
	if n != int64(len(payload)) {
		t.Fatalf("size=%d want=%d", n, len(payload))
	}
	if rel != filepath.Join(wantHex[0:2], wantHex[2:4], wantHex) {
		t.Fatalf("rel=%s", rel)
	}
	if _, err := os.Stat(s.Path(rel)); err != nil {
		t.Fatalf("expected file at %s: %v", s.Path(rel), err)
	}

	// Second put with same content should be a no-op (dedup).
	sum2, rel2, _, err := s.Put(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if sum2 != sum || rel2 != rel {
		t.Fatalf("dedup mismatch: %s/%s vs %s/%s", sum, rel, sum2, rel2)
	}

	// Temp directory should be empty (no orphaned temp files).
	entries, _ := os.ReadDir(filepath.Join(root, ".tmp"))
	if len(entries) != 0 {
		t.Fatalf(".tmp not empty: %v", entries)
	}
}
