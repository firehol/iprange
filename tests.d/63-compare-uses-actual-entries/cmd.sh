#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

binary="$tmpdir/fake-lines.bin"
text="$tmpdir/text.txt"

printf '4.3.2.1\n' | ../../iprange --print-binary >"$binary"
perl -0pi -e 's/lines 1\n/lines 999\n/' "$binary"

printf '4.3.2.5\n' >"$text"

../../iprange "$binary" --compare-next "$text" --header >"$tmpdir/out" 2>"$tmpdir/err"
rc=$?

if [ $rc -ne 0 ]; then
    echo "# ERROR: compare mode should succeed on valid inputs"
    cat "$tmpdir/err"
    exit 1
fi

if [ -s "$tmpdir/err" ]; then
    echo "# ERROR: compare mode emitted unexpected stderr"
    cat "$tmpdir/err"
    exit 1
fi

cat >"$tmpdir/expected" <<EOF
name1,name2,entries1,entries2,ips1,ips2,combined_ips,common_ips
$binary,$text,1,1,1,1,2,0
EOF

if ! diff -u "$tmpdir/expected" "$tmpdir/out"; then
    echo "# ERROR: compare mode should report actual entry counts, not forged line metadata"
    exit 1
fi

echo "# OK: compare mode uses actual entries"
