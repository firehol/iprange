#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

mkdir "$tmpdir/dir"
printf '10.0.0.1\n' >"$tmpdir/dir/z-last"
printf '10.0.0.1\n10.0.0.2\n' >"$tmpdir/dir/a-first"
printf '10.0.0.1\n' >"$tmpdir/direct"

../../iprange --header --count-unique-all "@$tmpdir/dir" >"$tmpdir/count"
../../iprange --header "@$tmpdir/dir" --compare-next "$tmpdir/direct" >"$tmpdir/compare" 2>"$tmpdir/compare.err"

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
