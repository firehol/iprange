#!/bin/bash

# --prefixes accepts only 1..32 (IPv4) and 1..128 (IPv6), and reports the
# value the way the C reference does.
#
# C parses the list with a strtol(3) loop (src/iprange.c:534-559 IPv4,
# src/iprange6_main.c:127-146 IPv6). Each token is cast from long to int
# before the bound test, so:
#   * 0 is rejected ("Only prefixes from 1 to 32 can be set (32 is always
#     enabled). 0 is invalid.") -- enabling nothing is a usage error, not an
#     empty selection;
#   * a token whose digits do not form a number yields strtol 0 and is
#     rejected as "0";
#   * an overflowing token saturates at LONG_MAX/LONG_MIN and then wraps to
#     int, so "2147483648" is reported as -2147483648 and "4294967299"
#     truncates to 3 and is accepted (a /30 has no /3 to print, so it falls
#     back to single addresses, like any prefix smaller than the range).
# The IPv6 diagnostic has no "(32 is always enabled)" clause. The bound is
# the one for the family active at that point in argv: --prefixes before -6
# is still checked against the IPv4 range.
#
# Only exit codes, stdout and the option diagnostic are pinned.

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

IPRANGE=../../iprange
REFERENCE=${IPRANGE_REFERENCE:-/usr/bin/iprange}

printf '10.0.0.0/30\n' > "$tmpdir/in4"
printf '2001:db8::/124\n' > "$tmpdir/in6"

fail=0
# check <label> <expected-rc> <expected-stdout> <expected-stderr> <file> <args...>
check() {
    local label=$1 erc=$2 eout=$3 eerr=$4 file=$5
    shift 5
    timeout 20 "$IPRANGE" "$@" "$file" < /dev/null > "$tmpdir/out" 2> "$tmpdir/err"
    local rc=$?
    if [ "$rc" -ne "$erc" ]; then
        echo "# ERROR: $label exit $rc, expected $erc"; cat "$tmpdir/err"; fail=1; return
    fi
    if ! cmp -s <(printf '%b' "$eout") "$tmpdir/out"; then
        echo "# ERROR: $label stdout differs"; cat -v "$tmpdir/out"; fail=1; return
    fi
    if ! cmp -s <(printf '%b' "$eerr") "$tmpdir/err"; then
        echo "# ERROR: $label stderr differs"; cat -v "$tmpdir/err"; fail=1; return
    fi
    if [ -x "$REFERENCE" ]; then
        timeout 20 "$REFERENCE" "$@" "$file" < /dev/null > "$tmpdir/ref_out" 2> "$tmpdir/ref_err"
        local rrc=$?
        if [ "$rrc" -ne "$rc" ] || ! cmp -s "$tmpdir/ref_out" "$tmpdir/out" ||
           ! cmp -s "$tmpdir/ref_err" "$tmpdir/err"; then
            echo "# ERROR: $label differs from the C reference ($REFERENCE)"; fail=1; return
        fi
    fi
    echo "# OK: $label (exit $rc)"
}

V4E='iprange: Only prefixes from 1 to 32 can be set (32 is always enabled).'
V6E='iprange: Only prefixes from 1 to 128 can be set.'

check "--prefixes 31 splits a /30 into /31s" 0 '10.0.0.0/31\n10.0.0.2/31\n' '' "$tmpdir/in4" --prefixes 31
check "--prefixes 0 rejected" 1 '' "$V4E 0 is invalid.\n" "$tmpdir/in4" --prefixes 0
check "--prefixes 33 rejected" 1 '' "$V4E 33 is invalid.\n" "$tmpdir/in4" --prefixes 33
check "--prefixes -1 rejected" 1 '' "$V4E -1 is invalid.\n" "$tmpdir/in4" --prefixes -1
check "--prefixes 1,0 rejected on the 0" 1 '' "$V4E 0 is invalid.\n" "$tmpdir/in4" --prefixes 1,0
check "--prefixes abc rejected as 0" 1 '' "$V4E 0 is invalid.\n" "$tmpdir/in4" --prefixes abc
check "--prefixes 3x rejected as 0" 1 '' "$V4E 0 is invalid.\n" "$tmpdir/in4" --prefixes 3x
check "--prefixes 0x10 rejected as 0" 1 '' "$V4E 0 is invalid.\n" "$tmpdir/in4" --prefixes 0x10
check "--prefixes , rejected as 0" 1 '' "$V4E 0 is invalid.\n" "$tmpdir/in4" --prefixes ,
check "--prefixes 1,,2 rejected as 0" 1 '' "$V4E 0 is invalid.\n" "$tmpdir/in4" --prefixes 1,,2
check "--prefixes 2147483648 wraps to int" 1 '' "$V4E -2147483648 is invalid.\n" "$tmpdir/in4" --prefixes 2147483648
check "--prefixes LONG_MAX wraps to -1" 1 '' "$V4E -1 is invalid.\n" "$tmpdir/in4" --prefixes 9223372036854775807
check "--prefixes out-of-range negative wraps to 0" 1 '' "$V4E 0 is invalid.\n" "$tmpdir/in4" --prefixes -99999999999999999999
check "--prefixes 4294967299 truncates to 3 and is accepted" 0 \
    '10.0.0.0\n10.0.0.1\n10.0.0.2\n10.0.0.3\n' '' "$tmpdir/in4" --prefixes 4294967299
check "--prefixes empty enables /32 only" 0 \
    '10.0.0.0\n10.0.0.1\n10.0.0.2\n10.0.0.3\n' '' "$tmpdir/in4" --prefixes ""
check "--prefixes 3, keeps the trailing comma" 0 \
    '10.0.0.0\n10.0.0.1\n10.0.0.2\n10.0.0.3\n' '' "$tmpdir/in4" --prefixes 3,
check "--prefixes 1 2 is a space separated list" 0 \
    '10.0.0.0\n10.0.0.1\n10.0.0.2\n10.0.0.3\n' '' "$tmpdir/in4" --prefixes "1 2"
check "--prefixes +5 is accepted" 0 \
    '10.0.0.0\n10.0.0.1\n10.0.0.2\n10.0.0.3\n' '' "$tmpdir/in4" --prefixes +5
check "-4 --prefixes 0 rejected" 1 '' "$V4E 0 is invalid.\n" "$tmpdir/in4" -4 --prefixes 0

check "-6 --prefixes 0 rejected" 1 '' "$V6E 0 is invalid.\n" "$tmpdir/in6" -6 --prefixes 0
check "-6 --prefixes 129 rejected" 1 '' "$V6E 129 is invalid.\n" "$tmpdir/in6" -6 --prefixes 129
check "-6 --prefixes -3 rejected" 1 '' "$V6E -3 is invalid.\n" "$tmpdir/in6" -6 --prefixes -3
check "-6 --prefixes 3,0 rejected on the 0" 1 '' "$V6E 0 is invalid.\n" "$tmpdir/in6" -6 --prefixes 3,0
check "-6 --prefixes 125 splits" 0 '2001:db8::/125\n2001:db8::8/125\n' '' "$tmpdir/in6" -6 --prefixes 125
# The bound is the one for the family active at that point in argv: with
# --prefixes before -6 the IPv4 range 1..32 still applies.
check "--prefixes 200 -6 uses the IPv4 bound" 1 '' "$V4E 200 is invalid.\n" "$tmpdir/in6" --prefixes 200 -6

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "# OK: 26 --prefixes cases match the C reference exit codes and diagnostics"
