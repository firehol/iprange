#!/bin/bash

tmpdir=$(mktemp -d)
stderr="$tmpdir/stderr"
trap 'rm -rf "$tmpdir"' EXIT

printf "localhost\n" > "$tmpdir/hosts.txt"
printf "10.0.0.1\n" > "$tmpdir/ips.txt"

if ! ../../iprange -v "$tmpdir/hosts.txt" "$tmpdir/ips.txt" >/dev/null 2>"$stderr"; then
    echo "# ERROR: iprange failed while checking DNS state reset"
    cat "$stderr"
    exit 1
fi

summary_count=$(grep -c "DNS: made" "$stderr" || true)

if [ "$summary_count" -ne 1 ]; then
    echo "# ERROR: expected exactly 1 DNS summary line, got $summary_count"
    grep "DNS:" "$stderr" || true
    exit 1
fi

echo "# OK: DNS state resets between file loads"
