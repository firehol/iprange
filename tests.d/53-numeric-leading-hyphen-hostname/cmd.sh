#!/bin/bash

tmpdir=$(mktemp -d)
stdout="$tmpdir/stdout"
stderr="$tmpdir/stderr"
trap 'rm -rf "$tmpdir"' EXIT

cat >"$tmpdir/input" <<'EOF'
1-foo.example.invalid
1-2.example.invalid
EOF

../../iprange "$tmpdir/input" >"$stdout" 2>"$stderr"
rc=$?

if [ $rc -eq 0 ]; then
    echo "# ERROR: unresolved numeric-leading hyphen hostnames should now fail the command"
    cat "$stderr"
    exit 1
fi

if [ -s "$stdout" ]; then
    echo "# ERROR: unresolved .invalid hostnames should not produce IP output"
    cat "$stdout"
    exit 1
fi

if grep -q "Cannot understand line" "$stderr"; then
    echo "# ERROR: numeric-leading hyphen hostname was rejected as invalid input"
    cat "$stderr"
    exit 1
fi

for host in "1-foo.example.invalid" "1-2.example.invalid"; do
    if ! grep -q "DNS: '$host'" "$stderr"; then
        echo "# ERROR: expected the hostname path to reach DNS resolution for $host"
        cat "$stderr"
        exit 1
    fi
done

echo "# OK: numeric-leading hyphen hostnames still use the hostname/DNS path"
