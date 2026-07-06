#!/bin/sh
# Copyright 2026 The plaid-lint Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.
#
# install-lint-binaries.sh installs the upstream binaries that
# plaid-lint's subprocess Runners (and the comparison run against
# golangci-lint v2.x) need on $PATH.
#
# Idempotent: skips an install when the binary already resolves on PATH.
# Output: a summary table of (name | version | path) at the end.
#
# Install target: $HOME/.local/bin (kept off the project module graph).
# Make sure this directory is on your PATH before running compare.

set -eu

INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
mkdir -p "$INSTALL_DIR"

# GNU time (for `/usr/bin/time -v` wall + VmHWM measurement in
# scripts/compare-against-c1.sh). The bash builtin `time` doesn't
# expose VmHWM. Base ubuntu images and minimal containers don't ship
# /usr/bin/time; macOS has it as `gtime` from brew's gnu-time package.
if ! command -v /usr/bin/time >/dev/null 2>&1 && ! command -v gtime >/dev/null 2>&1; then
    if command -v apt-get >/dev/null 2>&1; then
        echo "[install] GNU time (via apt-get)"
        sudo apt-get install -y time >/dev/null
    elif command -v brew >/dev/null 2>&1; then
        echo "[install] GNU time (via brew install gnu-time)"
        brew install gnu-time >/dev/null
    else
        echo "[warn] GNU time not available and no apt-get/brew found; install the 'time' package manually" >&2
    fi
fi

# go install respects $GOBIN; pin it so we never pollute the active
# module graph in the worktree where this script is invoked from.
export GOBIN="$INSTALL_DIR"

# Pinned golangci-lint version per ADR-10 / NOTES D-79 reference baseline.
GOLANGCI_VERSION="v2.9.0"

# Format: name|module@version
# The order matters only for the summary table.
PINS=$(cat <<'EOF'
unconvert|github.com/mdempsky/unconvert@latest
gochecknoinits|github.com/leighmcculloch/gochecknoinits@latest
dupl|github.com/mibk/dupl@latest
lll|github.com/walle/lll/cmd/lll@latest
gocyclo|github.com/fzipp/gocyclo/cmd/gocyclo@latest
nestif|github.com/nakabonne/nestif/cmd/nestif@latest
godox|github.com/766b/godox@latest
dogsled|github.com/alexkohler/dogsled/cmd/dogsled@latest
staticcheck|honnef.co/go/tools/cmd/staticcheck@latest
unparam|mvdan.cc/unparam@latest
golangci-lint|github.com/golangci/golangci-lint/v2/cmd/golangci-lint@__GOLANGCI_VERSION__
EOF
)

# Replace placeholder.
PINS=$(printf '%s\n' "$PINS" | sed "s|__GOLANGCI_VERSION__|$GOLANGCI_VERSION|g")

# Probe whether a binary exists on PATH and emit its version line.
binary_version() {
    bin="$1"
    if ! command -v "$bin" >/dev/null 2>&1; then
        echo "MISSING"
        return
    fi
    # Most of these answer to --version; gocyclo and dupl don't, so fall
    # back to "(no --version)". Suppress stderr to keep the table clean.
    "$bin" --version 2>/dev/null | head -1 \
        || "$bin" -version 2>/dev/null | head -1 \
        || echo "(no version flag)"
}

install_one() {
    name="$1"
    mod="$2"

    if command -v "$name" >/dev/null 2>&1; then
        echo "[skip] $name already on PATH at $(command -v "$name")"
        return
    fi

    echo "[install] $name <- $mod"
    # cd /tmp so go install can't pick up a workspace go.mod.
    (cd /tmp && go install "$mod") || {
        echo "[FAIL] $name install failed (mod=$mod)" >&2
        return 1
    }
    if ! command -v "$name" >/dev/null 2>&1; then
        echo "[FAIL] $name not on PATH after install; INSTALL_DIR=$INSTALL_DIR not in PATH?" >&2
        return 1
    fi
}

# Pre-flight: ensure $INSTALL_DIR is on PATH; if not, warn but don't die.
case ":$PATH:" in
    *":$INSTALL_DIR:"*)
        ;;
    *)
        echo "[warn] $INSTALL_DIR is not on PATH — installed binaries will not resolve until you add it." >&2
        ;;
esac

# Walk the pins.
FAILED=0
printf '%s\n' "$PINS" | while IFS='|' read -r name mod; do
    [ -z "$name" ] && continue
    install_one "$name" "$mod" || FAILED=$((FAILED + 1))
done

# Final summary table.
echo
echo "============================================================"
echo " Summary"
echo "============================================================"
printf '%-20s %-40s %s\n' "name" "version" "path"
printf '%-20s %-40s %s\n' "----" "-------" "----"
printf '%s\n' "$PINS" | while IFS='|' read -r name _; do
    [ -z "$name" ] && continue
    path=$(command -v "$name" 2>/dev/null || echo "MISSING")
    if [ "$path" = "MISSING" ]; then
        ver="MISSING"
    else
        ver=$(binary_version "$name")
    fi
    printf '%-20s %-40s %s\n' "$name" "$ver" "$path"
done

# Report GNU time availability (the comparison harness needs it).
if [ -x /usr/bin/time ]; then
    printf '%-20s %-40s %s\n' "GNU time" "$(/usr/bin/time --version 2>&1 | head -1)" "/usr/bin/time"
elif command -v gtime >/dev/null 2>&1; then
    printf '%-20s %-40s %s\n' "GNU time" "$(gtime --version 2>&1 | head -1)" "$(command -v gtime)"
else
    printf '%-20s %-40s %s\n' "GNU time" "MISSING" "(install 'time' package)"
fi

echo
echo "Done. INSTALL_DIR=$INSTALL_DIR"
