package cache

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// writeFileAtomic writes data to path with link(2) O_EXCL semantics:
//
//  1. Write data to "<path>.tmp.<random>" with mode perm.
//  2. os.Link the tmp file to path. If link fails with EEXIST, another
//     writer won the race; treat as success (the bytes for this content-
//     addressed name are already there, by construction equivalent).
//  3. Always unlink the tmp file.
//
// This is the "first writer wins, no global lock" primitive.
// Cross-platform note: os.Link wraps link(2) on Linux/macOS and
// CreateHardLink on Windows; both report EEXIST-equivalent on collision.
// Phase 1 targets Linux/amd64 + Linux/arm64, so this is well within the
// supported surface.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("plaid-lint cache: mkdir for atomic write: %w", err)
	}
	tmp, err := tempName(path)
	if err != nil {
		return err
	}
	// Write tmp; ensure removal on every exit path.
	if err := os.WriteFile(tmp, data, perm); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("plaid-lint cache: write tmp: %w", err)
	}
	// Link: first writer wins. EEXIST means somebody else already published
	// the same content-addressed name; that's a hit, not an error.
	linkErr := os.Link(tmp, path)
	// Always remove the tmp file. If link succeeded the tmp file has an
	// orphan inode reference; if it failed we leave nothing behind.
	if rmErr := os.Remove(tmp); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
		// Non-fatal: log via returned error if no other error occurred.
		if linkErr == nil {
			return fmt.Errorf("plaid-lint cache: remove tmp after link: %w", rmErr)
		}
	}
	if linkErr != nil {
		if errors.Is(linkErr, fs.ErrExist) {
			return nil // first writer already won; bytes are published.
		}
		return fmt.Errorf("plaid-lint cache: link to final: %w", linkErr)
	}
	return nil
}

// tempName returns "<path>.tmp.<8-byte-hex-rand>". The directory of the
// returned name is the same as path so the subsequent link(2) is on the
// same filesystem.
func tempName(path string) (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("plaid-lint cache: random tmp suffix: %w", err)
	}
	return path + ".tmp." + hex.EncodeToString(buf[:]), nil
}
