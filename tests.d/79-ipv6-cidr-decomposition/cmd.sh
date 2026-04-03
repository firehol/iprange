#!/bin/bash
# Test IPv6 CIDR decomposition

echo "# Full /128:"
echo "2001:db8::1" | ../../iprange -6

echo "# /126 block:"
echo "2001:db8::/126" | ../../iprange -6

echo "# /64 block:"
echo "2001:db8:1::/64" | ../../iprange -6

echo "# Range to CIDRs:"
echo "2001:db8::1 - 2001:db8::6" | ../../iprange -6

echo "# Compressed notation:"
echo "::1" | ../../iprange -6

echo "# Full notation:"
echo "2001:0db8:0000:0000:0000:0000:0000:0001" | ../../iprange -6
