#!/usr/bin/env bash
# Cross-compile gate for the Go v4 product on macOS, the BSD family and
# Windows.
#
# Three legs, all with CGO_ENABLED=0:
#
# 1. Cross-builds all three release binaries for GOOS in {darwin, freebsd,
#    openbsd, netbsd, dragonfly} x GOARCH in {amd64,arm64}:
#      ./cmd/iprange            (the iprange product)
#      ./cmd/iprange-v4-worker
#      ./cmd/iprange-v4-bench   (cross-build gate for bench stat constants)
#
# 2. `go vet ./...` for each of those GOOS/GOARCH pairs over the WHOLE
#    module.  `go build ./cmd/...` never type-checks _test.go files at
#    all, so a test-file-only compile gap for a target -- a test that
#    reaches for a symbol the target does not export, or a helper whose
#    //go:build line excludes it -- passes the build matrix silently and
#    only breaks `go test` when someone builds there.  vet type-checks
#    the test files, so the gap becomes a gate failure on Linux.
#
# 3. `go vet ./...` for GOOS=windows GOARCH=amd64 over the WHOLE module.
#    The Windows product ships, and a _test.go that uses a unix-only
#    symbol (Mkfifo and friends) without a //go:build unix tag compiles on
#    Linux and breaks the Windows build of the package, so that gap is a
#    shipping defect no Linux-side test run observes.
#
# Why this gate exists: the BSD and macOS targets are not built by CI today,
# and this class of breakage (OS-specific syscall constants, per-OS dirent
# layout, missing per-platform helpers, untaged test files) is silent on
# linux. The gates must be cheap (well under a minute) so it can run with
# the routine Go validation set.
#
# darwin is in scope because the source treats it as a shipping target:
# cmd/iprange-v4-worker/main.go gates the worker binary on
# (linux || darwin || freebsd || windows) && (amd64 || arm64),
# internal/calleropen/wait_darwin.go is gated `darwin || ios`, and the
# qualification records a darwin build id (BUILD_IDS_HOSTS in
# v4/cli/command_sanitize.py, expected_build_id.darwin in
# v4/cli/evidence/build-ids.json). Omitting darwin leaves those files
# type-checked by no leg of this matrix.
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
steps=0

# supported GOOS/GOARCH?
supported() {
    printf '%s\n' "$dist_list" | grep -qx "$1/$2"
}

for goos in darwin freebsd openbsd netbsd dragonfly; do
    for goarch in amd64 arm64; do
        if ! supported "$goos" "$goarch"; then
            printf '%b SKIP %s/%s %b(unsupported by the installed Go toolchain)\n' "$YELLOW" "$goos" "$goarch" "$NC"
            skips=$((skips + 1))
            continue
        fi

        # Leg 1: the three release binaries must cross-compile.
        if run nice env GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go build -C "$GO_DIR" -o "$tmp/" ./cmd/iprange ./cmd/iprange-v4-worker ./cmd/iprange-v4-bench; then
            printf '%b PASS %s/%s build ./cmd/...\n' "$GREEN" "$goos" "$goarch"
        else
            printf '%b FAIL %s/%s build ./cmd/... (the product does not cross-compile; see output above)\n' "$RED" "$goos" "$goarch"
            failures=$((failures + 1))
        fi
        steps=$((steps + 1))

        # Leg 2: the whole module, test files included, must type-check
        # for the same target.  A build-only matrix cannot see this class.
        if run nice env GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go vet -C "$GO_DIR" ./...; then
            printf '%b PASS %s/%s vet ./...\n' "$GREEN" "$goos" "$goarch"
        else
            printf '%b FAIL %s/%s vet ./... (a package or test file does not build for this target; see output above)\n' "$RED" "$goos" "$goarch"
            failures=$((failures + 1))
        fi
        steps=$((steps + 1))
    done
done

# Leg 3: Whole-module vet for Windows. `go build ./cmd/...` never type-checks
# _test.go files, so a test file that uses a unix-only symbol without a
# //go:build unix constraint passes the cross-build matrix and still
# breaks `go test` on Windows.
if ! supported windows amd64; then
    printf '%b SKIP windows/amd64 vet (unsupported by the installed Go toolchain)\n' "$YELLOW"
    skips=$((skips + 1))
else
    if run nice env GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet -C "$GO_DIR" ./...; then
        printf '%b PASS windows/amd64 vet ./...\n' "$GREEN"
    else
        printf '%b FAIL windows/amd64 vet ./... (a package or test file does not build for Windows; see output above)\n' "$RED"
        failures=$((failures + 1))
    fi
    steps=$((steps + 1))
fi

if [ "$failures" -ne 0 ]; then
    printf '%b %d of %d cross-platform step(s) failed; see output above.%b\n' "$RED" "$failures" "$steps" "$NC"
    exit 1
fi
printf '%b cross-platform matrix OK (%d steps ran, %d skipped: unsupported by toolchain).%b\n' "$GREEN" "$steps" "$skips" "$NC"
