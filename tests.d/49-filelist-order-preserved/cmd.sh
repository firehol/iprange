#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

printf '10.0.0.1\n' >"$tmpdir/a-first"
printf '10.0.0.1\n10.0.0.2\n' >"$tmpdir/z-last"
printf '10.0.0.1\n' >"$tmpdir/direct"
printf '%s\n%s\n' "$tmpdir/a-first" "$tmpdir/z-last" >"$tmpdir/list"

../../iprange --header --count-unique-all "@$tmpdir/list" >"$tmpdir/count"
../../iprange --header "@$tmpdir/list" --compare-next "$tmpdir/direct" >"$tmpdir/compare" 2>"$tmpdir/compare.err"

if [ -s "$tmpdir/compare.err" ]; then
    echo "# ERROR: unexpected stderr from compare-next"
    cat "$tmpdir/compare.err"
    exit 1
fi

{
    echo "# count-unique-all"
    sed "s#$tmpdir#TMP#g" "$tmpdir/count"
    echo "# compare-next"
    sed "s#$tmpdir#TMP#g" "$tmpdir/compare"
} 
