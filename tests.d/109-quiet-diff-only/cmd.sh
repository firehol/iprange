#!/bin/bash

# --quiet suppresses the DIFF result and nothing else.
#
# The C option text says "Do not print the actual ipset. Can only be used
# in DIFF mode." (src/iprange.c:305-308) and the flag gates exactly one
# call in each main: `if(!quiet) ipset_print(ips, print)` in the diff
# branch (src/iprange.c:1026) and the same line in the IPv6 runner
# (src/iprange6_main.c:414). Every other mode prints with --quiet set:
# merge, common and exclude call ipset_print() unconditionally, and the
# CSV modes have their own writers. --quiet never changes the diff exit
# code (1 when the diff is non-empty).
#
# Only exit codes and stdout are compared between engines.

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

IPRANGE=../../iprange
REFERENCE=${IPRANGE_REFERENCE:-/usr/bin/iprange}

printf '10.0.0.0/30\n'    > "$tmpdir/a"
printf '10.0.0.2/31\n'    > "$tmpdir/b"
printf '2001:db8::/125\n' > "$tmpdir/v6x"
printf '2001:db8::/126\n' > "$tmpdir/v6y"

fail=0
check() { # check <label> <expected-rc> <expected-stdout> <args...>
    local label=$1 erc=$2 eout=$3
    shift 3
    timeout 20 "$IPRANGE" "$@" < /dev/null > "$tmpdir/out" 2> "$tmpdir/err"
    local rc=$?
    if [ "$rc" -ne "$erc" ]; then
        echo "# ERROR: $label exit $rc, expected $erc"; cat "$tmpdir/err"; fail=1; return
    fi
    if ! cmp -s <(printf '%b' "$eout") "$tmpdir/out"; then
        echo "# ERROR: $label stdout differs"; cat -v "$tmpdir/out"; fail=1; return
    fi
    if [ -x "$REFERENCE" ]; then
        timeout 20 "$REFERENCE" "$@" < /dev/null > "$tmpdir/ref_out" 2> /dev/null
        local rrc=$?
        if [ "$rrc" -ne "$rc" ] || ! cmp -s "$tmpdir/ref_out" "$tmpdir/out"; then
            echo "# ERROR: $label differs from the C reference ($REFERENCE)"
            cat -v "$tmpdir/ref_out" "$tmpdir/out"; fail=1; return
        fi
    fi
    echo "# OK: $label (exit $rc)"
}

# --quiet keeps every non-diff print.
check "merge prints with --quiet"              0 '10.0.0.0/30\n'                 --quiet "$tmpdir/a"
check "--print-ranges prints with --quiet"     0 '10.0.0.0-10.0.0.3\n'           --print-ranges --quiet "$tmpdir/a"
check "--print-single-ips prints with --quiet" 0 '10.0.0.0\n10.0.0.1\n10.0.0.2\n10.0.0.3\n' --print-single-ips --quiet "$tmpdir/a"
check "common prints with --quiet"             0 '10.0.0.2/31\n'                 --common --quiet "$tmpdir/a" "$tmpdir/b"
check "exclude prints with --quiet"            0 '10.0.0.0/31\n'                 "$tmpdir/a" --except "$tmpdir/b" --quiet
check "compare CSV prints with --quiet"        0 "$tmpdir/a,$tmpdir/b,1,1,4,2,4,2\n" --compare --quiet "$tmpdir/a" "$tmpdir/b"
check "count-unique prints with --quiet"       0 '1,4\n'                          --count-unique --quiet "$tmpdir/a"
check "IPv6 merge prints with --quiet"         0 '::ffff:10.0.0.0/126\n'         -6 --quiet "$tmpdir/a"

# --quiet suppresses only the diff print, and never the exit code.
check "diff without --quiet prints and exits 1" 1 '10.0.0.0/31\n'                "$tmpdir/a" --diff "$tmpdir/b"
check "diff with --quiet is silent but exits 1" 1 ''                              "$tmpdir/a" --diff "$tmpdir/b" --quiet
check "empty diff with --quiet exits 0"         0 ''                              "$tmpdir/a" --diff "$tmpdir/a" --quiet
check "IPv6 diff with --quiet is silent"        1 ''                              -6 "$tmpdir/v6x" --diff "$tmpdir/v6y" --quiet

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "# OK: 12 --quiet cases match the C reference: only DIFF output is suppressed"
