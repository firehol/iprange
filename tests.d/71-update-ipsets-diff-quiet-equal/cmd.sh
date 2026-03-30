#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

cat >"$tmpdir/a" <<'EOF'
10.0.0.1
10.0.0.2
EOF

cat >"$tmpdir/b" <<'EOF'
10.0.0.1
10.0.0.2
EOF

../../iprange "$tmpdir/a" --diff "$tmpdir/b" --quiet >"$tmpdir/out" 2>"$tmpdir/err"
rc=$?

if [ $rc -ne 0 ]; then
    echo "# ERROR: quiet diff should return 0 for identical sets"
    cat "$tmpdir/err"
    exit 1
fi

if [ -s "$tmpdir/out" ]; then
    echo "# ERROR: quiet diff should not print stdout for identical sets"
    cat "$tmpdir/out"
    exit 1
fi

if [ -s "$tmpdir/err" ]; then
    echo "# ERROR: quiet diff should not print stderr for identical sets"
    cat "$tmpdir/err"
    exit 1
fi

echo "# OK: update-ipsets quiet diff equality contract works"
