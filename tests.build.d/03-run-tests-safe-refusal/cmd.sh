#!/bin/bash

tmpdir=$(mktemp -d)
log="$tmpdir/run-tests.log"
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
trap 'rm -rf "$tmpdir"' EXIT

mkdir -p "$tmpdir/tests.d/min"
cp "$script_dir/../../run-tests.sh" "$tmpdir/run-tests.sh"

printf '#!/bin/sh\necho original-iprange\n' >"$tmpdir/iprange"
chmod +x "$tmpdir/iprange"

printf '#!/bin/sh\necho external-iprange\n' >"$tmpdir/other-iprange"
chmod +x "$tmpdir/other-iprange"

printf '#!/bin/sh\nexit 0\n' >"$tmpdir/tests.d/min/cmd.sh"
chmod +x "$tmpdir/tests.d/min/cmd.sh"

: >"$tmpdir/tests.d/min/output"
original_contents=$(cat "$tmpdir/iprange")

if ! IPRANGE_BIN="$tmpdir/other-iprange" TEST_DIRS=tests.d "$tmpdir/run-tests.sh" >"$log" 2>&1; then
    echo "run-tests.sh failed unexpectedly"
    cat "$log"
    exit 1
fi

if ! grep -q 'Passed tests: 1' "$log"; then
    echo "run-tests.sh did not complete the embedded test successfully"
    cat "$log"
    exit 1
fi

if [ ! -f "$tmpdir/iprange" ] || [ -L "$tmpdir/iprange" ]; then
    echo "run-tests.sh removed or replaced the existing iprange file"
    cat "$log"
    exit 1
fi

if [ "$(cat "$tmpdir/iprange")" != "$original_contents" ]; then
    echo "run-tests.sh did not restore the original iprange file contents"
    cat "$log"
    exit 1
fi

echo "# OK: run-tests preserves and restores an existing iprange file"
