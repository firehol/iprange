#!/bin/bash
# Test mixed-family range endpoint rejection

tmpdir=$(mktemp -d)
stderr="$tmpdir/stderr"
trap 'rm -rf "$tmpdir"' EXIT

echo "# Mixed-family range should be rejected:"
echo "10.0.0.1 - 2001:db8::1" | ../../iprange -6 2>"$stderr"
rc=$?

if [ $rc -ne 0 ] && grep -q "Mixed-family range" "$stderr"; then
    echo "PASS: mixed-family range rejected"
else
    echo "FAIL: expected mixed-family rejection"
    cat "$stderr"
    exit 1
fi
