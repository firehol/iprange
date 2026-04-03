#!/bin/bash
# Test adjacency and merge behavior at IPv4 boundaries (0 and max)

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

echo "# Adjacent at bottom (0.0.0.0 + 0.0.0.1 merge to /31):"
printf "0.0.0.0\n0.0.0.1\n" | ../../iprange

echo "# Adjacent at top (255.255.255.254 + 255.255.255.255 merge to /31):"
printf "255.255.255.254\n255.255.255.255\n" | ../../iprange

echo "# Four at bottom merge to /30:"
printf "0.0.0.0\n0.0.0.1\n0.0.0.2\n0.0.0.3\n" | ../../iprange

echo "# Four at top merge to /30:"
printf "255.255.255.252\n255.255.255.253\n255.255.255.254\n255.255.255.255\n" | ../../iprange

# Exclude top from full range
echo "0.0.0.0/0" >"$tmpdir/full"
echo "255.255.255.255" >"$tmpdir/top"
echo "0.0.0.0" >"$tmpdir/bottom"
printf "0.0.0.0\n255.255.255.255\n" >"$tmpdir/both"
echo "255.255.255.255" >"$tmpdir/toponly"

echo "# Exclude top from full range:"
../../iprange "$tmpdir/full" --except "$tmpdir/top" | tail -3

echo "# Exclude bottom from full range:"
../../iprange "$tmpdir/full" --except "$tmpdir/bottom" | head -3

echo "# Common of {0,max} and {max}:"
../../iprange "$tmpdir/both" --common "$tmpdir/toponly"

echo "# Diff of {0} vs {max}:"
../../iprange "$tmpdir/bottom" --diff "$tmpdir/top"

echo "# Count adjacent merge at top:"
printf "255.255.255.254\n255.255.255.255\n" | ../../iprange -C
