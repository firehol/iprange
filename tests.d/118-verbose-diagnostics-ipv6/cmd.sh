#!/bin/bash

# Verbose diagnostics, IPv6: the twin of the IPv4 family set.
#
# The verbose diagnostics of the legacy CLI are released behaviour, so each
# line is compared byte for byte against the C reference (the released C
# binary is the oracle for the legacy CLI).  Exit codes and stdout are
# compared with no exception.
#
# The IPv6 loader and printer are a separate implementation
# (src/ipset6_load.c, src/ipset6_print.c) with its own message set: the load
# line carries " (IPv6 mode)", there are no "Loaded optimized"/"Binary
# loaded" bookkeeping lines, merging and optimizing say "Merging ... to
# combined ipset (IPv6)", --compare keeps "Combining A and B" plus
# "Optimizing combined (IPv6)" (ipset6_combine.c:15), and --prefixes prints
# nothing because iprange6_main.c has no debug statements at all.  The IPv6
# run also never reaches the IPv4 timing report, so no line here is masked.
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

mkdir -p 'dir6'
mkdir -p 'emptydir6'

printf '%b' '2001:db8::/126\n' > 'dir6/a.txt'
printf '%b' '# nothing\n' > 'emptylist.txt'
printf '%b' 'one6.iprange\ntwo6.iprange\n' > 'list6.txt'
printf '%b' '2001:db8::/125\n' > 'one6.iprange'
printf '%b' '2001:db8:0:1::/64\n' > 'three6.iprange'
printf '%b' '2001:db8::/126\n' > 'two6.iprange'
printf '%b' '2001:db8::/120\n2001:db8::/126\n' > 'unsorted6.iprange'
printf '%b' 'iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\0032\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\0015\0001 \0007\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\0015\0001 ' > 'v2.bin'
check 'v6 merge' 0 \
      '2001:db8::/125\n' \
      'iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Merging two6.iprange to combined ipset (IPv6)\niprange: Optimizing combined ipset (IPv6)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /125 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n' \
      $'-6' $'-v' $'one6.iprange' $'two6.iprange'

check 'v6 merge three' 0 \
      '2001:db8::/125\n2001:db8:0:1::/64\n' \
      'iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Loading from three6.iprange (IPv6 mode)\niprange: Merging two6.iprange to combined ipset (IPv6)\niprange: Merging three6.iprange to combined ipset (IPv6)\niprange: Optimizing combined ipset (IPv6)\niprange: Printing combined ipset (IPv6) with 2 ranges, 18446744073709551624 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /64 counts 1 entries\n\t- prefix /125 counts 1 entries\n\ntotals: 3 lines read, 2 distinct IP ranges found, 2 CIDR prefixes, 2 CIDRs printed, 18446744073709551624 unique IPs\n' \
      $'-6' $'-v' $'one6.iprange' $'two6.iprange' $'three6.iprange'

check 'v6 single set' 0 \
      '2001:db8::/125\n' \
      'iprange: Loading from one6.iprange (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /125 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n' \
      $'-6' $'-v' $'one6.iprange'

check 'v6 except names result' 0 \
      '2001:db8::4/126\n' \
      'iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Removing IPs in two6.iprange from one6.iprange (IPv6)\niprange: Printing one6.iprange (IPv6) with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /126 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n' \
      $'-6' $'-v' $'one6.iprange' $'--except' $'two6.iprange'

check 'v6 compare keeps combining' 0 \
      'one6.iprange,two6.iprange,1,1,8,4,8,4\n' \
      'iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Combining one6.iprange and two6.iprange (IPv6)\niprange: Optimizing combined (IPv6)\n' \
      $'-6' $'-v' $'--compare' $'one6.iprange' $'two6.iprange'

check 'v6 compare first' 0 \
      'two6.iprange,1,4,4\nthree6.iprange,1,18446744073709551616,0\n' \
      'iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Loading from three6.iprange (IPv6 mode)\niprange: Combining two6.iprange and one6.iprange (IPv6)\niprange: Optimizing combined (IPv6)\niprange: Combining three6.iprange and one6.iprange (IPv6)\niprange: Optimizing combined (IPv6)\n' \
      $'-6' $'-v' $'--compare-first' $'one6.iprange' $'two6.iprange' $'three6.iprange'

check 'v6 common' 0 \
      '2001:db8::/126\n' \
      'iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Finding common IPs in one6.iprange and two6.iprange (IPv6)\niprange: Printing common (IPv6) with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /126 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n' \
      $'-6' $'-v' $'--common' $'one6.iprange' $'two6.iprange'

check 'v6 diff' 1 \
      '2001:db8::4/126\n' \
      'iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Finding diff IPs in one6.iprange and two6.iprange (IPv6)\niprange: Printing diff (IPv6) with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /126 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n' \
      $'-6' $'-v' $'one6.iprange' $'--diff' $'two6.iprange'

check 'v6 count unique merged' 0 \
      '1,8\n' \
      'iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Merging two6.iprange to combined ipset (IPv6)\niprange: Optimizing combined ipset (IPv6)\n' \
      $'-6' $'-v' $'--count-unique' $'one6.iprange' $'two6.iprange'

check 'v6 count unique single' 0 \
      '1,8\n' \
      'iprange: Loading from one6.iprange (IPv6 mode)\n' \
      $'-6' $'-v' $'--count-unique' $'one6.iprange'

check 'v6 count unique all' 0 \
      'one6.iprange,1,8\ntwo6.iprange,1,4\n' \
      'iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\n' \
      $'-6' $'-v' $'--count-unique-all' $'one6.iprange' $'two6.iprange'

check 'v6 print binary merged' 0 \
      'iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 2\nunique ips 8\nM<+\0032\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\0015\0001 \0007\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\0015\0001 ' \
      'iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Merging two6.iprange to combined ipset (IPv6)\niprange: Optimizing combined ipset (IPv6)\n' \
      $'-6' $'-v' $'--print-binary' $'one6.iprange' $'two6.iprange'

check 'v6 prefixes silent' 0 \
      '2001:db8::\n2001:db8::1\n2001:db8::2\n2001:db8::3\n2001:db8::4\n2001:db8::5\n2001:db8::6\n2001:db8::7\n' \
      'iprange: Loading from one6.iprange (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n8 printed CIDRs, break down by prefix:\n\t- prefix /128 counts 8 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 8 CIDRs printed, 8 unique IPs\n' \
      $'-6' $'-v' $'--prefixes' $'24,128' $'one6.iprange'

check 'v6 dir expansion silent' 0 \
      '2001:db8::/126\n' \
      'iprange: Loading from dir6/a.txt (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /126 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n' \
      $'-6' $'-v' $'@dir6'

check 'v6 list expansion silent' 0 \
      '2001:db8::/125\n' \
      'iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Merging two6.iprange to combined ipset (IPv6)\niprange: Optimizing combined ipset (IPv6)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /125 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n' \
      $'-6' $'-v' $'@list6.txt'

check 'v6 empty dir' 1 \
      '' \
      'iprange: No valid files found in directory: emptydir6\n' \
      $'-6' $'-v' $'@emptydir6'

check 'v6 empty list' 1 \
      '' \
      'iprange: No valid files found in file list: emptylist.txt\n' \
      $'-6' $'-v' $'@emptylist.txt'

check 'v6 empty file silent' 1 \
      '' \
      'iprange: empty6.iprange - No such file or directory\niprange: Cannot load ipset: empty6.iprange\n' \
      $'-6' $'-v' $'empty6.iprange'

check 'v6 stdin silent' 0 \
      '' \
      'iprange: Loading from stdin (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 0 ranges, 0 unique IPs\n\n0 printed CIDRs, break down by prefix:\n\ntotals: 0 lines read, 0 distinct IP ranges found, 0 CIDR prefixes, 0 CIDRs printed, 0 unique IPs\n' \
      $'-6' $'-v' $'-'

check 'v6 binary load silent' 0 \
      '2001:db8::/125\n' \
      'iprange: Loading from v2.bin (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /125 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n' \
      $'-6' $'-v' $'v2.bin'

check 'v6 unsorted non-optimized' 0 \
      '2001:db8::/120\n' \
      'iprange: Loading from unsorted6.iprange (IPv6 mode)\niprange: NON-OPTIMIZED unsorted6.iprange at line 2, entry 1, last was 2001:db8:: - 2001:db8::ff, new is 2001:db8:: - 2001:db8::3\niprange: Optimizing combined ipset (IPv6)\niprange: Printing combined ipset (IPv6) with 1 ranges, 256 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /120 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 256 unique IPs\n' \
      $'-6' $'-v' $'unsorted6.iprange'

check 'v6 unsorted merge' 0 \
      '2001:db8::/120\n' \
      'iprange: Loading from unsorted6.iprange (IPv6 mode)\niprange: NON-OPTIMIZED unsorted6.iprange at line 2, entry 1, last was 2001:db8:: - 2001:db8::ff, new is 2001:db8:: - 2001:db8::3\niprange: Loading from one6.iprange (IPv6 mode)\niprange: Merging one6.iprange to combined ipset (IPv6)\niprange: Optimizing combined ipset (IPv6)\niprange: Printing combined ipset (IPv6) with 1 ranges, 256 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /120 counts 1 entries\n\ntotals: 3 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 256 unique IPs\n' \
      $'-6' $'-v' $'unsorted6.iprange' $'one6.iprange'

check 'v6 unsorted except' 0 \
      '2001:db8::4/126\n2001:db8::8/125\n2001:db8::10/124\n2001:db8::20/123\n2001:db8::40/122\n2001:db8::80/121\n' \
      'iprange: Loading from unsorted6.iprange (IPv6 mode)\niprange: NON-OPTIMIZED unsorted6.iprange at line 2, entry 1, last was 2001:db8:: - 2001:db8::ff, new is 2001:db8:: - 2001:db8::3\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Optimizing unsorted6.iprange (IPv6)\niprange: Removing IPs in two6.iprange from unsorted6.iprange (IPv6)\niprange: Printing unsorted6.iprange (IPv6) with 1 ranges, 252 unique IPs\n\n6 printed CIDRs, break down by prefix:\n\t- prefix /121 counts 1 entries\n\t- prefix /122 counts 1 entries\n\t- prefix /123 counts 1 entries\n\t- prefix /124 counts 1 entries\n\t- prefix /125 counts 1 entries\n\t- prefix /126 counts 1 entries\n\ntotals: 3 lines read, 1 distinct IP ranges found, 6 CIDR prefixes, 6 CIDRs printed, 252 unique IPs\n' \
      $'-6' $'-v' $'unsorted6.iprange' $'--except' $'two6.iprange'

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "# OK: 23 cases match the C reference byte for byte"
