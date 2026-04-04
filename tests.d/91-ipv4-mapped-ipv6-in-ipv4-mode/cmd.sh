#!/bin/bash
# Test: IPv4-mapped IPv6 addresses are converted back to IPv4 in default mode

echo "# Mapped IPv6 converted to IPv4:"
echo "::ffff:10.0.0.1" | ../../iprange

echo "# Mapped IPv6 with uppercase F:"
echo "::FFFF:192.168.1.1" | ../../iprange

echo "# Multiple mapped with regular IPv4:"
printf "::ffff:10.0.0.1\n10.0.0.2\n::ffff:10.0.0.3\n" | ../../iprange
