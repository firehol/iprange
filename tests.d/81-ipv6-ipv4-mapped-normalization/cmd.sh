#!/bin/bash
# Test IPv4-to-IPv6 mapped normalization in -6 mode

echo "# IPv4 address becomes mapped IPv6:"
echo "10.0.0.1" | ../../iprange -6

echo "# Explicit mapped IPv6 preserved:"
echo "::ffff:10.0.0.1" | ../../iprange -6

echo "# IPv4 and explicit mapped merge:"
printf "10.0.0.1\n::ffff:10.0.0.1\n" | ../../iprange -6

echo "# IPv4 CIDR becomes mapped range:"
echo "10.0.0.0/30" | ../../iprange -6

echo "# Mixed IPv4 and IPv6 merge:"
printf "2001:db8::1\n10.0.0.1\n" | ../../iprange -6
