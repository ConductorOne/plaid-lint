package cache

// backend abstracts the storage layer behind *Cache. The six hot-path
// methods (ReadL1, WriteL1, HasL1, ReadL2, WriteL2, HasL2) dispatch
// through this interface; *Cache owns envelope encoding, analyzer-name
// validation, and the L1/L2 namespace shape on top.
//
// Stage 1 (this file) ships a single localBackend implementation that
// preserves today's on-disk layout exactly. Stage 2 will add a
// GOCACHEPROG-backed implementation behind opt-in env, sharing this
// interface.
//
// Concurrency contract: backends must be safe for concurrent Get / Put
// / Has across goroutines and (where the backend's storage permits)
// processes. The Put contract is "publish bytes under (namespace, id),
// replacing any prior bytes is permitted but not required" — the cache
// is content-addressed so any two writers for the same id by
// construction publish equivalent bytes.
type backend interface {
	// Get returns the cached body for (namespace, id). On miss the
	// returned error must wrap fs.ErrNotExist so callers can pattern-
	// match with errors.Is.
	Get(namespace string, id ActionID) ([]byte, error)

	// Put publishes body under (namespace, id). It is permitted to
	// silently no-op on collision (first-writer-wins) so long as the
	// previously-published bytes remain readable.
	Put(namespace string, id ActionID, body []byte) error

	// Has reports whether (namespace, id) is present in the backend.
	// Implementations should make Has cheaper than Get (no decode, no
	// touch).
	Has(namespace string, id ActionID) bool
}

// Namespace constants. These are the only strings *Cache passes to the
// backend, and the localBackend uses them as on-disk directory
// segments so the format is preserved byte-for-byte across the seam.
const (
	// nsL2 is the L2 (per-package typecheck) namespace.
	nsL2 = "typecheck"
	// nsL1Prefix is the L1 (per-analyzer-per-package) namespace prefix;
	// the full namespace is nsL1Prefix + "/" + analyzerName.
	nsL1Prefix = "analyzer"
)

// l1Namespace returns the per-analyzer L1 namespace string used by Put
// / Get / Has. Mirrors the on-disk layout "analyzer/<name>/<shard>/<hex>".
func l1Namespace(analyzer string) string {
	return nsL1Prefix + "/" + analyzer
}

// Backend is the exported alias of the backend interface so callers
// outside this package (Stage 1.5: internal/l0) can dispatch through
// the same seam. The interface methods are identical; the alias keeps
// the unexported `backend` from sprawling into other packages while
// still letting L0 route bytes through whichever backend
// PLAID_CACHE_BACKEND selects.
type Backend = backend

// OpenBackend selects and constructs a Backend rooted at root, honouring
// PLAID_CACHE_BACKEND just like Open. Exposed so internal/l0 can route
// its (namespace, id) → bytes traffic through the same seam (local or
// gocacheprog) without duplicating the env-var dispatch.
//
// OpenBackend uses the global PLAID_CACHE_BACKEND only. Callers that
// know which tier they're opening should prefer OpenBackendForTier so
// per-tier overrides (PLAID_L0_CACHE_BACKEND, PLAID_L1_CACHE_BACKEND,
// PLAID_L2_CACHE_BACKEND) apply.
func OpenBackend(root string) (Backend, error) {
	return selectBackend(root)
}

// OpenBackendForTier is the tier-aware variant of OpenBackend. It
// consults PLAID_<TIER>_CACHE_BACKEND first, then falls back to the
// global PLAID_CACHE_BACKEND, then to the local backend. tier should
// be one of TierL0 / TierL1 / TierL2; an empty tier behaves like
// OpenBackend (global-only).
//
// Rationale: cascade benches show L0 hits short-circuit
// L1 on the warm path, so routing 575k tiny L1 entries through
// gocacheprog wastes cold-seed upload time for entries that don't help
// cross-machine sharing. Per-tier routing lets CI workers point L0 at
// the shared S3 backend while keeping L1 local.
func OpenBackendForTier(root, tier string) (Backend, error) {
	return selectBackendForTier(root, tier)
}
