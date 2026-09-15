#!/bin/bash

# Unique-address counting of the full IPv6 universe.
#
# An IPv6 set counts addresses in a 128-bit quantity. '::/0' holds 2^128
# addresses, one more than that counter can hold, so the count must saturate
# at 2^128-1. Two defects produce a silently wrong answer here: a range size
# computed as plain hi-lo+1 wraps that one range to zero, and an addition that
# tests only the low 64-bit limb for overflow ignores the carry into the high
# limb and wraps the total the same way. Both under-report the entire address
# space. The C reference saturates in ipset6_added_entry (src/ipset6.h:67-82,
# which counts into a uint128_t unique_ips), the Rust engine in Range::size
# (v4/rust/iprange-cli/src/legacy/range.rs:90-95, saturating_sub plus
# saturating_add), and both keep IPv4 exact because that family counts into a
# uint64_t (src/ipset.h:90). Every engine must print the saturated value for
# the full universe and exact arithmetic everywhere else.
#
# Two guard groups keep the fix honest. The exact-count cases prove the
# saturation is 2^128-1 rather than a smaller cap applied to every large range.
# The IPv4 cases prove it is not applied to IPv4 either: that family keeps a
# wider counter, so 0.0.0.0/0 is reported as its exact 2^32 addresses.
#
# Every case runs through the engine under test (../../iprange, which
# run-tests.sh points at the binary under validation) and, when it is
# installed, through the C reference at $IPRANGE_REFERENCE. The engine must
# match the pinned value, and it must match the reference on stdout and stderr.

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

IPRANGE=../../iprange
REFERENCE=${IPRANGE_REFERENCE:-/usr/bin/iprange}

FULL6='0000:0000:0000:0000:0000:0000:0000:0000-ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff'
HALF_LO='0000:0000:0000:0000:0000:0000:0000:0000-7fff:ffff:ffff:ffff:ffff:ffff:ffff:ffff'
HALF_HI='8000:0000:0000:0000:0000:0000:0000:0000-ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff'
TOP256='ffff:ffff:ffff:ffff:ffff:ffff:ffff:ff00-ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff'

# 2^128-1: 2^128 is not representable, so the full universe saturates here.
FULL6_COUNT='1,340282366920938463463374607431768211455'

reference_used=0
[ -x "$REFERENCE" ] && reference_used=1

compare_with_reference() {
    # compare_with_reference <label> <args...>: the reference must answer the
    # same input with the same stdout, stderr and exit code.
    local label=$1
    shift
    "$REFERENCE" "$@" <"$tmpdir/in" >"$tmpdir/ref_out" 2>"$tmpdir/ref_err"
    local rrc=$?
    if [ "$rc" -ne "$rrc" ]; then
        echo "# ERROR: $label: engine exit $rc but $REFERENCE exit $rrc"
        return 1
    fi
    if ! diff -u "$tmpdir/ref_out" "$tmpdir/out" >"$tmpdir/ref_diff"; then
        echo "# ERROR: $label: engine stdout differs from $REFERENCE"
        cat "$tmpdir/ref_diff"
        return 1
    fi
    if ! diff -u "$tmpdir/ref_err" "$tmpdir/err" >"$tmpdir/ref_diff"; then
        echo "# ERROR: $label: engine stderr differs from $REFERENCE"
        cat "$tmpdir/ref_diff"
        return 1
    fi
    return 0
}

check_count() {
    # check_count <label> <input> <expected-first-line> <args...>: the engine
    # must answer exactly the expected cardinality line and stay silent.
    local label=$1 input=$2 expected=$3
    shift 3
    printf '%b' "$input" >"$tmpdir/in"
    : >"$tmpdir/err"

    timeout 20 "$IPRANGE" "$@" <"$tmpdir/in" >"$tmpdir/out" 2>"$tmpdir/err"
    rc=$?
    if [ "$rc" -ne 0 ]; then
        echo "# ERROR: $label failed (rc=$rc; 124 = the run hung)"
        cat "$tmpdir/err"
        return 1
    fi
    if [ -s "$tmpdir/err" ]; then
        echo "# ERROR: $label must not emit diagnostics"
        cat "$tmpdir/err"
        return 1
    fi
    if ! printf '%s\n' "$expected" | diff -q - "$tmpdir/out" >/dev/null; then
        echo "# ERROR: $label cardinality is not the reference answer"
        echo "  expected: $expected"
        echo "  actual:   $(tr '\n' '|' <"$tmpdir/out")"
        cat "$tmpdir/err"
        return 1
    fi
    if [ "$reference_used" -eq 1 ]; then
        compare_with_reference "$label" "$@" || return 1
    fi
    echo "# OK: $label -> $expected"
    return 0
}

# The full universe through every input shape that must reach it.
check_count "ipv6 ::/0 cardinality" \
    '::/0\n' "$FULL6_COUNT" -6 -C || exit 1

check_count "duplicated ipv6 ::/0 cardinality" \
    '::/0\n::/0\n' "$FULL6_COUNT" -6 -C || exit 1

check_count "ipv6 explicit full range cardinality" \
    "$FULL6\n" "$FULL6_COUNT" -6 -C || exit 1

check_count "ipv6 full range and ::/0 together" \
    "$FULL6\n::/0\n" "$FULL6_COUNT" -6 -C || exit 1

# The two halves of the address space merge into the full range only when the
# merge crosses the 2^127 boundary, so the input order must not matter.
check_count "ipv6 two halves cardinality" \
    "$HALF_LO\n$HALF_HI\n" "$FULL6_COUNT" -6 -C || exit 1

check_count "ipv6 two halves reversed cardinality" \
    "$HALF_HI\n$HALF_LO\n" "$FULL6_COUNT" -6 -C || exit 1

check_count "ipv6 two halves duplicated cardinality" \
    "$HALF_HI\n$HALF_LO\n$HALF_HI\n$HALF_LO\n" "$FULL6_COUNT" -6 -C || exit 1

# Exact arithmetic next to the saturated cases: 2^96 plus two addresses, the
# top 256 addresses of the space plus three, and one IPv4-mapped IPv6 address
# beside one native address. A cap smaller than 2^128-1 fails these.
check_count "ipv6 exact count below saturation" \
    '2001:db8::/32\n::1-::3\n' '2,79228162514264337593543950339' -6 -C || exit 1

check_count "ipv6 exact count at the top of the space" \
    "$TOP256\n::1-::2\n" '2,258' -6 -C || exit 1

check_count "ipv6 with an ipv4-mapped address cardinality" \
    '::1\n1.2.3.4\n' '2,2' -6 -C || exit 1

# IPv4 counts with a wider counter: 0.0.0.0/0 keeps its exact 2^32 addresses,
# so a saturation helper must never be applied to this family.
check_count "ipv4 0.0.0.0/0 cardinality" \
    '0.0.0.0/0\n' '1,4294967296' -4 -C || exit 1

check_count "ipv4 two ranges cardinality" \
    '10.0.0.0/8\n1.2.3.0/24\n' '2,16777472' -4 -C || exit 1

# An IPv6 entry in the default (IPv4) mode is dropped with a notice, and the
# IPv4 cardinality stays exact: the IPv6 saturation path must not disturb it.
printf '%b' '1.2.3.0-1.2.3.9\n::/0\n' >"$tmpdir/in"
timeout 20 "$IPRANGE" -C <"$tmpdir/in" >"$tmpdir/out" 2>"$tmpdir/err"
rc=$?
if [ "$rc" -ne 0 ]; then
    echo "# ERROR: ipv6 entry dropped in ipv4 mode failed (rc=$rc)"
    cat "$tmpdir/err"
    exit 1
fi
if ! printf '%s\n' '1,10' | diff -q - "$tmpdir/out" >/dev/null; then
    echo "# ERROR: ipv6 entry dropped in ipv4 mode cardinality is not the reference answer"
    echo "  expected: 1,10"
    echo "  actual:   $(tr '\n' '|' <"$tmpdir/out")"
    exit 1
fi
if ! grep -q '1 IPv6 entries dropped (use -6 for IPv6 mode)' "$tmpdir/err"; then
    echo "# ERROR: ipv6 entry dropped in ipv4 mode must report the dropped entry"
    cat "$tmpdir/err"
    exit 1
fi
if [ "$reference_used" -eq 1 ]; then
    compare_with_reference "ipv6 entry dropped in ipv4 mode" -C || exit 1
fi
echo "# OK: ipv6 entry dropped in ipv4 mode -> 1,10 with the dropped-entry notice"

if [ "$reference_used" -eq 1 ]; then
    echo "# OK: 13 cardinality cases matched the pinned values and the C reference"
else
    echo "# OK: 13 cardinality cases matched the pinned values"
fi
exit 0
