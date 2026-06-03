#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

file="$tmpdir/fake-lines.bin"

printf '4.3.2.1\n' | ../../iprange --print-binary >"$file"
perl -0pi -e 's/lines 1\n/lines 999\n/' "$file"

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
