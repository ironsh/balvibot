package cas

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type Store struct {
	root string
}

func New(root string) (*Store, error) {
	if root == "" {
		return nil, fmt.Errorf("cas: root path is required")
	}
	if err := os.MkdirAll(filepath.Join(root, ".tmp"), 0o755); err != nil {
		return nil, fmt.Errorf("cas: create root: %w", err)
	}
	return &Store{root: root}, nil
}

// Put streams r into a temp file, computes SHA-256, and moves to the CAS layout.
// Returns the hex digest and the relative path under root (e.g. "ab/cd/abcd...").
func (s *Store) Put(r io.Reader) (sha string, relPath string, size int64, err error) {
	tmp, err := os.CreateTemp(filepath.Join(s.root, ".tmp"), "att-*")
	if err != nil {
		return "", "", 0, fmt.Errorf("cas: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(tmpPath) }

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		cleanup()
		return "", "", 0, fmt.Errorf("cas: copy: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", "", 0, fmt.Errorf("cas: close temp: %w", err)
	}

	sum := hex.EncodeToString(h.Sum(nil))
	rel := filepath.Join(sum[0:2], sum[2:4], sum)
	abs := filepath.Join(s.root, rel)

	if _, statErr := os.Stat(abs); statErr == nil {
		os.Remove(tmpPath)
		return sum, rel, n, nil
	} else if !os.IsNotExist(statErr) {
		os.Remove(tmpPath)
		return "", "", 0, fmt.Errorf("cas: stat dest: %w", statErr)
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		os.Remove(tmpPath)
		return "", "", 0, fmt.Errorf("cas: mkdir: %w", err)
	}
	if err := os.Rename(tmpPath, abs); err != nil {
		os.Remove(tmpPath)
		return "", "", 0, fmt.Errorf("cas: rename: %w", err)
	}
	return sum, rel, n, nil
}

// Path returns the absolute filesystem path for a relative CAS path.
func (s *Store) Path(rel string) string {
	return filepath.Join(s.root, rel)
}
