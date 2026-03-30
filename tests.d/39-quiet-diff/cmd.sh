#!/bin/bash

tmpdir=$(mktemp -d)
stdout="$tmpdir/stdout"
stderr="$tmpdir/stderr"
trap 'rm -rf "$tmpdir"' EXIT

cat > "$tmpdir/a" <<EOF
10.0.0.1
EOF

cat > "$tmpdir/b" <<EOF
10.0.0.2
EOF

../../iprange "$tmpdir/a" --diff "$tmpdir/b" --quiet >"$stdout" 2>"$stderr"
rc=$?

if [ $rc -ne 1 ]; then
    echo "# ERROR: Expected exit code 1, got $rc"
    cat "$stderr"
    exit 1
fi

if [ -s "$stdout" ]; then
    echo "# ERROR: Expected no stdout from --quiet diff"
    cat "$stdout"
    exit 1
fi

if [ -s "$stderr" ]; then
    echo "# ERROR: Expected no stderr from --quiet diff"
    cat "$stderr"
    exit 1
fi

echo "# OK: Differences found silently (exit code 1)"
