#!/bin/sh
# Copyright 2026 The plaid-lint Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.
#
# config-parity.sh diffs the set of linters resolved-enabled by
# plaid-lint vs golangci-lint when both are pointed at the same
# .golangci.yml. Any mismatch is reported and triggers a non-zero exit.
#
# Usage:
#   config-parity.sh [CONFIG_PATH]
#
# CONFIG_PATH defaults to /data/squire/src/c1/.golangci.yml.

set -eu

CONFIG="${1:-/data/squire/src/c1/.golangci.yml}"
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PLAID_LINT="${PLAID_LINT:-/tmp/plaid-lint}"

if [ ! -f "$CONFIG" ]; then
    echo "config-parity: $CONFIG not found" >&2
    exit 2
fi

if [ ! -x "$PLAID_LINT" ] || [ "$REPO_ROOT/cmd/plaid-lint/run.go" -nt "$PLAID_LINT" ]; then
    echo "Building $PLAID_LINT"
    (cd "$REPO_ROOT" && go build -o "$PLAID_LINT" ./cmd/plaid-lint/)
fi

GCI=/tmp/parity-golangci.json
CLK=/tmp/parity-plaid.json

# c1's config has a custom (.so) plugin (tracecheck). golangci-lint
# refuses to even list linters when it can't load the plugin, so we have
# to chdir to the config's directory AND accept that golangci-lint will
# fail on `tracecheck`. The workaround: copy the config into /tmp with
# the linters.custom block stripped.
CONFIG_CLEAN=/tmp/parity-golangci.yml
python3 - "$CONFIG" "$CONFIG_CLEAN" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
with open(src) as f:
    text = f.read()
# Crude but adequate: strip the linters.custom block by re-rendering YAML.
try:
    import yaml
except ImportError:
    # No PyYAML — fall back to manual line edit. Drop the `custom:` block
    # and any "- tracecheck" enable/disable references.
    out = []
    skip_block = False
    indent = None
    for line in text.splitlines():
        if line.strip().startswith("custom:"):
            skip_block = True
            indent = len(line) - len(line.lstrip())
            continue
        if skip_block:
            if line.strip() == "" or (len(line) - len(line.lstrip())) > indent:
                continue
            skip_block = False
        if "tracecheck" in line:
            continue
        out.append(line)
    open(dst, "w").write("\n".join(out) + "\n")
    sys.exit(0)
doc = yaml.safe_load(text)
if isinstance(doc, dict):
    linters = doc.get("linters") or {}
    settings = linters.get("settings") or {}
    settings.pop("custom", None)
    for key in ("enable", "disable"):
        if key in linters and isinstance(linters[key], list):
            linters[key] = [x for x in linters[key] if x != "tracecheck"]
    excl = (((doc.get("linters") or {}).get("exclusions") or {}).get("rules") or [])
    for rule in excl:
        if isinstance(rule, dict) and "linters" in rule and isinstance(rule["linters"], list):
            rule["linters"] = [x for x in rule["linters"] if x != "tracecheck"]
with open(dst, "w") as f:
    yaml.safe_dump(doc, f, sort_keys=False)
PY

echo "==> golangci-lint linters --json --config $CONFIG_CLEAN"
golangci-lint linters --json --config "$CONFIG_CLEAN" > "$GCI" 2>/tmp/parity-golangci.err || {
    echo "  golangci-lint failed:" >&2
    head -20 /tmp/parity-golangci.err >&2
    exit 1
}

echo "==> plaid-lint linters --json --config $CONFIG"
"$PLAID_LINT" linters --json --config "$CONFIG" > "$CLK" 2>/tmp/parity-plaid.err

# Extract enabled-linter names from each shape.
# golangci-lint v2 shape: {"Enabled":[{"name":"..."}], "Disabled":[...]}
# plaid shape: [{"name":"...","enabled":true}, ...] (note duplicates by sub-analyzer)
g_enabled=$(python3 -c '
import json,sys
doc=json.load(open(sys.argv[1]))
names=sorted({x["name"] for x in doc.get("Enabled",[])})
print("\n".join(names))
' "$GCI")

c_enabled=$(python3 -c '
import json,sys
doc=json.load(open(sys.argv[1]))
names=sorted({x["name"] for x in doc if x.get("enabled")})
print("\n".join(names))
' "$CLK")

echo "==> golangci-lint enabled set:"
echo "$g_enabled" | sed 's/^/  /'
echo
echo "==> plaid-lint enabled set:"
echo "$c_enabled" | sed 's/^/  /'
echo

# Diff. /tmp/parity-{g,c}.set contain the sorted name lists; comm gives us
# left-only / right-only / both.
printf '%s\n' "$g_enabled" > /tmp/parity-g.set
printf '%s\n' "$c_enabled" > /tmp/parity-c.set

GO_ONLY=$(comm -23 /tmp/parity-g.set /tmp/parity-c.set)
CL_ONLY=$(comm -13 /tmp/parity-g.set /tmp/parity-c.set)
BOTH=$(comm -12 /tmp/parity-g.set /tmp/parity-c.set | wc -l)

echo "==> Parity report"
echo "  both-enabled:   $BOTH"
echo "  golangci-only:  $(printf '%s' "$GO_ONLY" | grep -c . || true)"
echo "  plaid-only:   $(printf '%s' "$CL_ONLY" | grep -c . || true)"

if [ -n "$GO_ONLY" ]; then
    echo
    echo "  --- golangci-only ---"
    printf '  %s\n' $GO_ONLY
fi
if [ -n "$CL_ONLY" ]; then
    echo
    echo "  --- plaid-only ---"
    printf '  %s\n' $CL_ONLY
fi

if [ -z "$GO_ONLY" ] && [ -z "$CL_ONLY" ]; then
    echo
    echo "PARITY OK"
    exit 0
fi

echo
echo "PARITY MISMATCH"
exit 1
