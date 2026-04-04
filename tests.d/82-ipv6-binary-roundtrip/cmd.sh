#!/bin/bash
# Test IPv6 binary save/load roundtrip

echo "# Single IPv6 roundtrip:"
echo "2001:db8::1" | ../../iprange -6 --print-binary | ../../iprange -6

echo "# Multiple IPv6 roundtrip:"
printf "2001:db8::1\n2001:db8::2\nfe80::1\n" | ../../iprange -6 --print-binary | ../../iprange -6

echo "# IPv6 binary count roundtrip:"
echo "2001:db8::/32" | ../../iprange -6 --print-binary | ../../iprange -6 -C

echo "# Mapped IPv4 binary roundtrip:"
echo "10.0.0.1" | ../../iprange -6 --print-binary | ../../iprange -6
