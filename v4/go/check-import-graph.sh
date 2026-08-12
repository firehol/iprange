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

	if [ "$mutfail" -ne 0 ]; then
		echo "import-graph self-test FAILED"
		exit 1
	fi
	echo "import-graph self-test passed (all 47 mutation forms rejected)"
fi

if [ "$fail" -ne 0 ]; then
	echo "import-graph check FAILED"
	exit 1
fi
echo "import-graph check passed"
