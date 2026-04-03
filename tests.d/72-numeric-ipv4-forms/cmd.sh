#!/bin/bash
# Test inet_aton() numeric IPv4 forms that go through the IP parsing path:
# raw 32-bit integer, octal, two-part, three-part notation.
# These are accepted by the parser (digits + dots + slash characters)
# and passed to inet_aton() which handles all these forms.

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

# --- Individual numeric forms ---
# raw integer: 167772161 = 10.0.0.1
# octal: 012.0.0.1 = 10.0.0.1
# two-part: 10.1 = 10.0.0.1
# three-part: 10.0.1 = 10.0.0.1
cat >"$tmpdir/input" <<'EOF'
167772161
012.0.0.1
10.1
10.0.1
EOF

echo "# Numeric forms merged (all resolve to 10.0.0.1):"
../../iprange "$tmpdir/input"

# --- Integer range ---
echo "# Integer range 167772160-167772163 = 10.0.0.0/30:"
echo "167772160 - 167772163" | ../../iprange

# --- Octal CIDR ---
echo "# Octal CIDR 012.0.0.0/24 = 10.0.0.0/24:"
echo "012.0.0.0/24" | ../../iprange

# --- Two-part CIDR ---
echo "# Two-part 10.0/16 = 10.0.0.0/16:"
echo "10.0/16" | ../../iprange

# --- Count using integer notation ---
echo "# Count of integer 0/0 (entire IPv4 space):"
echo "0/0" | ../../iprange -C

# --- Verify integer zero ---
echo "# Integer 0 = 0.0.0.0:"
echo "0" | ../../iprange

# --- Verify max integer ---
echo "# Integer 4294967295 = 255.255.255.255:"
echo "4294967295" | ../../iprange
