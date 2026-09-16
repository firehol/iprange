#!/bin/bash

# Verbose diagnostics, IPv4: every -v line is the C's own bytes.
#
# The verbose diagnostics of the legacy CLI are released behaviour, so each
# line is compared byte for byte against the C reference (the released C
# binary is the oracle for the legacy CLI).  Exit codes and stdout are
# compared with no exception.
#
# The families pinned here are the ones the C prints while it loads,
# merges, optimizes, compares and prints: "Loading from NAME", "Loaded
# optimized NAME", "NAME is empty", "Binary loaded FLAG NAME", "Merging NAME
# to combined ipset", "Optimizing combined ipset", "Printing NAME with N
# ranges, M unique IPs", "Finding common IPs in A and B", "Finding diff IPs",
# "Removing IPs in B from A", "NON-OPTIMIZED ..." and "Enabling prefix N".
# --except names the result after group A (ipset_exclude.c:23), --compare
# prints one "Finding common IPs in" per pair (ipset_common.c:23) rather than
# the combine/optimize pair, and --count-unique uses the silent unique
# helper, so it prints no "Is already optimized" line.
#
# One line cannot be reproduced byte for byte: the C prints its own four
# wall-clock readings, "completed in %0.5f seconds (read %0.5f + think
# %0.5f + speak %0.5f)" (src/iprange.c:1216-1224).  A line is masked only
# when it matches that exact shape - the literal prefix, four decimal
# numbers with five fraction digits, and the literal separators - and both
# the engine and the reference are masked the same way.  A missing line or a
# line of any other shape is therefore compared verbatim and fails.

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

ROOT=$(cd ../.. && pwd)
IPRANGE="$ROOT/iprange"
REFERENCE=${IPRANGE_REFERENCE:-/usr/bin/iprange}

fail=0

# mask_wallclock <file>: rewrite a whole line of the exact C timing shape.
mask_wallclock() {
    sed -E -i 's/^completed in [0-9]+\.[0-9]{5} seconds \(read [0-9]+\.[0-9]{5} \+ think [0-9]+\.[0-9]{5} \+ speak [0-9]+\.[0-9]{5}\)$/\<WALLCLOCK>/' "$1"
}

# check <label> <rc> <stdout-printf> <masked-stderr-printf> <args...>
check() {
    local label=$1 erc=$2 eout=$3 eerr=$4
    shift 4
    timeout 20 "$IPRANGE" "$@" < /dev/null > "$tmpdir/out" 2> "$tmpdir/err"
    local rc=$?
    mask_wallclock "$tmpdir/err"
    if [ "$rc" -ne "$erc" ]; then
        echo "# ERROR: $label exit $rc, expected $erc"; cat -v "$tmpdir/err"; fail=1; return
    fi
    if ! cmp -s <(printf '%b' "$eout") "$tmpdir/out"; then
        echo "# ERROR: $label stdout differs"; cat -v "$tmpdir/out"; fail=1; return
    fi
    if ! cmp -s <(printf '%b' "$eerr") "$tmpdir/err"; then
        echo "# ERROR: $label stderr differs"
        echo "#   expected: $(printf '%b' "$eerr" | cat -v)"
        echo "#   actual:   $(cat -v "$tmpdir/err")"
        fail=1; return
    fi
    if [ -x "$REFERENCE" ]; then
        timeout 20 "$REFERENCE" "$@" < /dev/null > "$tmpdir/ref_out" 2> "$tmpdir/ref_err"
        local rrc=$?
        mask_wallclock "$tmpdir/ref_err"
        if [ "$rrc" -ne "$rc" ] || ! cmp -s "$tmpdir/ref_out" "$tmpdir/out" ||
           ! cmp -s "$tmpdir/ref_err" "$tmpdir/err"; then
            echo "# ERROR: $label differs from the C reference ($REFERENCE)"
            echo "#   reference exit $rrc, engine exit $rc"
            diff <(cat -v "$tmpdir/ref_err") <(cat -v "$tmpdir/err") | head -20
            fail=1; return
        fi
    fi
    echo "# OK: $label (exit $rc)"
}

# The diagnostics echo the name they were given, so every case runs from
# inside the fixture directory and uses relative names.  That keeps the pins
# independent of where the suite happens to be checked out.
cd "$tmpdir" || exit 1

BAD=$'\377'

mkdir -p 'dir'

printf '%b' '8.8.8.8\n' > 'dir/a.txt'
printf '%b' '9.9.9.9\n' > "dir/z"${BAD}"y.txt"
printf '%b' '' > 'empty.iprange'
printf '%b' 'one.iprange\ntwo.iprange\n' > 'list.txt'
printf '%b' '10.0.0.0/30\n' > 'one.iprange'
printf '%b' '10.0.0.4/30\n' > 'three.iprange'
printf '%b' '10.0.0.2/31\n' > 'two.iprange'
printf '%b' '10.0.0.8/30\n10.0.0.0/30\n1.1.1.1\n' > 'unsorted.iprange'
check 'merge two sets' 0 \
      '10.0.0.0/30\n' \
      'iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'one.iprange' $'two.iprange'

check 'merge three sets' 0 \
      '10.0.0.0/29\n' \
      'iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Loading from three.iprange\niprange: Loaded optimized three.iprange\niprange: Merging two.iprange to combined ipset\niprange: Merging three.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /29 counts 1 entries\n\ntotals: 3 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'one.iprange' $'two.iprange' $'three.iprange'

check 'merge single set' 0 \
      '10.0.0.0/30\n' \
      'iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'one.iprange'

check 'union two sets' 0 \
      '10.0.0.0/30\n' \
      'iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'--union' $'one.iprange' $'two.iprange'

check 'reduce merged sets' 0 \
      '10.0.0.0/30\n' \
      'iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\n\nCounting prefixes in combined ipset\nBreak down by prefix:\n\t- prefix /30 counts 1 entries\nTotal 1 entries generated\nAcceptable is to reach 16384 entries by reducing prefixes\n\tNothing more to reduce\n\nEliminated 0 out of 1 prefixes (1 remain in the final set).\n\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'--ipset-reduce' $'20' $'one.iprange' $'two.iprange'

check 'except names result by group A' 0 \
      '10.0.0.0/31\n' \
      'iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Removing IPs in two.iprange from one.iprange\niprange: Printing one.iprange with 1 ranges, 2 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /31 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'one.iprange' $'--except' $'two.iprange'

check 'except three group A sets' 0 \
      '10.0.0.0/31\n' \
      'iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to one.iprange\niprange: Optimizing one.iprange\niprange: Removing IPs in two.iprange from one.iprange\niprange: Printing one.iprange with 1 ranges, 2 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /31 counts 1 entries\n\ntotals: 3 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'one.iprange' $'two.iprange' $'--except' $'two.iprange'

check 'compare finds common ips' 0 \
      'one.iprange,two.iprange,1,1,4,2,4,2\n' \
      'iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Is already optimized one.iprange\niprange: Is already optimized two.iprange\niprange: Finding common IPs in one.iprange and two.iprange\n<WALLCLOCK>\n' \
      $'-v' $'--compare' $'one.iprange' $'two.iprange'

check 'compare three sets' 0 \
      'one.iprange,two.iprange,1,1,4,2,4,2\none.iprange,three.iprange,1,1,4,4,8,0\ntwo.iprange,three.iprange,1,1,2,4,6,0\n' \
      'iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Loading from three.iprange\niprange: Loaded optimized three.iprange\niprange: Is already optimized one.iprange\niprange: Is already optimized two.iprange\niprange: Is already optimized three.iprange\niprange: Finding common IPs in one.iprange and two.iprange\niprange: Finding common IPs in one.iprange and three.iprange\niprange: Finding common IPs in two.iprange and three.iprange\n<WALLCLOCK>\n' \
      $'-v' $'--compare' $'one.iprange' $'two.iprange' $'three.iprange'

check 'compare first' 0 \
      'two.iprange,1,2,2\nthree.iprange,1,4,0\n' \
      'iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Loading from three.iprange\niprange: Loaded optimized three.iprange\niprange: Is already optimized one.iprange\niprange: Is already optimized two.iprange\niprange: Is already optimized three.iprange\niprange: Finding common IPs in two.iprange and one.iprange\niprange: Finding common IPs in three.iprange and one.iprange\n<WALLCLOCK>\n' \
      $'-v' $'--compare-first' $'one.iprange' $'two.iprange' $'three.iprange'

check 'compare next' 0 \
      'one.iprange,two.iprange,1,1,4,2,4,2\n' \
      'iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Is already optimized one.iprange\niprange: Is already optimized two.iprange\niprange: Finding common IPs in one.iprange and two.iprange\n<WALLCLOCK>\n' \
      $'-v' $'one.iprange' $'--compare-next' $'two.iprange'

check 'common of two sets' 0 \
      '10.0.0.2/31\n' \
      'iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Finding common IPs in one.iprange and two.iprange\niprange: Printing common with 1 ranges, 2 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /31 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'--common' $'one.iprange' $'two.iprange'

check 'diff of two sets' 1 \
      '10.0.0.0/31\n' \
      'iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Finding diff IPs in one.iprange and two.iprange\niprange: Printing diff with 1 ranges, 2 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /31 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'one.iprange' $'--diff' $'two.iprange'

check 'count unique merged' 0 \
      '1,4\n' \
      'iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\n<WALLCLOCK>\n' \
      $'-v' $'--count-unique' $'one.iprange' $'two.iprange'

check 'count unique single set' 0 \
      '1,4\n' \
      'iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\n<WALLCLOCK>\n' \
      $'-v' $'--count-unique' $'one.iprange'

check 'count unique all' 0 \
      'one.iprange,1,4\ntwo.iprange,1,2\n' \
      'iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Is already optimized one.iprange\niprange: Is already optimized two.iprange\n<WALLCLOCK>\n' \
      $'-v' $'--count-unique-all' $'one.iprange' $'two.iprange'

check 'print binary merged' 0 \
      'iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 2\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' \
      'iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\n<WALLCLOCK>\n' \
      $'-v' $'--print-binary' $'one.iprange' $'two.iprange'

check 'print ranges merged' 0 \
      '10.0.0.0-10.0.0.3\n' \
      'iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\ntotals: 2 lines read, 1 distinct IP ranges found, 0 CIDR prefixes, 1 ranges printed, 4 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'--print-ranges' $'one.iprange' $'two.iprange'

check 'enabling prefix announcements' 0 \
      '10.0.0.0\n10.0.0.1\n10.0.0.2\n10.0.0.3\n' \
      'Enabling prefix 24\nEnabling prefix 32\niprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n4 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 4 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 4 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'--prefixes' $'24,32' $'one.iprange'

check 'enabling prefix duplicates' 0 \
      '10.0.0.0\n10.0.0.1\n10.0.0.2\n10.0.0.3\n' \
      'Enabling prefix 24\nEnabling prefix 24\nEnabling prefix 8\niprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n4 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 4 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 4 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'--prefixes' $'24,24,8' $'one.iprange'

check 'dir expansion' 0 \
      '8.8.8.8\n9.9.9.9\n' \
      'iprange: Loading files from directory dir\niprange: Loading file dir/a.txt from directory dir\niprange: Loading from dir/a.txt\niprange: Loaded optimized dir/a.txt\niprange: Loading file dir/z\0377y.txt from directory dir\niprange: Loading from dir/z\0377y.txt\niprange: Loaded optimized dir/z\0377y.txt\niprange: Merging dir/z\0377y.txt to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'@dir'

check 'list expansion' 0 \
      '10.0.0.0/30\n' \
      'iprange: Loading files from list list.txt\niprange: Loading file one.iprange from list (line 1)\niprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading file two.iprange from list (line 2)\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'@list.txt'

check 'empty file reports' 0 \
      '' \
      'iprange: Loading from empty.iprange\niprange: empty.iprange is empty\niprange: Printing combined ipset with 0 ranges, 0 unique IPs\n\n0 printed CIDRs, break down by prefix:\n\ntotals: 0 lines read, 0 distinct IP ranges found, 0 CIDR prefixes, 0 CIDRs printed, 0 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'empty.iprange'

check 'stdin' 0 \
      '' \
      'iprange: Loading from stdin\niprange: stdin is empty\niprange: Printing combined ipset with 0 ranges, 0 unique IPs\n\n0 printed CIDRs, break down by prefix:\n\ntotals: 0 lines read, 0 distinct IP ranges found, 0 CIDR prefixes, 0 CIDRs printed, 0 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'-'

check 'unsorted reports non-optimized' 0 \
      '1.1.1.1\n10.0.0.0/30\n10.0.0.8/30\n' \
      'iprange: Loading from unsorted.iprange\niprange: NON-OPTIMIZED unsorted.iprange at line 2, entry 1, last was 10.0.0.8 (167772168) - 10.0.0.11 (167772171), new is 10.0.0.0 (167772160) - 10.0.0.3 (167772163)\niprange: Loaded non-optimized unsorted.iprange\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 3 ranges, 9 unique IPs\n\n3 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 2 entries\n\t- prefix /32 counts 1 entries\n\ntotals: 3 lines read, 3 distinct IP ranges found, 2 CIDR prefixes, 3 CIDRs printed, 9 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'unsorted.iprange'

check 'unsorted merge' 0 \
      '1.1.1.1\n10.0.0.0/30\n10.0.0.8/30\n' \
      'iprange: Loading from unsorted.iprange\niprange: NON-OPTIMIZED unsorted.iprange at line 2, entry 1, last was 10.0.0.8 (167772168) - 10.0.0.11 (167772171), new is 10.0.0.0 (167772160) - 10.0.0.3 (167772163)\niprange: Loaded non-optimized unsorted.iprange\niprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Merging one.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 3 ranges, 9 unique IPs\n\n3 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 2 entries\n\t- prefix /32 counts 1 entries\n\ntotals: 4 lines read, 3 distinct IP ranges found, 2 CIDR prefixes, 3 CIDRs printed, 9 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'unsorted.iprange' $'one.iprange'

check 'unsorted except names result' 0 \
      '1.1.1.1\n10.0.0.0/31\n10.0.0.8/30\n' \
      'iprange: Loading from unsorted.iprange\niprange: NON-OPTIMIZED unsorted.iprange at line 2, entry 1, last was 10.0.0.8 (167772168) - 10.0.0.11 (167772171), new is 10.0.0.0 (167772160) - 10.0.0.3 (167772163)\niprange: Loaded non-optimized unsorted.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Optimizing unsorted.iprange\niprange: Removing IPs in two.iprange from unsorted.iprange\niprange: Printing unsorted.iprange with 3 ranges, 7 unique IPs\n\n3 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\t- prefix /31 counts 1 entries\n\t- prefix /32 counts 1 entries\n\ntotals: 4 lines read, 3 distinct IP ranges found, 3 CIDR prefixes, 3 CIDRs printed, 7 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'unsorted.iprange' $'--except' $'two.iprange'

check 'unsorted compare' 0 \
      'unsorted.iprange,two.iprange,3,1,9,2,9,2\n' \
      'iprange: Loading from unsorted.iprange\niprange: NON-OPTIMIZED unsorted.iprange at line 2, entry 1, last was 10.0.0.8 (167772168) - 10.0.0.11 (167772171), new is 10.0.0.0 (167772160) - 10.0.0.3 (167772163)\niprange: Loaded non-optimized unsorted.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Optimizing unsorted.iprange\niprange: Is already optimized two.iprange\niprange: Finding common IPs in unsorted.iprange and two.iprange\n<WALLCLOCK>\n' \
      $'-v' $'--compare' $'unsorted.iprange' $'two.iprange'

check 'single ips' 0 \
      '10.0.0.0\n10.0.0.1\n10.0.0.2\n10.0.0.3\n' \
      'iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 4 IPs printed, 4 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'-1' $'one.iprange'

check 'ranges mode' 1 \
      '' \
      'iprange: -r - No such file or directory\niprange: Cannot load ipset: -r\n' \
      $'-v' $'-r' $'one.iprange'

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "# OK: 30 cases match the C reference byte for byte"
