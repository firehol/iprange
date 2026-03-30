#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

cat >"$tmpdir/latest" <<'EOF'
10.0.0.0/24
10.0.1.0/24
EOF

cat >"$tmpdir/update" <<'EOF'
10.0.1.0/24
10.0.2.0/24
EOF

exclude_plain=$(../../iprange "$tmpdir/latest" --exclude-next "$tmpdir/update" 2>"$tmpdir/exclude_plain.err" | ../../iprange -C 2>"$tmpdir/exclude_plain_count.err")
if [ $? -ne 0 ]; then
    echo "# ERROR: exclude-next to count pipeline failed"
    cat "$tmpdir/exclude_plain.err"
    cat "$tmpdir/exclude_plain_count.err"
    exit 1
fi

exclude_binary=$(../../iprange "$tmpdir/latest" --exclude-next "$tmpdir/update" --print-binary 2>"$tmpdir/exclude_binary.err" | ../../iprange -C 2>"$tmpdir/exclude_binary_count.err")
if [ $? -ne 0 ]; then
    echo "# ERROR: exclude-next --print-binary pipeline failed"
    cat "$tmpdir/exclude_binary.err"
    cat "$tmpdir/exclude_binary_count.err"
    exit 1
fi

common_binary=$(../../iprange --common "$tmpdir/latest" "$tmpdir/update" --print-binary 2>"$tmpdir/common_binary.err" | ../../iprange -C 2>"$tmpdir/common_binary_count.err")
if [ $? -ne 0 ]; then
    echo "# ERROR: common --print-binary pipeline failed"
    cat "$tmpdir/common_binary.err"
    cat "$tmpdir/common_binary_count.err"
    exit 1
fi

for err in \
    "$tmpdir/exclude_plain.err" \
    "$tmpdir/exclude_plain_count.err" \
    "$tmpdir/exclude_binary.err" \
    "$tmpdir/exclude_binary_count.err" \
    "$tmpdir/common_binary.err" \
    "$tmpdir/common_binary_count.err"
do
    if [ -s "$err" ]; then
        echo "# ERROR: retention binary/plain set operations should not emit stderr"
        cat "$err"
        exit 1
    fi
done

if [ "$exclude_plain" != "1,256" ]; then
    echo "# ERROR: exclude-next plain-text count contract changed"
    echo "$exclude_plain"
    exit 1
fi

if [ "$exclude_binary" != "1,256" ]; then
    echo "# ERROR: exclude-next binary count contract changed"
    echo "$exclude_binary"
    exit 1
fi

if [ "$common_binary" != "1,256" ]; then
    echo "# ERROR: common binary count contract changed"
    echo "$common_binary"
    exit 1
fi

echo "# OK: update-ipsets retention binary operations keep their contracts"
