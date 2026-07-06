// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subproc

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/output"
)

// CustomRunner is the generic [Runner] for user-supplied plugin
// binaries declared under `linters.settings.custom.<name>` in the
// .golangci.yml. The full protocol it implements, in short:
//
//   - The plugin binary is invoked with cwd = [WorkspaceRef.ModuleRoot]
//     and a single positional arg `./...`. Build tags propagate via
//     `GOFLAGS=-tags=...`.
//   - `CustomLinterSettings.Settings`, when a `map[string]any`, is
//     forwarded as `--<key>=<value>` flags in lexicographic key order.
//   - stdout is NDJSON: one diagnostic per line in the shape
//     `{"file":..,"line":..,"column":..,"severity":..,"message":..,
//     "code":?,"related":[{...}]?}`.
//   - Exit 0 = clean no findings; exit 1 = clean with findings; exit
//     ≥2 = hard error (`*InvokeError`).
//   - The cache key folds the plugin-binary sha256 and the workspace's
//     `go.sum` sha256 into `linterVersion`, closing T3.1's documented
//     invalidation gap the same way [UnusedRunner] and
//     [UnparamRunner] do.
//
// Concurrency: CustomRunner is safe for concurrent use; per-Run state
// lives on the stack and [Cache] is itself concurrency-safe.
type CustomRunner struct {
	// name is the registry-canonical linter name. It is the
	// attribution surfaced through [output.Diagnostic.Linter] for
	// every emitted diagnostic, regardless of what the plugin sets
	// internally.
	name string

	// settings carries the user's plugin config (Path + free-form
	// Settings payload). Its full JSON encoding participates in the
	// cache key.
	settings config.CustomLinterSettings

	// cache is the shared subproc cache. Nil disables caching.
	cache Cache
}

// NewCustomRunner constructs a runner for the plugin identified by
// name with the given user settings. cache may be nil to disable
// caching (useful for tests and one-off invocations).
func NewCustomRunner(name string, settings config.CustomLinterSettings, cache Cache) *CustomRunner {
	return &CustomRunner{name: name, settings: settings, cache: cache}
}

// Name implements [Runner].
func (r *CustomRunner) Name() string { return r.name }

// Run implements [Runner].
//
//  1. Resolve the plugin binary path (absolute or via $PATH).
//  2. Compute linterVersion = sha256(binary) ⊕ sha256(go.sum).
//  3. Compute settingsHash from json.Marshal(settings).
//  4. CacheKey lookup; on hit return cached.
//  5. On miss: invoke the plugin with cwd=ModuleRoot, GOFLAGS for
//     build tags, `--<key>=<value>` flags from settings, and the
//     positional `./...`.
//  6. Parse NDJSON stdout → []SubprocDiagnostic.
//  7. Canonicalize and store in the cache.
func (r *CustomRunner) Run(ctx context.Context, _ *config.Config, ws WorkspaceRef) ([]output.Diagnostic, error) {
	binary, err := r.resolveBinary()
	if err != nil {
		return nil, &InvokeError{Name: r.binaryDisplayName(), ExitCode: -1, Err: err}
	}

	version, err := r.linterVersion(binary, ws)
	if err != nil {
		return nil, err
	}

	settingsHash, err := r.settingsHash()
	if err != nil {
		return nil, err
	}

	key, err := CacheKey(r.Name(), version, settingsHash, ws)
	if err != nil {
		return nil, err
	}

	if r.cache != nil {
		if diags, ok, lookupErr := r.cache.Lookup(key); lookupErr == nil && ok {
			return diags, nil
		}
	}

	args := r.buildArgs()
	env := r.buildEnv(ws)

	stdout, stderr, exitCode, invokeErr := invokeInDir(ctx, binary, ws.ModuleRoot, env, args)

	// Exit 0 (clean, no findings) and 1 (clean, findings present) are
	// both "ran cleanly"; stdout is authoritative. Anything else is a
	// hard error.
	if invokeErr != nil && exitCode != 0 && exitCode != 1 {
		return nil, invokeErr
	}

	diags, parseErr := parseCustomNDJSON(binary, stdout)
	if parseErr != nil {
		if len(stderr) > 0 {
			parseErr.Detail = parseErr.Detail + "; stderr=" + summarizeStderr(stderr)
		}
		return nil, parseErr
	}

	canon := Canonicalize(r.Name(), ws, diags)
	if r.cache != nil {
		if err := r.cache.Store(key, canon); err != nil {
			return nil, err
		}
	}
	return canon, nil
}

// resolveBinary returns the plugin binary path. Absolute is used
// as-is; relative is looked up on $PATH via exec.LookPath. An empty
// Path is a configuration error.
func (r *CustomRunner) resolveBinary() (string, error) {
	p := r.settings.Path
	if p == "" {
		return "", fmt.Errorf("custom %q: CustomLinterSettings.Path is empty", r.name)
	}
	if filepath.IsAbs(p) {
		return p, nil
	}
	resolved, err := exec.LookPath(p)
	if err != nil {
		return "", fmt.Errorf("custom %q: locate binary %q: %w", r.name, p, err)
	}
	return resolved, nil
}

// binaryDisplayName returns a stable label for error messages even
// when the resolve step failed.
func (r *CustomRunner) binaryDisplayName() string {
	if r.settings.Path != "" {
		return r.settings.Path
	}
	return r.name
}

// linterVersion folds sha256(binary) + sha256(go.sum) into a single
// hex digest, mirroring [UnusedRunner.linterVersion] and
// [UnparamRunner.linterVersion]. A missing go.sum contributes the
// empty string (length-prefixed) so its absence is part of the key.
func (r *CustomRunner) linterVersion(binary string, ws WorkspaceRef) (string, error) {
	binHash, err := sha256File(binary)
	if err != nil {
		return "", fmt.Errorf("custom %q: hash binary %s: %w", r.name, binary, err)
	}
	sumPath := filepath.Join(ws.ModuleRoot, "go.sum")
	sumHash, err := sha256File(sumPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		sumHash = ""
	case err != nil:
		return "", fmt.Errorf("custom %q: hash %s: %w", r.name, sumPath, err)
	}
	h := sha256.New()
	writeField(h, "bin", binHash)
	writeField(h, "go.sum", sumHash)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// settingsHash hashes the user's CustomLinterSettings. encoding/json
// emits map keys in sorted order, so any settings shape (typed or
// `map[string]any`) hashes deterministically.
func (r *CustomRunner) settingsHash() (string, error) {
	data, err := json.Marshal(&r.settings)
	if err != nil {
		return "", fmt.Errorf("custom %q: encode settings: %w", r.name, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// buildArgs produces the argv tail for the subprocess: deterministic
// `--<key>=<value>` flag list followed by `./...`. Non-map Settings
// (or nil) means no flags. See the protocol doc for the
// stringification rules.
func (r *CustomRunner) buildArgs() []string {
	args := []string{}
	if m, ok := r.settings.Settings.(map[string]any); ok && len(m) > 0 {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			args = append(args, "--"+k+"="+stringifySetting(m[k]))
		}
	}
	args = append(args, "./...")
	return args
}

// buildEnv produces the env extension for invokeInDir: parent ws.Env
// followed by GOFLAGS=-tags=... when BuildTags are set.
func (r *CustomRunner) buildEnv(ws WorkspaceRef) []string {
	env := append([]string(nil), ws.Env...)
	if len(ws.BuildTags) > 0 {
		// Sort + dedup so a tag-order reshuffle doesn't churn the
		// subprocess invocation (the cache key already canonicalizes
		// tags via WorkspaceContentHash, so the subprocess should see
		// the same canonical form).
		seen := make(map[string]struct{}, len(ws.BuildTags))
		tags := make([]string, 0, len(ws.BuildTags))
		for _, t := range ws.BuildTags {
			if _, ok := seen[t]; ok {
				continue
			}
			seen[t] = struct{}{}
			tags = append(tags, t)
		}
		sort.Strings(tags)
		env = append(env, "GOFLAGS=-tags="+joinTags(tags))
	}
	return env
}

// joinTags joins build tags with comma, matching the form go's flag
// parser accepts.
func joinTags(tags []string) string {
	switch len(tags) {
	case 0:
		return ""
	case 1:
		return tags[0]
	}
	var b bytes.Buffer
	for i, t := range tags {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(t)
	}
	return b.String()
}

// stringifySetting coerces any (the YAML-decoded Settings value type)
// to a stable string for the `--key=value` form. Booleans render as
// `true` / `false`; numbers via strconv; strings verbatim; everything
// else via fmt.Sprintf("%v"). The protocol forwards empty strings
// and `false` so the plugin sees what the user wrote.
func stringifySetting(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		// YAML / JSON numbers commonly decode as float64. Render
		// integers without a decimal point so a user-supplied `3`
		// becomes `--key=3` rather than `--key=3.000000`.
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// customDiagLine is the on-wire shape of a single NDJSON diagnostic.
// Required fields are required; optional fields stay zero-valued when
// the plugin omits them.
type customDiagLine struct {
	File     string            `json:"file"`
	Line     int               `json:"line"`
	Column   int               `json:"column"`
	Severity string            `json:"severity"`
	Message  string            `json:"message"`
	Code     string            `json:"code,omitempty"`
	Related  []customDiagPoint `json:"related,omitempty"`
}

// customDiagPoint is one entry in a diagnostic's optional `related`
// array.
type customDiagPoint struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Message string `json:"message"`
}

// parseCustomNDJSON decodes newline-delimited JSON from stdout into a
// slice of [SubprocDiagnostic]. Empty / whitespace-only stdout is a
// valid "ran cleanly, no findings" result and produces (nil, nil).
//
// Each non-empty line must be a valid JSON object matching
// [customDiagLine] with the four required fields (file, line,
// severity, message) present. Anything else is reported as
// *ParseError with the offending line number in Detail.
func parseCustomNDJSON(binary string, stdout []byte) ([]SubprocDiagnostic, *ParseError) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return nil, nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	// Plugins may emit long lines (large messages, long related
	// arrays); push the buffer cap up so we don't false-flag a parse
	// error on a legitimate 1 MiB diagnostic.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var diags []SubprocDiagnostic
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var line customDiagLine
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&line); err != nil {
			// Try once more without strict-unknown so a future plugin
			// that adds an extra optional field doesn't fail an older
			// runner. The protocol commits to additive-field
			// backwards-compat; if the relaxed decode also fails, the
			// line is genuinely malformed.
			var relaxed customDiagLine
			if err2 := json.Unmarshal(raw, &relaxed); err2 != nil {
				return nil, &ParseError{
					Name:   binary,
					Detail: fmt.Sprintf("decode ndjson line %d", lineNum),
					Err:    err2,
				}
			}
			line = relaxed
		}
		if line.File == "" || line.Line <= 0 || line.Message == "" {
			return nil, &ParseError{
				Name:   binary,
				Detail: fmt.Sprintf("ndjson line %d missing required field (file/line/message)", lineNum),
			}
		}
		sd := SubprocDiagnostic{
			Message:  line.Message,
			Severity: line.Severity,
			File:     line.File,
			Line:     line.Line,
			Column:   line.Column,
		}
		if len(line.Related) > 0 {
			sd.Related = make([]SubprocRelated, 0, len(line.Related))
			for _, p := range line.Related {
				sd.Related = append(sd.Related, SubprocRelated{
					Message: p.Message,
					File:    p.File,
					Line:    p.Line,
					Column:  p.Column,
				})
			}
		}
		diags = append(diags, sd)
	}
	if err := scanner.Err(); err != nil {
		return nil, &ParseError{
			Name:   binary,
			Detail: "scan ndjson stdout",
			Err:    err,
		}
	}
	return diags, nil
}
