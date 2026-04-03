#!/bin/bash
# Test IPv6 set operations: common, exclude, diff

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

printf "2001:db8::/32\n" > "$tmpdir/a"
printf "2001:db8:1::/48\n" > "$tmpdir/b"
printf "2001:db8::1\n" > "$tmpdir/c"
printf "2001:db8::2\n" > "$tmpdir/d"

echo "# Common:"
../../iprange -6 "$tmpdir/a" --common "$tmpdir/b"

echo "# Exclude (first 3 lines):"
../../iprange -6 "$tmpdir/a" --except "$tmpdir/b" | head -3

echo "# Diff (symmetric difference):"
../../iprange -6 "$tmpdir/c" --diff "$tmpdir/d"

echo "# Exclude empty result:"
../../iprange -6 "$tmpdir/b" --except "$tmpdir/a"
