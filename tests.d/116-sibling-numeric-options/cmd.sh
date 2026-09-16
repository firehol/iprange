#!/bin/bash

# The sibling numeric options of the legacy CLI must accept exactly what the
# C reference accepts.
#
# C parses --min-prefix, --default-prefix/-p and --dns-threads with
# parse_long_option_or_die() (src/iprange.c:452-463): strtol(3) base 10, so
# leading whitespace and an optional sign are part of the accepted grammar,
# and the value is rejected unless the whole string was consumed, errno is
# clear, and the result lies inside the option's [min, max]. Each rejection
# prints "iprange: Invalid value '<value>' for <option>. <expected>" and
# exits 1 (src/iprange.c:446-450).
#
# The per-option bounds and diagnostics, verified against the C source:
#   * --min-prefix, IPv4:      1..32   (src/iprange.c:527-533)
#   * --min-prefix, IPv6:      1..128  (src/iprange6_main.c:111-124, an
#     inline strtol with the same checks as the wrapper)
#   * --default-prefix and -p: 0..32   (src/iprange.c:559-567); the
#     diagnostic names the option token as it was typed
#   * --dns-threads:           1..INT_MAX (src/iprange.c:699-701), with no
#     family guard, so it is validated in both families
# Once IPv6 is the active family, --default-prefix/-p is skipped without any
# validation (src/iprange.c:563 and src/iprange6_main.c:152-157): IPv6 always
# uses /128. Both skips advance past the value, so the value never becomes an
# input file -- that is what the missing-file cases below pin.
#
# The bound is the one for the family active at that point in argv: with
# --min-prefix or --default-prefix before -6, the IPv4 range still applies.
#
# The unsigned reduce options use a different parser that must NOT accept a
# sign or whitespace: parse_size_option_or_die() tests that the first byte is
# a digit (src/iprange.c:465-479). Those controls are pinned here so a future
# widening of the signed parser cannot leak into them.
#
# Only exit codes, stdout and the option diagnostic are pinned.

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

IPRANGE=../../iprange
REFERENCE=${IPRANGE_REFERENCE:-/usr/bin/iprange}

printf '10.0.0.0/30\n' > "$tmpdir/in4"
printf '2001:db8::/124\n' > "$tmpdir/in6"
printf '10.0.0.7\n' > "$tmpdir/bare4"
printf '2001:db8::1\n' > "$tmpdir/bare6"

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

MPP4="iprange: Invalid value '%s' for --min-prefix. It must be between 1 and 32."
MPP6="iprange: Invalid value '%s' for --min-prefix. It must be between 1 and 128."
MDP="iprange: Invalid value '%s' for --default-prefix. It must be between 0 and 32."
MP_="iprange: Invalid value '%s' for -p. It must be between 0 and 32."
MDT="iprange: Invalid value '%s' for --dns-threads. It must be an integer greater than or equal to 1."

# bad <label> <printf-format> <value> <file> <args...>: the value must be
# rejected, and <args...> must end with the option whose value is under test.
bad() {
    local label=$1 fmt=$2 v=$3 file=$4
    shift 4
    check "$label" 1 '' "$(printf "$fmt" "$v")\n" "$file" "$@" "$v"
}

IN4="$tmpdir/in4" IN6="$tmpdir/in6" B4="$tmpdir/bare4" B6="$tmpdir/bare6"
C4='10.0.0.0/30\n' C6='2001:db8::/124\n'
P31='10.0.0.0/31\n10.0.0.2/31\n'
P32='10.0.0.0\n10.0.0.1\n10.0.0.2\n10.0.0.3\n'
V6ALL='2001:db8::\n2001:db8::1\n2001:db8::2\n2001:db8::3\n2001:db8::4\n2001:db8::5\n2001:db8::6\n2001:db8::7\n2001:db8::8\n2001:db8::9\n2001:db8::a\n2001:db8::b\n2001:db8::c\n2001:db8::d\n2001:db8::e\n2001:db8::f\n'

echo "===== 1. --min-prefix, IPv4 bound 1..32 ====="
# Present-value controls: the option works, and the value it carries is the
# one the prefix array sees.
check "--min-prefix 31 control (splits the /30)" 0 "$P31" '' "$IN4" --min-prefix 31
check "--min-prefix 30 control" 0 "$C4" '' "$IN4" --min-prefix 30
check "--min-prefix 32 control (single addresses)" 0 "$P32" '' "$IN4" --min-prefix 32
# strtol accepts a leading sign and leading whitespace, and the accepted
# spelling carries the same value as the plain digits.
check "--min-prefix +31 is 31" 0 "$P31" '' "$IN4" --min-prefix +31
check "--min-prefix ' 31' is 31" 0 "$P31" '' "$IN4" --min-prefix ' 31'
check "--min-prefix +30 is 30" 0 "$C4" '' "$IN4" --min-prefix +30
check "--min-prefix ' 30' is 30" 0 "$C4" '' "$IN4" --min-prefix ' 30'
check "--min-prefix 030 is 30 (leading zeros)" 0 "$C4" '' "$IN4" --min-prefix 030
check "--min-prefix +32 is 32" 0 "$P32" '' "$IN4" --min-prefix +32
check "--min-prefix +1 is 1" 0 "$C4" '' "$IN4" --min-prefix +1
# Rejections: below the bound, above the bound, and not-a-number.
bad "--min-prefix 0 rejected" "$MPP4" 0 "$IN4" --min-prefix
bad "--min-prefix +0 rejected" "$MPP4" +0 "$IN4" --min-prefix
bad "--min-prefix -0 rejected" "$MPP4" -0 "$IN4" --min-prefix
bad "--min-prefix 33 rejected" "$MPP4" 33 "$IN4" --min-prefix
bad "--min-prefix -1 rejected" "$MPP4" -1 "$IN4" --min-prefix
bad "--min-prefix abc rejected" "$MPP4" abc "$IN4" --min-prefix
bad "--min-prefix 30x rejected (trailing junk)" "$MPP4" 30x "$IN4" --min-prefix
bad "--min-prefix ' 30 ' rejected (trailing space)" "$MPP4" ' 30 ' "$IN4" --min-prefix
bad "--min-prefix 0x10 rejected (base 10 only)" "$MPP4" 0x10 "$IN4" --min-prefix
bad "--min-prefix 1e3 rejected" "$MPP4" 1e3 "$IN4" --min-prefix
bad "--min-prefix empty rejected" "$MPP4" '' "$IN4" --min-prefix
bad "--min-prefix comma rejected" "$MPP4" , "$IN4" --min-prefix
bad "--min-prefix sign only rejected" "$MPP4" + "$IN4" --min-prefix
bad "--min-prefix minus only rejected" "$MPP4" - "$IN4" --min-prefix
bad "--min-prefix space only rejected" "$MPP4" ' ' "$IN4" --min-prefix
bad "--min-prefix 2147483648 rejected" "$MPP4" 2147483648 "$IN4" --min-prefix
bad "--min-prefix LONG_MAX rejected" "$MPP4" 9223372036854775807 "$IN4" --min-prefix
bad "--min-prefix overflow rejected (C ERANGE)" "$MPP4" 99999999999999999999 "$IN4" --min-prefix
bad "--min-prefix negative overflow rejected" "$MPP4" -99999999999999999999 "$IN4" --min-prefix
# The bound is the family active at this point in argv, not the last -6.
check "--min-prefix 100 -6 uses the IPv4 bound" 1 '' "$(printf "$MPP4" 100)\n" "$IN6" --min-prefix 100 -6

echo "===== 2. --min-prefix, IPv6 bound 1..128 ====="
check "-6 --min-prefix 128 control (single addresses)" 0 "$V6ALL" '' "$IN6" -6 --min-prefix 128
check "-6 --min-prefix 1 control" 0 "$C6" '' "$IN6" -6 --min-prefix 1
check "-6 --min-prefix +128 is 128" 0 "$V6ALL" '' "$IN6" -6 --min-prefix +128
check "-6 --min-prefix ' 128' is 128" 0 "$V6ALL" '' "$IN6" -6 --min-prefix ' 128'
check "-6 --min-prefix +127 is 127" 0 '2001:db8::/127\n2001:db8::2/127\n2001:db8::4/127\n2001:db8::6/127\n2001:db8::8/127\n2001:db8::a/127\n2001:db8::c/127\n2001:db8::e/127\n' '' "$IN6" -6 --min-prefix +127
check "-6 --min-prefix +1 is 1" 0 "$C6" '' "$IN6" -6 --min-prefix +1
check "-6 --min-prefix ' 1' is 1" 0 "$C6" '' "$IN6" -6 --min-prefix ' 1'
check "-6 --min-prefix 007 is 7" 0 "$C6" '' "$IN6" -6 --min-prefix 007
bad "-6 --min-prefix 0 rejected" "$MPP6" 0 "$IN6" -6 --min-prefix
bad "-6 --min-prefix +0 rejected" "$MPP6" +0 "$IN6" -6 --min-prefix
bad "-6 --min-prefix -0 rejected" "$MPP6" -0 "$IN6" -6 --min-prefix
bad "-6 --min-prefix 129 rejected" "$MPP6" 129 "$IN6" -6 --min-prefix
bad "-6 --min-prefix -1 rejected" "$MPP6" -1 "$IN6" -6 --min-prefix
bad "-6 --min-prefix abc rejected" "$MPP6" abc "$IN6" -6 --min-prefix
bad "-6 --min-prefix 128x rejected" "$MPP6" 128x "$IN6" -6 --min-prefix
bad "-6 --min-prefix ' 128 ' rejected" "$MPP6" ' 128 ' "$IN6" -6 --min-prefix
bad "-6 --min-prefix empty rejected" "$MPP6" '' "$IN6" -6 --min-prefix
bad "-6 --min-prefix 2147483648 rejected" "$MPP6" 2147483648 "$IN6" -6 --min-prefix
bad "-6 --min-prefix overflow rejected" "$MPP6" 99999999999999999999 "$IN6" -6 --min-prefix

echo "===== 3. --default-prefix and -p, IPv4 bound 0..32 ====="
# A bare address shows the prefix the option actually selected, so the
# accepted spellings are pinned by their effect and not only by rc.
check "--default-prefix 28 control" 0 '10.0.0.0/28\n' '' "$B4" --default-prefix 28
check "--default-prefix +28 is 28" 0 '10.0.0.0/28\n' '' "$B4" --default-prefix +28
check "--default-prefix ' 28' is 28" 0 '10.0.0.0/28\n' '' "$B4" --default-prefix ' 28'
check "--default-prefix 007 is 7" 0 '10.0.0.0/7\n' '' "$B4" --default-prefix 007
check "--default-prefix 0 control (0.0.0.0/0)" 0 '0.0.0.0/0\n' '' "$B4" --default-prefix 0
check "--default-prefix +0 is 0" 0 '0.0.0.0/0\n' '' "$B4" --default-prefix +0
check "--default-prefix -0 is 0" 0 '0.0.0.0/0\n' '' "$B4" --default-prefix -0
check "--default-prefix 32 control (single address)" 0 '10.0.0.7\n' '' "$B4" --default-prefix 32
check "-p 28 control" 0 '10.0.0.0/28\n' '' "$B4" -p 28
check "-p +28 is 28" 0 '10.0.0.0/28\n' '' "$B4" -p +28
check "-p ' 28' is 28" 0 '10.0.0.0/28\n' '' "$B4" -p ' 28'
check "-p -0 is 0" 0 '0.0.0.0/0\n' '' "$B4" -p -0
bad "--default-prefix 33 rejected" "$MDP" 33 "$IN4" --default-prefix
bad "--default-prefix -1 rejected" "$MDP" -1 "$IN4" --default-prefix
bad "--default-prefix abc rejected" "$MDP" abc "$IN4" --default-prefix
bad "--default-prefix 32x rejected" "$MDP" 32x "$IN4" --default-prefix
bad "--default-prefix ' 32 ' rejected" "$MDP" ' 32 ' "$IN4" --default-prefix
bad "--default-prefix empty rejected" "$MDP" '' "$IN4" --default-prefix
bad "--default-prefix 2147483648 rejected" "$MDP" 2147483648 "$IN4" --default-prefix
bad "--default-prefix overflow rejected" "$MDP" 99999999999999999999 "$IN4" --default-prefix
# -p reports itself, not the long option, in the diagnostic.
bad "-p 33 rejected" "$MP_" 33 "$IN4" -p
bad "-p abc rejected" "$MP_" abc "$IN4" -p
bad "-p empty rejected" "$MP_" '' "$IN4" -p

echo "===== 4. --default-prefix and -p once IPv6 is active: consumed, unvalidated ====="
check "-6 --default-prefix 0 accepted" 0 "$C6" '' "$IN6" -6 --default-prefix 0
check "-6 --default-prefix 999 accepted (no IPv4 bound)" 0 "$C6" '' "$IN6" -6 --default-prefix 999
check "-6 --default-prefix abc accepted (no parse at all)" 0 "$C6" '' "$IN6" -6 --default-prefix abc
check "-6 --default-prefix +5 accepted" 0 "$C6" '' "$IN6" -6 --default-prefix +5
check "-6 --default-prefix ' 128 ' accepted (trailing space allowed here)" 0 "$C6" '' "$IN6" -6 --default-prefix ' 128 '
check "-6 --default-prefix empty accepted" 0 "$C6" '' "$IN6" -6 --default-prefix ''
check "-6 --default-prefix 33 accepted" 0 "$C6" '' "$IN6" -6 --default-prefix 33
check "-6 -p 33 accepted" 0 "$C6" '' "$IN6" -6 -p 33
check "-6 -p abc accepted" 0 "$C6" '' "$IN6" -6 -p abc
# The value is skipped, not loaded: a value naming something that cannot be
# opened must still succeed, and a value that is a valid CIDR must not add a
# second source.
check "-6 --default-prefix missing-file is skipped" 0 "$C6" '' "$IN6" -6 --default-prefix "$tmpdir/no-such-file"
check "-6 -p missing-file is skipped" 0 "$C6" '' "$IN6" -6 -p "$tmpdir/no-such-file"
check "-6 --default-prefix 203.0.113.0/24 does not load the value" 0 "$C6" '' "$IN6" -6 --default-prefix 203.0.113.0/24
# IPv6 keeps /128 for a bare address whatever the skipped value says.
check "-6 --default-prefix 64 keeps /128" 0 '2001:db8::1\n' '' "$B6" -6 --default-prefix 64
check "-6 -p 64 keeps /128" 0 '2001:db8::1\n' '' "$B6" -6 -p 64
# Before -6 the IPv4 bound applies, so the same value is rejected.
check "--default-prefix 33 -6 uses the IPv4 bound" 1 '' "$(printf "$MDP" 33)\n" "$IN6" --default-prefix 33 -6
check "-p 33 -6 uses the IPv4 bound" 1 '' "$(printf "$MP_" 33)\n" "$IN6" -p 33 -6

echo "===== 5. --dns-threads bound 1..INT_MAX, in both families ====="
check "--dns-threads 1 control" 0 "$C4" '' "$IN4" --dns-threads 1
check "--dns-threads 2147483647 control (INT_MAX)" 0 "$C4" '' "$IN4" --dns-threads 2147483647
check "--dns-threads +1 is 1" 0 "$C4" '' "$IN4" --dns-threads +1
check "--dns-threads ' 1' is 1" 0 "$C4" '' "$IN4" --dns-threads ' 1'
check "--dns-threads +2147483647 is INT_MAX" 0 "$C4" '' "$IN4" --dns-threads +2147483647
check "--dns-threads 007 is 7" 0 "$C4" '' "$IN4" --dns-threads 007
bad "--dns-threads 0 rejected" "$MDT" 0 "$IN4" --dns-threads
bad "--dns-threads +0 rejected" "$MDT" +0 "$IN4" --dns-threads
bad "--dns-threads -0 rejected" "$MDT" -0 "$IN4" --dns-threads
bad "--dns-threads -1 rejected" "$MDT" -1 "$IN4" --dns-threads
bad "--dns-threads 2147483648 rejected (above INT_MAX)" "$MDT" 2147483648 "$IN4" --dns-threads
bad "--dns-threads LONG_MAX rejected" "$MDT" 9223372036854775807 "$IN4" --dns-threads
bad "--dns-threads abc rejected" "$MDT" abc "$IN4" --dns-threads
bad "--dns-threads 1x rejected" "$MDT" 1x "$IN4" --dns-threads
bad "--dns-threads ' 1 ' rejected" "$MDT" ' 1 ' "$IN4" --dns-threads
bad "--dns-threads empty rejected" "$MDT" '' "$IN4" --dns-threads
bad "--dns-threads overflow rejected" "$MDT" 99999999999999999999 "$IN4" --dns-threads
# No family guard on this option (src/iprange.c:699): validated in IPv6 too.
check "-6 --dns-threads +1 is 1" 0 "$C6" '' "$IN6" -6 --dns-threads +1
check "-6 --dns-threads ' 1' is 1" 0 "$C6" '' "$IN6" -6 --dns-threads ' 1'
check "-6 --dns-threads 2147483647 control" 0 "$C6" '' "$IN6" -6 --dns-threads 2147483647
bad "-6 --dns-threads 0 rejected" "$MDT" 0 "$IN6" -6 --dns-threads
bad "-6 --dns-threads abc rejected" "$MDT" abc "$IN6" -6 --dns-threads

echo "===== 6. Controls: the unsigned reduce options must not accept a sign ====="
check "--reduce-entries 5 control" 0 "$C4" '' "$IN4" --reduce-entries 5
check "--reduce-entries 0 control" 0 "$C4" '' "$IN4" --reduce-entries 0
check "--reduce-factor 5 control" 0 "$C4" '' "$IN4" --reduce-factor 5
check "--ipset-reduce-entries 5 control" 0 "$C4" '' "$IN4" --ipset-reduce-entries 5
bad "--reduce-entries +5 rejected (unsigned parser)" "iprange: Invalid value '%s' for --reduce-entries. It must be a non-negative integer." +5 "$IN4" --reduce-entries
bad "--reduce-entries ' 5' rejected (unsigned parser)" "iprange: Invalid value '%s' for --reduce-entries. It must be a non-negative integer." ' 5' "$IN4" --reduce-entries
bad "--reduce-entries -1 rejected (unsigned parser)" "iprange: Invalid value '%s' for --reduce-entries. It must be a non-negative integer." -1 "$IN4" --reduce-entries
bad "--reduce-factor +5 rejected (unsigned parser)" "iprange: Invalid value '%s' for --reduce-factor. It must be a non-negative integer percentage." +5 "$IN4" --reduce-factor
bad "--reduce-factor ' 5' rejected (unsigned parser)" "iprange: Invalid value '%s' for --reduce-factor. It must be a non-negative integer percentage." ' 5' "$IN4" --reduce-factor
bad "--ipset-reduce-entries +5 rejected (unsigned parser)" "iprange: Invalid value '%s' for --ipset-reduce-entries. It must be a non-negative integer." +5 "$IN4" --ipset-reduce-entries

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "# OK: all sibling numeric option spellings match the C reference"
