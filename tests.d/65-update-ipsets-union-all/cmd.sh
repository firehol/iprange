#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

cat >"$tmpdir/a" <<'EOF'
10.0.0.0/24
10.0.1.0/24
EOF

cat >"$tmpdir/b" <<'EOF'
10.0.1.0/24
10.0.2.0/24
EOF

if ! ../../iprange "$tmpdir/a" --print-binary >"$tmpdir/a.bin" 2>"$tmpdir/a.err"; then
    echo "# ERROR: failed to save first history slot in binary form"
    cat "$tmpdir/a.err"
    exit 1
fi

if ! ../../iprange "$tmpdir/b" --print-binary >"$tmpdir/b.bin" 2>"$tmpdir/b.err"; then
    echo "# ERROR: failed to save second history slot in binary form"
    cat "$tmpdir/b.err"
    exit 1
fi

if [ -s "$tmpdir/a.err" ] || [ -s "$tmpdir/b.err" ]; then
    echo "# ERROR: --print-binary should not emit stderr for valid history inputs"
    cat "$tmpdir/a.err"
    cat "$tmpdir/b.err"
    exit 1
fi

if ! ../../iprange --union-all "$tmpdir/a.bin" "$tmpdir/b.bin" >"$tmpdir/out" 2>"$tmpdir/err"; then
    echo "# ERROR: --union-all failed on binary history slots"
    cat "$tmpdir/err"
    exit 1
fi

if [ -s "$tmpdir/err" ]; then
    echo "# ERROR: --union-all should not emit stderr for valid binary history slots"
    cat "$tmpdir/err"
    exit 1
fi

cat >"$tmpdir/expected" <<'EOF'
10.0.0.0/23
10.0.2.0/24
EOF

if ! diff -u "$tmpdir/expected" "$tmpdir/out"; then
    echo "# ERROR: --union-all output does not match the update-ipsets history contract"
    exit 1
fi

echo "# OK: update-ipsets history union contract works"
