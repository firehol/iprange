#!/bin/bash

# Diagnostic bytes: names with invalid UTF-8, and binary validation.
#
# The verbose diagnostics of the legacy CLI are released behaviour, so each
# line is compared byte for byte against the C reference (the released C
# binary is the oracle for the legacy CLI).  Exit codes and stdout are
# compared with no exception.
#
# Two classes share a fixture set here.  (1) A POSIX name is any
# sequence of non-NUL bytes, so a name may hold 0xFF; the C writes those
# bytes back verbatim from fprintf(stderr, "%s"), and an engine that decodes
# the name to a string first emits U+FFFD instead.  (2) A v1/v2 binary input
# that fails header validation is reported with the name and the raw text of
# the offending line (src/ipset_binary.c:203-297, src/ipset6_binary.c:136-230);
# the same rule applies to the name, so a name holding 0xFF must print 0xFF
# in every one of those messages, including the "Cannot load ipset:" line
# the caller appends after a fast-load failure.
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


printf '%b' '2001:db8::/125\n' > "bad6"${BAD}"name.iprange"
printf '%b' 'iprange binary format v1.0\nmaybe-optimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > "bad"${BAD}"flag.bin"
printf '%b' '1.2.3.4\n' > "bad"${BAD}"name.iprange"
printf '%b' 'iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > "bad"${BAD}"nomark.bin"
printf '%b' 'iprange binary format v1.0\noptimized\nrecord size 8\nrecords abc\nbytes 12\nlines 1\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > "bad"${BAD}"recs.bin"
printf '%b' 'iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nbytes 5\nbytes 36\nlines 1\nunique ips 8\nM<+\0032\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\0015\0001 \0007\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\0015\0001 ' > 'd6_badbytes.bin'
printf '%b' 'iprange binary format v2.0\nfamily ipv4\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\0032\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\0015\0001 \0007\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\0015\0001 ' > 'd6_badfamily.bin'
printf '%b' 'iprange binary format v2.0\nipv6\nrecord size 16\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\0032\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\0015\0001 \0007\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\0015\0001 ' > 'd6_badrecsize.bin'
printf '%b' 'iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nunique ips zz\nunique ips 8\nM<+\0032\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\0015\0001 \0007\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\0015\0001 ' > 'd6_badunique.bin'
printf '%b' 'iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nunique ips 0\nunique ips 8\nM<+\0032\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\0015\0001 \0007\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\0015\0001 ' > 'd6_lowunique.bin'
printf '%b' 'iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nbytes 5\nbytes 36\nlines 1\nunique ips 8\nM<+\0032\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\0015\0001 \0007\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\0015\0001 ' > "d6bad"${BAD}"bytes.bin"
printf '%b' 'iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 999\nlines 1\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > 'd_badbytes.bin'
printf '%b' 'iprange binary format v1.0\nmaybe-optimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > 'd_badflag.bin'
printf '%b' 'iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 0\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > 'd_badlines.bin'
printf '%b' 'iprange binary format v1.0\noptimized\nrecord size 8\nrecords abc\nbytes 12\nlines 1\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > 'd_badrecords.bin'
printf '%b' 'iprange binary format v1.0\noptimized\nrecord size 9\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > 'd_badrecsize.bin'
printf '%b' 'iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips qq\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > 'd_badunique.bin'
printf '%b' 'iprange binary format v1.0\noptimized\nrecord size 8\nrecords 18446744073709551615\nbytes 12\nlines 1\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > 'd_hugerecords.bin'
printf '%b' 'iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 0\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > 'd_lowunique.bin'
printf '%b' 'iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > 'd_nomarker.bin'
printf '%b' 'iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\njunk' > 'd_trailing.bin'
printf '%b' 'iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\0032\0000\0000' > 'd_truncated.bin'
printf '%b' 'x y\0000z\n' > 'nulsp.iprange'
printf '%b' '10.0.0.2/31\n' > 'two.iprange'
printf '%b' 'iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > 'v1.bin'
printf '%b' 'iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > "v1"${BAD}".bin"
printf '%b' 'iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\0032\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\0015\0001 \0007\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\0015\0001 ' > 'v2.bin'
printf '%b' 'iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\0032\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\0015\0001 \0007\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\0015\0001 ' > "v2"${BAD}".bin"
check 'merge names the bytes' 0 \
      '1.2.3.4\n10.0.0.2/31\n' \
      'iprange: Loading from bad\0377name.iprange\niprange: Loaded optimized bad\0377name.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 2 ranges, 3 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /31 counts 1 entries\n\t- prefix /32 counts 1 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 2 CIDR prefixes, 2 CIDRs printed, 3 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'bad\377name.iprange' $'two.iprange'

check 'as label with 0xFF' 0 \
      '1.2.3.4\n' \
      'iprange: Loading from bad\0377name.iprange\niprange: Loaded optimized bad\0377name.iprange\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'bad\377name.iprange' $'as' $'lab\377el'

check 'except names the bytes' 0 \
      '1.2.3.4\n' \
      'iprange: Loading from bad\0377name.iprange\niprange: Loaded optimized bad\0377name.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Removing IPs in two.iprange from bad\0377name.iprange\niprange: Printing bad\0377name.iprange with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'bad\377name.iprange' $'--except' $'two.iprange'

check 'compare names the bytes' 0 \
      'bad\0377name.iprange,two.iprange,1,1,1,2,3,0\n' \
      'iprange: Loading from bad\0377name.iprange\niprange: Loaded optimized bad\0377name.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Is already optimized bad\0377name.iprange\niprange: Is already optimized two.iprange\niprange: Finding common IPs in bad\0377name.iprange and two.iprange\n<WALLCLOCK>\n' \
      $'-v' $'--compare' $'bad\377name.iprange' $'two.iprange'

check 'compare next names the bytes' 0 \
      'bad\0377name.iprange,two.iprange,1,1,1,2,3,0\n' \
      'iprange: Loading from bad\0377name.iprange\niprange: Loaded optimized bad\0377name.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Is already optimized bad\0377name.iprange\niprange: Is already optimized two.iprange\niprange: Finding common IPs in bad\0377name.iprange and two.iprange\n<WALLCLOCK>\n' \
      $'-v' $'bad\377name.iprange' $'--compare-next' $'two.iprange'

check 'diff names the bytes' 1 \
      '1.2.3.4\n10.0.0.2/31\n' \
      'iprange: Loading from bad\0377name.iprange\niprange: Loaded optimized bad\0377name.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Finding diff IPs in bad\0377name.iprange and two.iprange\niprange: Printing diff with 2 ranges, 3 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /31 counts 1 entries\n\t- prefix /32 counts 1 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 2 CIDR prefixes, 2 CIDRs printed, 3 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'bad\377name.iprange' $'--diff' $'two.iprange'

check 'count unique all bytes' 0 \
      'bad\0377name.iprange,1,1\ntwo.iprange,1,2\n' \
      'iprange: Loading from bad\0377name.iprange\niprange: Loaded optimized bad\0377name.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Is already optimized bad\0377name.iprange\niprange: Is already optimized two.iprange\n<WALLCLOCK>\n' \
      $'-v' $'--count-unique-all' $'bad\377name.iprange' $'two.iprange'

check 'binary bytes in v4' 0 \
      '10.0.0.0/30\n' \
      'iprange: Loading from v1\0377.bin\niprange: Binary loaded optimized v1\0377.bin\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'v1\377.bin'

check 'binary bytes in v6' 0 \
      '2001:db8::/125\n' \
      'iprange: Loading from v2\0377.bin (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /125 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n' \
      $'-6' $'-v' $'v2\377.bin'

check 'v6 name bytes' 0 \
      '2001:db8::/125\n' \
      'iprange: Loading from bad6\0377name.iprange (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /125 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n' \
      $'-6' $'-v' $'bad6\377name.iprange'

check 'damaged flag line' 1 \
      '' \
      'iprange: d_badflag.bin 2nd line should be the optimized flag, but found \047maybe-optimized\n\047.\niprange: Cannot fast load d_badflag.bin\niprange: Cannot load ipset: d_badflag.bin\n' \
      $'d_badflag.bin'

check 'damaged flag line verbose' 1 \
      '' \
      'iprange: Loading from d_badflag.bin\niprange: d_badflag.bin 2nd line should be the optimized flag, but found \047maybe-optimized\n\047.\niprange: Cannot fast load d_badflag.bin\niprange: Cannot load ipset: d_badflag.bin\n' \
      $'-v' $'d_badflag.bin'

check 'damaged record size' 1 \
      '' \
      'iprange: d_badrecsize.bin: invalid record size 9 (expected 8)\niprange: Cannot fast load d_badrecsize.bin\niprange: Cannot load ipset: d_badrecsize.bin\n' \
      $'d_badrecsize.bin'

check 'damaged record size verbose' 1 \
      '' \
      'iprange: Loading from d_badrecsize.bin\niprange: d_badrecsize.bin: invalid record size 9 (expected 8)\niprange: Cannot fast load d_badrecsize.bin\niprange: Cannot load ipset: d_badrecsize.bin\n' \
      $'-v' $'d_badrecsize.bin'

check 'records not a number' 1 \
      '' \
      'iprange: d_badrecords.bin: invalid records value \047abc\n\047\niprange: Cannot fast load d_badrecords.bin\niprange: Cannot load ipset: d_badrecords.bin\n' \
      $'d_badrecords.bin'

check 'records overflow bound' 1 \
      '' \
      'iprange: d_hugerecords.bin: invalid number of records (18446744073709551615)\niprange: Cannot fast load d_hugerecords.bin\niprange: Cannot load ipset: d_hugerecords.bin\n' \
      $'d_hugerecords.bin'

check 'bytes mismatch' 1 \
      '' \
      'iprange: d_badbytes.bin invalid number of bytes, found 999, expected 12.\niprange: Cannot fast load d_badbytes.bin\niprange: Cannot load ipset: d_badbytes.bin\n' \
      $'d_badbytes.bin'

check 'bytes mismatch verbose' 1 \
      '' \
      'iprange: Loading from d_badbytes.bin\niprange: d_badbytes.bin invalid number of bytes, found 999, expected 12.\niprange: Cannot fast load d_badbytes.bin\niprange: Cannot load ipset: d_badbytes.bin\n' \
      $'-v' $'d_badbytes.bin'

check 'lines below entries' 1 \
      '' \
      'iprange: d_badlines.bin: lines (0) cannot be less than entries (1)\niprange: Cannot fast load d_badlines.bin\niprange: Cannot load ipset: d_badlines.bin\n' \
      $'d_badlines.bin'

check 'unique ips not a number' 1 \
      '' \
      'iprange: d_badunique.bin: invalid unique ips value \047qq\n\047\niprange: Cannot fast load d_badunique.bin\niprange: Cannot load ipset: d_badunique.bin\n' \
      $'d_badunique.bin'

check 'unique ips below entries' 1 \
      '' \
      'iprange: d_lowunique.bin: unique IPs (0) cannot be less than entries (1)\niprange: Cannot fast load d_lowunique.bin\niprange: Cannot load ipset: d_lowunique.bin\n' \
      $'d_lowunique.bin'

check 'truncated payload' 1 \
      '' \
      'iprange: d_truncated.bin: expected to load 1 entries, loaded 0\niprange: Cannot fast load d_truncated.bin\niprange: Cannot load ipset: d_truncated.bin\n' \
      $'d_truncated.bin'

check 'truncated payload verbose' 1 \
      '' \
      'iprange: Loading from d_truncated.bin\niprange: d_truncated.bin: expected to load 1 entries, loaded 0\niprange: Cannot fast load d_truncated.bin\niprange: Cannot load ipset: d_truncated.bin\n' \
      $'-v' $'d_truncated.bin'

check 'trailing data' 1 \
      '' \
      'iprange: d_trailing.bin: trailing data found after binary payload\niprange: Cannot fast load d_trailing.bin\niprange: Cannot load ipset: d_trailing.bin\n' \
      $'d_trailing.bin'

check 'trailing data verbose' 1 \
      '' \
      'iprange: Loading from d_trailing.bin\niprange: d_trailing.bin: trailing data found after binary payload\niprange: Cannot fast load d_trailing.bin\niprange: Cannot load ipset: d_trailing.bin\n' \
      $'-v' $'d_trailing.bin'

check 'bad endianness marker' 0 \
      '10.0.0.0/30\n' \
      '' \
      $'d_nomarker.bin'

check 'v2 in v4 mode' 1 \
      '' \
      'iprange: v2.bin: IPv6 binary file cannot be loaded in IPv4 mode (use -6)\niprange: Cannot load ipset: v2.bin\n' \
      $'v2.bin'

check 'v1 in v6 mode' 1 \
      '' \
      'iprange: v1.bin: IPv4 binary file cannot be loaded in IPv6 mode\niprange: Cannot load ipset: v1.bin\n' \
      $'-6' $'v1.bin'

check 'v6 damaged record size' 1 \
      '' \
      'iprange: d6_badrecsize.bin expected optimized flag but found \047record size 16\n\047.\niprange: Cannot load binary v2 d6_badrecsize.bin\niprange: Cannot load ipset: d6_badrecsize.bin\n' \
      $'-6' $'d6_badrecsize.bin'

check 'v6 damaged bytes' 1 \
      '' \
      'iprange: d6_badbytes.bin expected records count but found \047bytes 5\n\047.\niprange: Cannot load binary v2 d6_badbytes.bin\niprange: Cannot load ipset: d6_badbytes.bin\n' \
      $'-6' $'d6_badbytes.bin'

check 'v6 damaged bytes verbose' 1 \
      '' \
      'iprange: Loading from d6_badbytes.bin (IPv6 mode)\niprange: d6_badbytes.bin expected records count but found \047bytes 5\n\047.\niprange: Cannot load binary v2 d6_badbytes.bin\niprange: Cannot load ipset: d6_badbytes.bin\n' \
      $'-6' $'-v' $'d6_badbytes.bin'

check 'v6 damaged unique ips' 1 \
      '' \
      'iprange: d6_badunique.bin expected lines count but found \047unique ips zz\n\047.\niprange: Cannot load binary v2 d6_badunique.bin\niprange: Cannot load ipset: d6_badunique.bin\n' \
      $'-6' $'d6_badunique.bin'

check 'v6 damaged family line' 1 \
      '' \
      'iprange: d6_badfamily.bin expected family \047ipv6\047 but found \047family ipv4\n\047.\niprange: Cannot load binary v2 d6_badfamily.bin\niprange: Cannot load ipset: d6_badfamily.bin\n' \
      $'-6' $'d6_badfamily.bin'

check 'v6 unique below entries' 1 \
      '' \
      'iprange: d6_lowunique.bin expected lines count but found \047unique ips 0\n\047.\niprange: Cannot load binary v2 d6_lowunique.bin\niprange: Cannot load ipset: d6_lowunique.bin\n' \
      $'-6' $'d6_lowunique.bin'

check 'damaged flag line bytes' 1 \
      '' \
      'iprange: bad\0377flag.bin 2nd line should be the optimized flag, but found \047maybe-optimized\n\047.\niprange: Cannot fast load bad\0377flag.bin\niprange: Cannot load ipset: bad\0377flag.bin\n' \
      $'bad\377flag.bin'

check 'damaged records bytes' 1 \
      '' \
      'iprange: bad\0377recs.bin: invalid records value \047abc\n\047\niprange: Cannot fast load bad\0377recs.bin\niprange: Cannot load ipset: bad\0377recs.bin\n' \
      $'bad\377recs.bin'

check 'v6 damaged bytes name bytes' 1 \
      '' \
      'iprange: d6bad\0377bytes.bin expected records count but found \047bytes 5\n\047.\niprange: Cannot load binary v2 d6bad\0377bytes.bin\niprange: Cannot load ipset: d6bad\0377bytes.bin\n' \
      $'-6' $'d6bad\377bytes.bin'

check 'bad marker name bytes' 0 \
      '10.0.0.0/30\n' \
      '' \
      $'bad\377nomark.bin'

check 'valid v1 binary' 0 \
      '10.0.0.0/30\n' \
      '' \
      $'v1.bin'

check 'valid v1 binary verbose' 0 \
      '10.0.0.0/30\n' \
      'iprange: Loading from v1.bin\niprange: Binary loaded optimized v1.bin\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'v1.bin'

check 'valid v2 binary' 0 \
      '2001:db8::/125\n' \
      '' \
      $'-6' $'v2.bin'

check 'valid v2 binary verbose' 0 \
      '2001:db8::/125\n' \
      'iprange: Loading from v2.bin (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /125 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n' \
      $'-6' $'-v' $'v2.bin'

check 'record echo stops at NUL' 1 \
      '' \
      'iprange: Cannot understand line No 1 from nulsp.iprange: x y\niprange: Cannot load ipset: nulsp.iprange\n' \
      $'nulsp.iprange'

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "# OK: 43 cases match the C reference byte for byte"
