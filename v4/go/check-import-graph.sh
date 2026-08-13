#!/bin/sh
# check-import-graph.sh — enforce the v4 Go import boundaries and the
# mmap-only content-transfer ban.
#
# Mirrors v4/rust/check-source-graph.sh for the Go peer:
#   - internal/format is the wire-codec owner: stdlib only.
#   - internal/mapping is the mapping owner: stdlib + golang.org/x/sys +
#     internal/format (page-size constants and the shared error codes only).
#   - internal/reader is the reader core: stdlib + internal/format +
#     internal/mapping only, and no sync/sync/atomic/unsafe: the traversal
#     paths carry no per-call synchronization (design-iprange-engine.md).
#   - the module root (public facade) imports only stdlib + internal/format
#     + internal/reader. Nothing else may import internal/*.
#   - the obsolete milestone-0 internal/exactv4 tree was deleted with the
#     approved deletion set; no legacy transfer point remains.
#
# In addition to import boundaries, production sources are mechanically
# banned from content-transfer I/O so the mmap-only contract cannot regress.
# The content scan is AST-based (v4/go-gate/main.go, a stdlib-only tool):
# it parses every non-test .go file — build tags, line wrapping, comments,
# aliases, and file names are irrelevant to the token stream — and reports
#   - banned content-transfer imports and dot imports;
#   - banned selector call families (.Read/.Write/.Seek/..., reflection
#     Call, decoders/encoders, fmt.Fscan*, x/sys descriptor variants); and
#   - any *os.File value used outside the approved capability surface
#     (mapping lifecycle methods and same-package / module-internal / x/sys
#     consumers).
# The three in-memory inflater nodes in internal/reader/metadata.go
# (c.r.Read(p), c.r.ReadByte(), and the two exact
# io.ReadFull(zr, out[...int(meta.MetadataUncompressed)]) shapes) are
# exempted as exact call shapes and only when their receiver/arguments are
# not file-tainted; a file-backed reproduction of the same text fails.
#
# The gate is a mechanical tripwire, not a proof: it catches the transfer
# forms listed above. Runtime tracing of an actual open/lookup session
# (openat -> OFD lock -> mmap -> munmap -> unlock/close with no
# read/pread/readv/lseek on the database descriptor) is recorded in the
# milestone report as the runtime half of the mmap-only evidence.
#
# Usage: ./check-import-graph.sh [--self-test]   (run from the v4/go
# directory). --self-test copies the module into a private temporary
# directory, asserts the gate rejects every mutation form there, and exits
# nonzero on any miss. The self-test never writes to the reviewed tree:
# there is no startup sweep and no reserved file name.
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

# The AST content-transfer scanner. Built once per run into a private
# temp path; inner self-test runs reuse it via GATE_SCANNER_BIN (they run
# from a temp copy of the module, where the relative ../go-gate path does
# not exist, so the build is skipped whenever the binary is supplied).
scanner_bin=${GATE_SCANNER_BIN:-}
if [ -z "$scanner_bin" ]; then
	scanner_dir=$(cd "$PWD/../go-gate" && pwd)
	scanner_bin=$(mktemp /tmp/iprange-gatescan.XXXXXX)
	if ! go -C "$scanner_dir" build -o "$scanner_bin" .; then
		echo "gate scanner build failed"
		exit 1
	fi
fi

cleanup() {
	if [ -n "$scanner_bin" ] && [ -z "${GATE_SCANNER_BIN:-}" ]; then
		rm -r "$scanner_bin" 2>/dev/null || true
	fi
	if [ -n "${self_tree:-}" ]; then
		rm -r "$self_tree" 2>/dev/null || true
	fi
}
trap cleanup EXIT INT TERM

# per-package import list (every import on its own line; the first import is
# NOT swallowed — a join without the ImportPath prefix)
pkg_imports() {
	go list -f '{{join .Imports "\n"}}' "$1" 2>/dev/null
}

check() {
	pkg=$1
	allowed_prefix=$2
	content=$(pkg_imports "$pkg")
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

check "github.com/firehol/iprange/v4/go/internal/format" "github.com/firehol/iprange/v4/go/internal/format\$"
check "github.com/firehol/iprange/v4/go/internal/mapping" "github.com/firehol/iprange/v4/go/internal/\(format\|mapping\)"
check "github.com/firehol/iprange/v4/go/internal/reader" "github.com/firehol/iprange/v4/go/internal/\(format\|mapping\)"
# The module root (public facade) imports internal/format + internal/reader
# only; the legacy internal/exactv4 scalar aliases were deleted with the
# approved set.
check "github.com/firehol/iprange/v4/go" "github.com/firehol/iprange/v4/go/internal/\(format\|reader\)"

# The reader core is the synchronization-free zone: no sync, sync/atomic, or
# unsafe anywhere in its import closure.
for pkg in "github.com/firehol/iprange/v4/go/internal/format" \
		"github.com/firehol/iprange/v4/go/internal/reader"; do
	if printf '%s\n' "$(pkg_imports "$pkg")" | grep -qE '^(sync|sync/atomic|unsafe)$'; then
		echo "synchronization/unsafe violation: $pkg"
		fail=1
	fi
done

# AST content-transfer scan: banned imports/selectors and the *os.File
# capability surface, over every production file (all build contexts).
if ! "$scanner_bin" .; then
	echo "content-transfer violation in production sources"
	fail=1
fi

# Only the reader may hold the mapping, and only the reader core may be
# consumed by the facade. The check runs under every supported target so a
# build-tagged package that exists only on one GOOS/GOARCH cannot import
# internal packages unseen.
targets="linux/amd64 linux/386 linux/arm linux/arm64 linux/loong64 \
	darwin/amd64 darwin/arm64 freebsd/amd64 windows/amd64 windows/arm64"
for target in $targets; do
	GOOS=${target%/*} GOARCH=${target#*/} export GOOS GOARCH
	for pkg in $(go list ./... 2>/dev/null | grep -v '^github.com/firehol/iprange/v4/go$' \
			| grep -v '^github.com/firehol/iprange/v4/go/internal/format$' \
			| grep -v '^github.com/firehol/iprange/v4/go/internal/mapping$' \
			| grep -v '^github.com/firehol/iprange/v4/go/internal/reader$'); do
		case "$(pkg_imports "$pkg")" in
		*"github.com/firehol/iprange/v4/go/internal"*)
			echo "boundary violation: $pkg (target $target) imports internal packages"
			fail=1
			;;
		esac
	done
done

if [ "$self_test" -eq 1 ]; then
	# Durable mutation self-test: every transfer form below must make the
	# gate fail. The module is copied to a private temp tree; each mutation
	# is created, checked, and removed there, so the reviewed tree is never
	# modified and a miss points at exactly one form. No file name is
	# reserved: the gate must detect a gatemut_-named violation, not rely on
	# a sweep deleting it.
	self_tree=$(mktemp -d /tmp/iprange-gate-self.XXXXXX)
	mkdir -p "$self_tree/go"
	cp -a . "$self_tree/go"
	cd "$self_tree/go"

	muts=""
	cleanup_muts() {
		for m in $muts; do
			rm -r "$m" 2>/dev/null || true
		done
		muts=""
	}

	mutfail=0

	run_mut() {
		name=$1
		shift
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test MISS: mutation $name did not fail the gate"
			mutfail=1
		fi
		cleanup_muts
	}

	# Keep every mutation path in $muts so cleanup_muts removes exactly the
	# files of the current mutation, leaving the next mutation a clean tree.
	add_mut() {
		muts="$muts $1"
	}

	# --- 1: direct io.ReadAll call ---------------------------------------
	mkdir -p gatemut_readall
	add_mut gatemut_readall
	cat > gatemut_readall/mut.go <<'MUTEOF'
package gatemut_readall

import (
	"io"
	"os"
)

var file *os.File

func use() { _, _ = io.ReadAll(file) }
MUTEOF
	run_mut "direct io.ReadAll call"

	# --- 2: io.ReadAll function alias ------------------------------------
	mkdir -p gatemut_alias
	add_mut gatemut_alias
	cat > gatemut_alias/mut.go <<'MUTEOF'
package gatemut_alias

import (
	"io"
	"os"
)

var file *os.File

var rd = io.ReadAll

func use() { _, _ = rd(file) }
MUTEOF
	run_mut "io.ReadAll function alias"

	# --- 3: os.File.Read method value ------------------------------------
	mkdir -p gatemut_methodval
	add_mut gatemut_methodval
	cat > gatemut_methodval/mut.go <<'MUTEOF'
package gatemut_methodval

import "os"

var file *os.File

var m = file.Read

func use() { var b []byte; _, _ = m(b) }
MUTEOF
	run_mut "os.File.Read method value"

	# --- 4: os.File.Seek call --------------------------------------------
	mkdir -p gatemut_seek
	add_mut gatemut_seek
	cat > gatemut_seek/mut.go <<'MUTEOF'
package gatemut_seek

import "os"

var file *os.File

func use() { _, _ = file.Seek(0, 0) }
MUTEOF
	run_mut "os.File.Seek call"

	# --- 5: os.ReadFile in a new package directory -----------------------
	mkdir -p gatemut_newdir
	add_mut gatemut_newdir
	cat > gatemut_newdir/mut.go <<'MUTEOF'
package gatemut_newdir

import "os"

func read(p string) ([]byte, error) { return os.ReadFile(p) }
MUTEOF
	run_mut "os.ReadFile in a new package directory"

	# --- 6: unix.Readv descriptor read in the mapping owner --------------
	cat > internal/mapping/gatemut_readv.go <<'MUTEOF'
package mapping

import "golang.org/x/sys/unix"

func readv(fd int, b [][]byte) (int, error) { return unix.Readv(fd, b) }
MUTEOF
	add_mut internal/mapping/gatemut_readv.go
	run_mut "unix.Readv descriptor read in the mapping owner"

	# --- 7: bufio.NewReader(file).ReadByte ------------------------------
	mkdir -p gatemut_bufio
	add_mut gatemut_bufio
	cat > gatemut_bufio/mut.go <<'MUTEOF'
package gatemut_bufio

import (
	"bufio"
	"os"
)

var file *os.File

func use() (byte, error) { return bufio.NewReader(file).ReadByte() }
MUTEOF
	run_mut "bufio.NewReader(file).ReadByte"

	# --- 8: dot-imported os.ReadFile -------------------------------------
	mkdir -p gatemut_dotimport
	add_mut gatemut_dotimport
	cat > gatemut_dotimport/mut.go <<'MUTEOF'
package gatemut_dotimport

import . "os"

func read(p string) ([]byte, error) { return ReadFile(p) }
MUTEOF
	run_mut "dot-imported os.ReadFile"

	# --- 9: single-line bufio import with Peek ---------------------------
	cat > gatemut_singleline_bufio.go <<'MUTEOF'
package iprangedb

import "bufio"

var br *bufio.Reader

func peek() ([]byte, error) { return br.Peek(1) }
MUTEOF
	add_mut gatemut_singleline_bufio.go
	run_mut "single-line bufio import with Peek"

	# --- 10: aliased bufio import with Peek ------------------------------
	cat > gatemut_aliased_bufio.go <<'MUTEOF'
package iprangedb

import b "bufio"

var br *b.Reader

func peek() ([]byte, error) { return br.Peek(1) }
MUTEOF
	add_mut gatemut_aliased_bufio.go
	run_mut "aliased bufio import with Peek"

	# --- 11: windows-only package with os.ReadFile -----------------------
	mkdir -p gatemut_winfile
	add_mut gatemut_winfile
	cat > gatemut_winfile/mut.go <<'MUTEOF'
//go:build windows

package gatemut_winfile

import "os"

func read(p string) ([]byte, error) { return os.ReadFile(p) }
MUTEOF
	run_mut "windows-only package with os.ReadFile"

	# --- 12: fmt.Fscan over a file ----------------------------------------
	mkdir -p gatemut_fscan
	add_mut gatemut_fscan
	cat > gatemut_fscan/mut.go <<'MUTEOF'
package gatemut_fscan

import (
	"fmt"
	"os"
)

var f *os.File

func use(x any) (int, error) { return fmt.Fscan(f, x) }
MUTEOF
	run_mut "fmt.Fscan over a file"

	# --- 13: io.CopyN between files ---------------------------------------
	mkdir -p gatemut_copyn
	add_mut gatemut_copyn
	cat > gatemut_copyn/mut.go <<'MUTEOF'
package gatemut_copyn

import (
	"io"
	"os"
)

var f *os.File
var d *os.File

func use() { _, _ = io.CopyN(d, f, 10) }
MUTEOF
	run_mut "io.CopyN between files"

	# --- 14: reflection-invoked Read --------------------------------------
	mkdir -p gatemut_reflect
	add_mut gatemut_reflect
	cat > gatemut_reflect/mut.go <<'MUTEOF'
package gatemut_reflect

import (
	"os"
	"reflect"
)

var f *os.File

func use() { _ = reflect.ValueOf(f).MethodByName("Read").Call(nil) }
MUTEOF
	run_mut "reflection-invoked Read"

	# --- 15: raw unix.Syscall(SYS_READ) in the mapping owner -------------
	cat > internal/mapping/gatemut_rawsys.go <<'MUTEOF'
package mapping

import "golang.org/x/sys/unix"

func rawRead(fd int) (int, error) {
	n, _, e := unix.Syscall(unix.SYS_READ, uintptr(fd), 0, 0)
	return int(n), e
}
MUTEOF
	add_mut internal/mapping/gatemut_rawsys.go
	run_mut "raw unix.Syscall(SYS_READ) in the mapping owner"

	# --- 16: unix.CopyFileRange in the mapping owner ---------------------
	cat > internal/mapping/gatemut_cfr.go <<'MUTEOF'
package mapping

import "golang.org/x/sys/unix"

func copyRange(a, b, n int) (int, error) {
	return unix.CopyFileRange(a, nil, b, nil, n, 0)
}
MUTEOF
	add_mut internal/mapping/gatemut_cfr.go
	run_mut "unix.CopyFileRange in the mapping owner"

	# --- 17: forbidden transfer sharing a line with a tolerated call -----
	mkdir -p gatemut_exline
	add_mut gatemut_exline
	cat > gatemut_exline/mut.go <<'MUTEOF'
package gatemut_exline

import "os"

var f *os.File
var c = struct{ r *os.File }{f}

func use() {
	var b [1]byte
	_, _ = f.Read(b[:]); _, _ = c.r.Read(b[:]) // tolerated call on the same line must not hide the file read
}
MUTEOF
	run_mut "forbidden transfer sharing a line with a tolerated call"

	# --- 18: windows-only package importing internal/mapping -------------
	mkdir -p gatemut_winint
	add_mut gatemut_winint
	cat > gatemut_winint/mut.go <<'MUTEOF'
//go:build windows

package gatemut_winint

import "github.com/firehol/iprange/v4/go/internal/mapping"

var _ = mapping.Mapping{}
MUTEOF
	run_mut "windows-only package importing internal/mapping"

	# --- 19: encoding/json decoder over a file ---------------------------
	mkdir -p gatemut_decoder
	add_mut gatemut_decoder
	cat > gatemut_decoder/mut.go <<'MUTEOF'
package gatemut_decoder

import (
	"encoding/json"
	"os"
)

var f *os.File

func use() { var x any; _ = json.NewDecoder(f).Decode(&x) }
MUTEOF
	run_mut "encoding/json decoder over a file"

	# --- 20: os.File.WriteString -----------------------------------------
	mkdir -p gatemut_writestr
	add_mut gatemut_writestr
	cat > gatemut_writestr/mut.go <<'MUTEOF'
package gatemut_writestr

import "os"

var f *os.File

func use() { _, _ = f.WriteString("payload") }
MUTEOF
	run_mut "os.File.WriteString"

	# --- 21: nested transfer inside the tolerated call node --------------
	mkdir -p gatemut_nested
	add_mut gatemut_nested
	cat > gatemut_nested/mut.go <<'MUTEOF'
package gatemut_nested

import "os"

var f *os.File
var c = struct{ r *os.File }{f}

func use() {
	var b [1]byte
	_ = c.r.Read(f.Read(b[:])) // intentional textual probe (cannot typecheck: no
	// []byte-typed file-read expression exists); the nested transfer must
	// stay visible to the gate, not be blanked with the tolerated node
}
MUTEOF
	run_mut "forbidden transfer nested inside the tolerated call node"

	# --- 22: reflection Method(i).Call ------------------------------------
	mkdir -p gatemut_refmeth
	add_mut gatemut_refmeth
	cat > gatemut_refmeth/mut.go <<'MUTEOF'
package gatemut_refmeth

import (
	"os"
	"reflect"
)

var f *os.File

func use() { _ = reflect.ValueOf(f).Method(2).Call(nil) }
MUTEOF
	run_mut "reflection Method(i).Call"

	# --- 23: io.ReadFull over a file --------------------------------------
	mkdir -p gatemut_readfull
	add_mut gatemut_readfull
	cat > gatemut_readfull/mut.go <<'MUTEOF'
package gatemut_readfull

import (
	"io"
	"os"
)

var f *os.File

func use() { var b [10]byte; _, _ = io.ReadFull(f, b[:]) }
MUTEOF
	run_mut "io.ReadFull over a file"

	# --- 24: io.ReadAtLeast over a file -----------------------------------
	mkdir -p gatemut_readleast
	add_mut gatemut_readleast
	cat > gatemut_readleast/mut.go <<'MUTEOF'
package gatemut_readleast

import (
	"io"
	"os"
)

var f *os.File

func use() { var b [10]byte; _, _ = io.ReadAtLeast(f, b[:], 1) }
MUTEOF
	run_mut "io.ReadAtLeast over a file"

	# --- 25: log package writing to a file --------------------------------
	mkdir -p gatemut_logw
	add_mut gatemut_logw
	cat > gatemut_logw/mut.go <<'MUTEOF'
package gatemut_logw

import (
	"log"
	"os"
)

var f *os.File

func use() { log.New(f, "", 0).Println("payload") }
MUTEOF
	run_mut "log package writing to a file"

	# --- 26: flate.NewWriter over a file ----------------------------------
	mkdir -p gatemut_flatew
	add_mut gatemut_flatew
	cat > gatemut_flatew/mut.go <<'MUTEOF'
package gatemut_flatew

import (
	"compress/flate"
	"os"
)

var f *os.File

func use() { w, _ := flate.NewWriter(f, 6); w.Close() }
MUTEOF
	run_mut "flate.NewWriter over a file"

	# --- 27: transfer nested inside the io.ReadFull exemption node -------
	mkdir -p gatemut_rfshadow
	add_mut gatemut_rfshadow
	cat > gatemut_rfshadow/mut.go <<'MUTEOF'
package gatemut_rfshadow

import (
	"io"
	"os"
)

var f *os.File
var zr *os.File // in-memory-looking receiver name

func mv(n int, e error) []byte { var b [8]byte; _ = n; _ = e; return b[:] }

func use() {
	var b [8]byte
	_, _ = io.ReadFull(zr, mv(f.Read(b[:]))) // nested transfer must stay visible
}
MUTEOF
	run_mut "transfer nested inside the io.ReadFull exemption node"

	# --- 28: io.ReadFull over a file-backed flate reader -----------------
	mkdir -p gatemut_zrfile
	add_mut gatemut_zrfile
	cat > gatemut_zrfile/mut.go <<'MUTEOF'
package gatemut_zrfile

import (
	"compress/flate"
	"io"
	"os"
)

func use(f *os.File) {
	var b [8]byte
	zr := flate.NewReader(f) // file-backed inflater: must not be exempted
	_, _ = io.ReadFull(zr, b[:])
}
MUTEOF
	run_mut "io.ReadFull over a file-backed flate reader"

	# --- 29: file-backed c.r receiver -------------------------------------
	mkdir -p gatemut_crfile
	add_mut gatemut_crfile
	cat > gatemut_crfile/mut.go <<'MUTEOF'
package gatemut_crfile

import "os"

type T struct{ r *os.File }

func (c *T) use() {
	var b [1]byte
	_, _ = c.r.Read(b[:]) // file-backed receiver must not be exempted
}
MUTEOF
	run_mut "file-backed c.r receiver"

	# --- 30: file-backed zr/out reader with a different index shape ------
	mkdir -p gatemut_zrout
	add_mut gatemut_zrout
	cat > gatemut_zrout/mut.go <<'MUTEOF'
package gatemut_zrout

import (
	"compress/flate"
	"io"
	"os"
)

func use(f *os.File) {
	zr := flate.NewReader(f)
	var out [8]byte
	_, _ = io.ReadFull(zr, out[:]) // same names, different shape: must stay visible
}
MUTEOF
	run_mut "file-backed zr/out reader with a different index shape"

	# --- 31: selector split after the dot (method) ------------------------
	cat > internal/mapping/gatemut_splitmethod.go <<'MUTEOF'
package mapping

import "os"

func transferSplit(f *os.File, p []byte) (int, error) {
	return f.
		Read(p)
}
MUTEOF
	add_mut internal/mapping/gatemut_splitmethod.go
	run_mut "selector split after the dot (method)"

	# --- 32: selector split after the dot (package) -----------------------
	cat > internal/mapping/gatemut_splitpkg.go <<'MUTEOF'
package mapping

import (
	"io"
	"os"
)

func transferSplit(f *os.File) ([]byte, error) {
	return io.
		ReadAll(f)
}
MUTEOF
	add_mut internal/mapping/gatemut_splitpkg.go
	run_mut "selector split after the dot (package)"

	# --- 33: exact tolerated c.r.Read(p) text with a file-backed r -------
	cat > internal/mapping/gatemut_cr_exact.go <<'MUTEOF'
package mapping

import "os"

type reviewFileReader struct{ r *os.File }

func (c *reviewFileReader) transfer(p []byte) (int, error) {
	return c.r.Read(p)
}
MUTEOF
	add_mut internal/mapping/gatemut_cr_exact.go
	run_mut "exact tolerated c.r.Read(p) text with a file-backed r"

	# --- 34: exact tolerated io.ReadFull shape with zr *os.File ----------
	cat > internal/mapping/gatemut_rf_exact.go <<'MUTEOF'
package mapping

import (
	"io"
	"os"
)

type reviewMeta struct{ MetadataUncompressed uint64 }

func transferExact(zr *os.File, out []byte, meta reviewMeta) (int, error) {
	return io.ReadFull(zr, out[:int(meta.MetadataUncompressed)])
}
MUTEOF
	add_mut internal/mapping/gatemut_rf_exact.go
	run_mut "exact tolerated io.ReadFull shape with zr *os.File"

	# --- 35: compress/gzip.NewReader(file) + exact ReadFull shape --------
	cat > internal/mapping/gatemut_gzip.go <<'MUTEOF'
package mapping

import (
	"compress/gzip"
	"io"
	"os"
)

type reviewMeta struct{ MetadataUncompressed uint64 }

func transferGzip(f *os.File, out []byte, meta reviewMeta) (int, error) {
	zr, err := gzip.NewReader(f)
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	return io.ReadFull(zr, out[:int(meta.MetadataUncompressed)])
}
MUTEOF
	add_mut internal/mapping/gatemut_gzip.go
	run_mut "compress/gzip.NewReader(file) with the exact ReadFull shape"

	# --- 36: log/slog writer over a file ----------------------------------
	cat > internal/mapping/gatemut_slog.go <<'MUTEOF'
package mapping

import (
	"context"
	"log/slog"
	"os"
)

func transferSlog(f *os.File) error {
	h := slog.NewTextHandler(f, nil)
	return h.Handle(context.Background(), slog.Record{})
}
MUTEOF
	add_mut internal/mapping/gatemut_slog.go
	run_mut "log/slog.NewTextHandler over a file"

	# --- 37: runtime/trace writer over a file -----------------------------
	cat > internal/mapping/gatemut_trace.go <<'MUTEOF'
package mapping

import (
	"os"
	"runtime/trace"
)

func transferTrace(f *os.File) error {
	return trace.Start(f)
}
MUTEOF
	add_mut internal/mapping/gatemut_trace.go
	run_mut "runtime/trace.Start over a file"

	# --- 38: os.StartProcess with the artifact file attached --------------
	cat > internal/mapping/gatemut_startproc.go <<'MUTEOF'
package mapping

import "os"

func transferChild(path string, file *os.File) (*os.Process, error) {
	return os.StartProcess("/bin/cat", []string{"cat", path}, &os.ProcAttr{Files: []*os.File{file, file, file}})
}
MUTEOF
	add_mut internal/mapping/gatemut_startproc.go
	run_mut "os.StartProcess with the artifact file attached"

	# --- 39: gatemut_-named violation must be detected, not swept --------
	cat > internal/mapping/gatemut_hidden.go <<'MUTEOF'
package mapping

import "os"

func hidden(x *os.File) { var b [1]byte; _, _ = x.Read(b[:]) }
MUTEOF
	add_mut internal/mapping/gatemut_hidden.go
	run_mut "gatemut_-named file carrying a transfer"

	# --- 40: aliased os import must not dodge the producer taint --------
	cat > internal/mapping/gatemut_osalias.go <<'MUTEOF'
package mapping

import (
	fsp "os"
	"path/filepath"
)

func aliasProbe(path string) error {
	f, err := fsp.OpenFile(filepath.Clean(path), fsp.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Chdir() // unapproved file method: reachable only through the aliased producer taint
}
MUTEOF
	add_mut internal/mapping/gatemut_osalias.go
	run_mut "aliased os import dodging the producer taint"

	# --- 41: accessor-method *os.File return must keep the taint ----------
	cat > internal/mapping/gatemut_accessor.go <<'MUTEOF'
package mapping

import "os"

type reviewHolder struct{ f *os.File }

func (h *reviewHolder) file() *os.File { return h.f }

func accessorProbe() error {
	h := &reviewHolder{}
	return h.file().Chdir() // unapproved file method: reachable only through the accessor taint
}
MUTEOF
	add_mut internal/mapping/gatemut_accessor.go
	run_mut "accessor-method *os.File return"

	# --- 42: type-alias conversion of *os.File must keep the taint --------
	cat > internal/mapping/gatemut_aliasconv.go <<'MUTEOF'
package mapping

import "os"

type zrAlias = *os.File

func aliasConv(f *os.File) error {
	zr := zrAlias(f)
	return zr.Chdir() // alias conversion must not untaint the file
}
MUTEOF
	add_mut internal/mapping/gatemut_aliasconv.go
	run_mut "type-alias conversion of *os.File"

	# --- 43: type-alias parameter of *os.File must be tainted -------------
	cat > internal/mapping/gatemut_aliasparam.go <<'MUTEOF'
package mapping

import "os"

type zrAlias = *os.File

func aliasParam(zr zrAlias) error {
	return zr.Chdir() // aliased parameter type must still be file-tainted
}
MUTEOF
	add_mut internal/mapping/gatemut_aliasparam.go
	run_mut "type-alias parameter of *os.File"

	# --- 44: file-carrying struct built before the call -------------------
	cat > internal/mapping/gatemut_procattr.go <<'MUTEOF'
package mapping

import "os"

func leakStart() {
	f, _ := os.Open("/etc/hosts")
	pa := &os.ProcAttr{Files: []*os.File{f}}
	_, _ = os.StartProcess("/bin/cat", []string{"cat", "/dev/null"}, pa)
}
MUTEOF
	add_mut internal/mapping/gatemut_procattr.go
	run_mut "os.StartProcess with a separately built ProcAttr"

	# --- 45: os.Pipe file pair must be tainted ----------------------------
	cat > internal/mapping/gatemut_ospipe.go <<'MUTEOF'
package mapping

import "os"

func pipeProbe() error {
	_, w, err := os.Pipe()
	if err != nil {
		return err
	}
	return w.Chdir() // pipe file: reachable only through the os.Pipe producer taint
}
MUTEOF
	add_mut internal/mapping/gatemut_ospipe.go
	run_mut "os.Pipe producer taint"

	# --- 47: struct-field stored file behind the inflater exemption ------
	# A file parked in a struct field (declared or alias-typed) must stay
	# tainted when it shadows the exempted in-memory inflater reader.
	cat > internal/reader/gatemut_fieldbox.go <<'MUTEOF'
package reader

import "os"

type gatemutBox struct{ r *os.File }

var gatemutBoxVal gatemutBox

func init() { gatemutBoxVal.r, _, _ = os.Pipe() }
MUTEOF
	add_mut internal/reader/gatemut_fieldbox.go
	cp internal/reader/metadata.go "$self_tree/meta47.orig"
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\tzr = gatemutBoxVal.r"; next } print }' internal/reader/metadata.go > "$self_tree/meta47.new" && mv "$self_tree/meta47.new" internal/reader/metadata.go
	if ! grep -q "^$(printf '\t')zr = gatemutBoxVal.r" internal/reader/metadata.go; then
		echo "self-test ERROR: form 47 insert did not take"
		mutfail=1
	fi
	run_mut "struct-field stored file shadowing the inflater exemption"
	cp "$self_tree/meta47.orig" internal/reader/metadata.go

	# --- 48: channel-transported file behind the inflater exemption ------
	# A file sent through a chan *os.File must stay tainted when a receive
	# shadows the exempted in-memory inflater reader.
	cat > internal/reader/gatemut_chanbox.go <<'MUTEOF'
package reader

import "os"

var gatemutCh = make(chan *os.File)

func init() {
	r, _, _ := os.Pipe()
	gatemutCh <- r
}
MUTEOF
	add_mut internal/reader/gatemut_chanbox.go
	cp internal/reader/metadata.go "$self_tree/meta48.orig"
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\tzr = <-gatemutCh"; next } print }' internal/reader/metadata.go > "$self_tree/meta48.new" && mv "$self_tree/meta48.new" internal/reader/metadata.go
	if ! grep -q "^$(printf '\t')zr = <-gatemutCh" internal/reader/metadata.go; then
		echo "self-test ERROR: form 48 insert did not take"
		mutfail=1
	fi
	run_mut "channel-transported file shadowing the inflater exemption"
	cp "$self_tree/meta48.orig" internal/reader/metadata.go

	# --- 50: inline FuncLit returning *os.File behind the exemption ------
	# A closure that opens a file and returns it must keep the taint when
	# its call result shadows the exempted in-memory inflater reader.
	cat > internal/reader/gatemut_funclit.go <<'MUTEOF'
package reader
MUTEOF
	add_mut internal/reader/gatemut_funclit.go
	cp internal/reader/metadata.go "$self_tree/meta50.orig"
	INS='zr = func() *os.File { f, _ := os.Open("/dev/null"); return f }()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta50.new" && mv "$self_tree/meta50.new" internal/reader/metadata.go
	if grep -Fq 'zr = func() *os.File' internal/reader/metadata.go; then
		run_mut "inline FuncLit file behind the inflater exemption"
	else
		echo "self-test ERROR: form 50 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta50.orig" internal/reader/metadata.go

	# --- 51: type assertion to *os.File behind the exemption -------------
	# A file hidden in an interface field, recovered with a type
	# assertion, must keep the taint at the exempted inflater reader.
	cat > internal/reader/gatemut_assert.go <<'MUTEOF'
package reader

import "os"

type zrBox struct{ r any }

var zb zrBox

func init() {
	w, _, _ := os.Pipe()
	zb.r = w
}
MUTEOF
	add_mut internal/reader/gatemut_assert.go
	cp internal/reader/metadata.go "$self_tree/meta51.orig"
	INS='zr = zb.r.(*os.File)'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta51.new" && mv "$self_tree/meta51.new" internal/reader/metadata.go
	if grep -Fq 'zb.r.(*os.File)' internal/reader/metadata.go; then
		run_mut "type-assertion file behind the inflater exemption"
	else
		echo "self-test ERROR: form 51 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta51.orig" internal/reader/metadata.go

	# --- 52: two-hop channel transport behind the exemption --------------
	# A file moved through chan chan *os.File must stay tainted across
	# both receives at the exempted inflater reader.
	cat > internal/reader/gatemut_chan2.go <<'MUTEOF'
package reader

import "os"

var outer = make(chan chan *os.File)

func init() {
	inner := make(chan *os.File)
	w, _, _ := os.Pipe()
	inner <- w
	outer <- inner
}
MUTEOF
	add_mut internal/reader/gatemut_chan2.go
	cp internal/reader/metadata.go "$self_tree/meta52.orig"
	INS='inner2 := <-outer; zr = <-inner2'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta52.new" && mv "$self_tree/meta52.new" internal/reader/metadata.go
	if grep -Fq 'zr = <-inner2' internal/reader/metadata.go; then
		run_mut "two-hop channel file behind the inflater exemption"
	else
		echo "self-test ERROR: form 52 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta52.orig" internal/reader/metadata.go

	# --- 53: single-variable channel range must taint the element --------
	# for z := range ch on a chan *os.File puts the file in the Key slot;
	# an unapproved method on it must be flagged.
	cat > internal/reader/gatemut_chanrange.go <<'MUTEOF'
package reader

import "os"

var ch53 = make(chan *os.File)

func init() {
	w, _, _ := os.Pipe()
	ch53 <- w
}

func rangeProbe() error {
	for z := range ch53 {
		return z.Chdir() // unapproved method on a ranged channel element
	}
	return nil
}
MUTEOF
	add_mut internal/reader/gatemut_chanrange.go
	run_mut "single-variable channel range element"
	# --- 54: parenthesized producer call behind the exemption -----------
	# (getFile)() looks like a plain call to the parser; the producer taint
	# must survive the paren wrapper.
	cat > internal/reader/gatemut_parensel.go <<'MUTEOF'
package reader

import "os"

func getFile() *os.File { f, _ := os.Open("/dev/null"); return f }
MUTEOF
	add_mut internal/reader/gatemut_parensel.go
	cp internal/reader/metadata.go "$self_tree/meta54.orig"
	INS='zr = (getFile)()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta54.new" && mv "$self_tree/meta54.new" internal/reader/metadata.go
	if grep -Fq 'zr = (getFile)()' internal/reader/metadata.go; then
		run_mut "parenthesized producer call behind the inflater exemption"
	else
		echo "self-test ERROR: form 54 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta54.orig" internal/reader/metadata.go

	# --- 55: parenthesized inline FuncLit behind the exemption -----------
	# Wrapping a closure in parens must not hide its file-returning body.
	cat > internal/reader/gatemut_parenlit.go <<'MUTEOF'
package reader
MUTEOF
	add_mut internal/reader/gatemut_parenlit.go
	cp internal/reader/metadata.go "$self_tree/meta55.orig"
	INS='zr = (func() *os.File { f, _ := os.Open("/dev/null"); return f })()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta55.new" && mv "$self_tree/meta55.new" internal/reader/metadata.go
	if grep -Fq 'zr = (func() *os.File' internal/reader/metadata.go; then
		run_mut "parenthesized FuncLit file behind the inflater exemption"
	else
		echo "self-test ERROR: form 55 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta55.orig" internal/reader/metadata.go

	# --- 56: interface-typed closure returning a file --------------------
	# A closure whose result type is io.ReadCloser still returns a file
	# from its body; the body walk must keep the taint.
	cat > internal/reader/gatemut_ifacelit.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)
MUTEOF
	add_mut internal/reader/gatemut_ifacelit.go
	cp internal/reader/metadata.go "$self_tree/meta56.orig"
	INS='zr = func() io.ReadCloser { f, _ := os.Open("/dev/null"); return f }()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta56.new" && mv "$self_tree/meta56.new" internal/reader/metadata.go
	if grep -Fq 'zr = func() io.ReadCloser' internal/reader/metadata.go; then
		run_mut "interface-typed FuncLit file behind the inflater exemption"
	else
		echo "self-test ERROR: form 56 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta56.orig" internal/reader/metadata.go

	# --- 57: alias-typed function variable producing a file --------------
	# type fileFn = func() *os.File; var getFat fileFn must resolve as a
	# file producer when the alias is a type-only package declaration.
	cat > internal/reader/gatemut_aliasfn.go <<'MUTEOF'
package reader

import "os"

type fileFn = func() *os.File

var getFat fileFn

func init() {
	getFat = func() *os.File { f, _ := os.Open("/dev/null"); return f }
}
MUTEOF
	add_mut internal/reader/gatemut_aliasfn.go
	cp internal/reader/metadata.go "$self_tree/meta57.orig"
	INS='zr = getFat()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta57.new" && mv "$self_tree/meta57.new" internal/reader/metadata.go
	if grep -Fq 'zr = getFat()' internal/reader/metadata.go; then
		run_mut "alias-typed function variable producing a file"
	else
		echo "self-test ERROR: form 57 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta57.orig" internal/reader/metadata.go

	# --- 58: type-switch bound file behind the exemption -----------------
	# A file hidden in an any-typed package var and recovered through a
	# type-switch case must keep the taint at the exempted inflater.
	cat > internal/reader/gatemut_typeswitch.go <<'MUTEOF'
package reader

import "os"

var anyFile2 any

func init() {
	w, _, _ := os.Pipe()
	anyFile2 = w
}
MUTEOF
	add_mut internal/reader/gatemut_typeswitch.go
	cp internal/reader/metadata.go "$self_tree/meta58.orig"
	INS='switch zv := anyFile2.(type) { case *os.File: zr = zv }'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta58.new" && mv "$self_tree/meta58.new" internal/reader/metadata.go
	if grep -Fq 'case *os.File: zr = zv' internal/reader/metadata.go; then
		run_mut "type-switch bound file behind the inflater exemption"
	else
		echo "self-test ERROR: form 58 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta58.orig" internal/reader/metadata.go

	# --- 59: benign parenthesized call must pass (no false positive) -----
	# Identical shape to form 54 but the callee returns int: the scanner
	# must not flag the shadow when nothing file-typed is involved.
	cat > internal/reader/gatemut_benignpar.go <<'MUTEOF'
package reader

func getInt() int { return 1 }
MUTEOF
	add_mut internal/reader/gatemut_benignpar.go
	cp internal/reader/metadata.go "$self_tree/meta59.orig"
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\tzr = (getInt)()"; next } print }' internal/reader/metadata.go > "$self_tree/meta59.new" && mv "$self_tree/meta59.new" internal/reader/metadata.go
	if grep -Fq 'zr = (getInt)()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign parenthesized call passes the gate"
		else
			echo "self-test MISS: benign parenthesized call failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 59 insert did not take"
		mutfail=1
	fi
	cleanup_muts
	cp "$self_tree/meta59.orig" internal/reader/metadata.go


	# --- 60: defined func type variable producing a file -----------------
	# type F func() *os.File (a defined type, not a type alias) must
	# resolve as a file producer for package vars and parameters.
	cat > internal/reader/gatemut_deffn.go <<'MUTEOF'
package reader

import "os"

type fileFn3 func() *os.File

var getDef2 fileFn3

func init() {
	getDef2 = func() *os.File { f, _ := os.Open("/dev/null"); return f }
}
MUTEOF
	add_mut internal/reader/gatemut_deffn.go
	cp internal/reader/metadata.go "$self_tree/meta60.orig"
	INS='zr = getDef2()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta60.new" && mv "$self_tree/meta60.new" internal/reader/metadata.go
	if grep -Fq 'zr = getDef2()' internal/reader/metadata.go; then
		run_mut "defined func type variable producing a file"
	else
		echo "self-test ERROR: form 60 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta60.orig" internal/reader/metadata.go

	# --- 61: func-valued return through a same-package helper ------------
	# A helper returning a func() *os.File (which itself may come from a
	# defined func type) must keep the producer taint across the call.
	cat > internal/reader/gatemut_funcret.go <<'MUTEOF'
package reader

import "os"

type fileFn5 func() *os.File

func useDef2(g fileFn5) fileFn5 { return g }

var getDef3 fileFn5

func init() {
	getDef3 = func() *os.File { f, _ := os.Open("/dev/null"); return f }
}
MUTEOF
	add_mut internal/reader/gatemut_funcret.go
	cp internal/reader/metadata.go "$self_tree/meta61.orig"
	INS='g := useDef2(getDef3)
	h := g()
	zr = h'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta61.new" && mv "$self_tree/meta61.new" internal/reader/metadata.go
	if grep -Fq 'zr = h' internal/reader/metadata.go; then
		run_mut "func-valued return through a same-package helper"
	else
		echo "self-test ERROR: form 61 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta61.orig" internal/reader/metadata.go

	# --- 62: type-switch bound defined-func-type case --------------------
	# switch v := x.(type) { case fileFn4: zr = v() } binds v to a
	# file-producing func type; the bound name must enter funcFile so the
	# call result stays tainted.
	cat > internal/reader/gatemut_tsfunc.go <<'MUTEOF'
package reader

import "os"

type fileFn4 func() *os.File

var anyFn any

func init() {
	anyFn = func() *os.File { f, _ := os.Open("/dev/null"); return f }
}
MUTEOF
	add_mut internal/reader/gatemut_tsfunc.go
	cp internal/reader/metadata.go "$self_tree/meta62.orig"
	INS='switch v := anyFn.(type) { case fileFn4: zr = v() }'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta62.new" && mv "$self_tree/meta62.new" internal/reader/metadata.go
	if grep -Fq 'zr = v()' internal/reader/metadata.go; then
		run_mut "type-switch bound defined-func-type case"
	else
		echo "self-test ERROR: form 62 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta62.orig" internal/reader/metadata.go

	# --- 63: benign defined func type returning a reader must pass -------
	# Identical registration shape to form 60 but the func returns
	# *bytes.Reader: no *os.File anywhere, the gate must stay silent.
	cat > internal/reader/gatemut_benignfn.go <<'MUTEOF'
package reader

import "bytes"

type brFn func() *bytes.Reader

var getBR brFn

func init() { getBR = func() *bytes.Reader { return bytes.NewReader(nil) } }
MUTEOF
	add_mut internal/reader/gatemut_benignfn.go
	cp internal/reader/metadata.go "$self_tree/meta63.orig"
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\tzr = getBR()"; next } print }' internal/reader/metadata.go > "$self_tree/meta63.new" && mv "$self_tree/meta63.new" internal/reader/metadata.go
	if grep -Fq 'zr = getBR()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign defined func type passes the gate"
		else
			echo "self-test MISS: benign defined func type failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 63 insert did not take"
		mutfail=1
	fi
	cleanup_muts
	cp "$self_tree/meta63.orig" internal/reader/metadata.go

	# --- 64: method returning a defined func type (single hop) ----------
	# rf := zb.mk(); zr = rf(): the receiver at the call site is the var
	# name, the method table is keyed by the struct name; the producer
	# taint must survive the method boundary.
	cat > internal/reader/gatemut_methfn.go <<'MUTEOF'
package reader

import "os"

type fileFn6 func() *os.File

type zbox struct{}

var zb zbox

func (z *zbox) mk() fileFn6 { return func() *os.File { f, _ := os.Open("/dev/null"); return f } }
MUTEOF
	add_mut internal/reader/gatemut_methfn.go
	cp internal/reader/metadata.go "$self_tree/meta64.orig"
	INS='rf := zb.mk()
	zr = rf()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta64.new" && mv "$self_tree/meta64.new" internal/reader/metadata.go
	if grep -Fq 'zr = rf()' internal/reader/metadata.go; then
		run_mut "method returning a defined func type"
	else
		echo "self-test ERROR: form 64 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta64.orig" internal/reader/metadata.go

	# --- 65: method func-valued double call ---------------------------------
	# zr = zb.mk()(): the callee is itself a call whose value is a func
	# returning *os.File; the outer call yields the file.
	cat > internal/reader/gatemut_methdbl.go <<'MUTEOF'
package reader

import "os"

type fileFn7 func() *os.File

type ybox struct{}

var yb ybox

func (y *ybox) mk2() fileFn7 { return func() *os.File { f, _ := os.Open("/dev/null"); return f } }
MUTEOF
	add_mut internal/reader/gatemut_methdbl.go
	cp internal/reader/metadata.go "$self_tree/meta65.orig"
	INS='zr = yb.mk2()()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta65.new" && mv "$self_tree/meta65.new" internal/reader/metadata.go
	if grep -Fq 'zr = yb.mk2()()' internal/reader/metadata.go; then
		run_mut "method func-valued double call"
	else
		echo "self-test ERROR: form 65 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta65.orig" internal/reader/metadata.go

	# --- 66: same-package helper func-valued double call --------------------
	# zr = useDef2(getDef3)(): a helper returns a func() *os.File and the
	# outer call invokes it directly.
	cat > internal/reader/gatemut_funcdbl.go <<'MUTEOF'
package reader

import "os"

type fileFn8 func() *os.File

func useDef4(g fileFn8) fileFn8 { return g }

var getDef4 fileFn8

func init() {
	getDef4 = func() *os.File { f, _ := os.Open("/dev/null"); return f }
}
MUTEOF
	add_mut internal/reader/gatemut_funcdbl.go
	cp internal/reader/metadata.go "$self_tree/meta66.orig"
	INS='zr = useDef4(getDef4)()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta66.new" && mv "$self_tree/meta66.new" internal/reader/metadata.go
	if grep -Fq 'zr = useDef4(getDef4)()' internal/reader/metadata.go; then
		run_mut "same-package helper func-valued double call"
	else
		echo "self-test ERROR: form 66 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta66.orig" internal/reader/metadata.go

	# --- 67: benign method func-valued double call must pass ------------
	# Identical shape to form 65 but the func returns int: the gate must
	# not flag the double call when no *os.File is involved.
	cat > internal/reader/gatemut_benignmeth.go <<'MUTEOF'
package reader

type intFn2 func() int

type qbox struct{}

var qb qbox

func (q *qbox) mk3() intFn2 { return func() int { return 1 } }
MUTEOF
	add_mut internal/reader/gatemut_benignmeth.go
	cp internal/reader/metadata.go "$self_tree/meta67.orig"
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\tzr = qb.mk3()()"; next } print }' internal/reader/metadata.go > "$self_tree/meta67.new" && mv "$self_tree/meta67.new" internal/reader/metadata.go
	if grep -Fq 'zr = qb.mk3()()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign method double call passes the gate"
		else
			echo "self-test MISS: benign method double call failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 67 insert did not take"
		mutfail=1
	fi
	cleanup_muts
	cp "$self_tree/meta67.orig" internal/reader/metadata.go

	# --- 68: func value stored in a struct field ---------------------------
	# hb.fn where the field type is a func() *os.File: the call must be a
	# file producer even though the receiver is a struct instance.
	cat > internal/reader/gatemut_fnfield.go <<'MUTEOF'
package reader

import "os"

type fileFnA func() *os.File

type fnBox struct{ fn fileFnA }

var hb fnBox

func init() {
	hb.fn = func() *os.File { f, _ := os.Open("/dev/null"); return f }
}
MUTEOF
	add_mut internal/reader/gatemut_fnfield.go
	cp internal/reader/metadata.go "$self_tree/meta68.orig"
	INS='zr = hb.fn()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta68.new" && mv "$self_tree/meta68.new" internal/reader/metadata.go
	if grep -Fq 'zr = hb.fn()' internal/reader/metadata.go; then
		run_mut "func value stored in a struct field"
	else
		echo "self-test ERROR: form 68 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta68.orig" internal/reader/metadata.go

	# --- 69: chan of func() *os.File --------------------------------------
	# A receive from a channel whose element is a file-producer func must
	# taint the received name as a func so its call is a producer.
	cat > internal/reader/gatemut_chanfunc.go <<'MUTEOF'
package reader

import "os"

type fileFnB func() *os.File

var fnCh = make(chan fileFnB)

func init() {
	fnCh <- func() *os.File { f, _ := os.Open("/dev/null"); return f }
}
MUTEOF
	add_mut internal/reader/gatemut_chanfunc.go
	cp internal/reader/metadata.go "$self_tree/meta69.orig"
	INS='got := <-fnCh
	zr = got()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta69.new" && mv "$self_tree/meta69.new" internal/reader/metadata.go
	if grep -Fq 'zr = got()' internal/reader/metadata.go; then
		run_mut "chan of func type receive and call"
	else
		echo "self-test ERROR: form 69 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta69.orig" internal/reader/metadata.go

	# --- 70: any-erased func return asserted and called --------------------
	# (getFn().(func() *os.File))(): the asserted type is a func producing
	# a file; the outer call yields the file.
	cat > internal/reader/gatemut_anyfunc.go <<'MUTEOF'
package reader

import "os"

func getFn() any { return func() *os.File { f, _ := os.Open("/dev/null"); return f } }
MUTEOF
	add_mut internal/reader/gatemut_anyfunc.go
	cp internal/reader/metadata.go "$self_tree/meta70.orig"
	INS='zr = (getFn().(func() *os.File))()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta70.new" && mv "$self_tree/meta70.new" internal/reader/metadata.go
	if grep -Fq 'zr = (getFn().(func() *os.File))()' internal/reader/metadata.go; then
		run_mut "any-erased func return asserted and called"
	else
		echo "self-test ERROR: form 70 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta70.orig" internal/reader/metadata.go

	# --- 71: os.Stdout through an interface closure -------------------------
	# A closure returning os.Stdout behind io.ReadCloser must be a file
	# producer; the file reaches io.ReadAll and must be flagged.
	cat > internal/reader/gatemut_stdout.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

func useStdout3() {
	_, _ = io.ReadAll(func() io.ReadCloser { return os.Stdout }())
}
MUTEOF
	add_mut internal/reader/gatemut_stdout.go
	run_mut "os.Stdout through an interface closure"

	# --- 72: benign chan of func() int must pass ---------------------------
	# Identical shape to form 69 but the func returns int: no *os.File
	# anywhere, the gate must stay silent.
	cat > internal/reader/gatemut_benignchanfn.go <<'MUTEOF'
package reader

type intFn3 func() int

var intCh = make(chan intFn3)

func init() { intCh <- func() int { return 3 } }

func useChanInt() int {
	got := <-intCh
	return got()
}
MUTEOF
	add_mut internal/reader/gatemut_benignchanfn.go
	if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
		echo "self-test OK: benign chan of func() int passes the gate"
	else
		echo "self-test MISS: benign chan of func() int failed the gate (false positive)"
		mutfail=1
	fi

	# --- 73: nested struct-field func value ---------------------------------
	# nh.inner.fn() where inner is a struct field whose own field type is
	# func() *os.File: the field chain must resolve to the innermost
	# struct before the producer check.
	cat > internal/reader/gatemut_nestedfn.go <<'MUTEOF'
package reader

import "os"

type fileFnC func() *os.File

type nestI struct{ fn fileFnC }

type nestH struct{ inner nestI }

var nh nestH

func init() {
	nh.inner.fn = func() *os.File { f, _ := os.Open("/dev/null"); return f }
}
MUTEOF
	add_mut internal/reader/gatemut_nestedfn.go
	cp internal/reader/metadata.go "$self_tree/meta73.orig"
	INS='zr = nh.inner.fn()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta73.new" && mv "$self_tree/meta73.new" internal/reader/metadata.go
	if grep -Fq 'zr = nh.inner.fn()' internal/reader/metadata.go; then
		run_mut "nested struct-field func value"
	else
		echo "self-test ERROR: form 73 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta73.orig" internal/reader/metadata.go

	# --- 74: named interface-typed helper returning a tainted file ---------
	# A declared func with an io.ReadCloser result whose body returns a
	# pipe must be a producer at its call site.
	cat > internal/reader/gatemut_namedfn.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

func getNamed() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}
MUTEOF
	add_mut internal/reader/gatemut_namedfn.go
	cp internal/reader/metadata.go "$self_tree/meta74.orig"
	INS='zr = getNamed()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta74.new" && mv "$self_tree/meta74.new" internal/reader/metadata.go
	if grep -Fq 'zr = getNamed()' internal/reader/metadata.go; then
		run_mut "named interface-typed helper returning a tainted file"
	else
		echo "self-test ERROR: form 74 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta74.orig" internal/reader/metadata.go

	# --- 75: named helper returning os.Stdout -------------------------------
	# The same named-helper producer rule must see the os.Stdout singleton.
	cat > internal/reader/gatemut_namedstd.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

func getStd() io.ReadCloser { return os.Stdout }
MUTEOF
	add_mut internal/reader/gatemut_namedstd.go
	cp internal/reader/metadata.go "$self_tree/meta75.orig"
	INS='zr = getStd()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta75.new" && mv "$self_tree/meta75.new" internal/reader/metadata.go
	if grep -Fq 'zr = getStd()' internal/reader/metadata.go; then
		run_mut "named helper returning os.Stdout"
	else
		echo "self-test ERROR: form 75 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta75.orig" internal/reader/metadata.go

	# --- 76: chan of func through a same-package helper --------------------
	# A helper returning the chan itself must keep the chan-of-func taint.
	cat > internal/reader/gatemut_chanpass.go <<'MUTEOF'
package reader

import "os"

type fileFnD func() *os.File

var fnCh3 = make(chan fileFnD)

func init() {
	fnCh3 <- func() *os.File { f, _ := os.Open("/dev/null"); return f }
}

func passFn(ch chan fileFnD) chan fileFnD { return ch }
MUTEOF
	add_mut internal/reader/gatemut_chanpass.go
	cp internal/reader/metadata.go "$self_tree/meta76.orig"
	INS='got := <-passFn(fnCh3)
	zr = got()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta76.new" && mv "$self_tree/meta76.new" internal/reader/metadata.go
	if grep -Fq 'zr = got()' internal/reader/metadata.go; then
		run_mut "chan of func through a same-package helper"
	else
		echo "self-test ERROR: form 76 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta76.orig" internal/reader/metadata.go

	# --- 77: benign named interface-typed helper must pass -----------------
	# Identical shape to form 74 but the body returns a bytes.Reader
	# wrapper (compiling shape for the io.ReadCloser slot): no *os.File
	# anywhere, the gate must stay silent.
	cat > internal/reader/gatemut_benignnamed.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type brCloser struct{ *bytes.Reader }

func (b *brCloser) Close() error { return nil }

func getBR4() io.ReadCloser { return &brCloser{bytes.NewReader(nil)} }
MUTEOF
	add_mut internal/reader/gatemut_benignnamed.go
	cp internal/reader/metadata.go "$self_tree/meta77.orig"
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\tzr = getBR4()"; next } print }' internal/reader/metadata.go > "$self_tree/meta77.new" && mv "$self_tree/meta77.new" internal/reader/metadata.go
	if grep -Fq 'zr = getBR4()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign named interface-typed helper passes the gate"
		else
			echo "self-test MISS: benign named interface-typed helper failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 77 insert did not take"
		mutfail=1
	fi
	cleanup_muts
	cp "$self_tree/meta77.orig" internal/reader/metadata.go

	# --- 78: named method returning a tainted file --------------------------
	# mb.named() where the method body returns os.Pipe: the receiver
	# struct must be registered as a producer through retMethods.
	cat > internal/reader/gatemut_namedmeth.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type mbox struct{}

var mb mbox

func (m *mbox) named() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}
MUTEOF
	add_mut internal/reader/gatemut_namedmeth.go
	cp internal/reader/metadata.go "$self_tree/meta78.orig"
	INS='zr = mb.named()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta78.new" && mv "$self_tree/meta78.new" internal/reader/metadata.go
	if grep -Fq 'zr = mb.named()' internal/reader/metadata.go; then
		run_mut "named method returning a tainted file"
	else
		echo "self-test ERROR: form 78 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta78.orig" internal/reader/metadata.go

	# --- 79: named method returning os.Stdout --------------------------------
	# The retMethods rule must see the os.Stdout singleton through the
	# method body as well.
	cat > internal/reader/gatemut_namedmethstd.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type mbox2 struct{}

var mb2 mbox2

func (m *mbox2) namedstd() io.ReadCloser { return os.Stdout }
MUTEOF
	add_mut internal/reader/gatemut_namedmethstd.go
	cp internal/reader/metadata.go "$self_tree/meta79.orig"
	INS='zr = mb2.namedstd()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta79.new" && mv "$self_tree/meta79.new" internal/reader/metadata.go
	if grep -Fq 'zr = mb2.namedstd()' internal/reader/metadata.go; then
		run_mut "named method returning os.Stdout"
	else
		echo "self-test ERROR: form 79 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta79.orig" internal/reader/metadata.go

	# --- 80: deep method chain returning a tainted file ----------------------
	# deep() -> mid() -> os.Pipe: the fixpoint pre-scan must compose
	# method producers across the chain.
	cat > internal/reader/gatemut_deepmeth.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type hbox struct{}

var hb2 hbox

func (h *hbox) deep() io.ReadCloser {
	return h.mid()
}

func (h *hbox) mid() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}
MUTEOF
	add_mut internal/reader/gatemut_deepmeth.go
	cp internal/reader/metadata.go "$self_tree/meta80.orig"
	INS='zr = hb2.deep()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta80.new" && mv "$self_tree/meta80.new" internal/reader/metadata.go
	if grep -Fq 'zr = hb2.deep()' internal/reader/metadata.go; then
		run_mut "deep method chain returning a tainted file"
	else
		echo "self-test ERROR: form 80 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta80.orig" internal/reader/metadata.go

	# --- 81: benign named method must pass ------------------------------------
	# Identical shape to form 78 but the method returns a bytes.Reader
	# wrapper: the gate must stay silent.
	cat > internal/reader/gatemut_benignmeth2.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rbox struct{}

var rb rbox

func (r *rbox) named() io.ReadCloser { return &brCloser{bytes.NewReader(nil)} }
MUTEOF
	add_mut internal/reader/gatemut_benignmeth2.go
	cp internal/reader/metadata.go "$self_tree/meta81.orig"
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\tzr = rb.named()"; next } print }' internal/reader/metadata.go > "$self_tree/meta81.new" && mv "$self_tree/meta81.new" internal/reader/metadata.go
	if grep -Fq 'zr = rb.named()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign named method passes the gate"
		else
			echo "self-test MISS: benign named method failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 81 insert did not take"
		mutfail=1
	fi
	cleanup_muts
	cp "$self_tree/meta81.orig" internal/reader/metadata.go

	# --- 82: nested method receiver returning a tainted file ----------------
	# mhv.inner.mk()() walks a receiver field chain (mholder.inner of
	# type minner) before selecting the method; the scanner must resolve
	# the nested instance, not only a plain receiver identifier.
	cat > internal/reader/gatemut_nestedmethrecv.go <<'MUTEOF'
package reader

import "os"

type fileFnF func() *os.File

type minner struct{}

var mh minner

type mholder struct{ inner minner }

var mhv mholder

func (m *minner) mk() fileFnF { return func() *os.File { f, _ := os.Open("/dev/null"); return f } }
MUTEOF
	add_mut internal/reader/gatemut_nestedmethrecv.go
	cp internal/reader/metadata.go "$self_tree/meta82.orig"
	INS='zr = mhv.inner.mk()()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta82.new" && mv "$self_tree/meta82.new" internal/reader/metadata.go
	if grep -Fq 'zr = mhv.inner.mk()()' internal/reader/metadata.go; then
		run_mut "nested method receiver returning a tainted file"
	else
		echo "self-test ERROR: form 82 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta82.orig" internal/reader/metadata.go

	# --- 83: benign nested method must pass ----------------------------------
	# Identical shape to form 82 but the method returns a bytes.Reader
	# wrapper: the gate must stay silent.
	cat > internal/reader/gatemut_benignnestedmeth.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type inbox2 struct{}

var ib2 inbox2

type iholder struct{ inner inbox2 }

var ihv iholder

func (i *inbox2) named() io.ReadCloser { return &brCloser{bytes.NewReader(nil)} }
MUTEOF
	add_mut internal/reader/gatemut_benignnestedmeth.go
	cp internal/reader/metadata.go "$self_tree/meta83.orig"
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\tzr = ihv.inner.named()"; next } print }' internal/reader/metadata.go > "$self_tree/meta83.new" && mv "$self_tree/meta83.new" internal/reader/metadata.go
	if grep -Fq 'zr = ihv.inner.named()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign nested method passes the gate"
		else
			echo "self-test MISS: benign nested method failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 83 insert did not take"
		mutfail=1
	fi
	cleanup_muts
	cp "$self_tree/meta83.orig" internal/reader/metadata.go

	# --- 84: method value bound to a variable, called once ----------------
	# fn := m.get with m.get a file producer (io.ReadCloser behind an
	# interface); calling fn() must be seen as producing the file.
	cat > internal/reader/gatemut_methodval.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gm84 struct{}

var gm84v gm84

func (g *gm84) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}
MUTEOF
	add_mut internal/reader/gatemut_methodval.go
	cp internal/reader/metadata.go "$self_tree/meta84.orig"
	INS='zr = fn84()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\tfn84 := gm84v.get"; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta84.new" && mv "$self_tree/meta84.new" internal/reader/metadata.go
	if grep -Fq 'fn84 := gm84v.get' internal/reader/metadata.go; then
		run_mut "method value bound to a variable"
	else
		echo "self-test ERROR: form 84 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta84.orig" internal/reader/metadata.go

	# --- 85: helper returning a method value through an interface ---------
	# getFn85() returns func() io.ReadCloser whose body returns a
	# method value; the double call must be seen as producing the file.
	cat > internal/reader/gatemut_funcretmethval.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gm85 struct{}

var gm85v gm85

func (g *gm85) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

func getFn85() func() io.ReadCloser {
	return gm85v.get
}
MUTEOF
	add_mut internal/reader/gatemut_funcretmethval.go
	cp internal/reader/metadata.go "$self_tree/meta85.orig"
	INS='zr = getFn85()()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta85.new" && mv "$self_tree/meta85.new" internal/reader/metadata.go
	if grep -Fq 'zr = getFn85()()' internal/reader/metadata.go; then
		run_mut "helper returning a method value"
	else
		echo "self-test ERROR: form 85 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta85.orig" internal/reader/metadata.go

	# --- 86: nested-receiver method value, double call --------------------
	# fn := mhv.inner.mk with mk returning a defined func type
	# producing *os.File; fn()() must be seen as producing the file.
	cat > internal/reader/gatemut_nestedmethval.go <<'MUTEOF'
package reader

import "os"

type fileFnI func() *os.File

type minner86 struct{}

type mholder86 struct{ inner minner86 }

var mhv86 mholder86

func (m *minner86) mk() fileFnI { return func() *os.File { f, _ := os.Open("/dev/null"); return f } }
MUTEOF
	add_mut internal/reader/gatemut_nestedmethval.go
	cp internal/reader/metadata.go "$self_tree/meta86.orig"
	INS='zr = fn86()()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\tfn86 := mhv86.inner.mk"; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta86.new" && mv "$self_tree/meta86.new" internal/reader/metadata.go
	if grep -Fq 'fn86 := mhv86.inner.mk' internal/reader/metadata.go; then
		run_mut "nested-receiver method value double call"
	else
		echo "self-test ERROR: form 86 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta86.orig" internal/reader/metadata.go

	# --- 87: method value through a package-level channel -----------------
	# init() sends the method value into a package chan; a receive in
	# the checked function must carry the func-file taint.
	cat > internal/reader/gatemut_chanmethval.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gm87 struct{}

var gm87v gm87

func (g *gm87) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

var ch87 = make(chan func() io.ReadCloser)

func init() {
	ch87 <- gm87v.get
}
MUTEOF
	add_mut internal/reader/gatemut_chanmethval.go
	cp internal/reader/metadata.go "$self_tree/meta87.orig"
	INS='zr = (<-ch87)()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta87.new" && mv "$self_tree/meta87.new" internal/reader/metadata.go
	if grep -Fq 'zr = (<-ch87)()' internal/reader/metadata.go; then
		run_mut "method value through a package channel"
	else
		echo "self-test ERROR: form 87 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta87.orig" internal/reader/metadata.go

	# --- 88: generic pass-through of a file -------------------------------
	# idf88[T any](f T) T instantiated with *os.File routes the file
	# through the type parameter; the call must be a producer.
	cat > internal/reader/gatemut_genericfile.go <<'MUTEOF'
package reader

import "os"

func idf88[T any](f T) T { return f }

var osFile88 *os.File
MUTEOF
	add_mut internal/reader/gatemut_genericfile.go
	cp internal/reader/metadata.go "$self_tree/meta88.orig"
	INS='zr = idf88(osFile88)'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta88.new" && mv "$self_tree/meta88.new" internal/reader/metadata.go
	if grep -Fq 'zr = idf88(osFile88)' internal/reader/metadata.go; then
		run_mut "generic pass-through of a file"
	else
		echo "self-test ERROR: form 88 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta88.orig" internal/reader/metadata.go

	# --- 89: generic pass-through of a func-file, double call -------------
	# idg89(getDef89)() with idg89[T any](f T) T and T bound to a
	# func() *os.File value: the double call yields the file.
	cat > internal/reader/gatemut_genericfunc.go <<'MUTEOF'
package reader

import "os"

type fileFnJ func() *os.File

func idg89[T any](f T) T { return f }

var getDef89 fileFnJ

func init() {
	getDef89 = func() *os.File { f, _ := os.Open("/dev/null"); return f }
}
MUTEOF
	add_mut internal/reader/gatemut_genericfunc.go
	cp internal/reader/metadata.go "$self_tree/meta89.orig"
	INS='zr = idg89(getDef89)()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta89.new" && mv "$self_tree/meta89.new" internal/reader/metadata.go
	if grep -Fq 'zr = idg89(getDef89)()' internal/reader/metadata.go; then
		run_mut "generic pass-through of a func-file"
	else
		echo "self-test ERROR: form 89 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta89.orig" internal/reader/metadata.go

	# --- 90: benign method value must pass ----------------------------------
	# Identical shape to form 84 but the method returns a bytes.Reader
	# wrapper: the gate must stay silent.
	cat > internal/reader/gatemut_benignmethval.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type mcw90 struct{ *bytes.Reader }

func (w *mcw90) Close() error { return nil }

type gm90 struct{}

var gm90v gm90

func (g *gm90) get() io.ReadCloser { return &mcw90{bytes.NewReader(nil)} }
MUTEOF
	add_mut internal/reader/gatemut_benignmethval.go
	cp internal/reader/metadata.go "$self_tree/meta90.orig"
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\tfn90 := gm90v.get"; print "\tzr = fn90()"; next } print }' internal/reader/metadata.go > "$self_tree/meta90.new" && mv "$self_tree/meta90.new" internal/reader/metadata.go
	if grep -Fq 'fn90 := gm90v.get' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign method value passes the gate"
		else
			echo "self-test MISS: benign method value failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 90 insert did not take"
		mutfail=1
	fi
	cleanup_muts
	cp "$self_tree/meta90.orig" internal/reader/metadata.go

	# --- 91: benign generic pass-through must pass --------------------------
	# idh91[T any](f T) T with a non-file argument: the generic rule
	# must not fire.
	cat > internal/reader/gatemut_benigngeneric.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type mcw91 struct{ *bytes.Reader }

func (w *mcw91) Close() error { return nil }

func idh91[T any](f T) T { return f }

var rc91 io.ReadCloser = &mcw91{bytes.NewReader(nil)}
MUTEOF
	add_mut internal/reader/gatemut_benigngeneric.go
	cp internal/reader/metadata.go "$self_tree/meta91.orig"
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\tzr = idh91(rc91)"; next } print }' internal/reader/metadata.go > "$self_tree/meta91.new" && mv "$self_tree/meta91.new" internal/reader/metadata.go
	if grep -Fq 'zr = idh91(rc91)' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign generic pass-through passes the gate"
		else
			echo "self-test MISS: benign generic pass-through failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 91 insert did not take"
		mutfail=1
	fi
	cleanup_muts
	cp "$self_tree/meta91.orig" internal/reader/metadata.go

	# --- 92: generic container element binding a file ----------------------
	# get92[T any](xs []T) T instantiated with []*os.File: the result
	# position bound through the container element must be a file.
	cat > internal/reader/gatemut_genericcont.go <<'MUTEOF'
package reader

import "os"

func get92[T any](xs []T) T { return xs[0] }

var osFiles92 = []*os.File{os.Stdin}
MUTEOF
	add_mut internal/reader/gatemut_genericcont.go
	cp internal/reader/metadata.go "$self_tree/meta92.orig"
	INS='zr = get92(osFiles92)'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta92.new" && mv "$self_tree/meta92.new" internal/reader/metadata.go
	if grep -Fq 'zr = get92(osFiles92)' internal/reader/metadata.go; then
		run_mut "generic container element binding a file"
	else
		echo "self-test ERROR: form 92 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta92.orig" internal/reader/metadata.go

	# --- 93: method value returning a chan of func-file --------------------
	# fn := hh.ch with ch() chan fileFn; fn() yields the channel, a
	# receive yields the func-file, calling it yields the file.
	cat > internal/reader/gatemut_chanmethval.go <<'MUTEOF'
package reader

import "os"

type fileFnM func() *os.File

type chH93 struct{}

var chh93 chH93

func (h chH93) ch() chan fileFnM { return nil }
MUTEOF
	add_mut internal/reader/gatemut_chanmethval.go
	cp internal/reader/metadata.go "$self_tree/meta93.orig"
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\tfn93 := chh93.ch"; print "\tgot93 := <-fn93()"; print "\tzr = got93()"; next } print }' internal/reader/metadata.go > "$self_tree/meta93.new" && mv "$self_tree/meta93.new" internal/reader/metadata.go
	if grep -Fq 'fn93 := chh93.ch' internal/reader/metadata.go; then
		run_mut "method value returning a chan of func-file"
	else
		echo "self-test ERROR: form 93 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	cp "$self_tree/meta93.orig" internal/reader/metadata.go

	# --- 94: func-typed struct field assigned a file closure --------------
	# init() fills fb94.fn with a closure returning io.ReadCloser over
	# os.Pipe; calling the field must be a producer.
	cat > internal/reader/gatemut_fnfieldassign.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type fnBox94 struct{ fn func() io.ReadCloser }

var fb94 fnBox94

func init() {
	fb94.fn = func() io.ReadCloser {
		w, _, _ := os.Pipe()
		return w
	}
}
MUTEOF
	add_mut internal/reader/gatemut_fnfieldassign.go
	cp internal/reader/metadata.go "$self_tree/meta94.orig"
	INS='zr = fb94.fn()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta94.new" && mv "$self_tree/meta94.new" internal/reader/metadata.go
	if grep -Fq 'zr = fb94.fn()' internal/reader/metadata.go; then
		run_mut "func-typed field assigned a file closure"
	else
		echo "self-test ERROR: form 94 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta94.orig" internal/reader/metadata.go

	# --- 95: chan-typed struct field assigned a chan of func-file ---------
	cat > internal/reader/gatemut_chanfieldassign.go <<'MUTEOF'
package reader

import "os"

type fileFnN func() *os.File

type chBox95 struct{ ch chan fileFnN }

var cb95 chBox95

func init() {
	cb95.ch = make(chan fileFnN)
}
MUTEOF
	add_mut internal/reader/gatemut_chanfieldassign.go
	cp internal/reader/metadata.go "$self_tree/meta95.orig"
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\tgot95 := <-cb95.ch"; print "\tzr = got95()"; next } print }' internal/reader/metadata.go > "$self_tree/meta95.new" && mv "$self_tree/meta95.new" internal/reader/metadata.go
	if grep -Fq 'got95 := <-cb95.ch' internal/reader/metadata.go; then
		run_mut "chan-typed field assigned a chan of func-file"
	else
		echo "self-test ERROR: form 95 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	cp "$self_tree/meta95.orig" internal/reader/metadata.go

	# --- 96: benign generic container must pass ----------------------------
	cat > internal/reader/gatemut_benigengenericcont.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcw96 struct{ *bytes.Reader }

func (w *rcw96) Close() error { return nil }

func getB96[T any](xs []T) T { return xs[0] }

var rcList96 = []io.ReadCloser{&rcw96{bytes.NewReader(nil)}}
MUTEOF
	add_mut internal/reader/gatemut_benigengenericcont.go
	cp internal/reader/metadata.go "$self_tree/meta96.orig"
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\tzr = getB96(rcList96)"; next } print }' internal/reader/metadata.go > "$self_tree/meta96.new" && mv "$self_tree/meta96.new" internal/reader/metadata.go
	if grep -Fq 'zr = getB96(rcList96)' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign generic container passes the gate"
		else
			echo "self-test MISS: benign generic container failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 96 insert did not take"
		mutfail=1
	fi
	cleanup_muts
	cp "$self_tree/meta96.orig" internal/reader/metadata.go

	# --- 97: benign func-typed field must pass ------------------------------
	cat > internal/reader/gatemut_benignfnfield.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcw97 struct{ *bytes.Reader }

func (w *rcw97) Close() error { return nil }

type fnBox97 struct{ fn func() io.ReadCloser }

var fb97 fnBox97

func init() {
	fb97.fn = func() io.ReadCloser { return &rcw97{bytes.NewReader(nil)} }
}
MUTEOF
	add_mut internal/reader/gatemut_benignfnfield.go
	cp internal/reader/metadata.go "$self_tree/meta97.orig"
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\tzr = fb97.fn()"; next } print }' internal/reader/metadata.go > "$self_tree/meta97.new" && mv "$self_tree/meta97.new" internal/reader/metadata.go
	if grep -Fq 'zr = fb97.fn()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign func-typed field passes the gate"
		else
			echo "self-test MISS: benign func-typed field failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 97 insert did not take"
		mutfail=1
	fi
	cleanup_muts
	cp "$self_tree/meta97.orig" internal/reader/metadata.go

	# --- 98: range over a struct-field chan of func-file -------------------
	# for got := range cb.ch where the field holds chan fileFn: the
	# loop variable is a func-file and calling it yields the file.
	cat > internal/reader/gatemut_rangefieldchan.go <<'MUTEOF'
package reader

import "os"

type fileFnP func() *os.File

type chBoxR98 struct{ ch chan fileFnP }

var cbr98 chBoxR98

func init() {
	cbr98.ch = make(chan fileFnP)
}
MUTEOF
	add_mut internal/reader/gatemut_rangefieldchan.go
	cp internal/reader/metadata.go "$self_tree/meta98.orig"
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\tfor got98 := range cbr98.ch {"; print "\t\tzr = got98()"; print "\t}"; next } print }' internal/reader/metadata.go > "$self_tree/meta98.new" && mv "$self_tree/meta98.new" internal/reader/metadata.go
	if grep -Fq 'for got98 := range cbr98.ch' internal/reader/metadata.go; then
		run_mut "range over a field chan of func-file"
	else
		echo "self-test ERROR: form 98 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	cp "$self_tree/meta98.orig" internal/reader/metadata.go

	# --- 99: receive from a struct-field chan of files ---------------------
	cat > internal/reader/gatemut_recvfieldchan.go <<'MUTEOF'
package reader

import "os"

type chFileR99 struct{ ch chan *os.File }

var cfr99 chFileR99

func init() {
	cfr99.ch = make(chan *os.File)
}
MUTEOF
	add_mut internal/reader/gatemut_recvfieldchan.go
	cp internal/reader/metadata.go "$self_tree/meta99.orig"
	INS='zr = <-cfr99.ch'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta99.new" && mv "$self_tree/meta99.new" internal/reader/metadata.go
	if grep -Fq 'zr = <-cfr99.ch' internal/reader/metadata.go; then
		run_mut "receive from a field chan of files"
	else
		echo "self-test ERROR: form 99 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta99.orig" internal/reader/metadata.go

	# --- 100: method value sent into a field chan from another function ----
	cat > internal/reader/gatemut_sendfieldchan.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gm100 struct{}

var gs100 gm100

func (g *gm100) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

type chBox100 struct{ ch chan func() io.ReadCloser }

var cbs100 chBox100

func init() {
	cbs100.ch = make(chan func() io.ReadCloser)
}

func fill100() {
	cbs100.ch <- gs100.get
}
MUTEOF
	add_mut internal/reader/gatemut_sendfieldchan.go
	cp internal/reader/metadata.go "$self_tree/meta100.orig"
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\tgot100 := <-cbs100.ch"; print "\tzr = got100()"; next } print }' internal/reader/metadata.go > "$self_tree/meta100.new" && mv "$self_tree/meta100.new" internal/reader/metadata.go
	if grep -Fq 'got100 := <-cbs100.ch' internal/reader/metadata.go; then
		run_mut "method value sent into a field chan"
	else
		echo "self-test ERROR: form 100 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	cp "$self_tree/meta100.orig" internal/reader/metadata.go

	# --- 101: benign range over a field chan must pass ----------------------
	cat > internal/reader/gatemut_benignrangefieldchan.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcw101 struct{ *bytes.Reader }

func (w *rcw101) Close() error { return nil }

type chBoxB101 struct{ ch chan func() io.ReadCloser }

var cbb101 chBoxB101

func init() {
	cbb101.ch = make(chan func() io.ReadCloser)
}
MUTEOF
	add_mut internal/reader/gatemut_benignrangefieldchan.go
	cp internal/reader/metadata.go "$self_tree/meta101.orig"
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\tfor got101 := range cbb101.ch {"; print "\t\tzr = got101()"; print "\t}"; next } print }' internal/reader/metadata.go > "$self_tree/meta101.new" && mv "$self_tree/meta101.new" internal/reader/metadata.go
	if grep -Fq 'for got101 := range cbb101.ch' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign range over a field chan passes the gate"
		else
			echo "self-test MISS: benign range over a field chan failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 101 insert did not take"
		mutfail=1
	fi
	cleanup_muts
	cp "$self_tree/meta101.orig" internal/reader/metadata.go

	# --- 102: benign receive from a field chan must pass --------------------
	cat > internal/reader/gatemut_benignrecvfieldchan.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcw102 struct{ *bytes.Reader }

func (w *rcw102) Close() error { return nil }

type chBoxC102 struct{ ch chan io.ReadCloser }

var cbc102 chBoxC102

func init() {
	cbc102.ch = make(chan io.ReadCloser)
}
MUTEOF
	add_mut internal/reader/gatemut_benignrecvfieldchan.go
	cp internal/reader/metadata.go "$self_tree/meta102.orig"
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\tzr = <-cbc102.ch"; next } print }' internal/reader/metadata.go > "$self_tree/meta102.new" && mv "$self_tree/meta102.new" internal/reader/metadata.go
	if grep -Fq 'zr = <-cbc102.ch' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign receive from a field chan passes the gate"
		else
			echo "self-test MISS: benign receive from a field chan failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 102 insert did not take"
		mutfail=1
	fi
	cleanup_muts
	cp "$self_tree/meta102.orig" internal/reader/metadata.go

	# --- 103: map field element holding a file-producing closure ----------
	# fm.m["k"] with m map[string]func() io.ReadCloser filled by a
	# closure returning os.Pipe: the element read and call must be a
	# producer even though the declared element type hides the file.
	cat > internal/reader/gatemut_mapfield.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type fnMap103 struct{ m map[string]func() io.ReadCloser }

var fm103 fnMap103

func init() {
	fm103.m = map[string]func() io.ReadCloser{
		"k": func() io.ReadCloser {
			w, _, _ := os.Pipe()
			return w
		},
	}
}
MUTEOF
	add_mut internal/reader/gatemut_mapfield.go
	cp internal/reader/metadata.go "$self_tree/meta103.orig"
	INS='zr = fm103.m["k"]()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta103.new" && mv "$self_tree/meta103.new" internal/reader/metadata.go
	if grep -Fq 'zr = fm103.m["k"]()' internal/reader/metadata.go; then
		run_mut "map field element holding a file closure"
	else
		echo "self-test ERROR: form 103 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta103.orig" internal/reader/metadata.go

	# --- 104: slice field element holding a file-producing closure --------
	cat > internal/reader/gatemut_slicefield.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type fnSlice104 struct{ s []func() io.ReadCloser }

var fs104 fnSlice104

func init() {
	fs104.s = []func() io.ReadCloser{
		func() io.ReadCloser {
			w, _, _ := os.Pipe()
			return w
		},
	}
}
MUTEOF
	add_mut internal/reader/gatemut_slicefield.go
	cp internal/reader/metadata.go "$self_tree/meta104.orig"
	INS='zr = fs104.s[0]()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta104.new" && mv "$self_tree/meta104.new" internal/reader/metadata.go
	if grep -Fq 'zr = fs104.s[0]()' internal/reader/metadata.go; then
		run_mut "slice field element holding a file closure"
	else
		echo "self-test ERROR: form 104 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta104.orig" internal/reader/metadata.go

	# --- 105: method value stored in a map field ---------------------------
	cat > internal/reader/gatemut_mapmethodval.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gm105 struct{}

var gm105v gm105

func (g *gm105) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

type fnMap105 struct{ m map[string]func() io.ReadCloser }

var fg105 fnMap105

func init() {
	fg105.m = map[string]func() io.ReadCloser{}
	fg105.m["k"] = gm105v.get
}
MUTEOF
	add_mut internal/reader/gatemut_mapmethodval.go
	cp internal/reader/metadata.go "$self_tree/meta105.orig"
	INS='zr = fg105.m["k"]()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta105.new" && mv "$self_tree/meta105.new" internal/reader/metadata.go
	if grep -Fq 'zr = fg105.m["k"]()' internal/reader/metadata.go; then
		run_mut "method value stored in a map field"
	else
		echo "self-test ERROR: form 105 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta105.orig" internal/reader/metadata.go

	# --- 106: declared map element shape of a defined func-file type -------
	cat > internal/reader/gatemut_declaredmap.go <<'MUTEOF'
package reader

import "os"

type fileFnR func() *os.File

type fnMap106 struct{ m map[string]fileFnR }

var fm106 fnMap106

func init() {
	fm106.m = map[string]fileFnR{}
}
MUTEOF
	add_mut internal/reader/gatemut_declaredmap.go
	cp internal/reader/metadata.go "$self_tree/meta106.orig"
	INS='zr = fm106.m["k"]()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta106.new" && mv "$self_tree/meta106.new" internal/reader/metadata.go
	if grep -Fq 'zr = fm106.m["k"]()' internal/reader/metadata.go; then
		run_mut "declared map element of a func-file type"
	else
		echo "self-test ERROR: form 106 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta106.orig" internal/reader/metadata.go

	# --- 107: benign map field must pass ------------------------------------
	cat > internal/reader/gatemut_benignmapfield.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcw107 struct{ *bytes.Reader }

func (w *rcw107) Close() error { return nil }

type fnMapB107 struct{ m map[string]func() io.ReadCloser }

var fmb107 fnMapB107

func init() {
	fmb107.m = map[string]func() io.ReadCloser{
		"k": func() io.ReadCloser { return &rcw107{bytes.NewReader(nil)} },
	}
}
MUTEOF
	add_mut internal/reader/gatemut_benignmapfield.go
	cp internal/reader/metadata.go "$self_tree/meta107.orig"
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\tzr = fmb107.m[\"k\"]()"; next } print }' internal/reader/metadata.go > "$self_tree/meta107.new" && mv "$self_tree/meta107.new" internal/reader/metadata.go
	if grep -Fq 'zr = fmb107.m["k"]()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign map field passes the gate"
		else
			echo "self-test MISS: benign map field failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 107 insert did not take"
		mutfail=1
	fi
	cleanup_muts
	cp "$self_tree/meta107.orig" internal/reader/metadata.go

	# --- 108: anonymous-receiver method returning a file ------------------
	# func (T) m() with no receiver variable name: the receiver type alone
	# must key the method producer route and taint the caller.
	cat > internal/reader/gatemut_anonrecv.go <<'MUTEOF'
package reader

import "os"

type an108 struct{}

func (an108) getf() *os.File {
	f, _ := os.Open("/dev/null")
	return f
}
MUTEOF
	add_mut internal/reader/gatemut_anonrecv.go
	cp internal/reader/metadata.go "$self_tree/meta108.orig"
	INS='zr = an108{}.getf()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta108.new" && mv "$self_tree/meta108.new" internal/reader/metadata.go
	if grep -Fq 'zr = an108{}.getf()' internal/reader/metadata.go; then
		run_mut "anonymous-receiver method returning a file"
	else
		echo "self-test ERROR: form 108 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta108.orig" internal/reader/metadata.go

	# --- 109: anonymous-receiver method returning an interface file ------
	# Same hidden-by-interface shape as form 105, with an unnamed receiver.
	cat > internal/reader/gatemut_anonrecviface.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type an109 struct{}

func (an109) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}
MUTEOF
	add_mut internal/reader/gatemut_anonrecviface.go
	cp internal/reader/metadata.go "$self_tree/meta109.orig"
	INS='zr = an109{}.get()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta109.new" && mv "$self_tree/meta109.new" internal/reader/metadata.go
	if grep -Fq 'zr = an109{}.get()' internal/reader/metadata.go; then
		run_mut "anonymous-receiver method returning an interface-hidden file"
	else
		echo "self-test ERROR: form 109 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta109.orig" internal/reader/metadata.go

	# --- 110: anonymous pointer-receiver method returning a file ----------
	cat > internal/reader/gatemut_anonptrrecv.go <<'MUTEOF'
package reader

import "os"

type an110 struct{}

func (*an110) getf() *os.File {
	f, _ := os.Open("/dev/null")
	return f
}
MUTEOF
	add_mut internal/reader/gatemut_anonptrrecv.go
	cp internal/reader/metadata.go "$self_tree/meta110.orig"
	INS='zr = (&an110{}).getf()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta110.new" && mv "$self_tree/meta110.new" internal/reader/metadata.go
	if grep -Fq 'zr = (&an110{}).getf()' internal/reader/metadata.go; then
		run_mut "anonymous pointer-receiver method returning a file"
	else
		echo "self-test ERROR: form 110 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta110.orig" internal/reader/metadata.go

	# --- 111: anonymous-receiver method value stored in a map field ------
	cat > internal/reader/gatemut_anonrecvmap.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type an111 struct{}

func (an111) getf() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

type fnMap111 struct{ m map[string]func() io.ReadCloser }

var fm111 fnMap111

func init() {
	fm111.m = map[string]func() io.ReadCloser{}
	fm111.m["k"] = an111{}.getf
}
MUTEOF
	add_mut internal/reader/gatemut_anonrecvmap.go
	cp internal/reader/metadata.go "$self_tree/meta111.orig"
	INS='zr = fm111.m["k"]()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta111.new" && mv "$self_tree/meta111.new" internal/reader/metadata.go
	if grep -Fq 'zr = fm111.m["k"]()' internal/reader/metadata.go; then
		run_mut "anonymous-receiver method value stored in a map field"
	else
		echo "self-test ERROR: form 111 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta111.orig" internal/reader/metadata.go

	# --- 112: benign anonymous-receiver method must pass ------------------
	# Same receiver syntax as 108-110 but the payload never touches the
	# filesystem: the scanner must not flag it.
	cat > internal/reader/gatemut_benignanonrecv.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcw112 struct{ *bytes.Reader }

func (w *rcw112) Close() error { return nil }

type an112 struct{}

func (an112) get() io.ReadCloser { return &rcw112{bytes.NewReader(nil)} }
MUTEOF
	add_mut internal/reader/gatemut_benignanonrecv.go
	cp internal/reader/metadata.go "$self_tree/meta112.orig"
	INS='zr = an112{}.get()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta112.new" && mv "$self_tree/meta112.new" internal/reader/metadata.go
	if grep -Fq 'zr = an112{}.get()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign anonymous-receiver method passes the gate"
		else
			echo "self-test MISS: benign anonymous-receiver method failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 112 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta112.orig" internal/reader/metadata.go

	# --- 113: alias receiver with an interface-hidden result --------------
	# type a = s; func (a) m() io.ReadCloser { os.Pipe... }; var v a; v.m().
	# The receiver alias must resolve to the underlying struct or the
	# call-site lookup never finds the retMethods key.
	cat > internal/reader/gatemut_aliasrecv.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsT struct{}

type rT = gsT

func (rT) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

var al rT
MUTEOF
	add_mut internal/reader/gatemut_aliasrecv.go
	cp internal/reader/metadata.go "$self_tree/meta113.orig"
	INS='zr = al.get()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta113.new" && mv "$self_tree/meta113.new" internal/reader/metadata.go
	if grep -Fq 'zr = al.get()' internal/reader/metadata.go; then
		run_mut "alias-receiver method with an interface-hidden file"
	else
		echo "self-test ERROR: form 113 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta113.orig" internal/reader/metadata.go

	# --- 114: alias-named composite literal receiver returning a file -----
	# type f = s; f{}.getf() where getf returns *os.File: the composite
	# literal type name is an alias and must resolve to the struct for the
	# method lookup.
	cat > internal/reader/gatemut_aliaslit.go <<'MUTEOF'
package reader

import "os"

type gsF struct{}

type rF = gsF

func (rF) getf() *os.File {
	f, _ := os.Open("/dev/null")
	return f
}
MUTEOF
	add_mut internal/reader/gatemut_aliaslit.go
	cp internal/reader/metadata.go "$self_tree/meta114.orig"
	INS='zr = rF{}.getf()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta114.new" && mv "$self_tree/meta114.new" internal/reader/metadata.go
	if grep -Fq 'zr = rF{}.getf()' internal/reader/metadata.go; then
		run_mut "alias-named composite literal receiver returning a file"
	else
		echo "self-test ERROR: form 114 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta114.orig" internal/reader/metadata.go

	# --- 115: benign alias receiver must pass ------------------------------
	# Same alias-receiver syntax as 113-114 but the payload never touches
	# the filesystem: the scanner must not flag it.
	cat > internal/reader/gatemut_benignalias.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcb115 struct{ *bytes.Reader }

func (w *rcb115) Close() error { return nil }

type gsBn115 struct{}

type rBn115 = gsBn115

func (rBn115) get() io.ReadCloser { return &rcb115{bytes.NewReader(nil)} }
MUTEOF
	add_mut internal/reader/gatemut_benignalias.go
	cp internal/reader/metadata.go "$self_tree/meta115.orig"
	INS='zr = rBn115{}.get()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta115.new" && mv "$self_tree/meta115.new" internal/reader/metadata.go
	if grep -Fq 'zr = rBn115{}.get()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign alias receiver passes the gate"
		else
			echo "self-test MISS: benign alias receiver failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 115 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta115.orig" internal/reader/metadata.go

	# --- 116: defined-type receiver with an interface-hidden result ------
	# type b gsV (defined, not aliased) with func (bV) get(); the
	# receiver and the instance variable must both resolve through the
	# defined-type chain to the base struct.
	cat > internal/reader/gatemut_defrecv.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsV116 struct{}

type bV116 gsV116

func (bV116) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

var bv116 bV116
MUTEOF
	add_mut internal/reader/gatemut_defrecv.go
	cp internal/reader/metadata.go "$self_tree/meta116.orig"
	INS='zr = bv116.get()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta116.new" && mv "$self_tree/meta116.new" internal/reader/metadata.go
	if grep -Fq 'zr = bv116.get()' internal/reader/metadata.go; then
		run_mut "defined-type receiver with an interface-hidden file"
	else
		echo "self-test ERROR: form 116 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta116.orig" internal/reader/metadata.go

	# --- 117: generic-instantiated struct variable method call -----------
	# var gv gsG[int] with func (gsG[T]) get(): the instantiation suffix
	# must strip to the base struct name for the method lookup.
	cat > internal/reader/gatemut_geninst.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsG117[T any] struct{}

func (gsG117[T]) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

var gv117 gsG117[int]
MUTEOF
	add_mut internal/reader/gatemut_geninst.go
	cp internal/reader/metadata.go "$self_tree/meta117.orig"
	INS='zr = gv117.get()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta117.new" && mv "$self_tree/meta117.new" internal/reader/metadata.go
	if grep -Fq 'zr = gv117.get()' internal/reader/metadata.go; then
		run_mut "generic-instantiated struct variable method call"
	else
		echo "self-test ERROR: form 117 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta117.orig" internal/reader/metadata.go

	# --- 118: embedded-field method promotion ----------------------------
	# type hE struct{ gsE } with func (gsE) get(): hve.get() promotes
	# through the embedded field and must resolve to gsE's method.
	cat > internal/reader/gatemut_embedmeth.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsE118 struct{}

func (gsE118) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

type hE118 struct{ gsE118 }

var hve118 hE118
MUTEOF
	add_mut internal/reader/gatemut_embedmeth.go
	cp internal/reader/metadata.go "$self_tree/meta118.orig"
	INS='zr = hve118.get()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta118.new" && mv "$self_tree/meta118.new" internal/reader/metadata.go
	if grep -Fq 'zr = hve118.get()' internal/reader/metadata.go; then
		run_mut "embedded-field method promotion with an interface-hidden file"
	else
		echo "self-test ERROR: form 118 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta118.orig" internal/reader/metadata.go

	# --- 119: pointer-alias receiver --------------------------------------
	# type p = *gsP; func (p) get(): the alias resolves to the pointer
	# spelling and must reduce to the base struct on both the receiver
	# key and the instance variable.
	cat > internal/reader/gatemut_ptralias.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsP119 struct{}

type p119 = *gsP119

func (p119) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

var pv119 p119 = &gsP119{}
MUTEOF
	add_mut internal/reader/gatemut_ptralias.go
	cp internal/reader/metadata.go "$self_tree/meta119.orig"
	INS='zr = pv119.get()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta119.new" && mv "$self_tree/meta119.new" internal/reader/metadata.go
	if grep -Fq 'zr = pv119.get()' internal/reader/metadata.go; then
		run_mut "pointer-alias receiver with an interface-hidden file"
	else
		echo "self-test ERROR: form 119 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta119.orig" internal/reader/metadata.go

	# --- 120: benign embedded-promotion method must pass ------------------
	# Same promoted-method shape as form 118 but the payload never
	# touches the filesystem: the scanner must not flag it.
	cat > internal/reader/gatemut_benemb.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcE120 struct{ *bytes.Reader }

func (w *rcE120) Close() error { return nil }

type gsBE120 struct{}

func (gsBE120) get() io.ReadCloser { return &rcE120{bytes.NewReader(nil)} }

type hBE120 struct{ gsBE120 }

var hvbe120 hBE120
MUTEOF
	add_mut internal/reader/gatemut_benemb.go
	cp internal/reader/metadata.go "$self_tree/meta120.orig"
	INS='zr = hvbe120.get()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta120.new" && mv "$self_tree/meta120.new" internal/reader/metadata.go
	if grep -Fq 'zr = hvbe120.get()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign embedded-promotion method passes the gate"
		else
			echo "self-test MISS: benign embedded-promotion method failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 120 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta120.orig" internal/reader/metadata.go

	# --- 121: pointer to a defined type without an initializer -----------
	# var p *d with type d gs (defined type) and no value: the pointer
	# spelling must resolve through the defined-type chain to the base
	# struct for both the receiver key and the instance registration.
	cat > internal/reader/gatemut_ptrdef.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsN121 struct{}

type dN121 gsN121

func (dN121) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

var pn121 *dN121
MUTEOF
	add_mut internal/reader/gatemut_ptrdef.go
	cp internal/reader/metadata.go "$self_tree/meta121.orig"
	INS='zr = pn121.get()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta121.new" && mv "$self_tree/meta121.new" internal/reader/metadata.go
	if grep -Fq 'zr = pn121.get()' internal/reader/metadata.go; then
		run_mut "pointer to a defined type without an initializer"
	else
		echo "self-test ERROR: form 121 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta121.orig" internal/reader/metadata.go

	# --- 122: benign pointer to a defined type must pass ------------------
	# Same pointer-to-defined-type shape as form 121 but the payload
	# never touches the filesystem: the scanner must not flag it.
	cat > internal/reader/gatemut_benptrdef.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcN122 struct{ *bytes.Reader }

func (w *rcN122) Close() error { return nil }

type gsBN122 struct{}

type dBN122 gsBN122

func (dBN122) get() io.ReadCloser { return &rcN122{bytes.NewReader(nil)} }

var pbn122 *dBN122
MUTEOF
	add_mut internal/reader/gatemut_benptrdef.go
	cp internal/reader/metadata.go "$self_tree/meta122.orig"
	INS='zr = pbn122.get()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta122.new" && mv "$self_tree/meta122.new" internal/reader/metadata.go
	if grep -Fq 'zr = pbn122.get()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign pointer-to-defined-type method passes the gate"
		else
			echo "self-test MISS: benign pointer-to-defined-type method failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 122 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta122.orig" internal/reader/metadata.go

	# --- 123: new() of a defined type as the receiver ---------------------
	# new(d) with type d gs (defined type): the argument must resolve
	# through the defined-type chain to the base struct.
	cat > internal/reader/gatemut_newdef.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsM123 struct{}

type dM123 gsM123

func (dM123) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}
MUTEOF
	add_mut internal/reader/gatemut_newdef.go
	cp internal/reader/metadata.go "$self_tree/meta123.orig"
	INS='zr = new(dM123).get()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta123.new" && mv "$self_tree/meta123.new" internal/reader/metadata.go
	if grep -Fq 'zr = new(dM123).get()' internal/reader/metadata.go; then
		run_mut "new() of a defined type as the receiver"
	else
		echo "self-test ERROR: form 123 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta123.orig" internal/reader/metadata.go

	# --- 124: array-index receiver ----------------------------------------
	# arr[1].get() where var arr [3]*gsH: the element type of the
	# declared variable must resolve to the struct for the method call.
	cat > internal/reader/gatemut_arrrecv.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsH124 struct{}

func (gsH124) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

var arr124 [3]*gsH124
MUTEOF
	add_mut internal/reader/gatemut_arrrecv.go
	cp internal/reader/metadata.go "$self_tree/meta124.orig"
	INS='zr = arr124[1].get()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta124.new" && mv "$self_tree/meta124.new" internal/reader/metadata.go
	if grep -Fq 'zr = arr124[1].get()' internal/reader/metadata.go; then
		run_mut "array-index receiver with an interface-hidden file"
	else
		echo "self-test ERROR: form 124 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta124.orig" internal/reader/metadata.go

	# --- 125: map-index receiver ------------------------------------------
	# mm["k"].get() where var mm map[string]*gsH: the element type must
	# resolve through the map wrapper.
	cat > internal/reader/gatemut_maprecv.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsH125 struct{}

func (gsH125) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

var mm125 map[string]*gsH125
MUTEOF
	add_mut internal/reader/gatemut_maprecv.go
	cp internal/reader/metadata.go "$self_tree/meta125.orig"
	INS='zr = mm125["k"].get()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta125.new" && mv "$self_tree/meta125.new" internal/reader/metadata.go
	if grep -Fq 'zr = mm125["k"].get()' internal/reader/metadata.go; then
		run_mut "map-index receiver with an interface-hidden file"
	else
		echo "self-test ERROR: form 125 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta125.orig" internal/reader/metadata.go

	# --- 126: benign array-index receiver must pass -----------------------
	# Same indexed-receiver shape as forms 124-125 but the payload never
	# touches the filesystem: the scanner must not flag it.
	cat > internal/reader/gatemut_benarrrecv.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcbH126 struct{ *bytes.Reader }

func (w *rcbH126) Close() error { return nil }

type gsHB126 struct{}

func (gsHB126) get() io.ReadCloser { return &rcbH126{bytes.NewReader(nil)} }

var arrB126 [3]*gsHB126
MUTEOF
	add_mut internal/reader/gatemut_benarrrecv.go
	cp internal/reader/metadata.go "$self_tree/meta126.orig"
	INS='zr = arrB126[1].get()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta126.new" && mv "$self_tree/meta126.new" internal/reader/metadata.go
	if grep -Fq 'zr = arrB126[1].get()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign array-index receiver passes the gate"
		else
			echo "self-test MISS: benign array-index receiver failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 126 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta126.orig" internal/reader/metadata.go

	# --- 127: struct-field container index receiver -----------------------
	# s.arr[1].get() where s is a struct whose field arr holds the
	# pointers: the indexed base must resolve through the field type.
	cat > internal/reader/gatemut_fieldidx.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsF127 struct{}

func (gsF127) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

type sf127 struct{ arr []*gsF127 }

var s127 sf127

func fldGet127() io.ReadCloser { return s127.arr[1].get() }
MUTEOF
	add_mut internal/reader/gatemut_fieldidx.go
	cp internal/reader/metadata.go "$self_tree/meta127.orig"
	INS='zr = fldGet127()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta127.new" && mv "$self_tree/meta127.new" internal/reader/metadata.go
	if grep -Fq 'zr = fldGet127()' internal/reader/metadata.go; then
		run_mut "struct-field container index receiver"
	else
		echo "self-test ERROR: form 127 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta127.orig" internal/reader/metadata.go

	# --- 128: call-result index receiver ----------------------------------
	# arrSrc()[0].get(): the indexed base is a same-package call whose
	# declared result type names the container.
	cat > internal/reader/gatemut_callidx.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsC128 struct{}

func (gsC128) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

func arrSrc128() []*gsC128 { return nil }

func clGet128() io.ReadCloser { return arrSrc128()[0].get() }
MUTEOF
	add_mut internal/reader/gatemut_callidx.go
	cp internal/reader/metadata.go "$self_tree/meta128.orig"
	INS='zr = clGet128()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta128.new" && mv "$self_tree/meta128.new" internal/reader/metadata.go
	if grep -Fq 'zr = clGet128()' internal/reader/metadata.go; then
		run_mut "call-result index receiver"
	else
		echo "self-test ERROR: form 128 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta128.orig" internal/reader/metadata.go

	# --- 129: dereferenced pointer-to-container index receiver ------------
	# (*pdp)[0].get() with var pdp *[]*gs: the pointer wrapper and the
	# container wrapper must both strip to the element struct.
	cat > internal/reader/gatemut_derefidx.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsDP129 struct{}

func (gsDP129) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

var pdp129 *[]*gsDP129

func drGet129() io.ReadCloser { return (*pdp129)[0].get() }
MUTEOF
	add_mut internal/reader/gatemut_derefidx.go
	cp internal/reader/metadata.go "$self_tree/meta129.orig"
	INS='zr = drGet129()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta129.new" && mv "$self_tree/meta129.new" internal/reader/metadata.go
	if grep -Fq 'zr = drGet129()' internal/reader/metadata.go; then
		run_mut "dereferenced pointer-to-container index receiver"
	else
		echo "self-test ERROR: form 129 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta129.orig" internal/reader/metadata.go

	# --- 130: make() short-declared map receiver --------------------------
	# mm := make(map[string]*gs); mm["k"].get(): the short declaration
	# must record the container type from the make argument.
	cat > internal/reader/gatemut_makerecv.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsMK130 struct{}

func (gsMK130) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

func mkGet130() io.ReadCloser {
	mmk := make(map[string]*gsMK130)
	return mmk["k"].get()
}
MUTEOF
	add_mut internal/reader/gatemut_makerecv.go
	cp internal/reader/metadata.go "$self_tree/meta130.orig"
	INS='zr = mkGet130()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta130.new" && mv "$self_tree/meta130.new" internal/reader/metadata.go
	if grep -Fq 'zr = mkGet130()' internal/reader/metadata.go; then
		run_mut "make() short-declared map receiver"
	else
		echo "self-test ERROR: form 130 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta130.orig" internal/reader/metadata.go

	# --- 131: range-variable element receiver -----------------------------
	# for _, v := range arr { v.get() }: the range variable must
	# register as a struct instance from the ranged element type.
	cat > internal/reader/gatemut_rangerecv.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsR131 struct{}

func (gsR131) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

var arrr131 [2]*gsR131

func rngGet131() io.ReadCloser {
	for _, v := range arrr131 {
		return v.get()
	}
	return nil
}
MUTEOF
	add_mut internal/reader/gatemut_rangerecv.go
	cp internal/reader/metadata.go "$self_tree/meta131.orig"
	INS='zr = rngGet131()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta131.new" && mv "$self_tree/meta131.new" internal/reader/metadata.go
	if grep -Fq 'zr = rngGet131()' internal/reader/metadata.go; then
		run_mut "range-variable element receiver"
	else
		echo "self-test ERROR: form 131 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta131.orig" internal/reader/metadata.go

	# --- 132: chan-receive element receiver -------------------------------
	# (<-chch).get() with var chch chan *gs: the receive expression must
	# resolve the channel's element struct.
	cat > internal/reader/gatemut_chanrecv.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsCH132 struct{}

func (gsCH132) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

var chch132 chan *gsCH132

func chGet132() io.ReadCloser { return (<-chch132).get() }
MUTEOF
	add_mut internal/reader/gatemut_chanrecv.go
	cp internal/reader/metadata.go "$self_tree/meta132.orig"
	INS='zr = chGet132()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta132.new" && mv "$self_tree/meta132.new" internal/reader/metadata.go
	if grep -Fq 'zr = chGet132()' internal/reader/metadata.go; then
		run_mut "chan-receive element receiver"
	else
		echo "self-test ERROR: form 132 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta132.orig" internal/reader/metadata.go

	# --- 133: benign make() map receiver must pass ------------------------
	# Same make/map-indexed shape as form 130 with a bytes-only payload:
	# the scanner must not flag it.
	cat > internal/reader/gatemut_benmakerecv.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcbM133 struct{ *bytes.Reader }

func (w *rcbM133) Close() error { return nil }

type gsBM133 struct{}

func (gsBM133) get() io.ReadCloser { return &rcbM133{bytes.NewReader(nil)} }

func mkGetB133() io.ReadCloser {
	mb := make(map[string]*gsBM133)
	return mb["k"].get()
}
MUTEOF
	add_mut internal/reader/gatemut_benmakerecv.go
	cp internal/reader/metadata.go "$self_tree/meta133.orig"
	INS='zr = mkGetB133()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta133.new" && mv "$self_tree/meta133.new" internal/reader/metadata.go
	if grep -Fq 'zr = mkGetB133()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign make() map receiver passes the gate"
		else
			echo "self-test MISS: benign make() map receiver failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 133 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta133.orig" internal/reader/metadata.go

	# --- 134: range-variable container index receiver ---------------------
	# for _, sl := range arrx { sl[0].get() } with var arrx [][]*gs: the
	# range variable must record its element type so it can serve as an
	# indexed base.
	cat > internal/reader/gatemut_rangeidx.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsRG134 struct{}

func (gsRG134) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

var arrx134 [][]*gsRG134

func rgGet134() io.ReadCloser {
	for _, sl := range arrx134 {
		return sl[0].get()
	}
	return nil
}
MUTEOF
	add_mut internal/reader/gatemut_rangeidx.go
	cp internal/reader/metadata.go "$self_tree/meta134.orig"
	INS='zr = rgGet134()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta134.new" && mv "$self_tree/meta134.new" internal/reader/metadata.go
	if grep -Fq 'zr = rgGet134()' internal/reader/metadata.go; then
		run_mut "range-variable container index receiver"
	else
		echo "self-test ERROR: form 134 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta134.orig" internal/reader/metadata.go

	# --- 135: composite-literal indexed receiver --------------------------
	# map[string]*gs{"a": {}}["a"].get(): the literal's declared type
	# must name the container for the element resolution.
	cat > internal/reader/gatemut_litidx.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsMK135 struct{}

func (gsMK135) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

func litGet135() io.ReadCloser { return map[string]*gsMK135{"a": {}}["a"].get() }
MUTEOF
	add_mut internal/reader/gatemut_litidx.go
	cp internal/reader/metadata.go "$self_tree/meta135.orig"
	INS='zr = litGet135()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta135.new" && mv "$self_tree/meta135.new" internal/reader/metadata.go
	if grep -Fq 'zr = litGet135()' internal/reader/metadata.go; then
		run_mut "composite-literal indexed receiver"
	else
		echo "self-test ERROR: form 135 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta135.orig" internal/reader/metadata.go

	# --- 136: benign composite-literal indexed receiver must pass ---------
	# Same literal-indexed shape as form 135 with a bytes-only payload.
	cat > internal/reader/gatemut_benlitidx.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcq136 struct{ *bytes.Reader }

func (w *rcq136) Close() error { return nil }

type gsBQ136 struct{}

func (gsBQ136) get() io.ReadCloser { return &rcq136{bytes.NewReader(nil)} }

func litGetB136() io.ReadCloser { return map[string]*gsBQ136{"a": {}}["a"].get() }
MUTEOF
	add_mut internal/reader/gatemut_benlitidx.go
	cp internal/reader/metadata.go "$self_tree/meta136.orig"
	INS='zr = litGetB136()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta136.new" && mv "$self_tree/meta136.new" internal/reader/metadata.go
	if grep -Fq 'zr = litGetB136()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign composite-literal indexed receiver passes the gate"
		else
			echo "self-test MISS: benign composite-literal indexed receiver failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 136 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta136.orig" internal/reader/metadata.go

	# --- 137: type-switch bound struct receiver ----------------------------
	# switch v := iv.(type) { case *gs: v.get() }: the bound variable
	# must register as an instance of the case struct.
	cat > internal/reader/gatemut_typeswitch.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsTS137 struct{}

func (gsTS137) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

var iv137 interface{} = &gsTS137{}

func tsGet137() io.ReadCloser {
	switch v := iv137.(type) {
	case *gsTS137:
		return v.get()
	}
	return nil
}
MUTEOF
	add_mut internal/reader/gatemut_typeswitch.go
	cp internal/reader/metadata.go "$self_tree/meta137.orig"
	INS='zr = tsGet137()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta137.new" && mv "$self_tree/meta137.new" internal/reader/metadata.go
	if grep -Fq 'zr = tsGet137()' internal/reader/metadata.go; then
		run_mut "type-switch bound struct receiver"
	else
		echo "self-test ERROR: form 137 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta137.orig" internal/reader/metadata.go

	# --- 138: multi-assign call result index receiver --------------------
	# a, _ := mk2(); a[0].get(): the second lhs must record the call's
	# declared result type at its index so it can be an indexed base.
	cat > internal/reader/gatemut_multiasgn.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsMA138 struct{}

func (gsMA138) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

func mk2_138() ([]*gsMA138, error) { return []*gsMA138{}, nil }

func maGet138() io.ReadCloser {
	a, _ := mk2_138()
	return a[0].get()
}
MUTEOF
	add_mut internal/reader/gatemut_multiasgn.go
	cp internal/reader/metadata.go "$self_tree/meta138.orig"
	INS='zr = maGet138()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta138.new" && mv "$self_tree/meta138.new" internal/reader/metadata.go
	if grep -Fq 'zr = maGet138()' internal/reader/metadata.go; then
		run_mut "multi-assign call result index receiver"
	else
		echo "self-test ERROR: form 138 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta138.orig" internal/reader/metadata.go

	# --- 139: benign type-switch bound receiver must pass -----------------
	# Same type-switch shape as form 137 with a bytes-only payload.
	cat > internal/reader/gatemut_bents.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcv139 struct{ *bytes.Reader }

func (w *rcv139) Close() error { return nil }

type gsBT139 struct{}

func (gsBT139) get() io.ReadCloser { return &rcv139{bytes.NewReader(nil)} }

var ivB139 interface{} = &gsBT139{}

func tsGetB139() io.ReadCloser {
	switch v := ivB139.(type) {
	case *gsBT139:
		return v.get()
	}
	return nil
}
MUTEOF
	add_mut internal/reader/gatemut_bents.go
	cp internal/reader/metadata.go "$self_tree/meta139.orig"
	INS='zr = tsGetB139()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta139.new" && mv "$self_tree/meta139.new" internal/reader/metadata.go
	if grep -Fq 'zr = tsGetB139()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign type-switch bound receiver passes the gate"
		else
			echo "self-test MISS: benign type-switch bound receiver failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 139 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta139.orig" internal/reader/metadata.go









	# --- 140: single-LHS call-result index receiver ---------------------
	# a := mkArr(); a[0].get(): a single-value call result must record
	# the declared result type so the binding can be an indexed base.
	cat > internal/reader/gatemut_singlecall.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsSC140 struct{}

func (gsSC140) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

func mkArr140() []*gsSC140 { return nil }

func sgGet140() io.ReadCloser {
	a := mkArr140()
	return a[0].get()
}
MUTEOF
	add_mut internal/reader/gatemut_singlecall.go
	cp internal/reader/metadata.go "$self_tree/meta140.orig"
	INS='zr = sgGet140()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta140.new" && mv "$self_tree/meta140.new" internal/reader/metadata.go
	if grep -Fq 'zr = sgGet140()' internal/reader/metadata.go; then
		run_mut "single-LHS call-result index receiver"
	else
		echo "self-test ERROR: form 140 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta140.orig" internal/reader/metadata.go

	# --- 141: single-LHS method-call result index receiver ----------------
	# a := box.mkArr(); a[0].get(): the method-call result type resolves
	# through the receiver instance, like form 140's plain call.
	cat > internal/reader/gatemut_singlemeth.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsSC141 struct{}

func (gsSC141) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

type mkBox141 struct{}

func (mkBox141) mkArr() []*gsSC141 { return nil }

var mkbox141 mkBox141

func sgGet141() io.ReadCloser {
	a := mkbox141.mkArr()
	return a[0].get()
}
MUTEOF
	add_mut internal/reader/gatemut_singlemeth.go
	cp internal/reader/metadata.go "$self_tree/meta141.orig"
	INS='zr = sgGet141()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta141.new" && mv "$self_tree/meta141.new" internal/reader/metadata.go
	if grep -Fq 'zr = sgGet141()' internal/reader/metadata.go; then
		run_mut "single-LHS method-call result index receiver"
	else
		echo "self-test ERROR: form 141 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta141.orig" internal/reader/metadata.go

	# --- 142: type-switch default clause bound receiver -------------------
	# switch v := iv.(type) { default: v.get() }: the default clause
	# binds the switched expression's own type and instance, so the
	# bound variable resolves method receivers like case-bound forms.
	cat > internal/reader/gatemut_tsdefault.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type ifD142 interface{ get() io.ReadCloser }

type gsTS142 struct{}

func (gsTS142) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

var iv142 ifD142 = &gsTS142{}

func tsGet142() io.ReadCloser {
	switch v := iv142.(type) {
	default:
		return v.get()
	}
}
MUTEOF
	add_mut internal/reader/gatemut_tsdefault.go
	cp internal/reader/metadata.go "$self_tree/meta142.orig"
	INS='zr = tsGet142()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta142.new" && mv "$self_tree/meta142.new" internal/reader/metadata.go
	if grep -Fq 'zr = tsGet142()' internal/reader/metadata.go; then
		run_mut "type-switch default clause bound receiver"
	else
		echo "self-test ERROR: form 142 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta142.orig" internal/reader/metadata.go

	# --- 143: multi-assign non-call RHS index receiver --------------------
	# a, _ := mm["k"], 0; a[0].get(): the element read binds the
	# container's element type (one wrapper stripped), so the binding
	# stays a container-typed base for later indexing.
	cat > internal/reader/gatemut_multimap.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsMM143 struct{}

func (gsMM143) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

var mm143 map[string][]*gsMM143

func mgGet143() io.ReadCloser {
	a, _ := mm143["k"], 0
	return a[0].get()
}
MUTEOF
	add_mut internal/reader/gatemut_multimap.go
	cp internal/reader/metadata.go "$self_tree/meta143.orig"
	INS='zr = mgGet143()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta143.new" && mv "$self_tree/meta143.new" internal/reader/metadata.go
	if grep -Fq 'zr = mgGet143()' internal/reader/metadata.go; then
		run_mut "multi-assign non-call RHS index receiver"
	else
		echo "self-test ERROR: form 143 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta143.orig" internal/reader/metadata.go

	# --- 144: benign single-LHS call-result index must pass --------------
	# Same shape as forms 140-141 with a bytes-only payload: the gate
	# must not taint the binding when the call result holds no file.
	cat > internal/reader/gatemut_benscall.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcv144 struct{ *bytes.Reader }

func (w *rcv144) Close() error { return nil }

type gsSCB144 struct{}

func (gsSCB144) get() io.ReadCloser { return &rcv144{bytes.NewReader(nil)} }

func mkArrB144() []*gsSCB144 { return nil }

func sgGetB144() io.ReadCloser {
	a := mkArrB144()
	return a[0].get()
}
MUTEOF
	add_mut internal/reader/gatemut_benscall.go
	cp internal/reader/metadata.go "$self_tree/meta144.orig"
	INS='zr = sgGetB144()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta144.new" && mv "$self_tree/meta144.new" internal/reader/metadata.go
	if grep -Fq 'zr = sgGetB144()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign single-LHS call-result index passes the gate"
		else
			echo "self-test MISS: benign single-LHS call-result index failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 144 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta144.orig" internal/reader/metadata.go

	# --- 145: generic explicit-instantiation index receiver ---------------
	# a := mkGen[*gsG](); a[0].get(): the declared []T result binds T
	# from the call's type argument before the binding is an index base.
	cat > internal/reader/gatemut_gencall.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsG145 struct{ x int }

func (g *gsG145) get() io.ReadCloser {
	r, _, _ := os.Pipe()
	return r
}

func mkGen145[T any]() []T { return nil }

func sgGen145() io.ReadCloser {
	a := mkGen145[*gsG145]()
	return a[0].get()
}
MUTEOF
	add_mut internal/reader/gatemut_gencall.go
	cp internal/reader/metadata.go "$self_tree/meta145.orig"
	INS='zr = sgGen145()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta145.new" && mv "$self_tree/meta145.new" internal/reader/metadata.go
	if grep -Fq 'zr = sgGen145()' internal/reader/metadata.go; then
		run_mut "generic explicit-instantiation index receiver"
	else
		echo "self-test ERROR: form 145 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta145.orig" internal/reader/metadata.go

	# --- 146: type-switch default with interface-returning call -----------
	# switch v := mkIf().(type) { default: v.get() }: an interface
	# value from a helper binds the interface pseudo-struct, and its
	# signature-identical implementations make the call a producer.
	cat > internal/reader/gatemut_tsifc.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type ifD146 interface{ get() io.ReadCloser }

type gsD146 struct{}

func (gsD146) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

func mkIf146() ifD146 { return &gsD146{} }

func tsD146() io.ReadCloser {
	switch v := mkIf146().(type) {
	default:
		return v.get()
	}
}
MUTEOF
	add_mut internal/reader/gatemut_tsifc.go
	cp internal/reader/metadata.go "$self_tree/meta146.orig"
	INS='zr = tsD146()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta146.new" && mv "$self_tree/meta146.new" internal/reader/metadata.go
	if grep -Fq 'zr = tsD146()' internal/reader/metadata.go; then
		run_mut "type-switch default interface-returning call"
	else
		echo "self-test ERROR: form 146 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta146.orig" internal/reader/metadata.go

	# --- 147: type-switch default with interface-typed field --------------
	# sd := mkSd(); switch v := sd.f.(type) { default: v.get() }: the
	# field's interface type resolves like the call-returned interface.
	cat > internal/reader/gatemut_tsifcf.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type ifD147 interface{ get() io.ReadCloser }

type gsD147 struct{}

func (gsD147) get() io.ReadCloser {
	w, _, _ := os.Pipe()
	return w
}

type sdH147 struct{ f ifD147 }

func mkSd147() sdH147 { return sdH147{f: &gsD147{}} }

func tsF147() io.ReadCloser {
	sd := mkSd147()
	switch v := sd.f.(type) {
	default:
		return v.get()
	}
}
MUTEOF
	add_mut internal/reader/gatemut_tsifcf.go
	cp internal/reader/metadata.go "$self_tree/meta147.orig"
	INS='zr = tsF147()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta147.new" && mv "$self_tree/meta147.new" internal/reader/metadata.go
	if grep -Fq 'zr = tsF147()' internal/reader/metadata.go; then
		run_mut "type-switch default interface-typed field"
	else
		echo "self-test ERROR: form 147 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta147.orig" internal/reader/metadata.go

	# --- 148: multi-assign chan-receive index receiver --------------------
	# a, _ := (<-chS), 0; a[0].get(): a receive binding records the
	# channel's element type (one wrapper stripped) as an index base.
	cat > internal/reader/gatemut_chanrecv.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsCH148 struct{ x int }

func (g *gsCH148) get() io.ReadCloser {
	r, _, _ := os.Pipe()
	return r
}

var chS148 chan []*gsCH148

func mgCH148() io.ReadCloser {
	a, _ := (<-chS148), 0
	return a[0].get()
}
MUTEOF
	add_mut internal/reader/gatemut_chanrecv.go
	cp internal/reader/metadata.go "$self_tree/meta148.orig"
	INS='zr = mgCH148()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta148.new" && mv "$self_tree/meta148.new" internal/reader/metadata.go
	if grep -Fq 'zr = mgCH148()' internal/reader/metadata.go; then
		run_mut "multi-assign chan-receive index receiver"
	else
		echo "self-test ERROR: form 148 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta148.orig" internal/reader/metadata.go

	# --- 149: benign generic explicit-instantiation index must pass -------
	# Same shape as form 145 with a bytes-only payload: the type
	# argument substitution must not taint a non-file element struct.
	cat > internal/reader/gatemut_bengen.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcv149 struct{ *bytes.Reader }

func (w *rcv149) Close() error { return nil }

type gsGB149 struct{}

func (gsGB149) get() io.ReadCloser { return &rcv149{bytes.NewReader(nil)} }

func mkGenB149[T any]() []T { return nil }

func sgGenB149() io.ReadCloser {
	a := mkGenB149[*gsGB149]()
	return a[0].get()
}
MUTEOF
	add_mut internal/reader/gatemut_bengen.go
	cp internal/reader/metadata.go "$self_tree/meta149.orig"
	INS='zr = sgGenB149()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta149.new" && mv "$self_tree/meta149.new" internal/reader/metadata.go
	if grep -Fq 'zr = sgGenB149()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign generic explicit-instantiation index passes the gate"
		else
			echo "self-test MISS: benign generic explicit-instantiation index failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 149 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta149.orig" internal/reader/metadata.go

	# --- 150: benign type-switch default interface call must pass ---------
	# Same shape as forms 146-147 with a bytes-only implementation: no
	# signature-identical producer exists, so the default clause keeps
	# its receiver untainted.
	cat > internal/reader/gatemut_bentsifc.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcv150 struct{ *bytes.Reader }

func (w *rcv150) Close() error { return nil }

type ifDB150 interface{ get() io.ReadCloser }

type gsDB150 struct{}

func (gsDB150) get() io.ReadCloser { return &rcv150{bytes.NewReader(nil)} }

func mkIfB150() ifDB150 { return &gsDB150{} }

func tsDB150() io.ReadCloser {
	switch v := mkIfB150().(type) {
	default:
		return v.get()
	}
}
MUTEOF
	add_mut internal/reader/gatemut_bentsifc.go
	cp internal/reader/metadata.go "$self_tree/meta150.orig"
	INS='zr = tsDB150()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta150.new" && mv "$self_tree/meta150.new" internal/reader/metadata.go
	if grep -Fq 'zr = tsDB150()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign type-switch default interface passes the gate"
		else
			echo "self-test MISS: benign type-switch default interface failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 150 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta150.orig" internal/reader/metadata.go

	# --- 151: generic-receiver container index receiver -------------------
	# rr := gR[*gsG]{}; a := rr.mk(); a[0].get(): the receiver's type
	# arguments substitute into the method's raw []T result so the
	# binding serves as an indexed base.
	cat > internal/reader/gatemut_genrecv.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsG151 struct{ x int }

func (g *gsG151) get() io.ReadCloser {
	r, _, _ := os.Pipe()
	return r
}

type gR151[T any] struct{}

func (r gR151[T]) mk() []T { return nil }

func gb151() io.ReadCloser {
	rr := gR151[*gsG151]{}
	a := rr.mk()
	return a[0].get()
}
MUTEOF
	add_mut internal/reader/gatemut_genrecv.go
	cp internal/reader/metadata.go "$self_tree/meta151.orig"
	INS='zr = gb151()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta151.new" && mv "$self_tree/meta151.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb151()' internal/reader/metadata.go; then
		run_mut "generic-receiver container index receiver"
	else
		echo "self-test ERROR: form 151 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta151.orig" internal/reader/metadata.go

	# --- 152: generic-receiver direct file result --------------------------
	# rr := &gR[*os.File]{}; rr.mk(): the instantiated receiver makes
	# the address-of composite literal resolve, and the substituted T
	# result is a direct *os.File producer.
	cat > internal/reader/gatemut_genrecvf.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gR152[T any] struct{}

func (r *gR152[T]) mk() T { var z T; return z }

func gb152() io.ReadCloser {
	rr := &gR152[*os.File]{}
	return rr.mk()
}
MUTEOF
	add_mut internal/reader/gatemut_genrecvf.go
	cp internal/reader/metadata.go "$self_tree/meta152.orig"
	INS='zr = gb152()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta152.new" && mv "$self_tree/meta152.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb152()' internal/reader/metadata.go; then
		run_mut "generic-receiver direct file result"
	else
		echo "self-test ERROR: form 152 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta152.orig" internal/reader/metadata.go

	# --- 153: embedded generic-receiver promotion -------------------------
	# type hE struct{ gR[*gsG] }; he.mk(): promotion carries the
	# embedded field's instantiation into the method result binding.
	cat > internal/reader/gatemut_genpromo.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsG153 struct{ x int }

func (g *gsG153) get() io.ReadCloser {
	r, _, _ := os.Pipe()
	return r
}

type gR153[T any] struct{}

func (r gR153[T]) mk() []T { return nil }

type hE153 struct{ gR153[*gsG153] }

func gb153() io.ReadCloser {
	he := hE153{}
	a := he.mk()
	return a[0].get()
}
MUTEOF
	add_mut internal/reader/gatemut_genpromo.go
	cp internal/reader/metadata.go "$self_tree/meta153.orig"
	INS='zr = gb153()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta153.new" && mv "$self_tree/meta153.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb153()' internal/reader/metadata.go; then
		run_mut "embedded generic-receiver promotion"
	else
		echo "self-test ERROR: form 153 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta153.orig" internal/reader/metadata.go

	# --- 154: explicit-instantiation direct file flow ----------------------
	# zr = mkT[*os.File](): the substituted T result is a bare
	# *os.File entering the exempted io.ReadFull shape.
	cat > internal/reader/gatemut_genexpf.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

func mkT154[T any]() T { var zero T; return zero }

func gb154() io.ReadCloser {
	return mkT154[*os.File]()
}
MUTEOF
	add_mut internal/reader/gatemut_genexpf.go
	cp internal/reader/metadata.go "$self_tree/meta154.orig"
	INS='zr = gb154()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta154.new" && mv "$self_tree/meta154.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb154()' internal/reader/metadata.go; then
		run_mut "explicit-instantiation direct file flow"
	else
		echo "self-test ERROR: form 154 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta154.orig" internal/reader/metadata.go

	# --- 155: explicit-instantiation struct receiver -----------------------
	# zr = mkT[*gsG]().get(): the substituted result resolves the
	# struct so the method receiver classifies.
	cat > internal/reader/gatemut_genexps.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsG155 struct{ x int }

func (g *gsG155) get() io.ReadCloser {
	r, _, _ := os.Pipe()
	return r
}

func mkT155[T any]() T { var zero T; return zero }

func gb155() io.ReadCloser {
	return mkT155[*gsG155]().get()
}
MUTEOF
	add_mut internal/reader/gatemut_genexps.go
	cp internal/reader/metadata.go "$self_tree/meta155.orig"
	INS='zr = gb155()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta155.new" && mv "$self_tree/meta155.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb155()' internal/reader/metadata.go; then
		run_mut "explicit-instantiation struct receiver"
	else
		echo "self-test ERROR: form 155 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta155.orig" internal/reader/metadata.go

	# --- 156: arg-inferred struct-bound result ----------------------------
	# zr = mkT2(&gsG{}).get(): T binds from the argument's resolved
	# type, so the result registers as a struct receiver.
	cat > internal/reader/gatemut_geninf.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gsG156 struct{ x int }

func (g *gsG156) get() io.ReadCloser {
	r, _, _ := os.Pipe()
	return r
}

func mkT156[T any](x T) T { return x }

func gb156() io.ReadCloser {
	return mkT156(&gsG156{}).get()
}
MUTEOF
	add_mut internal/reader/gatemut_geninf.go
	cp internal/reader/metadata.go "$self_tree/meta156.orig"
	INS='zr = gb156()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta156.new" && mv "$self_tree/meta156.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb156()' internal/reader/metadata.go; then
		run_mut "arg-inferred struct-bound result"
	else
		echo "self-test ERROR: form 156 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta156.orig" internal/reader/metadata.go

	# --- 157: benign generic-receiver container must pass -----------------
	# Same shape as forms 151-153 with a bytes-only payload: the
	# receiver instantiation must not taint a non-file element struct.
	cat > internal/reader/gatemut_bengenrecv.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcv157 struct{ *bytes.Reader }

func (w *rcv157) Close() error { return nil }

type gsGB157 struct{}

func (gsGB157) get() io.ReadCloser { return &rcv157{bytes.NewReader(nil)} }

type gRB157[T any] struct{}

func (r gRB157[T]) mk() []T { return nil }

func gbb157() io.ReadCloser {
	rr := gRB157[*gsGB157]{}
	a := rr.mk()
	return a[0].get()
}
MUTEOF
	add_mut internal/reader/gatemut_bengenrecv.go
	cp internal/reader/metadata.go "$self_tree/meta157.orig"
	INS='zr = gbb157()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta157.new" && mv "$self_tree/meta157.new" internal/reader/metadata.go
	if grep -Fq 'zr = gbb157()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign generic-receiver container passes the gate"
		else
			echo "self-test MISS: benign generic-receiver container failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 157 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta157.orig" internal/reader/metadata.go

	# --- 158: benign inferred struct-bound result must pass ---------------
	# Same shape as form 156 with a bytes-only payload.
	cat > internal/reader/gatemut_bengeninf.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcv158 struct{ *bytes.Reader }

func (w *rcv158) Close() error { return nil }

type gsGB158 struct{}

func (gsGB158) get() io.ReadCloser { return &rcv158{bytes.NewReader(nil)} }

func mkTB158[T any](x T) T { return x }

func gbb158() io.ReadCloser {
	return mkTB158(&gsGB158{}).get()
}
MUTEOF
	add_mut internal/reader/gatemut_bengeninf.go
	cp internal/reader/metadata.go "$self_tree/meta158.orig"
	INS='zr = gbb158()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta158.new" && mv "$self_tree/meta158.new" internal/reader/metadata.go
	if grep -Fq 'zr = gbb158()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign inferred struct-bound result passes the gate"
		else
			echo "self-test MISS: benign inferred struct-bound result failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 158 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta158.orig" internal/reader/metadata.go

	# --- 49: benign same-shaped control must pass (no false positive) ----
	# Identical in shape to form 47 but with an int field: the scanner
	# must not flag the shadow when the field holds no file.
	cat > internal/reader/gatemut_benign.go <<'MUTEOF'
package reader

type gb8 struct{ r int }

var gb8v gb8

func init() { gb8v.r = 1 }
MUTEOF
	add_mut internal/reader/gatemut_benign.go
	cp internal/reader/metadata.go "$self_tree/meta49.orig"
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\tzr = gb8v.r"; next } print }' internal/reader/metadata.go > "$self_tree/meta49.new" && mv "$self_tree/meta49.new" internal/reader/metadata.go
	if grep -q "^$(printf '\t')zr = gb8v.r" internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign same-shaped control passes the gate"
		else
			echo "self-test MISS: benign same-shaped control failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 49 insert did not take"
		mutfail=1
	fi
	cleanup_muts
	cp "$self_tree/meta49.orig" internal/reader/metadata.go

	# --- 46: an innocent gatemut_-named file must survive and pass -------
	cat > internal/mapping/gatemut_innocent.go <<'MUTEOF'
package mapping

func gatemutInnocent() int { return 1 }
MUTEOF
	if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
		if [ -f internal/mapping/gatemut_innocent.go ]; then
			echo "self-test OK: an innocent gatemut_-named file is not deleted"
		else
			echo "self-test MISS: the gate deleted an innocent gatemut_-named file"
			mutfail=1
		fi
	else
		echo "self-test MISS: an innocent gatemut_-named file failed the gate"
		mutfail=1
	fi
	rm -r internal/mapping/gatemut_innocent.go 2>/dev/null || true

	# --- 159: alias-spelled generic receiver result -----------------------
	# rr := gRA[zfA]{}; rr.mk() with type zfA = *os.File: the type
	# argument must be alias-resolved before the substituted result is
	# compared, so the receiver instantiation cannot hide a file value.
	cat > internal/reader/gatemut_aliasrecv.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type zfA159 = *os.File

type gRA159[T any] struct{}

func (r gRA159[T]) mk() T { var z T; return z }

func gb159() io.ReadCloser {
	rr := gRA159[zfA159]{}
	return rr.mk()
}
MUTEOF
	add_mut internal/reader/gatemut_aliasrecv.go
	cp internal/reader/metadata.go "$self_tree/meta159.orig"
	INS='zr = gb159()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta159.new" && mv "$self_tree/meta159.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb159()' internal/reader/metadata.go; then
		run_mut "alias-spelled generic receiver result"
	else
		echo "self-test ERROR: form 159 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta159.orig" internal/reader/metadata.go

	# --- 160: nested alias generic receiver result ------------------------
	# var rr gRA[zfB]; zfB = zfA; zfA = *os.File: the alias chain must
	# resolve before substitution, not only the bare name.
	cat > internal/reader/gatemut_aliasnested.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type zfA160 = *os.File
type zfB160 = zfA160

type gRA160[T any] struct{}

func (r gRA160[T]) mk() T { var z T; return z }

func gb160() io.ReadCloser {
	var rr gRA160[zfB160]
	return rr.mk()
}
MUTEOF
	add_mut internal/reader/gatemut_aliasnested.go
	cp internal/reader/metadata.go "$self_tree/meta160.orig"
	INS='zr = gb160()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta160.new" && mv "$self_tree/meta160.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb160()' internal/reader/metadata.go; then
		run_mut "nested alias generic receiver result"
	else
		echo "self-test ERROR: form 160 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta160.orig" internal/reader/metadata.go

	# --- 161: alias-spelled explicit instantiation result -----------------
	# mkGen[zfA](): the direct generic call result must be alias-resolved
	# before the binding is recorded as a file value.
	cat > internal/reader/gatemut_aliasinst.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type zfA161 = *os.File

func mkGen161[T any]() T { var z T; return z }

func gb161() io.ReadCloser {
	return mkGen161[zfA161]()
}
MUTEOF
	add_mut internal/reader/gatemut_aliasinst.go
	cp internal/reader/metadata.go "$self_tree/meta161.orig"
	INS='zr = gb161()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta161.new" && mv "$self_tree/meta161.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb161()' internal/reader/metadata.go; then
		run_mut "alias-spelled explicit instantiation result"
	else
		echo "self-test ERROR: form 161 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta161.orig" internal/reader/metadata.go

	# --- 162: alias-spelled generic method value result -------------------
	# f := rr.mk; f(): the method value carries the alias-spelled
	# instantiation of the receiver through the call expression.
	cat > internal/reader/gatemut_aliasmethval.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type zfA162 = *os.File

type gRA162[T any] struct{}

func (r gRA162[T]) mk() T { var z T; return z }

func gb162() io.ReadCloser {
	rr := gRA162[zfA162]{}
	f := rr.mk
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_aliasmethval.go
	cp internal/reader/metadata.go "$self_tree/meta162.orig"
	INS='zr = gb162()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta162.new" && mv "$self_tree/meta162.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb162()' internal/reader/metadata.go; then
		run_mut "alias-spelled generic method value result"
	else
		echo "self-test ERROR: form 162 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta162.orig" internal/reader/metadata.go

	# --- 163: alias-spelled embedded generic promotion --------------------
	# type hEA struct{ gRA[zfA] }: the embedding chain carries the alias
	# type argument into the promoted method result.
	cat > internal/reader/gatemut_aliaspromo.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type zfA163 = *os.File

type gRA163[T any] struct{}

func (r gRA163[T]) mk() T { var z T; return z }

type hEA163 struct{ gRA163[zfA163] }

func gb163() io.ReadCloser {
	he := hEA163{}
	return he.mk()
}
MUTEOF
	add_mut internal/reader/gatemut_aliaspromo.go
	cp internal/reader/metadata.go "$self_tree/meta163.orig"
	INS='zr = gb163()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta163.new" && mv "$self_tree/meta163.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb163()' internal/reader/metadata.go; then
		run_mut "alias-spelled embedded generic promotion"
	else
		echo "self-test ERROR: form 163 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta163.orig" internal/reader/metadata.go

	# --- 164: alias-spelled inferred element binding ----------------------
	# idc([]zfA{nil}): the argument-inferred element type must resolve
	# the alias before the generic result is recorded.
	cat > internal/reader/gatemut_aliasinf.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type zfA164 = *os.File

func idc164[T any](xs []T) T { var z T; return z }

func gb164() io.ReadCloser {
	return idc164([]zfA164{nil})
}
MUTEOF
	add_mut internal/reader/gatemut_aliasinf.go
	cp internal/reader/metadata.go "$self_tree/meta164.orig"
	INS='zr = gb164()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta164.new" && mv "$self_tree/meta164.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb164()' internal/reader/metadata.go; then
		run_mut "alias-spelled inferred element binding"
	else
		echo "self-test ERROR: form 164 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta164.orig" internal/reader/metadata.go

	# --- 165: benign alias generic receiver must pass ---------------------
	# Same shape as form 159 with an alias to a bytes-backed element
	# struct: alias resolution must not taint a non-file element.
	cat > internal/reader/gatemut_benaliasrecv.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcv165 struct{ *bytes.Reader }

func (w *rcv165) Close() error { return nil }

type gsGB165 struct{}

func (gsGB165) get() io.ReadCloser { return &rcv165{bytes.NewReader(nil)} }

type zfC165 = *gsGB165

type gRB165[T any] struct{}

func (r gRB165[T]) mk() T { var z T; return z }

func gbb165() io.ReadCloser {
	rr := gRB165[zfC165]{}
	return rr.mk().get()
}
MUTEOF
	add_mut internal/reader/gatemut_benaliasrecv.go
	cp internal/reader/metadata.go "$self_tree/meta165.orig"
	INS='zr = gbb165()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta165.new" && mv "$self_tree/meta165.new" internal/reader/metadata.go
	if grep -Fq 'zr = gbb165()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign alias generic receiver passes the gate"
		else
			echo "self-test MISS: benign alias generic receiver failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 165 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta165.orig" internal/reader/metadata.go

	# --- 166: benign alias inferred element must pass ---------------------
	# Same shape as form 164 with an alias to a bytes-backed element
	# struct.
	cat > internal/reader/gatemut_benaliasinf.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcv166 struct{ *bytes.Reader }

func (w *rcv166) Close() error { return nil }

type gsGB166 struct{}

func (gsGB166) get() io.ReadCloser { return &rcv166{bytes.NewReader(nil)} }

type zfC166 = *gsGB166

func idcB166[T any](xs []T) T { var z T; return z }

func gbb166() io.ReadCloser {
	return idcB166([]zfC166{nil}).get()
}
MUTEOF
	add_mut internal/reader/gatemut_benaliasinf.go
	cp internal/reader/metadata.go "$self_tree/meta166.orig"
	INS='zr = gbb166()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta166.new" && mv "$self_tree/meta166.new" internal/reader/metadata.go
	if grep -Fq 'zr = gbb166()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign alias inferred element passes the gate"
		else
			echo "self-test MISS: benign alias inferred element failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 166 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta166.orig" internal/reader/metadata.go

	# --- 167: container element read of a generic result ----------------
	# rr := gR[[]zfA]{}; a := rr.mk(); a[0]: the element type of a
	# container-shaped generic result must taint the index read even
	# when the binding itself is not a marked container.
	cat > internal/reader/gatemut_elemgen.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type zfA167 = *os.File

type gR167[T any] struct{}

func (r gR167[T]) mk() T { var z T; return z }

func gb167() io.ReadCloser {
	rr := gR167[[]zfA167]{}
	a := rr.mk()
	return a[0]
}
MUTEOF
	add_mut internal/reader/gatemut_elemgen.go
	cp internal/reader/metadata.go "$self_tree/meta167.orig"
	INS='zr = gb167()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta167.new" && mv "$self_tree/meta167.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb167()' internal/reader/metadata.go; then
		run_mut "container element read of a generic result"
	else
		echo "self-test ERROR: form 167 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta167.orig" internal/reader/metadata.go

	# --- 168: map element read of a generic result ----------------------
	cat > internal/reader/gatemut_mapgen.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type zfA168 = *os.File

type gR168[T any] struct{}

func (r gR168[T]) mk() T { var z T; return z }

func gb168() io.ReadCloser {
	rr := gR168[map[string]zfA168]{}
	m := rr.mk()
	return m["k"]
}
MUTEOF
	add_mut internal/reader/gatemut_mapgen.go
	cp internal/reader/metadata.go "$self_tree/meta168.orig"
	INS='zr = gb168()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta168.new" && mv "$self_tree/meta168.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb168()' internal/reader/metadata.go; then
		run_mut "map element read of a generic result"
	else
		echo "self-test ERROR: form 168 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta168.orig" internal/reader/metadata.go

	# --- 169: pointer deref of a generic result -------------------------
	cat > internal/reader/gatemut_derefgen.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type zfA169 = *os.File

type gR169[T any] struct{}

func (r gR169[T]) mk() T { var z T; return z }

func gb169() io.ReadCloser {
	rr := gR169[*zfA169]{}
	p := rr.mk()
	return *p
}
MUTEOF
	add_mut internal/reader/gatemut_derefgen.go
	cp internal/reader/metadata.go "$self_tree/meta169.orig"
	INS='zr = gb169()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta169.new" && mv "$self_tree/meta169.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb169()' internal/reader/metadata.go; then
		run_mut "pointer deref of a generic result"
	else
		echo "self-test ERROR: form 169 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta169.orig" internal/reader/metadata.go

	# --- 170: chan-of-file receive in return position -------------------
	# var c chan *os.File; return <-c: the receive feeds the exempted
	# io.ReadFull node through a function result, so the producer
	# marking must classify receives in return positions.
	cat > internal/reader/gatemut_chanret.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

func gb170() io.ReadCloser {
	var c chan *os.File
	return <-c
}
MUTEOF
	add_mut internal/reader/gatemut_chanret.go
	cp internal/reader/metadata.go "$self_tree/meta170.orig"
	INS='zr = gb170()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta170.new" && mv "$self_tree/meta170.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb170()' internal/reader/metadata.go; then
		run_mut "chan-of-file receive in return position"
	else
		echo "self-test ERROR: form 170 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta170.orig" internal/reader/metadata.go

	# --- 171: method call yielding chan of files -------------------------
	cat > internal/reader/gatemut_methchan.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type hM171 struct{}

func (h hM171) ch() chan *os.File { return nil }

func gb171() io.ReadCloser {
	h := hM171{}
	c := h.ch()
	return <-c
}
MUTEOF
	add_mut internal/reader/gatemut_methchan.go
	cp internal/reader/metadata.go "$self_tree/meta171.orig"
	INS='zr = gb171()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta171.new" && mv "$self_tree/meta171.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb171()' internal/reader/metadata.go; then
		run_mut "method call yielding chan of files"
	else
		echo "self-test ERROR: form 171 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta171.orig" internal/reader/metadata.go

	# --- 172: generic func result chan of files --------------------------
	cat > internal/reader/gatemut_genchan.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

func mkC172[T any]() chan T { return nil }

func gb172() io.ReadCloser {
	c := mkC172[*os.File]()
	return <-c
}
MUTEOF
	add_mut internal/reader/gatemut_genchan.go
	cp internal/reader/metadata.go "$self_tree/meta172.orig"
	INS='zr = gb172()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta172.new" && mv "$self_tree/meta172.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb172()' internal/reader/metadata.go; then
		run_mut "generic func result chan of files"
	else
		echo "self-test ERROR: form 172 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta172.orig" internal/reader/metadata.go

	# --- 173: cross-package alias generic type argument ------------------
	# type MappingFile = *os.File in internal/mapping, then
	# gR[mapping.MappingFile]{}: the qualified alias must resolve
	# through the process-wide registry across directory boundaries.
	cat > internal/mapping/gatemut_qalias.go <<'MUTEOF'
package mapping

import "os"

type MappingFile = *os.File
MUTEOF
	add_mut internal/mapping/gatemut_qalias.go
	cat > internal/reader/gatemut_qalias.go <<'MUTEOF'
package reader

import (
	"io"

	"github.com/firehol/iprange/v4/go/internal/mapping"
)

type gR173[T any] struct{}

func (r gR173[T]) mk() T { var z T; return z }

func gb173() io.ReadCloser {
	rr := gR173[mapping.MappingFile]{}
	return rr.mk()
}
MUTEOF
	add_mut internal/reader/gatemut_qalias.go
	cp internal/reader/metadata.go "$self_tree/meta173.orig"
	INS='zr = gb173()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta173.new" && mv "$self_tree/meta173.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb173()' internal/reader/metadata.go; then
		run_mut "cross-package alias generic type argument"
	else
		echo "self-test ERROR: form 173 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta173.orig" internal/reader/metadata.go

	# --- 174: generic method result as struct receiver -------------------
	# r := rr.mk() with T bound to wS { f zfA }; r.f: the generic
	# method result must register as a struct instance so the field
	# read resolves the file taint.
	cat > internal/reader/gatemut_structgen.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type zfA174 = *os.File

type wS174 struct{ f zfA174 }

type gR174[T any] struct{}

func (r gR174[T]) mk() T { var z T; return z }

func gb174() io.ReadCloser {
	rr := gR174[wS174]{}
	r := rr.mk()
	return r.f
}
MUTEOF
	add_mut internal/reader/gatemut_structgen.go
	cp internal/reader/metadata.go "$self_tree/meta174.orig"
	INS='zr = gb174()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta174.new" && mv "$self_tree/meta174.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb174()' internal/reader/metadata.go; then
		run_mut "generic method result as struct receiver"
	else
		echo "self-test ERROR: form 174 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta174.orig" internal/reader/metadata.go

	# --- 175: benign container element read must pass --------------------
	# Same shape as forms 167-169 with a bytes-backed element struct:
	# the element read must not taint a non-file element.
	cat > internal/reader/gatemut_benelem.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcv175 struct{ *bytes.Reader }

func (w *rcv175) Close() error { return nil }

type gsGB175 struct{}

func (gsGB175) get() io.ReadCloser { return &rcv175{bytes.NewReader(nil)} }

type gRB175[T any] struct{}

func (r gRB175[T]) mk() T { var z T; return z }

func gbb175() io.ReadCloser {
	rr := gRB175[[]*gsGB175]{}
	a := rr.mk()
	return a[0].get()
}
MUTEOF
	add_mut internal/reader/gatemut_benelem.go
	cp internal/reader/metadata.go "$self_tree/meta175.orig"
	INS='zr = gbb175()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta175.new" && mv "$self_tree/meta175.new" internal/reader/metadata.go
	if grep -Fq 'zr = gbb175()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign container element read passes the gate"
		else
			echo "self-test MISS: benign container element read failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 175 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta175.orig" internal/reader/metadata.go

	# --- 176: benign method chan must pass -------------------------------
	cat > internal/reader/gatemut_benmethchan.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcv176 struct{ *bytes.Reader }

func (w *rcv176) Close() error { return nil }

type gsGB176 struct{}

func (gsGB176) get() io.ReadCloser { return &rcv176{bytes.NewReader(nil)} }

type hM176 struct{}

func (h hM176) ch() chan *gsGB176 { return nil }

func gbb176() io.ReadCloser {
	h := hM176{}
	c := h.ch()
	return (<-c).get()
}
MUTEOF
	add_mut internal/reader/gatemut_benmethchan.go
	cp internal/reader/metadata.go "$self_tree/meta176.orig"
	INS='zr = gbb176()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta176.new" && mv "$self_tree/meta176.new" internal/reader/metadata.go
	if grep -Fq 'zr = gbb176()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign method chan passes the gate"
		else
			echo "self-test MISS: benign method chan failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 176 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta176.orig" internal/reader/metadata.go

	# --- 177: benign cross-package alias must pass -----------------------
	cat > internal/mapping/gatemut_benqalias.go <<'MUTEOF'
package mapping

import "bytes"

type mrc177 struct{ *bytes.Reader }

func (w *mrc177) Close() error { return nil }

type MappingRC177 = *mrc177
MUTEOF
	add_mut internal/mapping/gatemut_benqalias.go
	cat > internal/reader/gatemut_benqalias.go <<'MUTEOF'
package reader

import (
	"io"

	"github.com/firehol/iprange/v4/go/internal/mapping"
)

type gRB177[T any] struct{}

func (r gRB177[T]) mk() T { var z T; return z }

func gbb177() io.ReadCloser {
	rr := gRB177[mapping.MappingRC177]{}
	return rr.mk()
}
MUTEOF
	add_mut internal/reader/gatemut_benqalias.go
	cp internal/reader/metadata.go "$self_tree/meta177.orig"
	INS='zr = gbb177()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta177.new" && mv "$self_tree/meta177.new" internal/reader/metadata.go
	if grep -Fq 'zr = gbb177()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign cross-package alias passes the gate"
		else
			echo "self-test MISS: benign cross-package alias failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 177 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta177.orig" internal/reader/metadata.go

	# --- 178: benign generic struct result must pass ---------------------
	cat > internal/reader/gatemut_benstruct.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type rcv178 struct{ *bytes.Reader }

func (w *rcv178) Close() error { return nil }

type gsGB178 struct{}

func (gsGB178) get() io.ReadCloser { return &rcv178{bytes.NewReader(nil)} }

type wSB178 struct{ f *gsGB178 }

type gRB178[T any] struct{}

func (r gRB178[T]) mk() T { var z T; return z }

func gbb178() io.ReadCloser {
	rr := gRB178[wSB178]{}
	r := rr.mk()
	return r.f.get()
}
MUTEOF
	add_mut internal/reader/gatemut_benstruct.go
	cp internal/reader/metadata.go "$self_tree/meta178.orig"
	INS='zr = gbb178()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta178.new" && mv "$self_tree/meta178.new" internal/reader/metadata.go
	if grep -Fq 'zr = gbb178()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign generic struct result passes the gate"
		else
			echo "self-test MISS: benign generic struct result failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 178 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta178.orig" internal/reader/metadata.go

	# --- 179: renamed-import qualified alias generic type argument ------
	# import mm ".../internal/mapping" + gR[mm.MappingFile]{}: the local
	# import qualifier must translate to the package path before the
	# process-wide alias registry can resolve the file taint.
	cat > internal/mapping/gatemut_rqalias.go <<'MUTEOF'
package mapping

import "os"

type MappingFile = *os.File
MUTEOF
	add_mut internal/mapping/gatemut_rqalias.go
	cat > internal/reader/gatemut_rqalias.go <<'MUTEOF'
package reader

import (
	"io"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

type gR179[T any] struct{}

func (r gR179[T]) mk() T { var z T; return z }

func gb179() io.ReadCloser {
	rr := gR179[mm.MappingFile]{}
	return rr.mk()
}
MUTEOF
	add_mut internal/reader/gatemut_rqalias.go
	cp internal/reader/metadata.go "$self_tree/meta179.orig"
	INS='zr = gb179()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta179.new" && mv "$self_tree/meta179.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb179()' internal/reader/metadata.go; then
		run_mut "renamed-import qualified alias generic type argument"
	else
		echo "self-test ERROR: form 179 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta179.orig" internal/reader/metadata.go

	# --- 180: local alias of renamed-import qualified alias --------------
	# type MChainRen180 = mm.MappingFile, then gR180[MChainRen180]{}: the
	# same-package alias must chain into the renamed-qualified lookup.
	cat > internal/mapping/gatemut_rqchain.go <<'MUTEOF'
package mapping

import "os"

type MappingFile = *os.File
MUTEOF
	add_mut internal/mapping/gatemut_rqchain.go
	cat > internal/reader/gatemut_rqchain.go <<'MUTEOF'
package reader

import (
	"io"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

type MChainRen180 = mm.MappingFile

type gR180[T any] struct{}

func (r gR180[T]) mk() T { var z T; return z }

func gb180() io.ReadCloser {
	rr := gR180[MChainRen180]{}
	return rr.mk()
}
MUTEOF
	add_mut internal/reader/gatemut_rqchain.go
	cp internal/reader/metadata.go "$self_tree/meta180.orig"
	INS='zr = gb180()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta180.new" && mv "$self_tree/meta180.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb180()' internal/reader/metadata.go; then
		run_mut "local alias of renamed-import qualified alias"
	else
		echo "self-test ERROR: form 180 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta180.orig" internal/reader/metadata.go

	# --- 181: renamed-import qualified alias element spelling ------------
	# gR181[[]mm.MappingFile]{} + a[0]: the container element must keep
	# the file taint through the renamed qualifier.
	cat > internal/mapping/gatemut_rqelem.go <<'MUTEOF'
package mapping

import "os"

type MappingFile = *os.File
MUTEOF
	add_mut internal/mapping/gatemut_rqelem.go
	cat > internal/reader/gatemut_rqelem.go <<'MUTEOF'
package reader

import (
	"io"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

type gR181[T any] struct{}

func (r gR181[T]) mk() T { var z T; return z }

func gb181() io.ReadCloser {
	rr := gR181[[]mm.MappingFile]{}
	a := rr.mk()
	return a[0]
}
MUTEOF
	add_mut internal/reader/gatemut_rqelem.go
	cp internal/reader/metadata.go "$self_tree/meta181.orig"
	INS='zr = gb181()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta181.new" && mv "$self_tree/meta181.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb181()' internal/reader/metadata.go; then
		run_mut "renamed-import qualified alias element spelling"
	else
		echo "self-test ERROR: form 181 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta181.orig" internal/reader/metadata.go

	# --- 182: renamed-import qualified alias declared variable -----------
	# var z mm.MappingFile; z.Chdir(): the declared variable type must
	# resolve through the renamed qualifier to the file producer taint.
	cat > internal/mapping/gatemut_rqvar.go <<'MUTEOF'
package mapping

import "os"

type MappingFile = *os.File
MUTEOF
	add_mut internal/mapping/gatemut_rqvar.go
	cat > internal/reader/gatemut_rqvar.go <<'MUTEOF'
package reader

import (
	"io"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

func gb182() io.ReadCloser {
	var z mm.MappingFile
	z.Chdir()
	return nil
}
MUTEOF
	add_mut internal/reader/gatemut_rqvar.go
	cp internal/reader/metadata.go "$self_tree/meta182.orig"
	INS='zr = gb182()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta182.new" && mv "$self_tree/meta182.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb182()' internal/reader/metadata.go; then
		run_mut "renamed-import qualified alias declared variable"
	else
		echo "self-test ERROR: form 182 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta182.orig" internal/reader/metadata.go

	# --- 183: benign renamed-import qualified alias must pass ------------
	# mm.MappingRC183 with a bytes-backed base: the renamed qualifier
	# must not flag a non-file alias.
	cat > internal/mapping/gatemut_benrq.go <<'MUTEOF'
package mapping

import "bytes"

type mrc183 struct{ *bytes.Reader }

func (w *mrc183) Close() error { return nil }

type MappingRC183 = *mrc183
MUTEOF
	add_mut internal/mapping/gatemut_benrq.go
	cat > internal/reader/gatemut_benrq.go <<'MUTEOF'
package reader

import (
	"io"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

type gRB183[T any] struct{}

func (r gRB183[T]) mk() T { var z T; return z }

func gbb183() io.ReadCloser {
	rr := gRB183[mm.MappingRC183]{}
	return rr.mk()
}
MUTEOF
	add_mut internal/reader/gatemut_benrq.go
	cp internal/reader/metadata.go "$self_tree/meta183.orig"
	INS='zr = gbb183()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta183.new" && mv "$self_tree/meta183.new" internal/reader/metadata.go
	if grep -Fq 'zr = gbb183()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign renamed-import qualified alias passes the gate"
		else
			echo "self-test MISS: benign renamed-import qualified alias failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 183 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta183.orig" internal/reader/metadata.go

	# --- 184: benign local alias of renamed-import alias must pass -------
	# type MChainRenB184 = mm.MappingRC184: the same-package alias chain
	# into a renamed-qualified bytes alias must stay benign.
	cat > internal/mapping/gatemut_benrqchain.go <<'MUTEOF'
package mapping

import "bytes"

type mrc184 struct{ *bytes.Reader }

func (w *mrc184) Close() error { return nil }

type MappingRC184 = *mrc184
MUTEOF
	add_mut internal/mapping/gatemut_benrqchain.go
	cat > internal/reader/gatemut_benrqchain.go <<'MUTEOF'
package reader

import (
	"io"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

type MChainRenB184 = mm.MappingRC184

type gRB184[T any] struct{}

func (r gRB184[T]) mk() T { var z T; return z }

func gbb184() io.ReadCloser {
	rr := gRB184[MChainRenB184]{}
	return rr.mk()
}
MUTEOF
	add_mut internal/reader/gatemut_benrqchain.go
	cp internal/reader/metadata.go "$self_tree/meta184.orig"
	INS='zr = gbb184()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta184.new" && mv "$self_tree/meta184.new" internal/reader/metadata.go
	if grep -Fq 'zr = gbb184()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign local alias of renamed-import alias passes the gate"
		else
			echo "self-test MISS: benign local alias of renamed-import alias failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 184 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta184.orig" internal/reader/metadata.go

	# --- 185: generic method func-typed type argument --------------------
	# gRZ[func() *os.File]{}.mk() bound to f, then f(): the func-typed
	# generic method result must register as a func-file so invoking
	# the bound func keeps the file taint behind the io.ReadFull
	# exemption.
	cat > internal/reader/gatemut_genmethfunc.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gRZ185[T any] struct{}

func (r gRZ185[T]) mk() T { var z T; return z }

func gb185() io.ReadCloser {
	rr := gRZ185[func() *os.File]{}
	f := rr.mk()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_genmethfunc.go
	cp internal/reader/metadata.go "$self_tree/meta185.orig"
	INS='zr = gb185()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta185.new" && mv "$self_tree/meta185.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb185()' internal/reader/metadata.go; then
		run_mut "generic method func-typed type argument"
	else
		echo "self-test ERROR: form 185 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta185.orig" internal/reader/metadata.go

	# --- 186: embedded generic receiver func-typed promotion -------------
	# type hE struct{ gRZ[func() *os.File] }; he.mk(): the promoted
	# generic method must register the func-file through embedding.
	cat > internal/reader/gatemut_genmethfuncemb.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gRZ186[T any] struct{}

func (r gRZ186[T]) mk() T { var z T; return z }

type hE186 struct{ gRZ186[func() *os.File] }

func gb186() io.ReadCloser {
	he := hE186{}
	f := he.mk()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_genmethfuncemb.go
	cp internal/reader/metadata.go "$self_tree/meta186.orig"
	INS='zr = gb186()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta186.new" && mv "$self_tree/meta186.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb186()' internal/reader/metadata.go; then
		run_mut "embedded generic receiver func-typed promotion"
	else
		echo "self-test ERROR: form 186 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta186.orig" internal/reader/metadata.go

	# --- 187: local alias of func-typed generic type argument ------------
	# type Fz = func() *os.File; gRZ[Fz]{}: the alias must resolve to
	# the func type before the taint class is chosen.
	cat > internal/reader/gatemut_genmethfuncalias.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type Fz187 = func() *os.File

type gRZ187[T any] struct{}

func (r gRZ187[T]) mk() T { var z T; return z }

func gb187() io.ReadCloser {
	rr := gRZ187[Fz187]{}
	f := rr.mk()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_genmethfuncalias.go
	cp internal/reader/metadata.go "$self_tree/meta187.orig"
	INS='zr = gb187()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta187.new" && mv "$self_tree/meta187.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb187()' internal/reader/metadata.go; then
		run_mut "local alias of func-typed generic type argument"
	else
		echo "self-test ERROR: form 187 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta187.orig" internal/reader/metadata.go

	# --- 188: renamed-import func alias as generic type argument ---------
	# mm.F with F = func() *os.File in internal/mapping: the qualified
	# func alias must keep the func-file class through the renamed
	# import qualifier.
	cat > internal/mapping/gatemut_rqfunc.go <<'MUTEOF'
package mapping

import "os"

type F188 = func() *os.File
MUTEOF
	add_mut internal/mapping/gatemut_rqfunc.go
	cat > internal/reader/gatemut_rqfunc.go <<'MUTEOF'
package reader

import (
	"io"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

type gRZ188[T any] struct{}

func (r gRZ188[T]) mk() T { var z T; return z }

func gb188() io.ReadCloser {
	rr := gRZ188[mm.F188]{}
	f := rr.mk()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_rqfunc.go
	cp internal/reader/metadata.go "$self_tree/meta188.orig"
	INS='zr = gb188()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta188.new" && mv "$self_tree/meta188.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb188()' internal/reader/metadata.go; then
		run_mut "renamed-import func alias as generic type argument"
	else
		echo "self-test ERROR: form 188 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta188.orig" internal/reader/metadata.go

	# --- 189: unapproved method on an invoked generic func result --------
	# f := rr.mk(); f().Chdir(): the invoked func-file result must be
	# file-tainted so the unapproved method call is flagged.
	cat > internal/reader/gatemut_genmethfuncchdir.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type gRZ189[T any] struct{}

func (r gRZ189[T]) mk() T { var z T; return z }

func gb189() io.ReadCloser {
	rr := gRZ189[func() *os.File]{}
	f := rr.mk()
	f().Chdir()
	return nil
}
MUTEOF
	add_mut internal/reader/gatemut_genmethfuncchdir.go
	cp internal/reader/metadata.go "$self_tree/meta189.orig"
	INS='zr = gb189()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta189.new" && mv "$self_tree/meta189.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb189()' internal/reader/metadata.go; then
		run_mut "unapproved method on invoked generic func result"
	else
		echo "self-test ERROR: form 189 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta189.orig" internal/reader/metadata.go

	# --- 190: benign func-typed generic method result must pass ----------
	# gRZ[func() *mrc190]{} with a bytes-backed func result: the
	# func-file class must not flag a non-file func type.
	cat > internal/reader/gatemut_bengenmethfunc.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type mrc190 struct{ *bytes.Reader }

func (w *mrc190) Close() error { return nil }

type gRB190[T any] struct{}

func (r gRB190[T]) mk() T { var z T; return z }

func gbb190() io.ReadCloser {
	rr := gRB190[func() *mrc190]{}
	f := rr.mk()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_bengenmethfunc.go
	cp internal/reader/metadata.go "$self_tree/meta190.orig"
	INS='zr = gbb190()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta190.new" && mv "$self_tree/meta190.new" internal/reader/metadata.go
	if grep -Fq 'zr = gbb190()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign func-typed generic method result passes the gate"
		else
			echo "self-test MISS: benign func-typed generic method result failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 190 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta190.orig" internal/reader/metadata.go

	# --- 191: mixed multi-result function, func-file at position 0 -------
	# getFn() (func() *os.File, error): the func-typed result position
	# must keep the func-file taint even though the error position is
	# not a producer.
	cat > internal/reader/gatemut_mixedfunc0.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

func getFn191() (func() *os.File, error) { return nil, nil }

func gb191() io.ReadCloser {
	f, _ := getFn191()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_mixedfunc0.go
	cp internal/reader/metadata.go "$self_tree/meta191.orig"
	INS='zr = gb191()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta191.new" && mv "$self_tree/meta191.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb191()' internal/reader/metadata.go; then
		run_mut "mixed multi-result function func-file at position 0"
	else
		echo "self-test ERROR: form 191 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta191.orig" internal/reader/metadata.go

	# --- 192: mixed multi-result function, func-file at position 1 -------
	# hW() (int, func() *os.File): the func-file at the second position
	# must still taint its binding.
	cat > internal/reader/gatemut_mixedfunc1.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

func hW192() (int, func() *os.File) { return 0, nil }

func gb192() io.ReadCloser {
	_, f := hW192()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_mixedfunc1.go
	cp internal/reader/metadata.go "$self_tree/meta192.orig"
	INS='zr = gb192()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta192.new" && mv "$self_tree/meta192.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb192()' internal/reader/metadata.go; then
		run_mut "mixed multi-result function func-file at position 1"
	else
		echo "self-test ERROR: form 192 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta192.orig" internal/reader/metadata.go

	# --- 193: defined type over a renamed-qualified alias ----------------
	# type F193 mm.FUnqual with FUnqual = func() *os.File in mapping:
	# the defined-type chain must expand through the renamed import
	# qualifier.
	cat > internal/mapping/gatemut_defren.go <<'MUTEOF'
package mapping

import "os"

type FUnqual = func() *os.File
MUTEOF
	add_mut internal/mapping/gatemut_defren.go
	cat > internal/reader/gatemut_defren.go <<'MUTEOF'
package reader

import (
	"io"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

type F193 mm.FUnqual

type gRZ193[T any] struct{}

func (r gRZ193[T]) mk() T { var z T; return z }

func gb193() io.ReadCloser {
	rr := gRZ193[F193]{}
	f := rr.mk()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_defren.go
	cp internal/reader/metadata.go "$self_tree/meta193.orig"
	INS='zr = gb193()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta193.new" && mv "$self_tree/meta193.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb193()' internal/reader/metadata.go; then
		run_mut "defined type over renamed-qualified alias"
	else
		echo "self-test ERROR: form 193 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta193.orig" internal/reader/metadata.go

	# --- 194: local defined func type as generic type argument -----------
	# type F194 func() *os.File (defined, not alias): the defined func
	# type must expand before the generic method result class is chosen.
	cat > internal/reader/gatemut_deffunc.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type F194 func() *os.File

type gRZ194[T any] struct{}

func (r gRZ194[T]) mk() T { var z T; return z }

func gb194() io.ReadCloser {
	rr := gRZ194[F194]{}
	f := rr.mk()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_deffunc.go
	cp internal/reader/metadata.go "$self_tree/meta194.orig"
	INS='zr = gb194()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta194.new" && mv "$self_tree/meta194.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb194()' internal/reader/metadata.go; then
		run_mut "local defined func type as generic type argument"
	else
		echo "self-test ERROR: form 194 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta194.orig" internal/reader/metadata.go

	# --- 195: cross-package defined func type as generic type argument ---
	# mm.F195 with F195 func() *os.File (defined, not alias): qualified
	# references to defined func types must resolve like aliases.
	cat > internal/mapping/gatemut_defqual.go <<'MUTEOF'
package mapping

import "os"

type F195 func() *os.File
MUTEOF
	add_mut internal/mapping/gatemut_defqual.go
	cat > internal/reader/gatemut_defqual.go <<'MUTEOF'
package reader

import (
	"io"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

type gRZ195[T any] struct{}

func (r gRZ195[T]) mk() T { var z T; return z }

func gb195() io.ReadCloser {
	rr := gRZ195[mm.F195]{}
	f := rr.mk()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_defqual.go
	cp internal/reader/metadata.go "$self_tree/meta195.orig"
	INS='zr = gb195()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta195.new" && mv "$self_tree/meta195.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb195()' internal/reader/metadata.go; then
		run_mut "cross-package defined func type as generic type argument"
	else
		echo "self-test ERROR: form 195 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta195.orig" internal/reader/metadata.go

	# --- 196: defined type over a cross-package defined func type --------
	# type F196 mm.F195: the chain must expand through both defined-type
	# hops.
	cat > internal/mapping/gatemut_defdef.go <<'MUTEOF'
package mapping

import "os"

type F196 func() *os.File
MUTEOF
	add_mut internal/mapping/gatemut_defdef.go
	cat > internal/reader/gatemut_defdef.go <<'MUTEOF'
package reader

import (
	"io"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

type F196B mm.F196

type gRZ196[T any] struct{}

func (r gRZ196[T]) mk() T { var z T; return z }

func gb196() io.ReadCloser {
	rr := gRZ196[F196B]{}
	f := rr.mk()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_defdef.go
	cp internal/reader/metadata.go "$self_tree/meta196.orig"
	INS='zr = gb196()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta196.new" && mv "$self_tree/meta196.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb196()' internal/reader/metadata.go; then
		run_mut "defined type over cross-package defined func type"
	else
		echo "self-test ERROR: form 196 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta196.orig" internal/reader/metadata.go

	# --- 197: benign mixed multi-result bytes func must pass -------------
	# (func() *mrc197, error): a bytes-backed func result at a mixed
	# position must not taint.
	cat > internal/reader/gatemut_benmixed.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type mrc197 struct{ *bytes.Reader }

func (w *mrc197) Close() error { return nil }

func getB197() (func() *mrc197, error) { return nil, nil }

func gbb197() io.ReadCloser {
	f, _ := getB197()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_benmixed.go
	cp internal/reader/metadata.go "$self_tree/meta197.orig"
	INS='zr = gbb197()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta197.new" && mv "$self_tree/meta197.new" internal/reader/metadata.go
	if grep -Fq 'zr = gbb197()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign mixed multi-result bytes func passes the gate"
		else
			echo "self-test MISS: benign mixed multi-result bytes func failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 197 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta197.orig" internal/reader/metadata.go

	# --- 198: benign defined-over-renamed bytes type must pass -----------
	# type B198 mm.MRC198 with MRC198 = func() *mrc198: the defined
	# chain over a renamed-qualified bytes alias must stay benign.
	cat > internal/mapping/gatemut_bandef.go <<'MUTEOF'
package mapping

import "bytes"

type mrc198 struct{ *bytes.Reader }

func (w *mrc198) Close() error { return nil }

type MRC198 = func() *mrc198
MUTEOF
	add_mut internal/mapping/gatemut_bandef.go
	cat > internal/reader/gatemut_bandef.go <<'MUTEOF'
package reader

import (
	"io"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

type B198 mm.MRC198

type gRB198[T any] struct{}

func (r gRB198[T]) mk() T { var z T; return z }

func gbb198() io.ReadCloser {
	rr := gRB198[B198]{}
	f := rr.mk()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_bandef.go
	cp internal/reader/metadata.go "$self_tree/meta198.orig"
	INS='zr = gbb198()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta198.new" && mv "$self_tree/meta198.new" internal/reader/metadata.go
	if grep -Fq 'zr = gbb198()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign defined-over-renamed bytes type passes the gate"
		else
			echo "self-test MISS: benign defined-over-renamed bytes type failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 198 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta198.orig" internal/reader/metadata.go






	# --- 199: interface-typed generic result of a mixed method, pos 0 ----
	# A generic receiver bound to an interface whose method declares
	# (func() *os.File, error): the func-file result position must keep
	# the invoke-able kind, not be claimed as a raw file position.
	cat > internal/reader/gatemut_ifacepos0.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type Iface199 interface{ Get() (func() *os.File, error) }

type gRZ199[T any] struct{}

func (r gRZ199[T]) mk() T { var z T; return z }

func gb199() io.ReadCloser {
	rr := gRZ199[Iface199]{}
	x := rr.mk()
	f, _ := x.Get()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_ifacepos0.go
	cp internal/reader/metadata.go "$self_tree/meta199.orig"
	INS='zr = gb199()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta199.new" && mv "$self_tree/meta199.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb199()' internal/reader/metadata.go; then
		run_mut "interface-typed generic result of a mixed method at position 0"
	else
		echo "self-test ERROR: form 199 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta199.orig" internal/reader/metadata.go

	# --- 200: interface-typed generic result of a mixed method, pos 1 ----
	# Same shape with the func-file at the second position.
	cat > internal/reader/gatemut_ifacepos1.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type Iface200 interface{ Get() (error, func() *os.File) }

type gRZ200[T any] struct{}

func (r gRZ200[T]) mk() T { var z T; return z }

func gb200() io.ReadCloser {
	rr := gRZ200[Iface200]{}
	x := rr.mk()
	_, f := x.Get()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_ifacepos1.go
	cp internal/reader/metadata.go "$self_tree/meta200.orig"
	INS='zr = gb200()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta200.new" && mv "$self_tree/meta200.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb200()' internal/reader/metadata.go; then
		run_mut "interface-typed generic result of a mixed method at position 1"
	else
		echo "self-test ERROR: form 200 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta200.orig" internal/reader/metadata.go

	# --- 201: interface-typed generic result, chan-of-func mixed method --
	# The chan position must keep the chan-of-func carrier kind through
	# the receive and the invocation.
	cat > internal/reader/gatemut_ifacechan.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type Iface201 interface{ Get() (chan func() *os.File, error) }

type gRZ201[T any] struct{}

func (r gRZ201[T]) mk() T { var z T; return z }

func gb201() io.ReadCloser {
	rr := gRZ201[Iface201]{}
	x := rr.mk()
	c, _ := x.Get()
	z := <-c
	return z()
}
MUTEOF
	add_mut internal/reader/gatemut_ifacechan.go
	cp internal/reader/metadata.go "$self_tree/meta201.orig"
	INS='zr = gb201()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta201.new" && mv "$self_tree/meta201.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb201()' internal/reader/metadata.go; then
		run_mut "interface-typed generic chan-of-func mixed method result"
	else
		echo "self-test ERROR: form 201 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta201.orig" internal/reader/metadata.go

	# --- 202: cross-package defined type over a same-package alias -------
	# mapping: type FA202 = func() *os.File; type FD202 FA202: the
	# qualified spelling mm.FD202 must expand through the defined hop and
	# the alias hop to the func text.
	cat > internal/mapping/gatemut_defalias.go <<'MUTEOF'
package mapping

import "os"

type FA202 = func() *os.File
type FD202 FA202
MUTEOF
	add_mut internal/mapping/gatemut_defalias.go
	cat > internal/reader/gatemut_defalias.go <<'MUTEOF'
package reader

import (
	"io"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

type gRZ202[T any] struct{}

func (r gRZ202[T]) mk() T { var z T; return z }

func gb202() io.ReadCloser {
	rr := gRZ202[mm.FD202]{}
	f := rr.mk()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_defalias.go
	cp internal/reader/metadata.go "$self_tree/meta202.orig"
	INS='zr = gb202()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta202.new" && mv "$self_tree/meta202.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb202()' internal/reader/metadata.go; then
		run_mut "cross-package defined type over a same-package alias"
	else
		echo "self-test ERROR: form 202 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta202.orig" internal/reader/metadata.go

	# --- 203: cross-package alias over a defined func type ---------------
	# mapping: type FD203 func() *os.File; type FE203 = FD203: the
	# alias hop must resolve into the defined func text.
	cat > internal/mapping/gatemut_aliasdef.go <<'MUTEOF'
package mapping

import "os"

type FD203 func() *os.File
type FE203 = FD203
MUTEOF
	add_mut internal/mapping/gatemut_aliasdef.go
	cat > internal/reader/gatemut_aliasdef.go <<'MUTEOF'
package reader

import (
	"io"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

type gRZ203[T any] struct{}

func (r gRZ203[T]) mk() T { var z T; return z }

func gb203() io.ReadCloser {
	rr := gRZ203[mm.FE203]{}
	f := rr.mk()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_aliasdef.go
	cp internal/reader/metadata.go "$self_tree/meta203.orig"
	INS='zr = gb203()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta203.new" && mv "$self_tree/meta203.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb203()' internal/reader/metadata.go; then
		run_mut "cross-package alias over a defined func type"
	else
		echo "self-test ERROR: form 203 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta203.orig" internal/reader/metadata.go

	# --- 204: non-generic method mixed results, func-file at pos 0 -------
	# A plain struct method declaring (func() *os.File, error): the
	# declared-result path must resolve methods whose ok flag is off.
	cat > internal/reader/gatemut_methpos0.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type hM204 struct{}

func (h hM204) mk() (func() *os.File, error) { return nil, nil }

func gb204() io.ReadCloser {
	h := hM204{}
	f, _ := h.mk()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_methpos0.go
	cp internal/reader/metadata.go "$self_tree/meta204.orig"
	INS='zr = gb204()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta204.new" && mv "$self_tree/meta204.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb204()' internal/reader/metadata.go; then
		run_mut "non-generic method mixed results at position 0"
	else
		echo "self-test ERROR: form 204 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta204.orig" internal/reader/metadata.go

	# --- 205: non-generic method mixed results, func-file at pos 1 -------
	cat > internal/reader/gatemut_methpos1.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type hM205 struct{}

func (h hM205) mk() (int, func() *os.File) { return 0, nil }

func gb205() io.ReadCloser {
	h := hM205{}
	_, f := h.mk()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_methpos1.go
	cp internal/reader/metadata.go "$self_tree/meta205.orig"
	INS='zr = gb205()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta205.new" && mv "$self_tree/meta205.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb205()' internal/reader/metadata.go; then
		run_mut "non-generic method mixed results at position 1"
	else
		echo "self-test ERROR: form 205 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta205.orig" internal/reader/metadata.go

	# --- 206: benign interface-typed generic mixed bytes method ----------
	# The same interface-method shape returning func() *mrc206 must stay
	# benign (no *os.File anywhere in the declared chain).
	cat > internal/reader/gatemut_beniface.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type mrc206 struct{ *bytes.Reader }

func (w *mrc206) Close() error { return nil }

type Iface206 interface{ Get() (func() *mrc206, error) }

type gRZ206[T any] struct{}

func (r gRZ206[T]) mk() T { var z T; return z }

func gbb206() io.ReadCloser {
	rr := gRZ206[Iface206]{}
	x := rr.mk()
	f, _ := x.Get()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_beniface.go
	cp internal/reader/metadata.go "$self_tree/meta206.orig"
	INS='zr = gbb206()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta206.new" && mv "$self_tree/meta206.new" internal/reader/metadata.go
	if grep -Fq 'zr = gbb206()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign interface-typed mixed bytes method passes the gate"
		else
			echo "self-test MISS: benign interface-typed mixed bytes method failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 206 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta206.orig" internal/reader/metadata.go


	# --- 207: embedded interface promotion, single func-file method ------
	# An interface embedding a file-producing interface promotes the
	# embedded methods: the resolved receiver is the embedding interface,
	# so the walk must follow the embedding chain and keep the promoted
	# declared results (the ok flag alone records body-marked claims).
	cat > internal/reader/gatemut_embiface.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type IBase207 interface{ Get() func() *os.File }

type IEmb207 interface{ IBase207 }

type gRZ207[T any] struct{}

func (r gRZ207[T]) mk() T { var z T; return z }

func gb207() io.ReadCloser {
	rr := gRZ207[IEmb207]{}
	x := rr.mk()
	return x.Get()()
}
MUTEOF
	add_mut internal/reader/gatemut_embiface.go
	cp internal/reader/metadata.go "$self_tree/meta207.orig"
	INS='zr = gb207()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta207.new" && mv "$self_tree/meta207.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb207()' internal/reader/metadata.go; then
		run_mut "embedded interface promotion with a single func-file method"
	else
		echo "self-test ERROR: form 207 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta207.orig" internal/reader/metadata.go


	# --- 208: embedded interface promotion, mixed multi-result method -----
	# The promoted method declares (func() *os.File, error): position 0
	# keeps the func-file kind through the assign and the invocation.
	cat > internal/reader/gatemut_embifacemixed.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type IBase208 interface{ Get() (func() *os.File, error) }

type IEmb208 interface{ IBase208 }

type gRZ208[T any] struct{}

func (r gRZ208[T]) mk() T { var z T; return z }

func gb208() io.ReadCloser {
	rr := gRZ208[IEmb208]{}
	x := rr.mk()
	f, _ := x.Get()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_embifacemixed.go
	cp internal/reader/metadata.go "$self_tree/meta208.orig"
	INS='zr = gb208()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta208.new" && mv "$self_tree/meta208.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb208()' internal/reader/metadata.go; then
		run_mut "embedded interface promotion with mixed multi-result method"
	else
		echo "self-test ERROR: form 208 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta208.orig" internal/reader/metadata.go


	# --- 209: cross-package defined struct method via generic arg ---------
	# The generic argument is a struct defined in mapping with a declared
	# file-producing method: the qualified spelling must resolve the
	# mirrored struct and its method.
	cat > internal/mapping/gatemut_crossstruct.go <<'MUTEOF'
package mapping

import "os"

type S209 struct{ F func() *os.File }

func (s S209) Get() func() *os.File { return s.F }

type Mk209[T any] struct{}

func (r Mk209[T]) Mk() T { var z T; return z }
MUTEOF
	add_mut internal/mapping/gatemut_crossstruct.go
	cat > internal/reader/gatemut_crossstruct.go <<'MUTEOF'
package reader

import (
	"io"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

type gRZ209[T any] struct{}

func (r gRZ209[T]) md() T { var z T; return z }

func gb209() io.ReadCloser {
	rr := gRZ209[mm.S209]{}
	s := rr.md()
	f := s.Get()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_crossstruct.go
	cp internal/reader/metadata.go "$self_tree/meta209.orig"
	INS='zr = gb209()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta209.new" && mv "$self_tree/meta209.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb209()' internal/reader/metadata.go; then
		run_mut "cross-package defined struct method through a generic argument"
	else
		echo "self-test ERROR: form 209 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta209.orig" internal/reader/metadata.go


	# --- 210: nine-hop qualified defined chain -----------------------------
	# A defined chain that needs the full alias/defined fixpoint (alias +
	# nine named hops): the qualified spelling mm.J210 must expand to the
	# func text so the generic result carries the file.
	cat > internal/mapping/gatemut_longchain.go <<'MUTEOF'
package mapping

import "os"

type A210 = func() *os.File
type B210 A210
type C210 B210
type D210 C210
type E210 D210
type F210 E210
type G210 F210
type H210 G210
type I210 H210
type J210 I210
MUTEOF
	add_mut internal/mapping/gatemut_longchain.go
	cat > internal/reader/gatemut_longchain.go <<'MUTEOF'
package reader

import (
	"io"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

type gRZ210[T any] struct{}

func (r gRZ210[T]) mc() T { var z T; return z }

func gb210() io.ReadCloser {
	rr := gRZ210[mm.J210]{}
	f := rr.mc()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_longchain.go
	cp internal/reader/metadata.go "$self_tree/meta210.orig"
	INS='zr = gb210()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta210.new" && mv "$self_tree/meta210.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb210()' internal/reader/metadata.go; then
		run_mut "nine-hop qualified defined chain through a generic argument"
	else
		echo "self-test ERROR: form 210 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta210.orig" internal/reader/metadata.go


	# --- 211: benign embedded interface bytes twin --------------------------
	# The same promoted-method shape over *bytes.Reader must stay benign
	# (no *os.File in the declared chain).
	cat > internal/reader/gatemut_benembiface.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type IBase211 interface{ Get() func() *bytes.Reader }

type IEmb211 interface{ IBase211 }

type gRZ211[T any] struct{}

func (r gRZ211[T]) mk() T { var z T; return z }

func gbb211() io.ReadCloser {
	rr := gRZ211[IEmb211]{}
	x := rr.mk()
	return io.NopCloser(x.Get()())
}
MUTEOF
	add_mut internal/reader/gatemut_benembiface.go
	cp internal/reader/metadata.go "$self_tree/meta211.orig"
	INS='zr = gbb211()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta211.new" && mv "$self_tree/meta211.new" internal/reader/metadata.go
	if grep -Fq 'zr = gbb211()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign embedded interface bytes method passes the gate"
		else
			echo "self-test MISS: benign embedded interface bytes method failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 211 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta211.orig" internal/reader/metadata.go


	# --- 212: benign cross-package struct method bytes twin ----------------
	# The same cross-package struct-method shape over *bytes.Reader must
	# stay benign.
	cat > internal/mapping/gatemut_bencrossstruct.go <<'MUTEOF'
package mapping

import "bytes"

type SB212 struct{ F func() *bytes.Reader }

func (s SB212) Get() func() *bytes.Reader { return s.F }

type Mk212[T any] struct{}

func (r Mk212[T]) Mk() T { var z T; return z }
MUTEOF
	add_mut internal/mapping/gatemut_bencrossstruct.go
	cat > internal/reader/gatemut_bencrossstruct.go <<'MUTEOF'
package reader

import (
	"io"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

type gRZ212[T any] struct{}

func (r gRZ212[T]) md() T { var z T; return z }

func gbb212() io.ReadCloser {
	rr := gRZ212[mm.SB212]{}
	s := rr.md()
	f := s.Get()
	return io.NopCloser(f())
}
MUTEOF
	add_mut internal/reader/gatemut_bencrossstruct.go
	cp internal/reader/metadata.go "$self_tree/meta212.orig"
	INS='zr = gbb212()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta212.new" && mv "$self_tree/meta212.new" internal/reader/metadata.go
	if grep -Fq 'zr = gbb212()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign cross-package struct method bytes twin passes the gate"
		else
			echo "self-test MISS: benign cross-package struct method bytes twin failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 212 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta212.orig" internal/reader/metadata.go


	# --- 213: renamed-qualifier cross-package interface embedding ---------
	# A reader interface embedding a mapping interface spelled with a
	# renamed import qualifier (mm.IMapBase) promotes the file-producing
	# method: the qualifier must reduce to the bare interface name
	# through the per-directory self-entry before method lookup.
	cat > internal/mapping/gatemut_rniface.go <<'MUTEOF'
package mapping

import "os"

type IMapBase213 interface{ Get() func() *os.File }
MUTEOF
	add_mut internal/mapping/gatemut_rniface.go
	cat > internal/reader/gatemut_rniface.go <<'MUTEOF'
package reader

import (
	"io"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

type IEmb213 interface{ mm.IMapBase213 }

type gRZ213[T any] struct{}

func (r gRZ213[T]) mk() T { var z T; return z }

func gb213() io.ReadCloser {
	x := gRZ213[IEmb213]{}.mk()
	return x.Get()()
}
MUTEOF
	add_mut internal/reader/gatemut_rniface.go
	cp internal/reader/metadata.go "$self_tree/meta213.orig"
	INS='zr = gb213()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta213.new" && mv "$self_tree/meta213.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb213()' internal/reader/metadata.go; then
		run_mut "renamed-qualifier cross-package interface embedding"
	else
		echo "self-test ERROR: form 213 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta213.orig" internal/reader/metadata.go


	# --- 214: generic-interface instantiation embedding, func arg ---------
	# IEmb embeds IBaseG[T any] instantiated with func() *os.File: the
	# promoted declared result carries the raw type parameter and must be
	# substituted from the embedding's type argument.
	cat > internal/reader/gatemut_geniface.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type IBaseGN214[T any] interface{ Get() T }
type IEmbGN214 interface{ IBaseGN214[func() *os.File] }

type gRZ214[T any] struct{}

func (r gRZ214[T]) mk() T { var z T; return z }

func gb214() io.ReadCloser {
	x := gRZ214[IEmbGN214]{}.mk()
	return x.Get()()
}
MUTEOF
	add_mut internal/reader/gatemut_geniface.go
	cp internal/reader/metadata.go "$self_tree/meta214.orig"
	INS='zr = gb214()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta214.new" && mv "$self_tree/meta214.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb214()' internal/reader/metadata.go; then
		run_mut "generic-interface instantiation embedding with func-file argument"
	else
		echo "self-test ERROR: form 214 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta214.orig" internal/reader/metadata.go


	# --- 215: generic-interface instantiation embedding, chan arg ---------
	# The same shape instantiated with chan func() *os.File: receive then
	# invoke must keep the chan-of-func carrier kind.
	cat > internal/reader/gatemut_genifacechan.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type IBaseGO215[T any] interface{ Get() T }
type IEmbGO215 interface{ IBaseGO215[chan func() *os.File] }

type gRZ215[T any] struct{}

func (r gRZ215[T]) mk() T { var z T; return z }

func gb215() io.ReadCloser {
	x := gRZ215[IEmbGO215]{}.mk()
	ch := x.Get()
	f := <-ch
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_genifacechan.go
	cp internal/reader/metadata.go "$self_tree/meta215.orig"
	INS='zr = gb215()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta215.new" && mv "$self_tree/meta215.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb215()' internal/reader/metadata.go; then
		run_mut "generic-interface instantiation embedding with chan-of-func argument"
	else
		echo "self-test ERROR: form 215 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta215.orig" internal/reader/metadata.go


	# --- 216: renamed-qualified generic interface instantiation -----------
	# Combined shape: a mapping generic interface spelled with a renamed
	# qualifier AND instantiated at the embedding site.
	cat > internal/mapping/gatemut_rngeniface.go <<'MUTEOF'
package mapping

type IMapBase216[T any] interface{ Get() T }
MUTEOF
	add_mut internal/mapping/gatemut_rngeniface.go
	cat > internal/reader/gatemut_rngeniface.go <<'MUTEOF'
package reader

import (
	"io"
	"os"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

type IEmb216 interface{ mm.IMapBase216[func() *os.File] }

type gRZ216[T any] struct{}

func (r gRZ216[T]) mk() T { var z T; return z }

func gb216() io.ReadCloser {
	x := gRZ216[IEmb216]{}.mk()
	return x.Get()()
}
MUTEOF
	add_mut internal/reader/gatemut_rngeniface.go
	cp internal/reader/metadata.go "$self_tree/meta216.orig"
	INS='zr = gb216()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta216.new" && mv "$self_tree/meta216.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb216()' internal/reader/metadata.go; then
		run_mut "renamed-qualified generic interface instantiation embedding"
	else
		echo "self-test ERROR: form 216 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta216.orig" internal/reader/metadata.go


	# --- 217: cross-package generic struct method via renamed qualifier ---
	# A mapping generic struct instantiated with func() *os.File and
	# invoked through the renamed qualifier: the receiver's type
	# parameters must substitute from the remote mirror.
	cat > internal/mapping/gatemut_rngenstruct.go <<'MUTEOF'
package mapping

type MkS217[T any] struct{}

func (r MkS217[T]) Mk() T { var z T; return z }
MUTEOF
	add_mut internal/mapping/gatemut_rngenstruct.go
	cat > internal/reader/gatemut_rngenstruct.go <<'MUTEOF'
package reader

import (
	"io"
	"os"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

func gb217() io.ReadCloser {
	rr := mm.MkS217[func() *os.File]{}
	f := rr.Mk()
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_rngenstruct.go
	cp internal/reader/metadata.go "$self_tree/meta217.orig"
	INS='zr = gb217()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta217.new" && mv "$self_tree/meta217.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb217()' internal/reader/metadata.go; then
		run_mut "cross-package generic struct method instantiated via renamed qualifier"
	else
		echo "self-test ERROR: form 217 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta217.orig" internal/reader/metadata.go


	# --- 218: benign renamed-qualifier interface bytes twin ---------------
	# The same promoted shape over *bytes.Reader must stay benign.
	cat > internal/mapping/gatemut_benrniface.go <<'MUTEOF'
package mapping

import "bytes"

type IMapBaseL218 interface{ Get() func() *bytes.Reader }
MUTEOF
	add_mut internal/mapping/gatemut_benrniface.go
	cat > internal/reader/gatemut_benrniface.go <<'MUTEOF'
package reader

import (
	"io"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

type IEmb218 interface{ mm.IMapBaseL218 }

type gRZ218[T any] struct{}

func (r gRZ218[T]) mk() T { var z T; return z }

func gbb218() io.ReadCloser {
	x := gRZ218[IEmb218]{}.mk()
	return io.NopCloser(x.Get()())
}
MUTEOF
	add_mut internal/reader/gatemut_benrniface.go
	cp internal/reader/metadata.go "$self_tree/meta218.orig"
	INS='zr = gbb218()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta218.new" && mv "$self_tree/meta218.new" internal/reader/metadata.go
	if grep -Fq 'zr = gbb218()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign renamed-qualifier interface bytes twin passes the gate"
		else
			echo "self-test MISS: benign renamed-qualifier interface bytes twin failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 218 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta218.orig" internal/reader/metadata.go


	# --- 219: benign generic-interface instantiation bytes twin -----------
	cat > internal/reader/gatemut_bengeniface.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type IBaseGP219[T any] interface{ Get() T }
type IEmbGP219 interface{ IBaseGP219[func() *bytes.Reader] }

type gRZ219[T any] struct{}

func (r gRZ219[T]) mk() T { var z T; return z }

func gbb219() io.ReadCloser {
	x := gRZ219[IEmbGP219]{}.mk()
	return io.NopCloser(x.Get()())
}
MUTEOF
	add_mut internal/reader/gatemut_bengeniface.go
	cp internal/reader/metadata.go "$self_tree/meta219.orig"
	INS='zr = gbb219()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta219.new" && mv "$self_tree/meta219.new" internal/reader/metadata.go
	if grep -Fq 'zr = gbb219()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign generic-interface instantiation bytes twin passes the gate"
		else
			echo "self-test MISS: benign generic-interface instantiation bytes twin failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 219 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta219.orig" internal/reader/metadata.go


	# --- 220: benign renamed-qualified generic interface bytes twin -------
	cat > internal/mapping/gatemut_benrngeniface.go <<'MUTEOF'
package mapping

type IMapBaseQ220[T any] interface{ Get() T }
MUTEOF
	add_mut internal/mapping/gatemut_benrngeniface.go
	cat > internal/reader/gatemut_benrngeniface.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

type IEmb220 interface{ mm.IMapBaseQ220[func() *bytes.Reader] }

type gRZ220[T any] struct{}

func (r gRZ220[T]) mk() T { var z T; return z }

func gbb220() io.ReadCloser {
	x := gRZ220[IEmb220]{}.mk()
	return io.NopCloser(x.Get()())
}
MUTEOF
	add_mut internal/reader/gatemut_benrngeniface.go
	cp internal/reader/metadata.go "$self_tree/meta220.orig"
	INS='zr = gbb220()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta220.new" && mv "$self_tree/meta220.new" internal/reader/metadata.go
	if grep -Fq 'zr = gbb220()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign renamed-qualified generic interface bytes twin passes the gate"
		else
			echo "self-test MISS: benign renamed-qualified generic interface bytes twin failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 220 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta220.orig" internal/reader/metadata.go


	# --- 221: benign remote generic struct bytes twin ---------------------
	cat > internal/mapping/gatemut_benrngenstruct.go <<'MUTEOF'
package mapping

type MkT221[T any] struct{}

func (r MkT221[T]) Mk() T { var z T; return z }
MUTEOF
	add_mut internal/mapping/gatemut_benrngenstruct.go
	cat > internal/reader/gatemut_benrngenstruct.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

func gbb221() io.ReadCloser {
	rr := mm.MkT221[func() *bytes.Reader]{}
	f := rr.Mk()
	return io.NopCloser(f())
}
MUTEOF
	add_mut internal/reader/gatemut_benrngenstruct.go
	cp internal/reader/metadata.go "$self_tree/meta221.orig"
	INS='zr = gbb221()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta221.new" && mv "$self_tree/meta221.new" internal/reader/metadata.go
	if grep -Fq 'zr = gbb221()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign remote generic struct bytes twin passes the gate"
		else
			echo "self-test MISS: benign remote generic struct bytes twin failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 221 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta221.orig" internal/reader/metadata.go


	# --- 222: defined type over instantiated generic interface ------------
	# type D IBaseG[func() *os.File]; type IEmb interface{ D }: the
	# embedding promotion must substitute the type arguments held in the
	# defined chain, not only in the raw embedded spelling.
	cat > internal/reader/gatemut_definst.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type IBaseGA222[T any] interface{ Get() T }
type DA222 IBaseGA222[func() *os.File]
type IEmbA222 interface{ DA222 }

type gRZ222[T any] struct{}

func (r gRZ222[T]) mk() T { var z T; return z }

func gb222() io.ReadCloser {
	x := gRZ222[IEmbA222]{}.mk()
	return x.Get()()
}
MUTEOF
	add_mut internal/reader/gatemut_definst.go
	cp internal/reader/metadata.go "$self_tree/meta222.orig"
	INS='zr = gb222()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta222.new" && mv "$self_tree/meta222.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb222()' internal/reader/metadata.go; then
		run_mut "defined type over instantiated generic interface embedding"
	else
		echo "self-test ERROR: form 222 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta222.orig" internal/reader/metadata.go


	# --- 223: renamed-qualified defined-over-instantiated interface -------
	# Combined shape: the defined hop's target spells the generic
	# interface with a renamed import qualifier.
	cat > internal/mapping/gatemut_rndefinst.go <<'MUTEOF'
package mapping

type IBaseGG223[T any] interface{ Get() T }

type Mk223[T any] struct{}

func (r Mk223[T]) Mk() T { var z T; return z }
MUTEOF
	add_mut internal/mapping/gatemut_rndefinst.go
	cat > internal/reader/gatemut_rndefinst.go <<'MUTEOF'
package reader

import (
	"io"
	"os"

	mm "github.com/firehol/iprange/v4/go/internal/mapping"
)

type DG223 mm.IBaseGG223[func() *os.File]
type IEmbG223 interface{ DG223 }

type gRZ223[T any] struct{}

func (r gRZ223[T]) mk() T { var z T; return z }

func gb223() io.ReadCloser {
	x := gRZ223[IEmbG223]{}.mk()
	return x.Get()()
}
MUTEOF
	add_mut internal/reader/gatemut_rndefinst.go
	cp internal/reader/metadata.go "$self_tree/meta223.orig"
	INS='zr = gb223()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta223.new" && mv "$self_tree/meta223.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb223()' internal/reader/metadata.go; then
		run_mut "renamed-qualified defined-over-instantiated generic interface embedding"
	else
		echo "self-test ERROR: form 223 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta223.orig" internal/reader/metadata.go


	# --- 224: benign defined-over-instantiated interface bytes twin -------
	cat > internal/reader/gatemut_bandefinst.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type IBaseGH224[T any] interface{ Get() T }
type DH224 IBaseGH224[func() *bytes.Reader]
type IEmbH224 interface{ DH224 }

type gRZ224[T any] struct{}

func (r gRZ224[T]) mk() T { var z T; return z }

func gbb224() io.ReadCloser {
	x := gRZ224[IEmbH224]{}.mk()
	return io.NopCloser(x.Get()())
}
MUTEOF
	add_mut internal/reader/gatemut_bandefinst.go
	cp internal/reader/metadata.go "$self_tree/meta224.orig"
	INS='zr = gbb224()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta224.new" && mv "$self_tree/meta224.new" internal/reader/metadata.go
	if grep -Fq 'zr = gbb224()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign defined-over-instantiated interface bytes twin passes the gate"
		else
			echo "self-test MISS: benign defined-over-instantiated interface bytes twin failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 224 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta224.orig" internal/reader/metadata.go


	# --- 225: multi-level generic-interface instantiation embedding -------
	# type InnerL[T] interface{ Get() T }; type IBaseGL[T] interface{
	# InnerL[T] }; type IEmbL interface{ IBaseGL[func() *os.File] }: the
	# embedding walk must thread the instantiation through every frame so
	# the declaring frame substitutes the accumulated arguments.
	cat > internal/reader/gatemut_nestedgen.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type InnerL225[T any] interface{ Get() T }
type IBaseGL225[T any] interface{ InnerL225[T] }
type IEmbL225 interface{ IBaseGL225[func() *os.File] }

type gRZ225[T any] struct{}

func (r gRZ225[T]) mk() T { var z T; return z }

func gb225() io.ReadCloser {
	x := gRZ225[IEmbL225]{}.mk()
	return x.Get()()
}
MUTEOF
	add_mut internal/reader/gatemut_nestedgen.go
	cp internal/reader/metadata.go "$self_tree/meta225.orig"
	INS='zr = gb225()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta225.new" && mv "$self_tree/meta225.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb225()' internal/reader/metadata.go; then
		run_mut "multi-level generic-interface instantiation embedding"
	else
		echo "self-test ERROR: form 225 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta225.orig" internal/reader/metadata.go


	# --- 226: three-level generic-interface instantiation embedding ------
	# One more frame (MidX[T] above InnerX[T]) with the chan-of-func
	# argument at the top frame: receive then invoke.
	cat > internal/reader/gatemut_nestedgen3.go <<'MUTEOF'
package reader

import (
	"io"
	"os"
)

type InnerX226[T any] interface{ Get() T }
type MidX226[T any] interface{ InnerX226[T] }
type TopX226 interface{ MidX226[chan func() *os.File] }

type gRZ226[T any] struct{}

func (r gRZ226[T]) mk() T { var z T; return z }

func gb226() io.ReadCloser {
	x := gRZ226[TopX226]{}.mk()
	ch := x.Get()
	f := <-ch
	return f()
}
MUTEOF
	add_mut internal/reader/gatemut_nestedgen3.go
	cp internal/reader/metadata.go "$self_tree/meta226.orig"
	INS='zr = gb226()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta226.new" && mv "$self_tree/meta226.new" internal/reader/metadata.go
	if grep -Fq 'zr = gb226()' internal/reader/metadata.go; then
		run_mut "three-level generic-interface instantiation embedding with chan argument"
	else
		echo "self-test ERROR: form 226 insert did not take"
		mutfail=1
		cleanup_muts
	fi
	unset INS
	cp "$self_tree/meta226.orig" internal/reader/metadata.go


	# --- 227: benign multi-level generic-interface bytes twin ------------
	cat > internal/reader/gatemut_bennestedgen.go <<'MUTEOF'
package reader

import (
	"bytes"
	"io"
)

type InnerXB227[T any] interface{ Get() T }
type MidXB227[T any] interface{ InnerXB227[T] }
type TopXB227 interface{ MidXB227[func() *bytes.Reader] }

type gRZ227[T any] struct{}

func (r gRZ227[T]) mk() T { var z T; return z }

func gbb227() io.ReadCloser {
	x := gRZ227[TopXB227]{}.mk()
	return io.NopCloser(x.Get()())
}
MUTEOF
	add_mut internal/reader/gatemut_bennestedgen.go
	cp internal/reader/metadata.go "$self_tree/meta227.orig"
	INS='zr = gbb227()'
	export INS
	awk '{ if (index($0, "zr := flate.NewReader(cr)")) { print; print "\t" ENVIRON["INS"]; next } print }' internal/reader/metadata.go > "$self_tree/meta227.new" && mv "$self_tree/meta227.new" internal/reader/metadata.go
	if grep -Fq 'zr = gbb227()' internal/reader/metadata.go; then
		if GATE_SCANNER_BIN="$scanner_bin" ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test OK: benign multi-level generic-interface bytes twin passes the gate"
		else
			echo "self-test MISS: benign multi-level generic-interface bytes twin failed the gate (false positive)"
			mutfail=1
		fi
	else
		echo "self-test ERROR: form 227 insert did not take"
		mutfail=1
	fi
	unset INS
	cleanup_muts
	cp "$self_tree/meta227.orig" internal/reader/metadata.go

	if [ "$mutfail" -ne 0 ]; then
		echo "import-graph self-test FAILED"
		exit 1
	fi
	echo "import-graph self-test passed (all 178 mutation forms rejected)"
fi

if [ "$fail" -ne 0 ]; then
	echo "import-graph check FAILED"
	exit 1
fi
echo "import-graph check passed"
