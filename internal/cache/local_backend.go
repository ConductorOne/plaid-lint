package cache

import (
	"os"
	"path/filepath"
	"time"
)

// localBackend is the filesystem-backed backend used today: sharded
// directory layout under a single root, link(2) O_EXCL atomic writes,
// best-effort mtime refresh on Get to keep hits alive across GC.
//
// The on-disk path for (namespace, id) is:
//
//	<root>/<namespace>/<shard>/<hex>
//
// where shard is the first two hex chars of id and hex is id.Hex().
// Namespace strings come from nsL2 / l1Namespace; the directory
// segments thus match the layout documented in cache.go's package
// docstring exactly.
type localBackend struct {
	root string
}

func newLocalBackend(root string) *localBackend {
	return &localBackend{root: root}
}

// entryPath returns the on-disk path for (namespace, id).
func (b *localBackend) entryPath(namespace string, id ActionID) string {
	hex := id.Hex()
	return filepath.Join(b.root, namespace, hex[:2], hex)
}

// Get reads the body for (namespace, id) and refreshes the file's
// mtime so the entry survives the next GC pass. Misses return an
// error that wraps fs.ErrNotExist (via os.ReadFile semantics).
func (b *localBackend) Get(namespace string, id ActionID) ([]byte, error) {
	p := b.entryPath(namespace, id)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	// Best-effort: refresh mtime so this entry survives the next GC
	// pass. Errors here are not actionable (the read succeeded; only
	// future GC eligibility is affected) so they are intentionally
	// dropped.
	now := time.Now()
	_ = os.Chtimes(p, now, now)
	return data, nil
}

// Put publishes body under (namespace, id) via writeFileAtomic's
// link(2) O_EXCL primitive. Returns nil on success or on a benign
// EEXIST collision (the content-addressed contract guarantees the
// already-published bytes are equivalent).
func (b *localBackend) Put(namespace string, id ActionID, body []byte) error {
	return writeFileAtomic(b.entryPath(namespace, id), body, 0o644)
}

// Has reports presence with a single os.Stat — no decode, no touch.
func (b *localBackend) Has(namespace string, id ActionID) bool {
	_, err := os.Stat(b.entryPath(namespace, id))
	return err == nil
}
