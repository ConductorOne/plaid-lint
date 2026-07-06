#!/bin/sh
# Copyright 2026 The plaid-lint Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.
#
# compare-against-c1.sh drives plaid-lint vs golangci-lint v2.x
# end-to-end against a target tree (default: c1's pkg/c1semconv) and
# produces a single markdown report covering wall time, peak RSS, and
# diagnostic-set divergence.
#
# Usage:
#   compare-against-c1.sh [TARGET]
#
# TARGET defaults to /data/squire/src/c1/pkg/c1semconv/... (the smoke
# target). Pass /data/squire/src/c1/... for the full-c1 run.
#
# Requirements: every binary listed in scripts/install-lint-binaries.sh
# must be on $PATH. The plaid-lint and compare-lints binaries must
# also be built; this script builds stale or missing local copies inline.

set -eu

TARGET="${1:-/data/squire/src/c1/pkg/c1semconv/...}"
REPORT="${REPORT:-/tmp/compare-against-c1.md}"
COLD_RUNS="${COLD_RUNS:-2}"
GOMAXPROCS="${GOMAXPROCS:-8}"
export GOMAXPROCS

# plaid-lint Phase-3 subprocess-runners with hardcoded `./...` /
# enumerateGoFiles arguments trip on `.git/logs/refs/remotes/origin/...go`
# paths created when c1 branches are named like `pkg/foo/bar.go`. Until
# the runners take the target package set, suppress them here. See
# NOTES.md D-101 (smoke blocker) for the full list and the runner-side
# fix scoped into the follow-up dispatch.
PLAID_DISABLE="${PLAID_DISABLE:-gochecknoinits,dogsled,dupl,gocyclo,godox,lll,nestif,unconvert}"

# c1's .golangci.yml references a custom Go-plugin (tracecheck) that
# ships as a .so the dev environment does not produce. golangci-lint
# refuses to run when it can't load it, so the runner uses a cleaned
# copy of the config (custom block + tracecheck enable/disable lines
# stripped). Plaid-lint accepts the original config and surfaces a
# warning, so it still gets the unmodified file. This matches what
# scripts/config-parity.sh does.
C1_CONFIG="${C1_CONFIG:-/data/squire/src/c1/.golangci.yml}"
C1_CONFIG_GCI="${C1_CONFIG_GCI:-/tmp/compare-golangci.yml}"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PLAID_LINT="${PLAID_LINT:-/tmp/plaid-lint}"
COMPARE_LINTS="${COMPARE_LINTS:-/tmp/compare-lints}"
STRIP_TRACECHECK="${STRIP_TRACECHECK:-$REPO_ROOT/cmd/strip-tracecheck-config/strip-tracecheck-config}"

# Build the YAML stripper if absent or stale. Replaces an earlier python
# heredoc that needed pyyaml — the Nix python3 3.13 in some Squire envs
# doesn't ship pyyaml and the system python3 it shadows can't be relied
# on either. The Go tool uses gopkg.in/yaml.v3 (already in go.mod).
if [ ! -x "$STRIP_TRACECHECK" ] || [ "$REPO_ROOT/cmd/strip-tracecheck-config/main.go" -nt "$STRIP_TRACECHECK" ]; then
    echo "  Building $STRIP_TRACECHECK"
    (cd "$REPO_ROOT" && go build -o "$STRIP_TRACECHECK" ./cmd/strip-tracecheck-config)
fi
if [ ! -f "$C1_CONFIG_GCI" ] || [ "$C1_CONFIG" -nt "$C1_CONFIG_GCI" ] || [ "$STRIP_TRACECHECK" -nt "$C1_CONFIG_GCI" ]; then
    "$STRIP_TRACECHECK" "$C1_CONFIG" "$C1_CONFIG_GCI"
fi

# Paths to JSON artifacts.
G_JSON=/tmp/golangci.json
C_JSON=/tmp/plaid.json
G_LOG=/tmp/golangci-time.log
C_LOG=/tmp/plaid-time.log

# REPO_ROOT, PLAID_LINT, COMPARE_LINTS, STRIP_TRACECHECK are defined
# above (the tracecheck-stripper needs REPO_ROOT to build).

# --- Preflight ---
echo "==> Preflight"
missing=0
for b in unconvert gochecknoinits dupl lll gocyclo nestif godox dogsled staticcheck unparam golangci-lint; do
    if ! command -v "$b" >/dev/null 2>&1; then
        echo "  [MISSING] $b — run scripts/install-lint-binaries.sh" >&2
        missing=$((missing + 1))
    fi
done
if [ "$missing" -gt 0 ]; then
    echo "Aborting: $missing required binaries missing." >&2
    exit 1
fi

# Find /usr/bin/time (the GNU one — bash builtin won't give us VmHWM).
TIME_BIN=""
if [ -x /usr/bin/time ]; then
    TIME_BIN="/usr/bin/time"
elif command -v gtime >/dev/null 2>&1; then
    TIME_BIN="$(command -v gtime)"
else
    echo "[warn] GNU time not found; falling back to shell builtin (no VmHWM)" >&2
fi

# Build plaid-lint + compare-lints if absent or stale.
if [ ! -x "$PLAID_LINT" ] || [ "$REPO_ROOT/cmd/plaid-lint/run.go" -nt "$PLAID_LINT" ]; then
    echo "  Building $PLAID_LINT"
    (cd "$REPO_ROOT" && go build -o "$PLAID_LINT" ./cmd/plaid-lint/)
fi
if [ ! -x "$COMPARE_LINTS" ] || [ "$REPO_ROOT/cmd/compare-lints/main.go" -nt "$COMPARE_LINTS" ]; then
    echo "  Building $COMPARE_LINTS"
    (cd "$REPO_ROOT" && go build -o "$COMPARE_LINTS" ./cmd/compare-lints/)
fi

# --- Cache wipe ---
echo "==> Wiping caches"
PLAID_CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/plaid-lint"
GCI_CACHE_DIR="${GOLANGCI_LINT_CACHE:-$HOME/.cache/golangci-lint}"
rm -rf "$PLAID_CACHE" "$GCI_CACHE_DIR"
echo "  removed $PLAID_CACHE"
echo "  removed $GCI_CACHE_DIR"

# --- Run helpers ---
# run_with_time LOG CMD...
# Writes GNU time output (verbose) to LOG. Returns the command's exit status.
run_with_time() {
    log="$1"
    shift
    if [ -n "$TIME_BIN" ]; then
        # -v prints all known fields. We post-process for wall+VmHWM.
        "$TIME_BIN" -v -o "$log" "$@"
    else
        { time "$@" ; } 2>"$log"
    fi
}

# extract_wall LOG -> seconds (float, may be empty if unavailable).
extract_wall() {
    log="$1"
    # GNU time: "Elapsed (wall clock) time (h:mm:ss or m:ss): 0:01.23"
    grep -E 'Elapsed \(wall' "$log" 2>/dev/null \
        | sed -E 's/.*: ([0-9:.]+)$/\1/' \
        | awk -F: '
            NF==1 { print $1 }
            NF==2 { print $1*60 + $2 }
            NF==3 { print $1*3600 + $2*60 + $3 }
        '
}

# extract_rss LOG -> kilobytes (integer).
extract_rss() {
    log="$1"
    grep -E 'Maximum resident set size' "$log" 2>/dev/null \
        | sed -E 's/.*: ([0-9]+).*/\1/'
}

# --- Cold runs ---
echo "==> Cold runs (GOMAXPROCS=$GOMAXPROCS, $COLD_RUNS iterations each)"

# Collect wall + rss per iteration.
g_walls="" ; g_rsses=""
c_walls="" ; c_rsses=""

i=1
while [ "$i" -le "$COLD_RUNS" ]; do
    echo "  cold #$i  golangci-lint"
    rm -rf "$GCI_CACHE_DIR"
    rm -f "$G_JSON"
    set +e
    run_with_time "$G_LOG" golangci-lint run --output.json.path="$G_JSON" --config="$C1_CONFIG_GCI" "$TARGET"
    set -e
    w=$(extract_wall "$G_LOG"); r=$(extract_rss "$G_LOG")
    echo "    wall=${w}s rss=${r}KB"
    g_walls="$g_walls $w" ; g_rsses="$g_rsses $r"
    # Stash run #1 JSON for the comparator.
    if [ "$i" = "1" ]; then
        cp -f "$G_LOG" "${G_LOG}.cold"
        cp -f "$G_JSON" "${G_JSON}.cold"
    fi

    echo "  cold #$i  plaid-lint"
    rm -rf "$PLAID_CACHE"
    rm -f "$C_JSON"
    set +e
    run_with_time "$C_LOG" "$PLAID_LINT" run --output.json.path="$C_JSON" --config="$C1_CONFIG" --disable="$PLAID_DISABLE" "$TARGET"
    set -e
    w=$(extract_wall "$C_LOG"); r=$(extract_rss "$C_LOG")
    echo "    wall=${w}s rss=${r}KB"
    c_walls="$c_walls $w" ; c_rsses="$c_rsses $r"
    if [ "$i" = "1" ]; then
        cp -f "$C_LOG" "${C_LOG}.cold"
        cp -f "$C_JSON" "${C_JSON}.cold"
    fi

    i=$((i + 1))
done

# --- Warm run (cache populated by last cold run) ---
echo "==> Warm runs"
echo "  warm golangci-lint"
set +e
run_with_time "${G_LOG}.warm" golangci-lint run --output.json.path="$G_JSON" --config="$C1_CONFIG_GCI" "$TARGET"
set -e
g_warm_wall=$(extract_wall "${G_LOG}.warm")
g_warm_rss=$(extract_rss "${G_LOG}.warm")
echo "    wall=${g_warm_wall}s rss=${g_warm_rss}KB"

echo "  warm plaid-lint"
set +e
run_with_time "${C_LOG}.warm" "$PLAID_LINT" run --output.json.path="$C_JSON" --config="$C1_CONFIG" --disable="$PLAID_DISABLE" "$TARGET"
set -e
c_warm_wall=$(extract_wall "${C_LOG}.warm")
c_warm_rss=$(extract_rss "${C_LOG}.warm")
echo "    wall=${c_warm_wall}s rss=${c_warm_rss}KB"

# --- Invoke comparator ---
echo "==> Running diagnostic comparator"
COMPARE_REPORT=/tmp/compare-report.md
"$COMPARE_LINTS" --golangci="${G_JSON}.cold" --plaid="${C_JSON}.cold" --out="$COMPARE_REPORT" --detailed

# --- Compute min / median / max ---
stats_min_med_max() {
    # space-separated list of numbers on stdin or arg list.
    printf '%s\n' "$@" | tr ' ' '\n' | grep -E '^[0-9]' | awk '
        { v[NR]=$1 }
        END {
            n=NR
            for (i=1; i<=n; i++) for (j=i+1; j<=n; j++) if (v[i]>v[j]) { t=v[i];v[i]=v[j];v[j]=t }
            if (n==0) { print "n/a n/a n/a"; exit }
            min=v[1]; max=v[n]
            if (n%2==1) med=v[(n+1)/2]; else med=(v[n/2]+v[n/2+1])/2
            printf "%s %s %s\n", min, med, max
        }
    '
}

g_wall_stats=$(stats_min_med_max $g_walls)
c_wall_stats=$(stats_min_med_max $c_walls)
g_rss_stats=$(stats_min_med_max $g_rsses)
c_rss_stats=$(stats_min_med_max $c_rsses)

# --- Final report ---
echo "==> Writing $REPORT"
{
    echo "# plaid-lint vs golangci-lint: comparison report"
    echo
    echo "_Generated $(date -u +%Y-%m-%dT%H:%M:%SZ) on $(uname -n)_"
    echo
    echo "## Run metadata"
    echo
    echo "- Target: \`$TARGET\`"
    echo "- GOMAXPROCS=$GOMAXPROCS"
    echo "- Cold iterations: $COLD_RUNS"
    echo "- Host: \`$(uname -a)\`"
    echo "- nproc: $(nproc 2>/dev/null || echo unknown)"
    echo "- plaid-lint binary: \`$PLAID_LINT\`"
    echo "- golangci-lint: \`$(command -v golangci-lint)\` ($(golangci-lint version 2>&1 | head -1))"
    if cd "$REPO_ROOT" 2>/dev/null; then
        echo "- plaid-lint commit: \`$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)\` on branch \`$(git -C "$REPO_ROOT" rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)\`"
    fi
    echo
    echo "## Performance"
    echo
    echo "Cold runs (min / median / max across $COLD_RUNS iterations):"
    echo
    echo "| Tool | Cold wall (s) | Cold peak RSS (KB) | Warm wall (s) | Warm peak RSS (KB) |"
    echo "|---|---|---|---|---|"
    echo "| golangci-lint | $g_wall_stats | $g_rss_stats | $g_warm_wall | $g_warm_rss |"
    echo "| plaid-lint  | $c_wall_stats | $c_rss_stats | $c_warm_wall | $c_warm_rss |"
    echo
    echo "## Diagnostic divergence"
    echo
    cat "$COMPARE_REPORT"
} > "$REPORT"

echo "==> Done. Report at $REPORT"
echo "    (detailed divergence dump: ${COMPARE_REPORT%.md}.diff.md)"
