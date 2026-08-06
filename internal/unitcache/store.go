// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unitcache

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Outputs names the declared outputs of one unit action. Facts is
// empty for actions that declare none (mode=module).
type Outputs struct {
	Sarif string
	Facts string
}

// Entry is everything a unit action observably produces: the bytes of
// its declared outputs plus the warnings it wrote to stderr. A cache
// hit must reproduce all of it — a run that silently dropped the
// warning "linter X is not supported in unit mode" would be a
// different run.
//
// One wrinkle, in the warnings only: a persistent worker prints
// config-load warnings once per distinct config rather than once per
// request (it memoizes the parsed config), so an entry filled by a
// worker's second request carries no config warnings while one filled
// by a one-shot run does. The declared outputs are unaffected — they
// are a function of the declared inputs, which is what the key covers.
//
// The exit status is not stored because only successful actions are
// cached: an action whose inputs were unusable is an infrastructure
// failure, and replaying one would hide a real problem.
type Entry struct {
	Sarif    []byte
	Facts    []byte
	HasFacts bool
	Warnings []string
}

// entryMagic prefixes every serialized entry: "PLUC" (plaid-lint unit
// cache) plus one format version byte. Readers reject anything else,
// so a format change is a clean miss rather than a misparse.
var entryMagic = []byte{'P', 'L', 'U', 'C', 0x01}

// Store is a content-addressed directory of entries. It is safe for
// concurrent use across goroutines and processes: writes publish
// through a temp file and a rename, and entries are immutable once
// published (any two writers for a key hold, by construction,
// equivalent bytes).
type Store struct {
	root string
}

// Open prepares dir as a cache root, creating it if needed. dir is
// always an explicit caller-supplied path; this package never
// discovers a location from the environment.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("unitcache: empty cache directory")
	}
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return nil, fmt.Errorf("unitcache: create %s: %w", dir, err)
	}
	return &Store{root: dir}, nil
}

// Root returns the cache directory.
func (s *Store) Root() string { return s.root }

// path returns the on-disk location of an entry, sharded by the first
// byte of the key so a large cache does not pile every entry into one
// directory.
func (s *Store) path(k Key) string {
	h := k.Hex()
	return filepath.Join(s.root, h[:2], h)
}

// Get returns the entry for k, or nil when there is none.
//
// A corrupt or truncated entry is reported as an error AND as a miss:
// callers recompute. Never returning half an entry is what lets the
// caller treat any successful Get as equivalent to a cold run.
func (s *Store) Get(k Key) (*Entry, error) {
	body, err := os.ReadFile(s.path(k))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("unitcache: read entry: %w", err)
	}
	e, err := decodeEntry(k, body)
	if err != nil {
		return nil, err
	}
	return e, nil
}

// Put publishes e under k. Publishing is atomic (temp file + rename),
// so a killed process never leaves an entry a later run could read
// half of.
func (s *Store) Put(k Key, e *Entry) error {
	dest := s.path(k)
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return fmt.Errorf("unitcache: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".plaid-unitcache-*")
	if err != nil {
		return fmt.Errorf("unitcache: create temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(encodeEntry(k, e)); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("unitcache: write entry: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("unitcache: write entry: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("unitcache: publish entry: %w", err)
	}
	return nil
}

// ReadOutputs captures the declared outputs a completed action wrote,
// pairing them with the warnings it produced, ready for Put.
func ReadOutputs(out Outputs, warnings []string) (*Entry, error) {
	sarif, err := os.ReadFile(out.Sarif)
	if err != nil {
		return nil, fmt.Errorf("unitcache: read sarif output: %w", err)
	}
	e := &Entry{Sarif: sarif, Warnings: warnings}
	if out.Facts != "" {
		facts, err := os.ReadFile(out.Facts)
		if err != nil {
			return nil, fmt.Errorf("unitcache: read facts output: %w", err)
		}
		e.Facts, e.HasFacts = facts, true
	}
	return e, nil
}

// Write materializes a cached entry as the action's declared outputs.
//
// An entry that does not carry facts cannot satisfy an action that
// declares a facts output (and vice versa): rather than leave a
// declared output missing, that mismatch is an error and the caller
// falls back to a cold run.
func (e *Entry) Write(out Outputs) error {
	if (out.Facts != "") != e.HasFacts {
		return fmt.Errorf("unitcache: entry facts presence (%t) does not match the action's declared outputs", e.HasFacts)
	}
	if err := writeFileAtomic(out.Sarif, e.Sarif); err != nil {
		return fmt.Errorf("unitcache: write sarif output: %w", err)
	}
	if out.Facts != "" {
		if err := writeFileAtomic(out.Facts, e.Facts); err != nil {
			return fmt.Errorf("unitcache: write facts output: %w", err)
		}
	}
	return nil
}

// encodeEntry serializes an entry:
//
//	magic | key | nWarnings | (len, bytes)* | hasFacts | sarif | facts | sha256
//
// The key is stored inside the entry and checked on read so a
// misfiled or hand-edited entry is rejected rather than served under
// the wrong inputs; the trailing checksum makes a partially written or
// bit-rotted file a miss rather than a wrong answer.
func encodeEntry(k Key, e *Entry) []byte {
	var b bytes.Buffer
	b.Write(entryMagic)
	b.Write(k[:])
	writeUint32(&b, uint32(len(e.Warnings)))
	for _, w := range e.Warnings {
		writeBytes(&b, []byte(w))
	}
	var hasFacts byte
	if e.HasFacts {
		hasFacts = 1
	}
	b.WriteByte(hasFacts)
	writeBytes(&b, e.Sarif)
	writeBytes(&b, e.Facts)
	sum := sha256.Sum256(b.Bytes())
	b.Write(sum[:])
	return b.Bytes()
}

// decodeEntry is encodeEntry's inverse. Every failure mode returns an
// error; there is no partial success.
func decodeEntry(k Key, body []byte) (*Entry, error) {
	if len(body) < len(entryMagic)+sha256.Size {
		return nil, fmt.Errorf("unitcache: entry %s: truncated (%d bytes)", k, len(body))
	}
	if !bytes.Equal(body[:len(entryMagic)], entryMagic) {
		return nil, fmt.Errorf("unitcache: entry %s: bad magic", k)
	}
	payload, want := body[:len(body)-sha256.Size], body[len(body)-sha256.Size:]
	if got := sha256.Sum256(payload); !bytes.Equal(got[:], want) {
		return nil, fmt.Errorf("unitcache: entry %s: checksum mismatch", k)
	}
	r := payload[len(entryMagic):]
	if len(r) < sha256.Size {
		return nil, fmt.Errorf("unitcache: entry %s: truncated key", k)
	}
	if !bytes.Equal(r[:sha256.Size], k[:]) {
		return nil, fmt.Errorf("unitcache: entry %s: key mismatch", k)
	}
	r = r[sha256.Size:]

	n, r, err := readUint32(r)
	if err != nil {
		return nil, fmt.Errorf("unitcache: entry %s: %w", k, err)
	}
	e := &Entry{}
	for range n {
		var w []byte
		w, r, err = readBytes(r)
		if err != nil {
			return nil, fmt.Errorf("unitcache: entry %s: %w", k, err)
		}
		e.Warnings = append(e.Warnings, string(w))
	}
	if len(r) < 1 {
		return nil, fmt.Errorf("unitcache: entry %s: truncated body", k)
	}
	e.HasFacts, r = r[0] == 1, r[1:]
	if e.Sarif, r, err = readBytes(r); err != nil {
		return nil, fmt.Errorf("unitcache: entry %s: %w", k, err)
	}
	if e.Facts, r, err = readBytes(r); err != nil {
		return nil, fmt.Errorf("unitcache: entry %s: %w", k, err)
	}
	if len(r) != 0 {
		return nil, fmt.Errorf("unitcache: entry %s: %d trailing bytes", k, len(r))
	}
	return e, nil
}

func writeUint32(b *bytes.Buffer, v uint32) {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	b.Write(buf[:])
}

func writeBytes(b *bytes.Buffer, p []byte) {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(len(p)))
	b.Write(buf[:])
	b.Write(p)
}

func readUint32(r []byte) (uint32, []byte, error) {
	if len(r) < 4 {
		return 0, nil, fmt.Errorf("truncated count")
	}
	return binary.LittleEndian.Uint32(r), r[4:], nil
}

func readBytes(r []byte) ([]byte, []byte, error) {
	if len(r) < 8 {
		return nil, nil, fmt.Errorf("truncated length")
	}
	n := binary.LittleEndian.Uint64(r)
	r = r[8:]
	if uint64(len(r)) < n {
		return nil, nil, fmt.Errorf("truncated payload (want %d, have %d)", n, len(r))
	}
	return r[:n], r[n:], nil
}

// writeFileAtomic writes body to path via a temp file + rename, the
// same discipline internal/unit uses for the driver's own outputs: a
// build system must never observe a torn declared output.
func writeFileAtomic(path string, body []byte) error {
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o777); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, ".plaid-unitcache-out-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
