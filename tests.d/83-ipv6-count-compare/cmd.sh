#!/bin/bash
# Test IPv6 count and compare modes

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

printf "2001:db8::/48\n" > "$tmpdir/a"
printf "2001:db8:1::/48\n" > "$tmpdir/b"

echo "# Count unique merged:"
printf "2001:db8::1\n2001:db8::2\n2001:db8::1\n" | ../../iprange -6 -C

echo "# Count unique all:"
../../iprange -6 --header --count-unique-all "$tmpdir/a" as netA "$tmpdir/b" as netB

echo "# Compare-next:"
../../iprange -6 --header "$tmpdir/a" as netA --compare-next "$tmpdir/b" as netB

echo "# /128 count:"
echo "2001:db8::1" | ../../iprange -6 -C

echo "# /64 count:"
echo "2001:db8:1::/64" | ../../iprange -6 -C
