#!/bin/bash

tmpdir=$(mktemp -d)
log="$tmpdir/run-tests.log"
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
trap 'rm -rf "$tmpdir"' EXIT

mkdir -p "$tmpdir/tests.d/min"
cp "$script_dir/../../run-tests.sh" "$tmpdir/run-tests.sh"

printf '#!/bin/sh\nexit 0\n' >"$tmpdir/iprange"
chmod +x "$tmpdir/iprange"

printf '#!/bin/sh\nexit 0\n' >"$tmpdir/other-iprange"
chmod +x "$tmpdir/other-iprange"

printf '#!/bin/sh\nexit 0\n' >"$tmpdir/tests.d/min/cmd.sh"
chmod +x "$tmpdir/tests.d/min/cmd.sh"

: >"$tmpdir/tests.d/min/output"

if IPRANGE_BIN="$tmpdir/other-iprange" TEST_DIRS=tests.d "$tmpdir/run-tests.sh" >"$log" 2>&1; then
    echo "run-tests.sh unexpectedly succeeded"
    cat "$log"
    exit 1
fi

if ! grep -q 'cannot replace existing non-symlink' "$log"; then
    echo "run-tests.sh did not report the expected refusal"
    cat "$log"
    exit 1
fi

if [ ! -f "$tmpdir/iprange" ] || [ -L "$tmpdir/iprange" ]; then
    echo "run-tests.sh removed or replaced the existing iprange file"
    cat "$log"
    exit 1
fi

echo "# OK: run-tests refusal keeps the existing iprange file intact"
