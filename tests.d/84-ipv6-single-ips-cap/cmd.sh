#!/bin/bash
# Test IPv6 -1 (single IPs) output and cap behavior

echo "# Small range single IPs:"
echo "2001:db8::/126" | ../../iprange -6 -1

echo "# Single IP output:"
echo "2001:db8::1" | ../../iprange -6 -1

echo "# Range output:"
printf "2001:db8::1\n2001:db8::2\n2001:db8::3\n" | ../../iprange -6 -j
