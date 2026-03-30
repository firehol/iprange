#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

cat >"$tmpdir/a" <<'EOF'
10.0.0.1
10.0.0.2
EOF

cat >"$tmpdir/b" <<'EOF'
10.0.0.2
10.0.0.3
EOF

if ! ../../iprange "$tmpdir/a" --compare "$tmpdir/b" >"$tmpdir/out" 2>"$tmpdir/err"; then
    echo "# ERROR: --compare failed"
    cat "$tmpdir/err"
    exit 1
fi

if [ -s "$tmpdir/err" ]; then
    echo "# ERROR: --compare should not emit stderr for valid inputs"
    cat "$tmpdir/err"
    exit 1
fi

cat >"$tmpdir/expected" <<EOF
$tmpdir/a,$tmpdir/b,1,1,2,2,3,1
EOF

if ! diff -u "$tmpdir/expected" "$tmpdir/out"; then
    echo "# ERROR: --compare CSV contract changed"
    exit 1
fi

echo "# OK: update-ipsets compare CSV contract works"
