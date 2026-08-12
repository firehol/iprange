#!/bin/sh
# check-import-graph.sh — enforce the v4 Go import boundaries.
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
# banned from content-transfer I/O (Read/Write/ReadAt/WriteAt/Pread/Pwrite/
# ReadFile/WriteFile) so the mmap-only contract cannot regress with a future
# commit, and the stdlib syscall package is banned everywhere (x/sys is the
# mapping owner's syscall surface).
#
# Usage: ./check-import-graph.sh   (run from the v4/go directory)

set -eu
cd "$(dirname "$0")"

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

# Content-transfer I/O ban in production sources: persistent artifact bytes
# must never move through read/write/seek APIs (mmap-only contract). Test-only
# fixture builders are exempt. Comments (line and multi-line block, and //
# inside string literals) are stripped by a stateful awk before matching.
# In-memory decompression readers are exempt: the metadata inflater's
# consumedReader reads a heap buffer, not SDK artifact content — the one
# production .Read() call the tree allows.
strip_comments() {
	# awk state machine: strips /* */ and // comments but keeps string
	# literals intact (a "//" inside a double-quoted string or a raw
	# backtick string is data, not a comment), so a real content-transfer
	# call after a string containing "//" is still detected.
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

# content_violations prints every production source line that still mentions
# a content-transfer selector after comment stripping. The strip runs on
# whole files first: grep-then-strip would lose the block-comment state
# across lines. The directory discovery comes from `go list` (all packages,
# including any added later), so a new production package cannot escape the
# scan. The word-boundary match catches direct calls, method values
# (m := f.Read), function aliases (rd := io.ReadAll), and Seek, not only
# parenthesized calls.
content_violations() {
	for dir in $(go list -f '{{.Dir}}' ./... | sort -u); do
		for f in "$dir"/*.go; do
			[ -f "$f" ] || continue
			case "$f" in *_test.go) continue ;; esac
			strip_comments < "$f" | sed "s@^@$f:@" | grep -v '^[^:]*:.*c\.r\.Read('
		done
	done
}
if [ -n "$(content_violations | grep -E '\.(Read|Write|Seek|ReadAt|WriteAt|Pread|Pwrite|ReadFile|WriteFile|ReadAll|Copy)\b')" ]; then
	echo "content-transfer I/O violation in production sources"
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

if [ "$fail" -ne 0 ]; then
	echo "import-graph check FAILED"
	exit 1
fi
echo "import-graph check passed"
