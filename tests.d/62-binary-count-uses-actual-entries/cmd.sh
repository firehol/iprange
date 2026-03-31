#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

file="$tmpdir/fake-lines.bin"

{
    printf 'iprange binary format v1.0\n'
    printf 'optimized\n'
    printf 'record size 8\n'
    printf 'records 1\n'
    printf 'bytes 12\n'
    printf 'lines 999\n'
    printf 'unique ips 1\n'
    perl -e 'print pack("V", 0x1A2B3C4D), pack("V", 0x04030201), pack("V", 0x04030201)'
} >"$file"

../../iprange "$file" --count-unique --header >"$tmpdir/count.out" 2>"$tmpdir/count.err"
rc=$?

if [ $rc -ne 0 ]; then
    echo "# ERROR: count mode should succeed on valid binary payload"
    cat "$tmpdir/count.err"
    exit 1
fi

if [ -s "$tmpdir/count.err" ]; then
    echo "# ERROR: count mode emitted unexpected stderr"
    cat "$tmpdir/count.err"
    exit 1
fi

cat >"$tmpdir/expected" <<EOF
entries,unique_ips
1,1
EOF

if ! diff -u "$tmpdir/expected" "$tmpdir/count.out"; then
    echo "# ERROR: count mode should report actual entry counts, not forged line metadata"
    exit 1
fi

echo "# OK: count mode uses actual entries"
