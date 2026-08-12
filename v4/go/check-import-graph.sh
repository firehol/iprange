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
#   - internal/exactv4 is the legacy tree awaiting the approved deletion and
#     is intentionally unconstrained; it is removed with the deletion set.
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
# The module root (public facade) imports internal/format + internal/reader,
# plus the legacy internal/exactv4 scalar aliases (types.go/errors.go) that
# are the transfer point until the approved deletion set lands.
check "github.com/firehol/iprange/v4/go" "github.com/firehol/iprange/v4/go/internal/\(format\|reader\|exactv4\)"

# The reader core is the synchronization-free zone: no sync, sync/atomic, or
# unsafe anywhere in its import closure.
for pkg in "github.com/firehol/iprange/v4/go/internal/format" \
		"github.com/firehol/iprange/v4/go/internal/reader"; do
	if printf '%s\n' "$(pkg_imports "$pkg")" | grep -qE '^(sync|sync/atomic|unsafe)$'; then
		echo "synchronization/unsafe violation: $pkg"
		fail=1
	fi
done

# Only the reader may hold the mapping, and only the reader core may be
# consumed by the facade.
for pkg in $(go list ./... | grep -v '^github.com/firehol/iprange/v4/go$' \
		| grep -v '^github.com/firehol/iprange/v4/go/internal/exactv4$' \
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
