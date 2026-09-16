#!/bin/bash

# Unrecognized dash-prefixed arguments: the two mains differ.
#
# The IPv4 main tries any argument its option chain did not match as an
# input file (src/iprange.c:905-915), so `--bogus` fails as a missing file.
# The IPv6 runner re-scans argv and skips every argument that starts with
# '-' and is not exactly "-" (src/iprange6_main.c:176), so in IPv6 mode an
# unknown option is silently ignored and, when nothing else was given, the
# run reads stdin. Lowercase -c is not the counting option (that is -C), so
# it belongs to this skipped class; -- is skipped as well because its second
# character is not NUL.
#
# The class is exercised in both orders: a dash argument before -6 is seen
# by the IPv4 main with the family still unset, so it is a file there.
#
# Only exit codes and stdout are compared between engines.

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

IPRANGE=../../iprange
REFERENCE=${IPRANGE_REFERENCE:-/usr/bin/iprange}

printf '10.0.0.0/30\n' > "$tmpdir/a"
printf '::/0\n'        > "$tmpdir/v6in"
: > "$tmpdir/emptyin"

fail=0
check_stdin() { # check <label> <expected-rc> <expected-stdout> <stdin-file> <args...>
    local label=$1 erc=$2 eout=$3 file=$4
    shift 4
    timeout 20 "$IPRANGE" "$@" < "$file" > "$tmpdir/out" 2> "$tmpdir/err"
    local rc=$?
    if [ "$rc" -ne "$erc" ]; then
        echo "# ERROR: $label exit $rc, expected $erc"; cat "$tmpdir/err"; fail=1; return
    fi
    if ! cmp -s <(printf '%b' "$eout") "$tmpdir/out"; then
        echo "# ERROR: $label stdout differs"; cat -v "$tmpdir/out"; fail=1; return
    fi
    if [ -x "$REFERENCE" ]; then
        timeout 20 "$REFERENCE" "$@" < "$file" > "$tmpdir/ref_out" 2> /dev/null
        local rrc=$?
        if [ "$rrc" -ne "$rc" ] || ! cmp -s "$tmpdir/ref_out" "$tmpdir/out"; then
            echo "# ERROR: $label differs from the C reference ($REFERENCE)"
            cat -v "$tmpdir/ref_out" "$tmpdir/out"; fail=1; return
        fi
    fi
    echo "# OK: $label (exit $rc)"
}

# IPv4 (default and -4): the unknown option is a file, so the run fails.
check_stdin "--bogus is a file"           1 '' "$tmpdir/emptyin" --bogus-opt -
check_stdin "-c is a file"                1 '' "$tmpdir/emptyin" -c -
check_stdin "-- is a file"                1 '' "$tmpdir/emptyin" -- -
check_stdin "-4 --bogus is a file"        1 '' "$tmpdir/emptyin" -4 --bogus-opt -
check_stdin "--bogus before a real file"  1 '' "$tmpdir/emptyin" --bogus-opt "$tmpdir/a"
check_stdin "--bogus after a real file"   1 '' "$tmpdir/emptyin" "$tmpdir/a" --bogus-opt

# IPv6: the unknown option is skipped.
check_stdin "-6 -c reads stdin"           0 '::/0\n'        "$tmpdir/v6in" -6 -c
check_stdin "-6 --bogus - reads stdin"    0 '::/0\n'        "$tmpdir/v6in" -6 --bogus -
check_stdin "-6 -- - reads stdin"         0 '::/0\n'        "$tmpdir/v6in" -6 -- -
check_stdin "-6 --bogus --merge -"        0 '::/0\n'        "$tmpdir/v6in" -6 --bogus --merge -
check_stdin "-6 -c then an IPv4 file"     0 '::ffff:10.0.0.0/126\n' "$tmpdir/emptyin" -6 -c "$tmpdir/a"
# Order matters: -c is seen before -6, so the IPv4 main loads it as a file.
check_stdin "-c before -6 is a file"      1 ''              "$tmpdir/emptyin" -c -6 "$tmpdir/a"

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "# OK: 11 unrecognized-dash-argument cases match the C reference"
