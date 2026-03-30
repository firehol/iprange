#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

cat > "$tmpdir/a" <<EOF
10.0.0.0/24
10.0.1.0/24
EOF

cat > "$tmpdir/b" <<EOF
10.0.1.0/24
10.0.2.0/24
EOF

cat > "$tmpdir/c" <<EOF
10.0.2.0/24
EOF

../../iprange "$tmpdir/a" as baseline "$tmpdir/b" as overlap "$tmpdir/c" as isolated --compare-first --header
