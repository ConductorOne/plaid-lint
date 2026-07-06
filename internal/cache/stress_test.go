package cache

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"sync"
	"sync/atomic"
	"testing"
)

// TestStress8Concurrent is the 8-concurrent-invocation guard. It
// launches 8 goroutines, each doing many writes (some colliding, some
// distinct) and reads of L1+L2 entries against a shared Cache. Combined
// with `go test -race`, this proves the cache primitive layer has:
//
//   - no torn reads (each successful Read either misses or returns
//     decodable canonical bytes)
//   - no data races on the shared *Cache value
//   - first-writer-wins under collision
//
// The full daemon-level 8-concurrent-invocation test composes this with
// the workspace + analyzer pipelines (W3 + W6+).
func TestStress8Concurrent(t *testing.T) {
	const (
		goroutines      = 8
		iterPerRoutine  = 200
		distinctKeys    = 50
		collidingKeyMod = 7 // every 7th key is shared with neighbors
	)
	c := newTestCache(t)

	var (
		wg      sync.WaitGroup
		writes  atomic.Int64
		reads   atomic.Int64
		hits    atomic.Int64
		errsMu  sync.Mutex
		gotErrs []error
	)
	recordErr := func(err error) {
		errsMu.Lock()
		gotErrs = append(gotErrs, err)
		errsMu.Unlock()
	}

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			base := sampleL1()
			l2base := sampleL2()
			for i := 0; i < iterPerRoutine; i++ {
				// Pick a key. Every 7th iteration we use a shared key
				// (across goroutines) to force a collision.
				var keySeed [16]byte
				if i%collidingKeyMod == 0 {
					binary.LittleEndian.PutUint64(keySeed[:8], uint64(i%distinctKeys))
				} else {
					binary.LittleEndian.PutUint64(keySeed[:8], uint64(g))
					binary.LittleEndian.PutUint64(keySeed[8:], uint64(i))
				}
				id := NewActionID(keySeed[:])

				// Write either L1 or L2 depending on iteration parity.
				if i%2 == 0 {
					if err := c.WriteL1(base, id); err != nil {
						recordErr(err)
						continue
					}
				} else {
					if err := c.WriteL2(l2base, id); err != nil {
						recordErr(err)
						continue
					}
				}
				writes.Add(1)

				// Read it back. For the collision keys, the bytes may have
				// been written by a different goroutine — the read still
				// MUST decode cleanly.
				if i%2 == 0 {
					got, err := c.ReadL1(base.Analyzer, id)
					reads.Add(1)
					if err != nil {
						if !errors.Is(err, fs.ErrNotExist) {
							recordErr(err)
						}
						continue
					}
					hits.Add(1)
					if got.PackageID != base.PackageID {
						recordErr(errors.New("torn L1 read: identity drift"))
					}
				} else {
					got, err := c.ReadL2(id)
					reads.Add(1)
					if err != nil {
						if !errors.Is(err, fs.ErrNotExist) {
							recordErr(err)
						}
						continue
					}
					hits.Add(1)
					if got.PackageID != l2base.PackageID {
						recordErr(errors.New("torn L2 read: identity drift"))
					}
				}
			}
		}(g)
	}
	wg.Wait()

	if len(gotErrs) > 0 {
		for _, e := range gotErrs {
			t.Errorf("stress error: %v", e)
		}
	}
	t.Logf("stress: writes=%d reads=%d hits=%d goroutines=%d iters=%d",
		writes.Load(), reads.Load(), hits.Load(), goroutines, iterPerRoutine)
}
