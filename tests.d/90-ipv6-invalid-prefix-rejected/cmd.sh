#!/bin/bash
# Regression: non-numeric CIDR prefixes must be rejected
# Previously: /abc silently became /0, expanding to ::/0

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

for prefix in "abc" "0FFF" "32abc" "999999999999" ""; do
    printf "2001:db8::/$prefix\n" | ../../iprange -6 2>/dev/null
    if [ $? -eq 0 ]; then
        echo "FAIL: /$prefix should have been rejected"
        exit 1
    fi
done

echo "PASS: all invalid prefixes rejected"
