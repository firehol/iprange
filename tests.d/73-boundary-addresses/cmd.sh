#!/bin/bash
# Test boundary address behavior: 0.0.0.0, 255.255.255.255, and edge ranges

echo "# Single 0.0.0.0:"
echo "0.0.0.0" | ../../iprange

echo "# Single 255.255.255.255:"
echo "255.255.255.255" | ../../iprange

echo "# Range 0.0.0.0 - 0.0.0.0:"
echo "0.0.0.0 - 0.0.0.0" | ../../iprange

echo "# Range 255.255.255.255 - 255.255.255.255:"
echo "255.255.255.255 - 255.255.255.255" | ../../iprange

echo "# Full range 0.0.0.0 - 255.255.255.255:"
echo "0.0.0.0 - 255.255.255.255" | ../../iprange

echo "# Count 0.0.0.0:"
echo "0.0.0.0" | ../../iprange -C

echo "# Count 255.255.255.255:"
echo "255.255.255.255" | ../../iprange -C

echo "# Count 0.0.0.0/0:"
echo "0.0.0.0/0" | ../../iprange -C

echo "# Print ranges for boundary:"
printf "0.0.0.0\n255.255.255.255\n" | ../../iprange -j

echo "# Print single IPs for boundaries:"
printf "0.0.0.0\n255.255.255.255\n" | ../../iprange -1
