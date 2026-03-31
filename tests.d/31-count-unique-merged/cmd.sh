#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

cat > "$tmpdir/input" <<EOF
10.0.0.1
EOF

echo "10.0.0.2" | ../../iprange "$tmpdir/input" - --count-unique --header
