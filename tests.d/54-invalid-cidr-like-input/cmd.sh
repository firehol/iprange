#!/bin/bash

tmpdir=$(mktemp -d)
stdout="$tmpdir/stdout"
stderr="$tmpdir/stderr"
trap 'rm -rf "$tmpdir"' EXIT

printf '1.2.3.4/24.example.invalid\n' >"$tmpdir/input"

../../iprange "$tmpdir/input" >"$stdout" 2>"$stderr"
rc=$?

if [ $rc -eq 0 ]; then
    echo "# ERROR: malformed CIDR-like input should fail the command"
    cat "$stderr"
    exit 1
fi

if [ -s "$stdout" ]; then
    echo "# ERROR: malformed CIDR-like input should not produce IP output"
    cat "$stdout"
    exit 1
fi

if grep -q "DNS:" "$stderr"; then
    echo "# ERROR: malformed CIDR-like input should not trigger DNS fallback"
    cat "$stderr"
    exit 1
fi

if grep -q "Ignoring text after hostname" "$stderr"; then
    echo "# ERROR: malformed CIDR-like input should not be partially accepted as a hostname"
    cat "$stderr"
    exit 1
fi

if ! grep -q "Cannot understand line No 1" "$stderr"; then
    echo "# ERROR: expected malformed CIDR-like input to be rejected"
    cat "$stderr"
    exit 1
fi

echo "# OK: malformed CIDR-like input is rejected without partial hostname parsing"
