#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

printf '!\n' >"$tmpdir/invalid.txt"
../../iprange "$tmpdir/invalid.txt" >"$tmpdir/invalid.out" 2>"$tmpdir/invalid.err"
rc=$?

if [ $rc -eq 0 ]; then
    echo "# ERROR: invalid input should fail with non-zero exit"
    cat "$tmpdir/invalid.err"
    exit 1
fi

if [ -s "$tmpdir/invalid.out" ]; then
    echo "# ERROR: invalid input should not produce stdout"
    cat "$tmpdir/invalid.out"
    exit 1
fi

if ! grep -q "Cannot understand line No 1" "$tmpdir/invalid.err"; then
    echo "# ERROR: expected parse error message for invalid input"
    cat "$tmpdir/invalid.err"
    exit 1
fi

printf '1.2.3.4\n!\n' >"$tmpdir/mixed.txt"
../../iprange "$tmpdir/mixed.txt" >"$tmpdir/mixed.out" 2>"$tmpdir/mixed.err"
rc=$?

if [ $rc -eq 0 ]; then
    echo "# ERROR: mixed valid and invalid input should still fail"
    cat "$tmpdir/mixed.err"
    exit 1
fi

if [ -s "$tmpdir/mixed.out" ]; then
    echo "# ERROR: mixed valid and invalid input should not produce partial stdout"
    cat "$tmpdir/mixed.out"
    exit 1
fi

if ! grep -q "Cannot understand line No 2" "$tmpdir/mixed.err"; then
    echo "# ERROR: expected parse error message for mixed input"
    cat "$tmpdir/mixed.err"
    exit 1
fi

echo "# OK: parse errors now fail the command"
