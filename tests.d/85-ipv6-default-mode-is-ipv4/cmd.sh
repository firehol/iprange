#!/bin/bash
# Test that default mode (no -4 or -6) still behaves as IPv4

echo "# Default mode still parses IPv4:"
echo "10.0.0.1" | ../../iprange

echo "# Explicit -4 works same as default:"
echo "10.0.0.1" | ../../iprange -4

echo "# Default mode count:"
echo "10.0.0.0/24" | ../../iprange -C

echo "# Explicit -4 count:"
echo "10.0.0.0/24" | ../../iprange -4 -C
