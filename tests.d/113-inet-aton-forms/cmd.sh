#!/bin/bash

# inet_aton(3) numeric IPv4 forms survive the legacy input path.
#
# C classifies a line whose first token is not pure [0-9./] as a hostname
# (src/ipset_load.c parse_line/parse_hostname) and hands it to
# getaddrinfo(), which answers numeric forms locally instead of querying
# DNS. The forms a hostname-shaped token can take are therefore still
# addresses: whole 32-bit hex, mixed-radix parts, and the abbreviated
# a / a.b / a.b.c shapes. A Go build without libc resolution needs the same
# short-circuit in its resolver, or the whole load aborts on a DNS error
# that the C reference never produces.
#
# These cases use only names that cannot be delegated, so they need no
# working DNS and no network: a numeric form must be answered locally, and
# a form the C rejects must fail.
#
# Only exit codes and stdout are compared between engines.

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

IPRANGE=../../iprange
REFERENCE=${IPRANGE_REFERENCE:-/usr/bin/iprange}

fail=0
check_form() { # check_form <expected-rc> <expected-stdout> <input-line>
    local erc=$1 eout=$2 line=$3
    printf '%s\n' "$line" > "$tmpdir/in"
    timeout 25 "$IPRANGE" < "$tmpdir/in" > "$tmpdir/out" 2> "$tmpdir/err"
    local rc=$?
    if [ "$rc" -ne "$erc" ]; then
        echo "# ERROR: form [$line] exit $rc, expected $erc"; cat "$tmpdir/err"; fail=1; return
    fi
    if ! cmp -s <(printf '%b' "$eout") "$tmpdir/out"; then
        echo "# ERROR: form [$line] stdout differs"; cat -v "$tmpdir/out"; fail=1; return
    fi
    if [ -x "$REFERENCE" ]; then
        timeout 25 "$REFERENCE" < "$tmpdir/in" > "$tmpdir/ref_out" 2> /dev/null
        local rrc=$?
        if [ "$rrc" -ne "$rc" ] || ! cmp -s "$tmpdir/ref_out" "$tmpdir/out"; then
            echo "# ERROR: form [$line] differs from the C reference ($REFERENCE)"
            cat -v "$tmpdir/ref_out" "$tmpdir/out"; fail=1; return
        fi
    fi
    echo "# OK: $line -> ${eout%\\n} (exit $rc)"
}

# Whole-value and abbreviated hex.
check_form 0 '10.0.0.1\n'         0x0A000001
check_form 0 '127.0.0.1\n'        0x7f000001
check_form 0 '255.255.255.255\n'  0xffffffff
check_form 0 '222.173.190.239\n'  0xdeadbeef
check_form 0 '10.11.12.13\n'      0x0a0b0c0d
check_form 0 '10.0.0.1\n'         0X0A000001
check_form 0 '0.0.0.10\n'         0xA
check_form 0 '0.0.0.0\n'          0x0
# Abbreviated part counts.
check_form 0 '127.0.0.1\n'        0x7f.1
check_form 0 '10.0.0.1\n'         0xA.1
check_form 0 '127.0.0.1\n'        0x7f.0x1
check_form 0 '1.2.3.4\n'          0x1.0x2.0x3.0x4
check_form 0 '1.2.0.3\n'          0x1.0x2.0x3
check_form 0 '1.0.0.2\n'          0x1.2
check_form 0 '8.0.0.1\n'          010.0x1
check_form 0 '1.255.255.255\n'    0x1.0xffffff
check_form 0 '1.1.255.255\n'      1.1.0xffff
check_form 0 '1.1.1.255\n'        0x1.0x1.0x1.0xff
# Octal and decimal abbreviations already worked; pinned to keep them so.
check_form 0 '10.0.0.1\n'         012.0.0.1
check_form 0 '8.8.8.8\n'          010.010.010.010
check_form 0 '10.0.0.1\n'         167772161
check_form 0 '10.0.0.1\n'         10.1
# Rejected forms: inet_aton fails, so C looks the token up as a name and the
# load aborts with exit 1 and no addresses.
for bad in 0x 0xzz 0x1. 1.0x 0x.1 0x-1 0x0x1 0x1_2 0x100000000 0x1.0x1000000 \
           1.1.0x10000 0x1.0x1.0x1.0x100 0x100.1.1.1 1.2.3.0x4 0x0A000001/24; do
    check_form 1 '' "$bad"
done

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "# OK: 38 inet_aton form cases match the C reference"
