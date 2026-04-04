#!/bin/bash
# Test that operations near the max IPv4 address (255.255.255.255)
# handle wrap-around safely without overflow or underflow.

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

echo "# Optimize preserves max-adjacent pair:"
printf "255.255.255.254\n255.255.255.255\n" | ../../iprange

echo "# Binary round-trip of max address:"
echo "255.255.255.255" | ../../iprange --print-binary | ../../iprange

echo "# Binary round-trip of max /31:"
printf "255.255.255.254\n255.255.255.255\n" | ../../iprange --print-binary | ../../iprange

echo "# Exclude max from max /24:"
echo "255.255.255.0/24" >"$tmpdir/net"
echo "255.255.255.255" >"$tmpdir/top"
../../iprange "$tmpdir/net" --except "$tmpdir/top" | tail -1

echo "# Exclude everything except max:"
echo "0.0.0.0/0" >"$tmpdir/all"
echo "0.0.0.0 - 255.255.255.254" >"$tmpdir/below"
../../iprange "$tmpdir/all" --except "$tmpdir/below"

echo "# Count of /31 at max:"
printf "255.255.255.254/31\n" | ../../iprange -C

echo "# Single IP output of max /30:"
printf "255.255.255.252/30\n" | ../../iprange -1
