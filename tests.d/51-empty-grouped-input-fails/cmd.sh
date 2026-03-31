#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

printf '10.0.0.1\n' >"$tmpdir/good.txt"
printf '10.0.0.1\n' >"$tmpdir/direct"
: >"$tmpdir/empty.list"
mkdir "$tmpdir/emptydir"

../../iprange "$tmpdir/good.txt" "@$tmpdir/empty.list" >"$tmpdir/list.out" 2>"$tmpdir/list.err"
list_rc=$?

../../iprange "$tmpdir/good.txt" "@$tmpdir/emptydir" >"$tmpdir/dir.out" 2>"$tmpdir/dir.err"
dir_rc=$?

if [ $list_rc -eq 0 ]; then
    echo "# ERROR: empty file list should fail"
    cat "$tmpdir/list.out"
    cat "$tmpdir/list.err"
    exit 1
fi

if [ $dir_rc -eq 0 ]; then
    echo "# ERROR: empty directory should fail"
    cat "$tmpdir/dir.out"
    cat "$tmpdir/dir.err"
    exit 1
fi

if ! grep -q "No valid files found in file list" "$tmpdir/list.err"; then
    echo "# ERROR: missing empty file list error"
    cat "$tmpdir/list.err"
    exit 1
fi

if ! grep -q "No valid files found in directory" "$tmpdir/dir.err"; then
    echo "# ERROR: missing empty directory error"
    cat "$tmpdir/dir.err"
    exit 1
fi

echo "# OK: empty grouped inputs fail with non-zero status"
