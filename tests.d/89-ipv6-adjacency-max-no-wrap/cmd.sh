#!/bin/bash
# Regression: ipset6_added_entry adjacency check must not wrap at IPV6_ADDR_MAX
# Previously: "ffff:...:ffff" + "::" merged to "::/0" (catastrophic corruption)

echo "# Two distinct addresses (max and zero) stay separate:"
printf "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff\n::\n" | ../../iprange -6

echo "# Count should be 2, not 2^128:"
printf "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff\n::\n" | ../../iprange -6 -C
