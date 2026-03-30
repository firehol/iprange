#!/bin/bash

tmpdir=$(mktemp -d)
stdout="$tmpdir/stdout"
stderr="$tmpdir/stderr"
trap 'rm -rf "$tmpdir"' EXIT

cat > "$tmpdir/input" <<EOF
10.0.0.1
10.0.0.2
10.0.0.3
EOF

if ! echo "10.0.0.4" | ../../iprange "$tmpdir/input" as file - as stdin --count-unique-all --header >"$stdout" 2>"$stderr"; then
    echo "# ERROR: --count-unique-all failed"
    cat "$stderr"
    exit 1
fi

if [ -s "$stderr" ]; then
    echo "# ERROR: Unexpected stderr from --count-unique-all"
    cat "$stderr"
    exit 1
fi

cat "$stdout"
