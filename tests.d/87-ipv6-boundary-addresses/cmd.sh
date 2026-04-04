#!/bin/bash
# Test IPv6 boundary addresses

echo "# All zeros:"
echo "::" | ../../iprange -6

echo "# Loopback:"
echo "::1" | ../../iprange -6

echo "# All ones (ffff:...:ffff):"
echo "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff" | ../../iprange -6

echo "# Full range:"
echo "::/0" | ../../iprange -6

echo "# Link-local:"
echo "fe80::1" | ../../iprange -6

echo "# Adjacent at bottom:"
printf "::\n::1\n" | ../../iprange -6

echo "# Count /1 (half the IPv6 space):"
echo "::/1" | ../../iprange -6 -C
