#!/bin/bash

tmpdir=$(mktemp -d)
stdout="$tmpdir/stdout"
stderr="$tmpdir/stderr"
trap 'rm -rf "$tmpdir"' EXIT

printf '1.example.invalid\n' >"$tmpdir/input"

../../iprange "$tmpdir/input" >"$stdout" 2>"$stderr"
rc=$?

if [ $rc -eq 0 ]; then
    echo "# ERROR: unresolved numeric-leading hostname should now fail the command"
    cat "$stderr"
    exit 1
fi

if grep -q "Cannot understand line" "$stderr"; then
    echo "# ERROR: numeric-leading dotted hostname was rejected as invalid input"
    cat "$stderr"
    exit 1
fi

if ! grep -q "DNS:" "$stderr"; then
    echo "# ERROR: expected the hostname path to reach DNS resolution"
    cat "$stderr"
    exit 1
fi

echo "# OK: numeric-leading dotted hostnames still use the hostname/DNS path"
