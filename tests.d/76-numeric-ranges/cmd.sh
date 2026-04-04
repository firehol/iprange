#!/bin/bash
# Test numeric forms in ranges and CIDR combinations

echo "# Integer range:"
echo "167772160 - 167772163" | ../../iprange

echo "# Octal range:"
echo "012.0.0.0 - 012.0.0.3" | ../../iprange

echo "# Mixed numeric: octal start, dotted end:"
echo "012.0.0.0 - 10.0.0.3" | ../../iprange

echo "# Two-part range:"
echo "10.0 - 10.3" | ../../iprange

echo "# Integer CIDR:"
echo "167772160/30" | ../../iprange

echo "# Octal CIDR count:"
echo "012.0.0.0/24" | ../../iprange -C

echo "# Integer zero with prefix 0:"
echo "0/0" | ../../iprange -C

echo "# Large integer as single IP:"
echo "3232235777" | ../../iprange
