#!/bin/bash

# A value-taking option that is the last argument is not an option.
#
# Every value-taking branch in C is guarded by `i+1 < argc` (for example
# --min-prefix at src/iprange.c:525 and --print-prefix at src/iprange.c:678).
# With no following argument the branch is never entered, so the option
# token itself falls through to the input path and is opened as a file:
# IPv4 fails with "Cannot load ipset: <option>" and exit 1, and the IPv6
# runner skips it as an unknown flag, leaving stdin as the input.
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
check() { # check <label> <expected-rc> <expected-stdout> <stdin-file> <args...>
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

for opt in --min-prefix --prefixes --default-prefix -p --dns-threads \
           --print-prefix --print-prefix-ips --print-prefix-nets \
           --print-suffix --print-suffix-ips --print-suffix-nets \
           --ipset-reduce --reduce-factor --ipset-reduce-entries --reduce-entries; do
    check "V4 trailing $opt is a file" 1 '' "$tmpdir/emptyin" "$opt"
done

# In IPv6 mode the trailing token is skipped by the re-scan, so stdin is read.
for opt in --min-prefix --prefixes --default-prefix --print-prefix; do
    check "-6 trailing $opt reads stdin" 0 '::/0\n' "$tmpdir/v6in" -6 "$opt"
done

# With an argument present the same options keep their normal effect.
check "--prefixes 31 with a value" 0 '10.0.0.0/31\n10.0.0.2/31\n' "$tmpdir/emptyin" --prefixes 31 "$tmpdir/a"

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "# OK: 20 trailing-value-option cases match the C reference"
