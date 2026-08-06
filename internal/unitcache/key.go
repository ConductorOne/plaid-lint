// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package unitcache implements the optional content-addressed cache
// for `plaid-lint unit`.
//
// The unit driver is a pure function of its declared inputs — that is
// the whole point of the mode (see internal/unit's package doc), and
// it is why the driver deliberately opens none of the engine's L0/L1/
// L2 caches: those resolve their root from the ambient environment
// (XDG_CACHE_HOME, PLAID_*, GOCACHEPROG), which would make a build
// system's action depend on host state the build system cannot see.
//
// This cache keeps that property. Its key is derived from exactly two
// things:
//
//   - the action's declared inputs: the unit.json bytes verbatim plus
//     the content of every file unit.json names (sources, ignored
//     sources, the importcfg and every export-data file it lists, the
//     dependency fact files, the .golangci config, go.mod, and the
//     declared stdlib tree), and
//   - the identity of the analyzing binary.
//
// Nothing else participates. No environment variable is read here or
// on any path this package calls, so the cache cannot return one
// answer on a machine with GOCACHEPROG set and another on a machine
// without it. Where the cache LIVES is likewise never discovered: the
// caller passes an explicit directory (`plaid-lint unit --cache-dir`),
// which is off unless asked for.
//
// The soundness argument is therefore: a key collision aside, an entry
// can only be reached by an action whose declared inputs and tool are
// byte-identical to the ones that produced it — and such an action, by
// the driver's own contract, computes byte-identical outputs.
package unitcache

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/conductorone/plaid-lint/internal/unit"
)

// keySchema is the version of the key derivation itself. Bumping it
// invalidates every existing entry, which is the required action
// whenever the set of inputs folded into a key changes — an old entry
// was keyed under a weaker premise and must never be reused.
const keySchema = "plaid-lint/unit-cache/key/v1"

// Key is the content-addressed key of a cache entry: a SHA-256 over a
// structured, length-prefixed digest of the action's declared inputs
// and the tool identity.
type Key [sha256.Size]byte

// Hex returns the lowercase hex encoding of the key (64 chars).
func (k Key) Hex() string { return hex.EncodeToString(k[:]) }

// String implements fmt.Stringer.
func (k Key) String() string { return k.Hex() }

// ComputeKey derives the cache key for one unit action.
//
// cfgPath is the unit.json path (its bytes are folded in verbatim, so
// a field added to the schema in a later release cannot silently
// escape the key); cfg is the parsed form, used only to find the files
// unit.json names. toolID identifies the analyzing binary — see
// the caller for how it is derived.
//
// An error means the key could not be proven to cover the action —
// typically a declared input that could not be read. Callers must
// treat that as "run uncached", never as "cache under a partial key".
func ComputeKey(cfgPath string, cfg *unit.Config, toolID string) (Key, error) {
	if toolID == "" {
		return Key{}, fmt.Errorf("unitcache: empty tool identity")
	}
	cfgBody, err := os.ReadFile(cfgPath)
	if err != nil {
		return Key{}, fmt.Errorf("unitcache: read %s: %w", cfgPath, err)
	}

	kb := newKeyBuilder()
	kb.field("schema", []byte(keySchema))
	kb.field("tool", []byte(toolID))
	kb.field("unit.json", cfgBody)

	// The exclusion filter anchors path-relative rules at the process
	// working directory, but only ever applies it to ABSOLUTE
	// diagnostic paths (exclusion.Filter.relativePath); a diagnostic
	// that already carries a relative path is matched as-is. So the
	// working directory can change the result only when the action
	// declares absolute paths — the shape a hand-written unit.json
	// has, not the execroot-relative shape a build system emits.
	// Folding it in unconditionally would pin every entry to one
	// checkout's absolute path and destroy sharing for the case the
	// cache exists to serve; folding it in exactly when it can matter
	// keeps both properties.
	if anyAbsolutePath(cfg) {
		wd, err := os.Getwd()
		if err != nil {
			return Key{}, fmt.Errorf("unitcache: getwd: %w", err)
		}
		kb.field("wd", []byte(wd))
	}

	for _, f := range cfg.Package.GoFiles {
		if err := kb.file("go_file", f); err != nil {
			return Key{}, err
		}
	}
	// Ignored files are constraint-excluded from compilation but are
	// handed to analyzers as analysis.Pass.IgnoredFiles, so an
	// analyzer may read them.
	for _, f := range cfg.Package.IgnoredFiles {
		if err := kb.file("ignored_file", f); err != nil {
			return Key{}, err
		}
	}

	if cfg.Deps.Importcfg != "" {
		if err := kb.file("importcfg", cfg.Deps.Importcfg); err != nil {
			return Key{}, err
		}
		// The importcfg names the export data the type-checker reads.
		// Its own bytes above pin the mapping; these pin what the
		// mapping points AT — a rebuilt dependency changes the key
		// even though the importcfg line is unchanged.
		files, err := unit.ImportcfgFiles(cfg.Deps.Importcfg)
		if err != nil {
			return Key{}, fmt.Errorf("unitcache: %w", err)
		}
		for _, ip := range sortedKeys(files) {
			kb.field("packagefile", []byte(ip))
			if err := kb.file("packagefile_body", files[ip]); err != nil {
				return Key{}, err
			}
		}
	}

	for _, ip := range sortedKeys(cfg.Deps.Facts) {
		kb.field("dep_facts", []byte(ip))
		if err := kb.file("dep_facts_body", cfg.Deps.Facts[ip]); err != nil {
			return Key{}, err
		}
	}

	if cfg.Analysis.Config != "" {
		if err := kb.file("golangci", cfg.Analysis.Config); err != nil {
			return Key{}, err
		}
	}
	if cfg.Module.GoMod != "" {
		if err := kb.file("go_mod", cfg.Module.GoMod); err != nil {
			return Key{}, err
		}
	}
	if cfg.Deps.StdlibDir != "" {
		if err := kb.tree("stdlib_dir", cfg.Deps.StdlibDir); err != nil {
			return Key{}, err
		}
	}

	var out Key
	copy(out[:], kb.h.Sum(nil))
	return out, nil
}

// anyAbsolutePath reports whether the action declares any input or
// output path in absolute form. See ComputeKey for why that decides
// whether the working directory participates in the key.
func anyAbsolutePath(cfg *unit.Config) bool {
	paths := make([]string, 0, len(cfg.Package.GoFiles)+len(cfg.Package.IgnoredFiles)+len(cfg.Deps.Facts)+5)
	paths = append(paths, cfg.Package.GoFiles...)
	paths = append(paths, cfg.Package.IgnoredFiles...)
	for _, p := range cfg.Deps.Facts {
		paths = append(paths, p)
	}
	paths = append(paths,
		cfg.Deps.Importcfg, cfg.Deps.StdlibDir,
		cfg.Analysis.Config, cfg.Module.GoMod,
		cfg.Out.Sarif, cfg.Out.Facts)
	for _, p := range paths {
		if p != "" && filepath.IsAbs(p) {
			return true
		}
	}
	return false
}

// keyBuilder accumulates the structured digest. Every contribution is
// tagged and length-prefixed so no two distinct input sets can
// concatenate to the same byte stream.
type keyBuilder struct {
	h    hash.Hash
	seen map[string][sha256.Size]byte
}

func newKeyBuilder() *keyBuilder {
	return &keyBuilder{h: sha256.New(), seen: map[string][sha256.Size]byte{}}
}

// field folds a tagged literal value into the digest.
func (kb *keyBuilder) field(tag string, val []byte) {
	kb.write([]byte(tag))
	kb.write(val)
}

// file folds a tagged (path, content-digest) pair into the digest. The
// path participates because diagnostics carry it: two byte-identical
// sources at different paths produce different SARIF.
//
// Digests are memoized per builder so an export-data file reachable
// under several import paths (vendoring, importmap aliases) is read
// once.
func (kb *keyBuilder) file(tag, path string) error {
	kb.write([]byte(tag))
	kb.write([]byte(path))
	sum, ok := kb.seen[path]
	if !ok {
		var err error
		sum, err = fileDigest(path)
		if err != nil {
			return fmt.Errorf("unitcache: digest declared input: %w", err)
		}
		kb.seen[path] = sum
	}
	kb.write(sum[:])
	return nil
}

// tree folds an entire declared directory into the digest: every file
// beneath it, in lexical order, by relative path and content.
//
// This is what deps.stdlib_dir requires. The driver resolves an import
// against that tree only on demand, so which files it reads is not
// knowable before the run; the whole tree is therefore the honest
// input boundary. Symlinks are followed (a build system's declared
// directory is commonly a tree of links into its output base) with
// loop protection, and the link target string is folded in too so a
// retargeted link is a key change even when both targets have the same
// content.
func (kb *keyBuilder) tree(tag, root string) error {
	kb.write([]byte(tag))
	kb.write([]byte(root))
	visited := map[string]bool{}
	return kb.walkTree(root, "", visited)
}

func (kb *keyBuilder) walkTree(dir, prefix string, visited map[string]bool) error {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("unitcache: resolve %s: %w", dir, err)
	}
	if visited[real] {
		// A link cycle. Record the revisit so the digest still
		// distinguishes "cycle here" from "nothing here" and stop.
		kb.field("cycle", []byte(prefix))
		return nil
	}
	visited[real] = true

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("unitcache: read %s: %w", dir, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		name := e.Name()
		path := filepath.Join(dir, name)
		rel := prefix + name
		if e.Type()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("unitcache: readlink %s: %w", path, err)
			}
			kb.field("symlink", []byte(rel))
			kb.field("symlink_target", []byte(target))
		}
		info, err := os.Stat(path) // follows symlinks
		if err != nil {
			return fmt.Errorf("unitcache: stat %s: %w", path, err)
		}
		if info.IsDir() {
			kb.field("dir", []byte(rel))
			if err := kb.walkTree(path, rel+"/", visited); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			// A device, socket or fifo is not content-addressable;
			// refusing here is what keeps "the key covers the action"
			// true rather than approximately true.
			return fmt.Errorf("unitcache: %s: not a regular file (%s)", path, info.Mode())
		}
		kb.write([]byte("tree_file"))
		kb.write([]byte(rel))
		sum, err := fileDigest(path)
		if err != nil {
			return fmt.Errorf("unitcache: digest declared input: %w", err)
		}
		kb.write(sum[:])
	}
	return nil
}

// write length-prefixes a chunk before hashing it, so ("ab", "c") and
// ("a", "bc") cannot collide.
func (kb *keyBuilder) write(p []byte) {
	var lenBuf [8]byte
	binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(p)))
	_, _ = kb.h.Write(lenBuf[:])
	_, _ = kb.h.Write(p)
}

// fileDigest returns the SHA-256 of a file's content, streamed so a
// large export-data archive never lands in memory whole.
func fileDigest(path string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	f, err := os.Open(path)
	if err != nil {
		return sum, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return sum, fmt.Errorf("read %s: %w", path, err)
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// sortedKeys returns a map's keys in lexical order, so map iteration
// order never reaches the digest.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
