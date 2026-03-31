#!/bin/bash

tmpdir=$(mktemp -d)
stderr="$tmpdir/stderr"
trap 'rm -rf "$tmpdir"' EXIT

cat > "$tmpdir/a" <<EOF
10.0.0.1
EOF

cat > "$tmpdir/b" <<EOF
10.0.0.2
EOF

cat > "$tmpdir/c" <<EOF
10.0.0.3
EOF

../../iprange -v "$tmpdir/a" as left --diff "$tmpdir/b" "$tmpdir/c" --quiet >/dev/null 2>"$stderr"
rc=$?

if [ $rc -ne 1 ]; then
    echo "# ERROR: Expected diff to exit 1, got $rc"
    cat "$stderr"
    exit 1
fi

if ! grep -q "Finding diff IPs in left and ipset B" "$stderr"; then
    echo "# ERROR: Diff debug labels are wrong"
    cat "$stderr"
    exit 1
fi

echo "# OK: diff debug labels preserve left label and use ipset B"
