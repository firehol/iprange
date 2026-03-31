#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

if ! ../../iprange --has-directory-loading >/dev/null 2>"$tmpdir/hasdir.err"; then
    echo "# ERROR: --has-directory-loading should succeed for update-ipsets"
    cat "$tmpdir/hasdir.err"
    exit 1
fi

mkdir "$tmpdir/new"
printf '10.0.0.1\n' >"$tmpdir/latest"
printf '10.0.0.1\n10.0.0.2\n' >"$tmpdir/new/100"
printf '10.0.0.3\n' >"$tmpdir/new/200"

if ! ../../iprange "$tmpdir/latest" --compare-next "@$tmpdir/new" >"$tmpdir/compare.out" 2>"$tmpdir/compare.err"; then
    echo "# ERROR: compare-next @directory failed"
    cat "$tmpdir/compare.err"
    exit 1
fi

if ! ../../iprange --count-unique-all "@$tmpdir/new" >"$tmpdir/count.out" 2>"$tmpdir/count.err"; then
    echo "# ERROR: count-unique-all @directory failed"
    cat "$tmpdir/count.err"
    exit 1
fi

if [ -s "$tmpdir/compare.err" ] || [ -s "$tmpdir/count.err" ]; then
    echo "# ERROR: @directory retention commands should not emit stderr"
    cat "$tmpdir/compare.err"
    cat "$tmpdir/count.err"
    exit 1
fi

cat >"$tmpdir/expected.compare" <<EOF
$tmpdir/latest,$tmpdir/new/100,1,1,1,2,2,1
$tmpdir/latest,$tmpdir/new/200,1,1,1,1,2,0
EOF

cat >"$tmpdir/expected.count" <<EOF
$tmpdir/new/100,1,2
$tmpdir/new/200,1,1
EOF

if ! diff -u "$tmpdir/expected.compare" "$tmpdir/compare.out"; then
    echo "# ERROR: compare-next @directory CSV contract changed"
    exit 1
fi

if ! diff -u "$tmpdir/expected.count" "$tmpdir/count.out"; then
    echo "# ERROR: count-unique-all @directory CSV contract changed"
    exit 1
fi

echo "# OK: update-ipsets directory retention CSV contracts work"
