#!/bin/bash
# Test: IPv6 addresses in IPv4 mode are dropped gracefully with one warning

tmpdir=$(mktemp -d)
stderr="$tmpdir/stderr"
stdout="$tmpdir/stdout"
trap 'rm -rf "$tmpdir"' EXIT

printf "10.0.0.1\n2001:db8::1\nfe80::1\n10.0.0.2\n" | ../../iprange >"$stdout" 2>"$stderr"
rc=$?

echo "# Exit code should be 0:"
echo "rc=$rc"

echo "# Output should contain only IPv4:"
cat "$stdout"

echo "# Warning should mention dropped count:"
if grep -q "IPv6 entries dropped" "$stderr"; then
    echo "PASS: warning printed"
else
    echo "FAIL: no warning"
    cat "$stderr"
    exit 1
fi
