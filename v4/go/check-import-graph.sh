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
# banned from content-transfer I/O so the mmap-only contract cannot regress:
#   - selector ban: any .Read/.Write/.Seek-family selector (direct calls,
#     method values, function aliases, wrappers) anywhere in a production
#     source file, including build-tagged files on every platform and files
#     in packages added later (discovery is a whole-tree find, not a fixed
#     directory list);
#   - dot-import ban: a dot import hides the package qualifier from the
#     selector scan;
#   - buffered-IO import ban: bufio wraps *os.File reads behind methods the
#     selector scan does not enumerate (Peek, NewReader, ...), and the
#     SDK has no legitimate bufio use (metadata decompression uses flate
#     over an in-memory reader, which is exempt);
#   - stdlib syscall is banned everywhere (x/sys is the mapping owner's
#     syscall surface), and the reader core carries no
#     sync/sync/atomic/unsafe.
#
# In-memory decompression readers are exempt: the metadata inflater's
# consumedReader reads a heap buffer, not SDK artifact content — the one
# production .Read() call the tree allows.
#
# The gate is a mechanical tripwire, not a proof: it catches the transfer
# forms listed above. Runtime tracing of an actual open/lookup session
# (openat -> OFD lock -> mmap -> munmap -> unlock/close with no
# read/pread/readv/lseek on the database descriptor) is recorded in the
# milestone report as the runtime half of the mmap-only evidence.
#
# Usage: ./check-import-graph.sh [--self-test]   (run from the v4/go
# directory). --self-test creates temporary mutation packages, asserts the
# gate rejects every one, removes them, and exits nonzero on any miss.

set -eu
cd "$(dirname "$0")"

self_test=0
if [ "${1:-}" = "--self-test" ]; then
	self_test=1
fi

fail=0

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

# strip_comments removes line and multi-line block comments while keeping
# string literals intact (a "//" inside a double-quoted string or a raw
# backtick string is data, not a comment), so a real content-transfer call
# after a string containing "//" is still detected.
strip_comments() {
	awk '
	function strip_line(line,   out, i, n, c) {
		out = ""
		n = length(line)
		i = 1
		while (i <= n) {
			c = substr(line, i, 1)
			if (inblock) {
				if (c == "*" && substr(line, i + 1, 1) == "/") { inblock = 0; i += 2; continue }
				i++; continue
			}
			if (in_str) {
				out = out c
				if (c == "\\") { out = out substr(line, i + 1, 1); i += 2; continue }
				if (c == "\"") in_str = 0
				i++; continue
			}
			if (in_raw) {
				out = out c
				if (c == "`") in_raw = 0
				i++; continue
			}
			if (c == "/" && substr(line, i + 1, 1) == "*") { inblock = 1; i += 2; continue }
			if (c == "/" && substr(line, i + 1, 1) == "/") { break }
			if (c == "\"") in_str = 1
			if (c == "`") in_raw = 1
			out = out c
			i++
		}
		return out
	}
	BEGIN { inblock = 0; in_str = 0; in_raw = 0 }
	{ print strip_line($0) }
	'
}

# content_violations prints every production source line, after comment
# stripping, that either mentions a content-transfer selector, uses a dot
# import, or imports bufio/io/ioutil. Discovery is a whole-tree find over
# every .go file (excluding _test.go), so build-tagged files for other
# platforms and packages added later are scanned regardless of the active
# GOOS/GOARCH.
content_violations() {
	find . -name '*.go' -not -name '*_test.go' -print | sort | while read -r f; do
		strip_comments < "$f" | sed "s@^@$f:@" | grep -v '^[^:]*:.*c\.r\.Read\(Byte\)\?('
	done
}

violations=$(content_violations)

# Content-transfer selector ban: word boundary after each name catches
# direct calls (f.Read(x)), method values (m := f.Read), function aliases
# (rd := io.ReadAll), wrapper methods (bufio ... .ReadByte()), and Seek.
# The set covers the read/write/seek language API families including the
# x/sys descriptor variants (Readv/Writev/Preadv/Pwritev).
if printf '%s\n' "$violations" | grep -E '\.(Read|Write|Seek|Pread|Pwrite|Readv|Writev|Preadv|Pwritev|ReadAt|WriteAt|ReadFile|WriteFile|ReadAll|Copy|ReadByte|WriteByte|ReadRune|ReadFrom|WriteTo|ReadString|ReadLine|Peek)\b'; then
	echo "content-transfer I/O violation in production sources"
	fail=1
fi

# Dot-import ban: an unqualified call (ReadFile(path)) would hide its
# package qualifier from the selector scan.
if printf '%s\n' "$violations" | grep -E '^[^:]*:[[:space:]]*(import[[:space:]]*)?\.'; then
	echo "dot-import violation in production sources"
	fail=1
fi

# Buffered-IO import ban: bufio (and the deprecated io/ioutil) wrap *os.File
# behind methods not enumerated above; the SDK has no legitimate use.
if printf '%s\n' "$violations" | grep -E '(^|[[:space:]()])"(bufio|io/ioutil)"'; then
	echo "buffered-IO import violation in production sources"
	fail=1
fi

# Only the reader may hold the mapping, and only the reader core may be
# consumed by the facade.
for pkg in $(go list ./... | grep -v '^github.com/firehol/iprange/v4/go$' \
		| grep -v '^github.com/firehol/iprange/v4/go/internal/format$' \
		| grep -v '^github.com/firehol/iprange/v4/go/internal/mapping$' \
		| grep -v '^github.com/firehol/iprange/v4/go/internal/reader$'); do
	case "$(pkg_imports "$pkg")" in
	*"github.com/firehol/iprange/v4/go/internal"*)
		echo "boundary violation: $pkg imports internal packages"
		fail=1
		;;
	esac
done

if [ "$self_test" -eq 1 ]; then
	# Durable mutation self-test: every transfer form below must make the
	# gate fail. Each mutation is created, checked, and removed on its own
	# so a miss points at exactly one form.
	cleanup() {
		rm -rf gatemut_readall gatemut_alias gatemut_methodval gatemut_seek \
			gatemut_newdir gatemut_bufio gatemut_dotimport gatemut_winfile
		rm -f internal/mapping/gatemut_readv.go \
			gatemut_singleline_bufio.go gatemut_aliased_bufio.go
	}
	trap cleanup EXIT INT TERM

	mutfail=0

	run_mut() {
		name=$1
		if ./check-import-graph.sh >/dev/null 2>&1; then
			echo "self-test MISS: mutation $name did not fail the gate"
			mutfail=1
		fi
		cleanup
	}

	mkdir -p gatemut_readall
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

	mkdir -p gatemut_alias
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

	mkdir -p gatemut_methodval
	cat > gatemut_methodval/mut.go <<'MUTEOF'
package gatemut_methodval

import "os"

var file *os.File

var m = file.Read

func use() { var b []byte; _ = m(b) }
MUTEOF
	run_mut "os.File.Read method value"

	mkdir -p gatemut_seek
	cat > gatemut_seek/mut.go <<'MUTEOF'
package gatemut_seek

import "os"

var file *os.File

func use() { _, _ = file.Seek(0, 0) }
MUTEOF
	run_mut "os.File.Seek call"

	mkdir -p gatemut_newdir
	cat > gatemut_newdir/mut.go <<'MUTEOF'
package gatemut_newdir

import "os"

func read(p string) ([]byte, error) { return os.ReadFile(p) }
MUTEOF
	run_mut "os.ReadFile in a new package directory"

	cat > internal/mapping/gatemut_readv.go <<'MUTEOF'
package mapping

import "golang.org/x/sys/unix"

func readv(fd int, b [][]byte) (int, error) { return unix.Readv(fd, b) }
MUTEOF
	run_mut "unix.Readv descriptor read in the mapping owner"

	mkdir -p gatemut_bufio
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

	mkdir -p gatemut_dotimport
	cat > gatemut_dotimport/mut.go <<'MUTEOF'
package gatemut_dotimport

import . "os"

func read(p string) ([]byte, error) { return ReadFile(p) }
MUTEOF
	run_mut "dot-imported os.ReadFile"

	cat > gatemut_singleline_bufio.go <<'MUTEOF'
package iprangedb

import "bufio"

var br *bufio.Reader

func peek() ([]byte, error) { return br.Peek(1) }
MUTEOF
	run_mut "single-line bufio import with Peek"

	cat > gatemut_aliased_bufio.go <<'MUTEOF'
package iprangedb

import b "bufio"

var br *b.Reader

func peek() ([]byte, error) { return br.Peek(1) }
MUTEOF
	run_mut "aliased bufio import with Peek"

	mkdir -p gatemut_winfile
	cat > gatemut_winfile/mut.go <<'MUTEOF'
//go:build windows

package gatemut_winfile

import "os"

func read(p string) ([]byte, error) { return os.ReadFile(p) }
MUTEOF
	run_mut "windows-only package with os.ReadFile"

	if [ "$mutfail" -ne 0 ]; then
		echo "import-graph self-test FAILED"
		exit 1
	fi
	echo "import-graph self-test passed (all 11 mutation forms rejected)"
fi

if [ "$fail" -ne 0 ]; then
	echo "import-graph check FAILED"
	exit 1
fi
echo "import-graph check passed"
