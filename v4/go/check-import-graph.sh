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

	if [ "$mutfail" -ne 0 ]; then
		echo "import-graph self-test FAILED"
		exit 1
	fi
	echo "import-graph self-test passed (all 73 mutation forms rejected)"
fi

if [ "$fail" -ne 0 ]; then
	echo "import-graph check FAILED"
	exit 1
fi
echo "import-graph check passed"
