#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

seq 1 100000 | awk '{ value = $1 - 1; printf("10.%d.%d.%d\n", int(value / 32768), int((value % 32768) / 128), ((value % 128) * 2) + 1) }' > "$tmpdir/many"

bash -lc 'set -o pipefail; ../../iprange "$1" --print-binary | head -c 1 >/dev/null' _ "$tmpdir/many"
rc=$?

if [ $rc -eq 0 ]; then
    echo "# ERROR: broken pipe should not look successful"
    exit 1
fi

echo "# OK: broken pipe returns non-zero"
