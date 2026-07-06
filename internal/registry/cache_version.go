// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import "github.com/conductorone/plaid-lint/internal/analyzers"

// CacheVersion is the engine-level cache-stamp. Re-export of
// analyzers.EngineCacheVersion: lives in the lower-level analyzers
// package so the gopls cache fork can fold it into the L1 key without
// importing this higher-level registry. Wrappers that want to bump the
// engine-level stamp edit analyzers.EngineCacheVersion; references via
// registry.CacheVersion keep working.
//
// A per-wrapper bump (analyzers.AnalyzerDescriptor.CacheVersion field)
// invalidates only that wrapper. A bump here invalidates everything.
//
// Intentionally NOT auto-derived. The motivating case was an errcheck
// wrapper rewrite that didn't invalidate the cache.
const CacheVersion = analyzers.EngineCacheVersion
