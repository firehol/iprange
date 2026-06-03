#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

printf "10.0.0.1\n" > "$tmpdir/ipv4-a"
printf "10.0.0.2\n" > "$tmpdir/ipv4-b"
printf "2001:db8::1\n" > "$tmpdir/ipv6-a"
printf "2001:db8::2\n" > "$tmpdir/ipv6-b"

echo "# IPv4 count-unique-all:"
../../iprange --header --count-unique-all "$tmpdir/ipv4-a" as 'alpha,set' "$tmpdir/ipv4-b" as 'beta"set'

echo "# IPv4 compare-next:"
../../iprange --header "$tmpdir/ipv4-a" as 'alpha,set' --compare-next "$tmpdir/ipv4-b" as 'beta"set'

echo "# IPv6 count-unique-all:"
../../iprange -6 --header --count-unique-all "$tmpdir/ipv6-a" as 'v6,alpha' "$tmpdir/ipv6-b" as 'v6"beta'

echo "# IPv6 compare-next:"
../../iprange -6 --header "$tmpdir/ipv6-a" as 'v6,alpha' --compare-next "$tmpdir/ipv6-b" as 'v6"beta'
