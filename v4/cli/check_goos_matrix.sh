#!/usr/bin/env bash
# Cross-compile gate for the Go v4 product on the BSD family.
#
# Builds all three release binaries for GOOS in {freebsd,openbsd,
# netbsd, dragonfly} x GOARCH in {amd64,arm64} with CGO_ENABLED=0:
#   ./cmd/iprange            (the iprange product)
#   ./cmd/iprange-v4-worker
#   ./cmd/iprange-v4-bench   (cross-build gate for bench stat constants)
#
# Why this gate exists: the BSD targets are not built by CI today, and
# this class of breakage (OS-specific syscall constants, per-OS dirent
# layout, missing per-platform helpers) is silent on linux. The gates
# must be cheap (well under a minute) so it can run with the routine Go
# validation set.
#
# GOOS/GOARCH pairs the installed Go toolchain does not support (e.g.
# dragonfly/arm64) are skipped with a note, not failed: that is a
# toolchain limitation, not a product defect.
#
# Output goes to a scratch dir; nothing is written into the source tree.

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
GRAY='\033[0;90m'
NC='\033[0m'

ROOT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && cd ../.. && pwd)
GO_DIR="$ROOT_DIR/v4/go"

run() {
    printf >&2 "${GRAY}%s >${NC} " "$(pwd)"
    printf >&2 '%b' "$YELLOW"
    printf >&2 "%q " "$@"
    printf >&2 '%b\n' "$NC"
    "$@"
}

dist_list=$(go tool dist list)

# One scratch dir shared by every combo; binaries are overwritten per
# build and the dir is removed when the script exits.
tmp=$(mktemp -d "${TMPDIR:-/tmp}/iprange-goos-matrix.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

failures=0
skips=0
for goos in freebsd openbsd netbsd dragonfly; do
    for goarch in amd64 arm64; do
        if ! printf '%s\n' "$dist_list" | grep -qx "$goos/$goarch"; then
            printf '%b SKIP %s/%s %b(unsupported by the installed Go toolchain)\n' "$YELLOW" "$goos" "$goarch" "$NC"
            skips=$((skips + 1))
            continue
        fi
        if run nice env GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go build -C "$GO_DIR" -o "$tmp/" ./cmd/iprange ./cmd/iprange-v4-worker ./cmd/iprange-v4-bench; then
            printf '%b PASS %s/%s %b\n' "$GREEN" "$goos" "$goarch" "$NC"
        else
            printf '%b FAIL %s/%s %b\n' "$RED" "$goos" "$goarch" "$NC"
            failures=$((failures + 1))
        fi
    done
done

if [ "$failures" -ne 0 ]; then
    printf '%b %d BSD cross-build(s) failed; see output above.%b\n' "$RED" "$failures" "$NC"
    exit 1
fi
printf '%b BSD cross-build matrix OK (%d skipped: unsupported by toolchain).%b\n' "$GREEN" "$skips" "$NC"
