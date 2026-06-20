#!/bin/bash
# IPv6 symmetric difference (--diff) -> exercises ipset6_diff.
# Regression guard: the common address (2001:db8::1) must NOT appear in the
# result. A past bug re-emitted the common head of a range into the diff.
out=$(../../iprange -6 input1 --diff input2)
rc=$?

if [ $rc -ne 1 ]; then
    echo "# ERROR: expected exit code 1 (differences found), got $rc"
    exit 1
fi

if echo "$out" | grep -qx '2001:db8::1'; then
    echo "# ERROR: common IP 2001:db8::1 leaked into the symmetric difference"
    exit 1
fi

echo "$out"
echo "# OK: symmetric difference excludes the common IP (exit code 1)"
