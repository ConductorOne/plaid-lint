package cache

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/conductorone/plaid-lint/internal/cache/gocacheprog"
)

// gocacheprogBackend dispatches L1/L2 reads/writes to a long-lived
// GOCACHEPROG helper. Stage 2; opt-in
// behind PLAID_CACHE_BACKEND=gocacheprog.
//
// The helper sees opaque 32-byte action IDs, so the L1/L2 namespace
// string is folded into the derived ID via deriveActionID below. The
// "plaid-lint/v1/" prefix is a versioned epoch — bump to v2 to
// invalidate the entire shared cache without touching the local
// on-disk layout's CacheVersion stamp.
type gocacheprogBackend struct {
	client *gocacheprog.Client
}

func newGocacheprogBackend(cacheprog string) (*gocacheprogBackend, error) {
	// GOCACHEPROG is a command line — the helper program followed by optional
	// arguments, tokenized the way the go command does it — not a bare
	// executable path. Split it before exec so a value like "helper --dir /tmp"
	// runs `helper` with `--dir /tmp`, instead of trying to exec a file whose
	// name is the entire string (which fails with "no such file or directory").
	name, args, err := splitCacheProg(cacheprog)
	if err != nil {
		return nil, err
	}
	c, err := gocacheprog.New(name, args)
	if err != nil {
		return nil, err
	}
	return &gocacheprogBackend{client: c}, nil
}

// splitCacheProg tokenizes a GOCACHEPROG command line into the helper program
// and its arguments. GOCACHEPROG may carry flags, so the whole value is not a
// bare executable path.
func splitCacheProg(cacheprog string) (name string, args []string, err error) {
	fields, err := splitQuoted(cacheprog)
	if err != nil {
		return "", nil, fmt.Errorf("plaid-lint: parse GOCACHEPROG: %w", err)
	}
	if len(fields) == 0 {
		return "", nil, errors.New("plaid-lint: GOCACHEPROG is empty")
	}
	return fields[0], fields[1:], nil
}

// splitQuoted splits s into space-separated fields, allowing single- or
// double-quoted fields to contain spaces. It mirrors the tokenization the go
// command applies to GOCACHEPROG (cmd/internal/quoted.Split): quotes are not
// nestable or mixable and escapes are not expanded.
func splitQuoted(s string) ([]string, error) {
	var fields []string
	for len(s) > 0 {
		for len(s) > 0 && isCacheProgSpace(s[0]) {
			s = s[1:]
		}
		if len(s) == 0 {
			break
		}
		if c := s[0]; c == '"' || c == '\'' {
			s = s[1:]
			i := 0
			for i < len(s) && s[i] != c {
				i++
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unterminated %c quote", c)
			}
			fields = append(fields, s[:i])
			s = s[i+1:]
			continue
		}
		i := 0
		for i < len(s) && !isCacheProgSpace(s[i]) {
			i++
		}
		fields = append(fields, s[:i])
		s = s[i:]
	}
	return fields, nil
}

func isCacheProgSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// deriveActionID folds namespace into the action ID. The null byte
// after namespace prevents the "abc"+"def" vs "ab"+"cdef" collision
// class an unsalted concat would admit.
func deriveActionID(namespace string, id ActionID) [32]byte {
	h := sha256.New()
	h.Write([]byte("plaid-lint/v1/"))
	h.Write([]byte(namespace))
	h.Write([]byte{0})
	h.Write(id[:])
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func (b *gocacheprogBackend) Get(namespace string, id ActionID) ([]byte, error) {
	outputID, body, hit, err := b.client.Get(context.Background(), deriveActionID(namespace, id))
	if err != nil {
		return nil, fmt.Errorf("gocacheprog backend: get: %w", err)
	}
	if !hit {
		return nil, fs.ErrNotExist
	}
	// OutputID is content-addressed: sha256(body). This catches transport
	// or object corruption on the helper path. It is not an authentication
	// boundary: a writer that can control both the action record and body can
	// still make them match, so shared-cache safety depends on writer
	// isolation at the cache namespace/storage layer.
	if !validGocacheprogBody(outputID, body) {
		return nil, fs.ErrNotExist
	}
	return body, nil
}

func validGocacheprogBody(outputID [32]byte, body []byte) bool {
	return sha256.Sum256(body) == outputID
}

func (b *gocacheprogBackend) Put(namespace string, id ActionID, body []byte) error {
	// OutputID is content-addressed: sha256(body). Parity with the
	// Go tool's own ProgCache.
	out := sha256.Sum256(body)
	if err := b.client.Put(context.Background(), deriveActionID(namespace, id), out, body); err != nil {
		return fmt.Errorf("gocacheprog backend: put: %w", err)
	}
	return nil
}

func (b *gocacheprogBackend) Has(namespace string, id ActionID) bool {
	hit, err := b.client.Stat(context.Background(), deriveActionID(namespace, id))
	return err == nil && hit
}

// Close terminates the helper subprocess. The clcache.Cache and
// l0.Cache wrappers expose io.Closer; their Close routes here when
// PLAID_CACHE_BACKEND selects this backend. Without this wiring the
// helper child sits in futex_wait_queue after the lint work
// completes and the parent process cannot exit.
func (b *gocacheprogBackend) Close() error { return b.client.Close() }

var errBackendUnknown = errors.New("plaid-lint: unknown cache backend")

// Tier identifiers passed to selectBackendForTier / OpenBackendForTier.
// These map 1:1 to the per-tier env vars PLAID_<TIER>_CACHE_BACKEND
// (TierL0 → PLAID_L0_CACHE_BACKEND, etc.). Callers outside the cache
// package (internal/l0, cmd/plaid-lint) use the exported constants.
const (
	TierL0 = "l0"
	TierL1 = "l1"
	TierL2 = "l2"
)

// backendForTier returns the backend name selected for tier, honouring
// the per-tier override (PLAID_L0_CACHE_BACKEND, PLAID_L1_CACHE_BACKEND,
// PLAID_L2_CACHE_BACKEND) and falling back to the global
// PLAID_CACHE_BACKEND, then to "local".
//
// L1 is excluded from the global gocacheprog fallback: when
// PLAID_CACHE_BACKEND=gocacheprog is set without an explicit L1 override,
// L1 stays local. cascade-l0-gocacheprog-rebench (2026-05-26) measured
// 575k L1 entries written to S3 on cold seed but zero L1 queries on the
// warm-S3 path — L0 hits short-circuit L1 entirely, so cross-machine L1
// sharing pays IPC cost for no measured benefit. Users who explicitly
// want all three tiers shared can set PLAID_L1_CACHE_BACKEND=gocacheprog.
//
// Tier strings are case-insensitive but the env var names are upper-
// case; pass TierL0/TierL1/TierL2 from this package.
func backendForTier(tier string) string {
	if tier != "" {
		envKey := "PLAID_" + strings.ToUpper(tier) + "_CACHE_BACKEND"
		if v := os.Getenv(envKey); v != "" {
			return v
		}
	}
	if v := os.Getenv("PLAID_CACHE_BACKEND"); v != "" {
		if tier == TierL1 && v == "gocacheprog" {
			return "local"
		}
		return v
	}
	return "local"
}

// selectBackend picks a backend for the global default (no per-tier
// override). Equivalent to selectBackendForTier(root, ""); kept for
// callers that don't know — or don't care about — the tier.
func selectBackend(root string) (backend, error) {
	return selectBackendForTier(root, "")
}

// selectBackendForTier picks a backend by name using backendForTier:
//   - "" or "local"  → localBackend (default; identical to pre-Stage-2)
//   - "gocacheprog"  → gocacheprogBackend, GOCACHEPROG must point at helper
//   - anything else  → error (deliberately strict — a typo should fail loud)
func selectBackendForTier(root, tier string) (backend, error) {
	name := backendForTier(tier)
	switch name {
	case "local":
		return newLocalBackend(root), nil
	case "gocacheprog":
		binary := os.Getenv("GOCACHEPROG")
		if binary == "" {
			return nil, errors.New("plaid-lint: cache backend \"gocacheprog\" requires GOCACHEPROG env var to point at the helper binary")
		}
		return newGocacheprogBackend(binary)
	default:
		return nil, fmt.Errorf("%w: %q (want one of: local, gocacheprog)", errBackendUnknown, name)
	}
}
