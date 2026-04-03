#!/bin/bash
# Test --has-ipv6 feature detection flag

../../iprange --has-ipv6 2>&1
rc=$?

if [ $rc -eq 0 ]; then
    echo "PASS: --has-ipv6 exits with 0"
else
    echo "FAIL: --has-ipv6 exited with $rc"
    exit 1
fi
