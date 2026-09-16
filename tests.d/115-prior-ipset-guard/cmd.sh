#!/bin/bash

# The "an ipset is needed before <operator>" guard, and the family that
# applies it.
#
# C checks for a prior ipset in main() while scanning argv, only when the
# active family is not IPv6 (`if(active_family != 6)` at src/iprange.c:615,
# 623, 640), and it names the CANONICAL option in the diagnostic: passing
# the alias --complement still reports --except, and --diff-next still
# reports --diff. In IPv6 mode main() defers every load to iprange6_run(),
# which builds its own two groups and reports "No valid ipsets to process."
# instead, so the guard must not fire there.
#
# Because the guard is raised during the argv scan, the family is the one
# active at that position: "--diff FILE -6" is still an IPv4 guard failure.
#
# Only exit codes and stdout are compared between engines; stderr is
# pinned byte for byte against the C reference.

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

IPRANGE=../../iprange
REFERENCE=${IPRANGE_REFERENCE:-/usr/bin/iprange}

printf '10.0.0.0/30\n' > "$tmpdir/a"
printf '10.0.0.2/31\n' > "$tmpdir/b"
printf '::1\n'         > "$tmpdir/v6in"
: > "$tmpdir/emptyin"

fail=0
# check <label> <expected-rc> <expected-stdout> <expected-stderr> <stdin> <args...>
check() {
    local label=$1 erc=$2 eout=$3 eerr=$4 file=$5
    shift 5
    timeout 20 "$IPRANGE" "$@" < "$file" > "$tmpdir/out" 2> "$tmpdir/err"
    local rc=$?
    if [ "$rc" -ne "$erc" ]; then
        echo "# ERROR: $label exit $rc, expected $erc"; cat -v "$tmpdir/err"; fail=1; return
    fi
    if ! cmp -s <(printf '%b' "$eout") "$tmpdir/out"; then
        echo "# ERROR: $label stdout differs"; cat -v "$tmpdir/out"; fail=1; return
    fi
    if ! cmp -s <(printf '%b' "$eerr") "$tmpdir/err"; then
        echo "# ERROR: $label stderr differs"; cat -v "$tmpdir/err"; fail=1; return
    fi
    if [ -x "$REFERENCE" ]; then
        timeout 20 "$REFERENCE" "$@" < "$file" > "$tmpdir/ref_out" 2> "$tmpdir/ref_err"
        local rrc=$?
        if [ "$rrc" -ne "$rc" ] || ! cmp -s "$tmpdir/ref_out" "$tmpdir/out" ||
           ! cmp -s "$tmpdir/ref_err" "$tmpdir/err"; then
            echo "# ERROR: $label differs from the C reference ($REFERENCE)"; fail=1; return
        fi
    fi
    echo "# OK: $label (exit $rc)"
}

# IPv4: the guard fires, naming the canonical option.
check "--diff with no prior ipset" 1 '' \
    'iprange: An ipset is needed before --diff\n' "$tmpdir/emptyin" --diff "$tmpdir/a"
check "--except with no prior ipset" 1 '' \
    'iprange: An ipset is needed before --except\n' "$tmpdir/emptyin" --except "$tmpdir/a"
check "--compare-next with no prior ipset" 1 '' \
    'iprange: An ipset is needed before --compare-next\n' "$tmpdir/emptyin" --compare-next "$tmpdir/a"
# Aliases report the canonical option name, not the spelling used.
check "--complement reports --except" 1 '' \
    'iprange: An ipset is needed before --except\n' "$tmpdir/emptyin" --complement "$tmpdir/a"
check "--diff-next reports --diff" 1 '' \
    'iprange: An ipset is needed before --diff\n' "$tmpdir/emptyin" --diff-next "$tmpdir/a"
# --common needs two ipsets and has its own text.
check "--common with no prior ipset" 1 '' \
    'iprange: two ipsets at least are needed to be compared to find their common IPs.\n' \
    "$tmpdir/emptyin" --common "$tmpdir/a"

# IPv6: the guard is skipped and the run fails later instead.
check "-6 --diff reports no valid ipsets" 1 '' \
    'iprange: No valid ipsets to process.\n' "$tmpdir/v6in" -6 --diff "$tmpdir/a"
check "-6 --except reports no valid ipsets" 1 '' \
    'iprange: No valid ipsets to process.\n' "$tmpdir/emptyin" -6 --except "$tmpdir/a"
check "-6 --compare-next reports no valid ipsets" 1 '' \
    'iprange: No valid ipsets to process.\n' "$tmpdir/emptyin" -6 --compare-next "$tmpdir/a"
check "-6 --complement reports no valid ipsets" 1 '' \
    'iprange: No valid ipsets to process.\n' "$tmpdir/emptyin" -6 --complement "$tmpdir/a"
# Position decides the family: before -6 the IPv4 guard still applies.
check "--diff seen before -6 uses the IPv4 guard" 1 '' \
    'iprange: An ipset is needed before --diff\n' "$tmpdir/v6in" --diff "$tmpdir/a" -6
# With a prior ipset the same operators work normally: the diff is
# computed and printed instead of the guard firing.
check "--diff with a prior ipset works" 1 '10.0.0.0/31\n' '' "$tmpdir/emptyin" "$tmpdir/a" --diff "$tmpdir/b"

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "# OK: 12 prior-ipset guard cases match the C reference"
