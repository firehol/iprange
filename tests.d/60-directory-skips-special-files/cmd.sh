#!/bin/bash

tmpdir=$(mktemp -d)
stdout="$tmpdir/stdout"
stderr="$tmpdir/stderr"
trap 'rm -rf "$tmpdir"' EXIT

printf '1.2.3.4\n' >"$tmpdir/good.txt"
mkfifo "$tmpdir/pipe"

timeout 2 ../../iprange "@$tmpdir" >"$stdout" 2>"$stderr"
rc=$?

if [ $rc -ne 0 ]; then
    echo "# ERROR: @directory should skip FIFOs and complete successfully"
    echo "# rc=$rc"
    cat "$stderr"
    exit 1
fi

if [ -s "$stderr" ]; then
    echo "# ERROR: skipping special files should not emit errors"
    cat "$stderr"
    exit 1
fi

if ! grep -qx '1.2.3.4' "$stdout"; then
    echo "# ERROR: expected only the regular file to be loaded"
    cat "$stdout"
    exit 1
fi

echo "# OK: @directory skips FIFOs and other special files"
