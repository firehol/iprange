#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

printf 'definitely-not-a-real-hostname.invalid\n' >"$tmpdir/invalid-host.txt"
../../iprange --count-unique-all "$tmpdir/invalid-host.txt" >"$tmpdir/invalid.out" 2>"$tmpdir/invalid.err"
rc=$?

if [ $rc -eq 0 ]; then
    echo "# ERROR: unresolved hostname should fail the command"
    cat "$tmpdir/invalid.err"
    exit 1
fi

if [ -s "$tmpdir/invalid.out" ]; then
    echo "# ERROR: unresolved hostname should not produce count output"
    cat "$tmpdir/invalid.out"
    exit 1
fi

if ! grep -q "failed permanently" "$tmpdir/invalid.err"; then
    echo "# ERROR: expected DNS failure message for unresolved hostname"
    cat "$tmpdir/invalid.err"
    exit 1
fi

cat >"$tmpdir/mixed-hosts.txt" <<EOF
localhost
definitely-not-a-real-hostname.invalid
EOF

../../iprange "$tmpdir/mixed-hosts.txt" >"$tmpdir/mixed.out" 2>"$tmpdir/mixed.err"
rc=$?

if [ $rc -eq 0 ]; then
    echo "# ERROR: mixed resolvable/unresolvable hostnames should fail the command"
    cat "$tmpdir/mixed.err"
    exit 1
fi

if [ -s "$tmpdir/mixed.out" ]; then
    echo "# ERROR: mixed hostname input should not produce partial stdout"
    cat "$tmpdir/mixed.out"
    exit 1
fi

if ! grep -q "failed permanently" "$tmpdir/mixed.err"; then
    echo "# ERROR: expected DNS failure message for mixed hostname input"
    cat "$tmpdir/mixed.err"
    exit 1
fi

echo "# OK: DNS failures now fail the command"
