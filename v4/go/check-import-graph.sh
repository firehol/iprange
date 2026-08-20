#!/bin/sh
# check-import-graph.sh — enforce the v4 Go import boundaries and the
# mmap-only content-transfer ban.
#
# Mirrors v4/rust/check-source-graph.sh for the Go peer:
#   - internal/format is the wire-codec owner: stdlib + internal/work
#     (test-only counters; Rust slotted_page.rs imports crate::work).
#   - internal/mapping is the mapping owner: stdlib + golang.org/x/sys +
#     internal/format (page-size constants and the shared error codes) +
#     internal/work (test-only necessary-work counters) only.
#   - internal/bootstrap is the pure two-meta selection authority: stdlib
#     + internal/format only, shared by the reader and the writer, and no
#     sync/sync/atomic/unsafe.
#   - internal/tree is the generic COW B+tree core: stdlib + internal/format
#     + internal/work only.
#   - internal/bitmap is the hierarchical-bitmap core: stdlib + internal/
#     format + internal/tree + internal/work only.
#   - internal/retire is the retirement-extent tree: stdlib + internal/
#     format + internal/tree only.
#   - internal/writer is the writer core: stdlib + internal/bootstrap +
#     internal/format + internal/mapping + internal/work + internal/tree +
#     internal/bitmap + internal/retire only.
#   - internal/reader is the reader core: stdlib + internal/format +
#     internal/mapping + internal/work only, and no sync/sync/atomic/unsafe:
#     the traversal paths carry no per-call synchronization
#     (design-iprange-engine.md).
#   - the module root (public facade) imports only stdlib + internal/format
#     + internal/reader + internal/writer. Nothing else may import
#     internal/* (the writer boundary joined the root facade in SOW-0025
#     chunk 6 so the public SDK stays the single iprangedb package).
#   - golang.org/x/sys is the mapping owner's surface only: every package,
#     on every target, must not import it (checked in the per-target loop
#     below, so a build-tagged new package cannot bypass the owner rule).
#
# Mapped-view holder whitelist (SOW-0025 decision 2026-08-19): only
# internal/mapping, internal/format, internal/reader, internal/writer,
# and the module root may handle mapped page views. The import rules
# above stop every other package from importing the mapping owner, and
# the gatescan rule checkViewHolderExports fails closed on any mapped
# view exported from a non-holder package (unit-pinned in
# v4/go-gate/viewholder_test.go; end-to-end battery forms B1-B4).
#
# In addition to import boundaries, production sources are mechanically
# banned from content-transfer I/O so the mmap-only contract cannot regress.
# The content scan is the typed gatescan tool (v4/go-gate, stdlib-only): it
# type-checks every non-test package under every target build context and
# reports
#   - banned content-transfer imports and dot imports;
#   - banned selector call families (.Read/.Write/.Seek/..., reflection
#     Call, decoders/encoders, fmt.Fscan*, x/sys descriptor variants);
#   - any *os.File/*os.Root value used outside the approved capability
#     surface (mapping lifecycle methods and same-package / module-internal
#     / x/sys consumers);
#   - complete-page ownership: copy/append/array-conversion sinks that move
#     a full mapped page view (>= 4096 bytes) into an owned buffer
#     (binary-format-v4.md:108). The single legal in-memory exception, the
#     metadata inflater in internal/reader/metadata.go, is exempted by
#     exact call shape.
#
# The gate is a mechanical tripwire, not a proof: it catches the transfer
# forms listed above. Runtime tracing of an actual open/lookup session
# (openat -> OFD lock -> mmap -> munmap -> unlock/close with no
# read/pread/readv/lseek on the database descriptor) is recorded in the
# milestone report as the runtime half of the mmap-only evidence.
#
# Usage: ./check-import-graph.sh [--self-test]   (run from the v4/go
# directory). --self-test runs the durable mutation battery: the typed
# source-mutation battery lives in the gatescan tool itself (the tool
# prints the exact case counts), and the shell keeps the module-graph /
# boundary / environment shapes the Go table cannot express (internal-
# import boundary, x/sys outside the mapping owner, bare assembly object,
# go.mod replace, go.work, poisoned x/sys cache/proxy, unlistable module).
# The self-test never writes to the reviewed tree: every mutation is
# applied to a private temporary copy.
#
# The scanner tool lives outside this module (v4/go-gate) so the gate can
# scan every file under v4/go without scanning itself.

set -eu
cd "$(dirname "$0")"

self_test=0
if [ "${1:-}" = "--self-test" ]; then
	self_test=1
fi

fail=0

# The typed content-transfer scanner. Built once per run into a private
# temp path; inner self-test runs reuse it via GATE_SCANNER_BIN (they run
# from a temp copy of the module, where the relative ../go-gate path does
# not exist, so the build is skipped whenever the binary is supplied).
scanner_bin=${GATE_SCANNER_BIN:-}
if [ -z "$scanner_bin" ]; then
	scanner_dir=$(cd "$PWD/../go-gate" && pwd)
	scanner_bin=$(mktemp /tmp/iprange-gatescan.XXXXXX)
	if ! nice go -C "$scanner_dir" build -o "$scanner_bin" .; then
		echo "gate scanner build failed"
		exit 1
	fi
fi

self_tree=""
cleanup() {
	if [ -n "$scanner_bin" ] && [ -z "${GATE_SCANNER_BIN:-}" ]; then
		rm -r "$scanner_bin" 2>/dev/null || true
	fi
	if [ -n "$self_tree" ]; then
		rm -r "$self_tree" 2>/dev/null || true
	fi
}
trap cleanup EXIT INT TERM

# per-package import list (every import on its own line; the first import is
# NOT swallowed — a join without the ImportPath prefix). A package the go
# toolchain cannot list is a failed gate, not an empty import set: the
# callers fail closed on listing errors.
pkg_imports() {
	if ! out=$(go list -f '{{join .Imports "\n"}}' "$1" 2>/dev/null); then
		return 1
	fi
	printf '%s\n' "$out"
}

check() {
	pkg=$1
	allowed_prefix=$2
	content=$(pkg_imports "$pkg") || {
		echo "boundary violation: $pkg cannot be listed by the go toolchain"
		fail=1
		return
	}
	if [ -n "$content" ]; then
		for imp in $content; do
			case "$imp" in
			"syscall")
				echo "boundary violation: $pkg imports stdlib syscall (use golang.org/x/sys in mapping only)"
				fail=1
				;;
			"github.com/firehol/iprange/v4/go"*)
				if ! printf '%s\n' "$imp" | grep -q "^$allowed_prefix"; then
					echo "boundary violation: $pkg imports $imp"
					fail=1
				fi
				;;
			"golang.org/x/sys"*)
				if [ "$pkg" != "github.com/firehol/iprange/v4/go/internal/mapping" ]; then
					echo "boundary violation: $pkg imports $imp (mapping is the syscall owner)"
					fail=1
				fi
				;;
			*)
				if printf '%s\n' "$imp" | grep -q '\.'; then
					echo "boundary violation: $pkg imports non-stdlib $imp"
					fail=1
				fi
				;;
			esac
		done
	fi
}

check "github.com/firehol/iprange/v4/go/internal/format" "github.com/firehol/iprange/v4/go/internal/\(format\|work\)"
check "github.com/firehol/iprange/v4/go/internal/bootstrap" "github.com/firehol/iprange/v4/go/internal/\(bootstrap\|format\)"
check "github.com/firehol/iprange/v4/go/internal/mapping" "github.com/firehol/iprange/v4/go/internal/\(format\|mapping\|work\)"
check "github.com/firehol/iprange/v4/go/internal/tree" "github.com/firehol/iprange/v4/go/internal/\(format\|tree\|work\)"
check "github.com/firehol/iprange/v4/go/internal/bitmap" "github.com/firehol/iprange/v4/go/internal/\(bitmap\|format\|tree\|work\)"
check "github.com/firehol/iprange/v4/go/internal/retire" "github.com/firehol/iprange/v4/go/internal/\(format\|retire\|tree\)"
check "github.com/firehol/iprange/v4/go/internal/writer" "github.com/firehol/iprange/v4/go/internal/\(bitmap\|bootstrap\|fault\|format\|mapping\|retire\|tree\|work\)"
check "github.com/firehol/iprange/v4/go/internal/reader" "github.com/firehol/iprange/v4/go/internal/\(bootstrap\|format\|mapping\|work\)"
check "github.com/firehol/iprange/v4/go" "github.com/firehol/iprange/v4/go/internal/\(format\|reader\|writer\)"

# The reader and its import closure (format, bootstrap) are the
# synchronization-free zone: no sync, sync/atomic, or unsafe in any of
# these packages.
for pkg in "github.com/firehol/iprange/v4/go/internal/format" \
		"github.com/firehol/iprange/v4/go/internal/bootstrap" \
		"github.com/firehol/iprange/v4/go/internal/reader"; do
	if printf '%s\n' "$(pkg_imports "$pkg")" | grep -qE '^(sync|sync/atomic|unsafe)$'; then
		echo "synchronization/unsafe violation: $pkg"
		fail=1
	fi
done

# Typed content-transfer scan: banned imports/selectors, the *os.File
# capability surface, and the complete-page ownership rule, over every
# production file (all build contexts). The GATESCAN_CONFIGS developer
# knob is a local iteration aid only: it must never narrow the
# authoritative production scan, so a polluted environment cannot make
# the gate report success on a subset of the OS set.
unset GATESCAN_CONFIGS
if ! nice "$scanner_bin" .; then
	echo "content-transfer violation in production sources"
	fail=1
fi

# The module graph is a capability surface: a go.mod require/replace or a
# go.work workspace can attach out-of-tree code whose files this scan never
# walks. The graph must be exactly this module plus golang.org/x/sys, with
# no workspace active; any other module, or a graph that cannot be
# resolved, fails closed.
gowork=$(go env GOWORK 2>/dev/null)
if [ -n "$gowork" ] && [ "$gowork" != "off" ]; then
	echo "boundary violation: a go.work workspace is active ($gowork); the gate validates only the module's own tree"
	fail=1
fi
mods=$(go list -m -f '{{.Path}}' all 2>/dev/null) || {
	echo "boundary violation: the module graph cannot be resolved (go list -m all failed)"
	fail=1
}
for m in $mods; do
	case "$m" in
	"github.com/firehol/iprange/v4/go"|"golang.org/x/sys")
		;;
	*)
		echo "boundary violation: module graph contains $m (only golang.org/x/sys may join the module)"
		fail=1
		;;
	esac
done
# A replace (or exclude) directive can redirect any module - including
# golang.org/x/sys itself - to a directory the scan never walks while the
# allowed path stays in the graph; no source redirection is permitted.
if grep -Eq '^(replace|exclude) ' go.mod; then
	echo "boundary violation: go.mod contains a replace or exclude directive (module source redirection)"
	fail=1
fi
# Defense in depth: the resolved x/sys source must be the module-cache
# checkout of the exact official version with the official content. The
# path alone is not enough: GOMODCACHE and GOPROXY are environment
# inputs, so a poisoned module cache or a proxy-served imposter keeps the
# allowed path and version while loading code this scan never walks (the
# ban list cannot know a function the genuine module does not have). The
# extracted-tree hash and the module zip/go.mod sums are pinned to the
# official values; any deviation fails closed.
x_sys_version=v0.35.0
x_sys_zip_sum=h1:vz1N37gP5bs89s7He8XuIYXpyY0+QlsKmzipCbUtyxI=
x_sys_mod_sum=h1:BJP2sWEmIv4KK5OTEluFJCKSidICx8ciO85XgH3Ak8k=
if [ "$fail" -eq 0 ]; then
	modroot=$(go env GOMODCACHE)
	xsys_dir=$(go list -m -f '{{.Dir}}' golang.org/x/sys 2>/dev/null)
	xsys_ver=$(go list -m -f '{{.Version}}' golang.org/x/sys 2>/dev/null)
	if [ "$xsys_ver" != "$x_sys_version" ]; then
		echo "boundary violation: golang.org/x/sys version is ${xsys_ver:-unresolved}, want $x_sys_version"
		fail=1
	fi
	case "$xsys_dir" in
	"$modroot"/golang.org/x/sys@v0.35.0)
		;;
	"")
		echo "boundary violation: golang.org/x/sys cannot be resolved (go list -m failed)"
		fail=1
		;;
	*)
		echo "boundary violation: golang.org/x/sys resolves to $xsys_dir (expected the module-cache checkout of v0.35.0)"
		fail=1
		;;
	esac
	if [ -n "$xsys_dir" ] && [ "$fail" -eq 0 ]; then
		got_hash=$("$scanner_bin" --dirhash "golang.org/x/sys@v0.35.0" "$xsys_dir" 2>/dev/null)
		if [ "$got_hash" != "$x_sys_zip_sum" ]; then
			echo "boundary violation: golang.org/x/sys checkout content hash ${got_hash:-unreadable} does not match the official module ($x_sys_zip_sum); the module cache is not the genuine source"
			fail=1
		fi
	fi
	if [ "$fail" -eq 0 ]; then
		dl=$(go mod download -json golang.org/x/sys 2>/dev/null || true)
		dl_sum=$(printf '%s\n' "$dl" | grep -o '"Sum": "[^"]*"' | head -1 | cut -d'"' -f4)
		dl_modsum=$(printf '%s\n' "$dl" | grep -o '"GoModSum": "[^"]*"' | head -1 | cut -d'"' -f4)
		if [ "$dl_sum" != "$x_sys_zip_sum" ] || [ "$dl_modsum" != "$x_sys_mod_sum" ]; then
			echo "boundary violation: golang.org/x/sys module sums (${dl_sum:-unresolved}, ${dl_modsum:-unresolved}) do not match the official module ($x_sys_zip_sum, $x_sys_mod_sum); a proxy or cache is serving an imposter"
			fail=1
		fi
	fi
fi

# Only the reader may hold the mapping, and only the reader core may be
# consumed by the facade. The check runs under every supported target so a
# build-tagged package that exists only on one GOOS/GOARCH cannot import
# internal packages unseen.
targets="linux/amd64 linux/386 linux/arm linux/arm64 linux/loong64 \
	darwin/amd64 darwin/arm64 freebsd/amd64 netbsd/amd64 windows/amd64 windows/arm64"
for target in $targets; do
	GOOS=${target%/*} GOARCH=${target#*/} export GOOS GOARCH
	target_pkgs=$(go list ./... 2>/dev/null) || {
		echo "boundary violation: go list ./... failed under $GOOS/$GOARCH; a module that cannot be listed must not pass the gate"
		fail=1
		target_pkgs=""
	}
	for pkg in $target_pkgs; do
		imps=$(pkg_imports "$pkg") || {
			echo "boundary violation: $pkg (target $target) cannot be listed by the go toolchain"
			fail=1
			imps=""
		}
		case "$pkg" in
		"github.com/firehol/iprange/v4/go/internal/mapping")
			# the mapping owner is the syscall authority
			;;
		*)
			case "$imps" in
			*"golang.org/x/sys"*)
				echo "boundary violation: $pkg (target $target) imports golang.org/x/sys (mapping is the syscall owner)"
				fail=1
				;;
			esac
			;;
		esac
		case "$pkg" in
		"github.com/firehol/iprange/v4/go"|\
		"github.com/firehol/iprange/v4/go/internal/format"|\
		"github.com/firehol/iprange/v4/go/internal/bootstrap"|\
		"github.com/firehol/iprange/v4/go/internal/mapping"|\
		"github.com/firehol/iprange/v4/go/internal/tree"|\
		"github.com/firehol/iprange/v4/go/internal/bitmap"|\
		"github.com/firehol/iprange/v4/go/internal/retire"|\
		"github.com/firehol/iprange/v4/go/internal/writer"|\
		"github.com/firehol/iprange/v4/go/internal/reader")
			# these may import internal/* by the approved boundary
			;;
		*)
			case "$imps" in
			*"github.com/firehol/iprange/v4/go/internal"*)
				echo "boundary violation: $pkg (target $target) imports internal packages"
				fail=1
				;;
			esac
			;;
		esac
	done
done

if [ "$self_test" -eq 1 ]; then
	# The source-mutation battery is table data inside the gatescan tool;
	# the tool prints the exact case counts on every self-test run. It
	# runs every case against its own private copy of this tree.
	if ! nice "$scanner_bin" --self-test .; then
		echo "gatescan self-test failed"
		fail=1
	fi

	# Shell-side durable mutations: module-graph and environment shapes the
	# Go table cannot express. Each mutation is applied to a private copy
	# of the module; the gate must reject it there. The reviewed tree is
	# never modified.
	self_tree=$(mktemp -d /tmp/iprange-gate-shell.XXXXXX)
	mkdir -p "$self_tree/go"
	cp -a . "$self_tree/go"
	cd "$self_tree/go"

	muts=""
	mut_restores=""
	cleanup_muts() {
		for m in $muts; do
			rm -r "$m" 2>/dev/null || true
		done
		muts=""
		for f in $mut_restores; do
			if [ -f "$f.gatemut-save" ]; then
				cp "$f.gatemut-save" "$f"
				rm -f "$f.gatemut-save" 2>/dev/null || true
			fi
		done
		mut_restores=""
	}

	mutfail=0
	mut_env=""

	run_mut() {
		name=$1
		if env GATE_SCANNER_BIN="$scanner_bin" $mut_env ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test MISS: mutation $name did not fail the gate"
			mutfail=1
		fi
		cleanup_muts
	}

	add_mut() {
		muts="$muts $1"
	}

	# --- 238: x/sys imported outside the mapping owner ------------------
	# The syscall surface is the mapping owner's alone; a new package
	# importing golang.org/x/sys (even for an innocent call) breaks the
	# owner rule and must fail in the per-target boundary loop.
	mkdir -p gatemut_sysowner
	cat > gatemut_sysowner/gatemut_sysowner_linux.go <<'MUTEOF'
//go:build linux
package gatemut_sysowner

import "golang.org/x/sys/unix"

func gateClose238(fd int) error {
	return unix.Close(fd)
}
MUTEOF
	add_mut gatemut_sysowner
	run_mut "x/sys import outside the mapping owner"

	# --- 239: assembly object without a Go declaration ------------------
	# A .s file alone cannot be called without a bodyless declaration or
	# //go:linkname (both banned), but the walk must fail closed on the
	# object itself so no future relaxation silently attaches a syscall
	# body. The probe is never compiled: the self-test tree is scanned,
	# not built.
	mkdir -p gatemut_asmfile
	cat > gatemut_asmfile/gatemut_asmfile_linux.s <<'MUTEOF'
//go:build linux

TEXT ·gateAsmNop239(SB),NOSPLIT,$0
	RET
MUTEOF
	add_mut gatemut_asmfile
	run_mut "assembly object file"

	# --- 18: windows-only package importing internal/mapping --------------
	# Only the four approved packages may import internal/*; a new root
	# package that exists only on one target must fail in the per-target
	# boundary loop (a build-tagged package cannot bypass the boundary).
	mkdir -p gatemut_winint
	cat > gatemut_winint/mut.go <<'MUTEOF'
//go:build windows

package gatemut_winint

import "github.com/firehol/iprange/v4/go/internal/mapping"

var _ = mapping.Mapping{}
MUTEOF
	add_mut gatemut_winint
	run_mut "windows-only package importing internal/mapping"

	# --- 241: go.mod replace attaching an out-of-tree module -------------
	# A replace directive can point the import graph at a directory the
	# scanner never walks; the module graph itself must fail closed.
	mkdir -p gatemut_wrap
	cat > gatemut_wrap/go.mod <<'MUTEOF'
module wrapper

go 1.23.0
MUTEOF
	cat > gatemut_wrap/wrap.go <<'MUTEOF'
package wrapper

import "golang.org/x/sys/unix"

func Fetch(fd int) error { return unix.Close(fd) }
MUTEOF
	add_mut gatemut_wrap
	cp go.mod "$self_tree/gomod.sav"
	printf '\nrequire wrapper v0.0.0\nreplace wrapper => ./gatemut_wrap\n' >> go.mod
	run_mut "go.mod replace to an out-of-tree module"
	cp "$self_tree/gomod.sav" go.mod
	rm "$self_tree/gomod.sav"

	# --- 242: go.work workspace attaching an out-of-tree module ----------
	# A workspace can import modules by directory without touching go.mod;
	# any active workspace fails closed.
	cat > go.work <<'MUTEOF'
go 1.23.0

use .
MUTEOF
	add_mut go.work
	run_mut "go.work workspace"

	# --- 243: replace of golang.org/x/sys to an evil source --------------
	# The allowed path survives in the module graph; the replace itself
	# must fail closed because the replaced source is never walked.
	mkdir -p gatemut_xsys/unix
	cat > gatemut_xsys/go.mod <<'MUTEOF'
module golang.org/x/sys

go 1.23.0
MUTEOF
	cat > gatemut_xsys/unix/smuggle.go <<'MUTEOF'
package unix

func Smuggle(fd int, p []byte) (int, error) { return 0, nil }
MUTEOF
	cat > internal/mapping/gatemut_xsread_linux.go <<'MUTEOF'
//go:build linux
package mapping

import "golang.org/x/sys/unix"

func gateXsysRead243(fd uintptr, b []byte) (int, error) {
	return unix.Smuggle(int(fd), b)
}
MUTEOF
	add_mut gatemut_xsys
	cp go.mod "$self_tree/gomod.sav"
	printf '\nreplace golang.org/x/sys => ./gatemut_xsys\n' >> go.mod
	run_mut "replace of golang.org/x/sys to an evil source"
	cp "$self_tree/gomod.sav" go.mod
	rm "$self_tree/gomod.sav"

	# The evil x/sys module used by forms 245-246: a fake golang.org/x/sys
	# whose unix package adds Pread2, a content-transfer function the ban
	# list cannot know because the genuine module does not have it. Built
	# once; hashes are computed with the scanner's own Hash1 so the forged
	# go.sum is self-consistent.
	mkdir -p gatemut_evil_src/unix
	cat > gatemut_evil_src/go.mod <<'MUTEOF'
module golang.org/x/sys

go 1.23.0
MUTEOF
	cat > gatemut_evil_src/unix/smuggle.go <<'MUTEOF'
package unix

func Pread2(fd int, p []byte, offset int64) (int, error) { return 0, nil }
MUTEOF
	"$scanner_bin" --makezip golang.org/x/sys@v0.35.0 gatemut_evil_src gatemut_evil.zip
	evil_sum=$("$scanner_bin" --dirhash golang.org/x/sys@v0.35.0 gatemut_evil_src)

	# --- 245: poisoned module cache serving an evil x/sys checkout -------
	# GOMODCACHE is an environment input. An evil extraction plus download
	# cache at the allowed path resolves normally while smuggling a
	# function the ban list has never heard of; the pinned content hash
	# must fail closed.
	mkdir -p gatemut_cache/cache/download/golang.org/x/sys/@v
	cp gatemut_evil.zip gatemut_cache/cache/download/golang.org/x/sys/@v/v0.35.0.zip
	cp gatemut_evil_src/go.mod gatemut_cache/cache/download/golang.org/x/sys/@v/v0.35.0.mod
	printf '{"Version":"v0.35.0","Time":"2026-01-01T00:00:00Z"}\n' > gatemut_cache/cache/download/golang.org/x/sys/@v/v0.35.0.info
	mkdir -p gatemut_cache/golang.org/x/sys@v0.35.0/unix
	cp gatemut_evil_src/go.mod gatemut_cache/golang.org/x/sys@v0.35.0/go.mod
	cp gatemut_evil_src/unix/smuggle.go gatemut_cache/golang.org/x/sys@v0.35.0/unix/smuggle.go
	add_mut gatemut_cache
	cp go.sum "$self_tree/gosum.sav"
	printf 'golang.org/x/sys v0.35.0 %s\ngolang.org/x/sys v0.35.0/go.mod %s\n' "$evil_sum" "h1:BJP2sWEmIv4KK5OTEluFJCKSidICx8ciO85XgH3Ak8k=" > go.sum
	mut_env="GOMODCACHE=$PWD/gatemut_cache GOPROXY=off GOSUMDB=off"
	# Pre-verify the seeded cache exactly as the toolchain does: Go only
	# treats a module as downloaded (go list -m resolves a Dir) after the
	# zip is verified and the .ziphash marker is written. Without this the
	# form rejected via the fail-closed listing fallback and the content
	# pin never ran; with it, the checkout content-hash boundary fires.
	env $mut_env go mod download golang.org/x/sys
	run_mut "poisoned module cache with an evil x/sys checkout"
	mut_env=""
	cp "$self_tree/gosum.sav" go.sum
	rm "$self_tree/gosum.sav"

	# --- 246: file proxy serving an evil x/sys with a forged go.sum ------
	# GOPROXY is an environment input. A file proxy plus a repo go.sum
	# rewritten with the evil zip's self-consistent hashes launders the
	# imposter into a fresh cache; the pinned official sums must fail
	# closed.
	mkdir -p gatemut_proxy/golang.org/x/sys/@v
	cp gatemut_evil.zip gatemut_proxy/golang.org/x/sys/@v/v0.35.0.zip
	cp gatemut_evil_src/go.mod gatemut_proxy/golang.org/x/sys/@v/v0.35.0.mod
	printf '{"Version":"v0.35.0","Time":"2026-01-01T00:00:00Z"}\n' > gatemut_proxy/golang.org/x/sys/@v/v0.35.0.info
	mkdir -p gatemut_proxycache
	add_mut gatemut_proxy; add_mut gatemut_proxycache
	cp go.sum "$self_tree/gosum.sav"
	printf 'golang.org/x/sys v0.35.0 %s\ngolang.org/x/sys v0.35.0/go.mod %s\n' "$evil_sum" "h1:BJP2sWEmIv4KK5OTEluFJCKSidICx8ciO85XgH3Ak8k=" > go.sum
	mut_env="GOMODCACHE=$PWD/gatemut_proxycache GOPROXY=file://$PWD/gatemut_proxy GOSUMDB=off"
	# Same pre-verification as form 245: the proxy zip is fetched and
	# verified against the forged go.sum, writing the .ziphash marker so
	# the resolved checkout reaches the content-hash boundary instead of
	# dying in the listing fallback.
	env $mut_env go mod download golang.org/x/sys
	run_mut "file proxy serving an evil x/sys with a forged go.sum"
	mut_env=""
	cp "$self_tree/gosum.sav" go.sum
	rm "$self_tree/gosum.sav"

	# --- 248: module that cannot be listed or built ----------------------
	# A module the go toolchain cannot list must fail closed, not pass
	# vacuously. A directory holding two package names makes go list
	# ./... fail, which trips the gate's per-target listing boundary.
	cat > internal/mapping/gatemut_broken.go <<'MUTEOF'
package mapping2

var GateMutBroken int = 1
MUTEOF
	add_mut internal/mapping/gatemut_broken.go
	run_mut "module that cannot be listed or built"

	cd "$OLDPWD"
	if [ "$mutfail" -ne 0 ]; then
		echo "import-graph shell self-test FAILED"
		fail=1
	fi
fi

if [ "$fail" -ne 0 ]; then
	echo "import-graph check FAILED"
	exit 1
fi
echo "import-graph check passed"
