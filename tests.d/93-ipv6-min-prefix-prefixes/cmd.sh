#!/bin/bash
# Test: --min-prefix and --prefixes work in IPv6 mode

echo "# Min-prefix 126 (only /126, /127, /128):"
echo "2001:db8::/120" | ../../iprange -6 --min-prefix 126 | head -5

echo "# Prefixes 128 only (individual IPs):"
echo "2001:db8::/126" | ../../iprange -6 --prefixes 128 | head -5
