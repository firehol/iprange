#!/bin/bash
# Test that default mode (no family flag) behaves as IPv4.
# This is the future-proofing contract: once -4/-6 exist,
# the default must still be IPv4 for backward compatibility.

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

echo "# Default prefix is /32:"
echo "10.0.0.1" | ../../iprange -C

echo "# Default merge of standard dotted-quad:"
printf "192.168.1.0/24\n192.168.2.0/24\n" | ../../iprange

echo "# Default count-unique-all:"
printf "10.0.0.0/24\n" >"$tmpdir/a"
printf "10.0.1.0/24\n" >"$tmpdir/b"
../../iprange --count-unique-all --header "$tmpdir/a" as setA "$tmpdir/b" as setB

echo "# Default compare-next:"
../../iprange --header "$tmpdir/a" as setA --compare-next "$tmpdir/b" as setB

echo "# Default range parsing:"
echo "10.0.0.1 - 10.0.0.10" | ../../iprange -j

echo "# Default CIDR with netmask notation:"
echo "10.0.0.0/255.255.255.0" | ../../iprange -C

echo "# Default --dont-fix-network:"
echo "10.0.0.5/24" | ../../iprange --dont-fix-network -j
