#!/usr/bin/env bash
# Cross-compile gate for the Go v4 product on the BSD family.
#
# Two checks, both with CGO_ENABLED=0:
#
# 1. Cross-builds all three release binaries for GOOS in {freebsd,
#    openbsd, netbsd, dragonfly} x GOARCH in {amd64,arm64}:
#      ./cmd/iprange            (the iprange product)
#      ./cmd/iprange-v4-worker
#      ./cmd/iprange-v4-bench   (cross-build gate for bench stat constants)
#
# 2. `go vet ./...` for GOOS=windows GOARCH=amd64 over the WHOLE module.
#    Building only ./cmd/... cannot see the platform gap in the test
#    files, and vet type-checks them: a _test.go that reaches for a
#    unix-only symbol (Mkfifo and friends) without a //go:build unix tag
#    compiles on Linux and breaks the Windows build of the package. The
#    Windows product ships, so that gap is a shipping defect that no
#    Linux-side check can observe.
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

# Whole-module vet for Windows. `go build ./cmd/...` never type-checks
# _test.go files, so a test file that uses a unix-only symbol without a
# //go:build unix constraint passes the cross-build matrix and still
# breaks `go test` on Windows.
if ! printf '%s\n' "$dist_list" | grep -qx "windows/amd64"; then
    printf '%b SKIP windows/amd64 vet (unsupported by the installed Go toolchain)\n' "$YELLOW"
    skips=$((skips + 1))
else
    if run nice env GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet -C "$GO_DIR" ./...; then
        printf '%b PASS windows/amd64 vet ./...\n' "$GREEN"
    else
        printf '%b FAIL windows/amd64 vet ./... (a package or test file does not build for Windows; see output above)\n' "$RED"
        failures=$((failures + 1))
    fi
fi

if [ "$failures" -ne 0 ]; then
    printf '%b %d cross-platform step(s) failed; see output above.%b\n' "$RED" "$failures" "$NC"
    exit 1
fi
printf '%b cross-platform matrix OK (%d skipped: unsupported by toolchain).%b\n' "$GREEN" "$skips" "$NC"
