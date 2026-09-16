#!/bin/bash

# Interior NUL bytes in text records: line classification against the C.
#
# The released C tool reads a record with fgets(line, MAX_LINE, fp) and
# classifies it with parse_line() (src/ipset_load.c:137) or parse_line6()
# (src/ipset6_load.c:59). Both test single bytes against a terminator set that
# includes '\0' (src/ipset_load.c:150,173,195,224; src/ipset6_load.c:68,99,
# 127,144) and scan tokens with predicates that reject '\0', and the load-time
# IPv6 scan strchr(line, ":") (src/ipset_load.c:309) plus the record echo
# fprintf(..., ": %s\n", line) (src/ipset_load.c:343,355,369;
# src/ipset6_load.c:250,262,278) stop at the first NUL too. So for the C a
# record is exactly the bytes before its first NUL: the hidden tail cannot make
# an address invalid, cannot complete a range, cannot add a comment marker, and
# cannot add a second colon. Record count and line ids are unaffected, because
# fgets still splits on '\n'.
#
# Both directions are covered: a record that is valid up to its NUL must be
# accepted with the tail ignored (exit code 0 and the entry on stdout), and a
# record that is invalid up to its NUL must still fail with the extra lines the
# C prints on the way there - "Invalid netmask" (src/iprange.h:219), "Invalid
# address" (src/iprange.h:176), "Incomplete range" (src/ipset_load.c:196,
# src/ipset6_load.c:128), "Cannot parse address" (src/ipset6_load.c:175),
# "Mixed-family range" (src/ipset6_load.c:287).
#
# The same C-string rule applies one level up, to a `@file-list` entry: the
# list line is read with fgets and every later step is a C-string API, so the
# blank skip (src/iprange.c:868-871), the trailing-whitespace trim
# (src/iprange.h:102-112), the fopen (src/iprange.c:878 ->
# src/ipset_load.c:248) and the echoes at src/iprange.c:875,879 and
# src/ipset_load.c:255 all see only the bytes before the entry's first NUL. The
# two cuts are independent and compose: the entry cut decides which file opens,
# the record cut decides how each of its lines reads. Both are pinned, and the
# `@dir` expansion is pinned with NUL-bearing files inside the directory
# (opendir/readdir cannot yield a NUL in a POSIX name, so the directory branch
# needs no cut of its own).
#
# Two exceptions, both narrow and both applied to the engine and the reference
# the same way:
#
#   * The C's own wall-clock line. It is masked only when a whole line matches
#     that exact shape (the literal prefix, four decimals with five fraction
#     digits, the literal separators), so a missing, duplicated or differently
#     shaped timing line still fails.
#   * The cases whose outcome the machine resolver decides, where a
#     NUL-truncated token is a hostname. The C prints its per-reply lines from
#     resolver threads, so those cases are judged by comparing the engine to the
#     reference in the same environment: same exit code, and the same stdout and
#     masked-stderr lines as a multiset. No other case is judged that way.

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

# check_resolver_dependent <label> <args...>: the engine and the C reference,
# run in the same environment, must agree on the exit code and on the multiset
# of stdout lines and masked stderr lines. Order is not contract here, because
# the C writes its per-reply DNS lines from its resolver threads.
check_resolver_dependent() {
    local label=$1
    shift
    if [ ! -x "$REFERENCE" ]; then
        echo "# ERROR: $label needs the C reference at $REFERENCE"; fail=1; return
    fi
    timeout 20 "$IPRANGE" "$@" < /dev/null > "$tmpdir/out" 2> "$tmpdir/err"
    local rc=$?
    timeout 20 "$REFERENCE" "$@" < /dev/null > "$tmpdir/ref_out" 2> "$tmpdir/ref_err"
    local rrc=$?
    mask_wallclock "$tmpdir/err"
    mask_wallclock "$tmpdir/ref_err"
    if [ "$rc" -ne "$rrc" ]; then
        echo "# ERROR: $label exit $rc, reference $rrc"; fail=1; return
    fi
    if ! diff -q <(sort "$tmpdir/out") <(sort "$tmpdir/ref_out") >/dev/null; then
        echo "# ERROR: $label stdout lines differ from the reference"
        diff <(sort "$tmpdir/out") <(sort "$tmpdir/ref_out") | head -20
        fail=1; return
    fi
    if ! diff -q <(sort "$tmpdir/err") <(sort "$tmpdir/ref_err") >/dev/null; then
        echo "# ERROR: $label stderr lines differ from the reference"
        diff <(sort "$tmpdir/err") <(sort "$tmpdir/ref_err") | head -20
        fail=1; return
    fi
    echo "# OK: $label (exit $rc, resolver-dependent)"
}

# The diagnostics echo the record they were given, so every case runs from
# inside the fixture directory and uses relative names.
# Fixture directory: every record byte is written by printf '%b'.
cd "$tmpdir" || exit 1

mkdir -p 'n1_entry_named_dir__sub'
mkdir -p 'n1_x_dir_entry_record_nul__sub'

printf '%b' '1.2.3.4\0000junk\n' > 'a.iprange'
printf '%b' '10.0.0.0/30\n' > 'b.iprange'
printf '%b' '1.2.3.4/24\0000junk\n' > 'c_24__t.iprange'
printf '%b' '10.0.0.0/8\0000junk junk\n' > 'c_8__t.iprange'
printf '%b' '1.2.3.4/99\0000junk\n' > 'c_99__t.iprange'
printf '%b' '1.2.3.4/9\0000 9\n' > 'c_9sp__t.iprange'
printf '%b' '1.2.3.4//\0000junk\n' > 'c_double_slash__t.iprange'
printf '%b' '1.2.3.4/0x10\0000junk\n' > 'c_hex__t.iprange'
printf '%b' '1.2.3.4/2\0004\n' > 'c_in_prefix__t.iprange'
printf '%b' '1.2.3.4/\0000junk\n' > 'c_slash_only__t.iprange'
printf '%b' '999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999\0000junk\n' > 'cap_300digits__t.iprange'
printf '%b' 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\0000junk\n' > 'cap_300host__t.iprange'
printf '%b' '1.2.3.4/999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999\0000junk\n' > 'cap_300prefix__t.iprange'
printf '%b' '!\0000:::bad\n' > 'd_bang_nul_colons__t.iprange'
printf '%b' ':::\0000junk\n' > 'd_colons_nul__t.iprange'
printf '%b' '::ffff:1.2.3.4\0000junk\n' > 'd_mapped_nul__t.iprange'
printf '%b' '::ffff:1.2.3.4 junk\0000\n' > 'd_mapped_sp__t.iprange'
printf '%b' '1.2.3.4\n!\0000:::bad\n5.6.7.8\n' > 'd_mixed3__t.iprange'
printf '%b' ':\0000::x\n' > 'd_one_colon__t.iprange'
printf '%b' '10.0.0.1\0000localhost\n' > 'h_addr_nul_host__t.iprange'
printf '%b' 'localhost x\0000junk\n' > 'h_hostname_nul_bad__t.iprange'
printf '%b' 'localhost # c\0000junk\n' > 'h_localhost_comment__t.iprange'
printf '%b' 'localhost\0000\0000\n' > 'h_localhost_double__t.iprange'
printf '%b' 'localhost\0000junk\n' > 'h_localhost_nul__t.iprange'
printf '%b' 'localhost\0000\n' > 'h_localhost_nul_nl__t.iprange'
printf '%b' 'localhost\0000A\nlocalhost\0000B\n' > 'h_localhost_twice__t.iprange'
printf '%b' '1abc\0000junk\n' > 'h_num_letter__t.iprange'
printf '%b' 'a_b\0000x\n' > 'h_underscore__t.iprange'
printf '%b' '# comment\0000junk\n' > 'm_hash__t.iprange'
printf '%b' '  # x\0000y\n' > 'm_hash_ws__t.iprange'
printf '%b' '1.2.3.4\0000# junk\n' > 'm_nul_before_hash__t.iprange'
printf '%b' '\0000#junk\n' > 'm_nul_first_hash__t.iprange'
printf '%b' '; comment\0000junk\n' > 'm_semi__t.iprange'
printf '%b' '1.2.3.4 # c\0000junk\n' > 'm_trail_hash__t.iprange'
printf '%b' '1.2.3.4\t;\0000junk\n' > 'm_trail_semi_tab__t.iprange'
printf '%b' '1.2.3.4\n' > 'n1_bad_then_valid__e.iprange'
printf '%b' 'nope\0000junk\ne.iprange\n' > 'n1_bad_then_valid__list.txt'
printf '%b' '2001:db8::1\n' > 'n1_entry_exists6__e.iprange'
printf '%b' 'e.iprange\0000junk\n' > 'n1_entry_exists6__list.txt'
printf '%b' '1.2.3.4\n' > 'n1_entry_exists__e.iprange'
printf '%b' 'e.iprange\0000junk\n' > 'n1_entry_exists__list.txt'
printf '%b' '1.2.3.4\n' > 'n1_entry_exists_fifo__e.iprange'
printf '%b' 'e.iprange\0000junk\n' > 'n1_entry_exists_fifo__list.txt'
printf '%b' '1.2.3.4\n' > 'n1_entry_exists_nonl__e.iprange'
printf '%b' 'e.iprange\0000junk' > 'n1_entry_exists_nonl__list.txt'
printf '%b' '1.2.3.4\n' > 'n1_entry_exists_sp__e.iprange'
printf '%b' 'e.iprange  \0000junk\n' > 'n1_entry_exists_sp__list.txt'
printf '%b' '1.2.3.4\n' > 'n1_entry_exists_tab__e.iprange'
printf '%b' 'e.iprange\t\0000junk\n' > 'n1_entry_exists_tab__list.txt'
printf '%b' '1.2.3.4\n' > 'n1_entry_exists_v__e.iprange'
printf '%b' 'e.iprange\0000junk\n' > 'n1_entry_exists_v__list.txt'
printf '%b' '1.2.3.4\n' > 'n1_entry_exists_ws_only__e.iprange'
printf '%b' '  e.iprange  \n' > 'n1_entry_exists_ws_only__list.txt'
printf '%b' 'nope.iprange\0000junk\n' > 'n1_entry_missing6__list.txt'
printf '%b' 'nope.iprange\0000junk\n' > 'n1_entry_missing__list.txt'
printf '%b' 'nope.iprange\0000junk' > 'n1_entry_missing_nonl__list.txt'
printf '%b' 'no pe.iprange\0000junk\n' > 'n1_entry_missing_sp_name__list.txt'
printf '%b' 'nope.iprange\0000junk\n' > 'n1_entry_missing_v__list.txt'
printf '%b' '  nope.iprange \0000junk\n' > 'n1_entry_missing_ws__list.txt'
printf '%b' 'sub\0000junk\n' > 'n1_entry_named_dir__list.txt'
printf '%b' '1.2.3.4\n' > 'n1_entry_nul_at_end__e.iprange'
printf '%b' 'e.iprange\0000\n' > 'n1_entry_nul_at_end__list.txt'
printf '%b' '1.2.3.4\n' > 'n1_entry_then_wsline__e.iprange'
printf '%b' 'e.iprange\n \0000junk\n' > 'n1_entry_then_wsline__list.txt'
printf '%b' '# c\0000junk\n' > 'n1_hash_then_nul__list.txt'
printf '%b' '1.2.3.4\n' > 'n1_hash_then_valid__e.iprange'
printf '%b' '# c\0000junk\ne.iprange\n' > 'n1_hash_then_valid__list.txt'
printf '%b' 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\0000junk\n' > 'n1_long_entry__list.txt'
printf '%b' 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\0000junk\n' > 'n1_long_entry_nul_at_cut__list.txt'
printf '%b' '\0000\0000\0000junk\n' > 'n1_multi_nul__list.txt'
printf '%b' '1.2.3.4\n' > 'n1_nul_hides_entry__e.iprange'
printf '%b' '\0000e.iprange\n' > 'n1_nul_hides_entry__list.txt'
printf '%b' '1.2.3.4\n' > 'n1_nul_line_between__e.iprange'
printf '%b' '5.6.7.8\n' > 'n1_nul_line_between__f.iprange'
printf '%b' 'e.iprange\n\0000junk\nf.iprange\n' > 'n1_nul_line_between__list.txt'
printf '%b' '\0000junk\n' > 'n1_only_nul6__list.txt'
printf '%b' '\0000junk\n' > 'n1_only_nul__list.txt'
printf '%b' '\0000' > 'n1_only_nul_bare__list.txt'
printf '%b' '\0000junk' > 'n1_only_nul_nonl__list.txt'
printf '%b' '\0000junk\n' > 'n1_only_nul_v__list.txt'
printf '%b' '1.2.3.4\n' > 'n1_second_entry_empty__e.iprange'
printf '%b' 'e.iprange\n\0000\n' > 'n1_second_entry_empty__list.txt'
printf '%b' '; c\0000junk\n' > 'n1_semi_then_nul__list.txt'

# W15 composition fixtures: the entry name inside the list is the exact name of
# the fixture it opens (with a NUL and hidden junk appended), so the entry cut and
# the record cut apply in the same run and every diagnostic names the truncated file.
printf '%b' '1.2.3.4\0000junk\n5.6.7.8-9.10.11.12\0000x\n' > 'cmp_ok_ent.iprange'
printf '%b' 'cmp_ok_ent.iprange\0000junk\n' > 'cmp_ok_list.txt'
printf '%b' '1.2.3.4\0000junk\n5.6.7.8-9.10.11.12\0000x\n' > 'cmp_ok_v_ent.iprange'
printf '%b' 'cmp_ok_v_ent.iprange\0000junk\n' > 'cmp_ok_v_list.txt'
printf '%b' '1.2.3.4/99\0000x\n5.6.7.8\n' > 'cmp_bad_ent.iprange'
printf '%b' 'cmp_bad_ent.iprange\0000junk\n' > 'cmp_bad_list.txt'
printf '%b' '1.2.3.4/99\0000x\n5.6.7.8\n' > 'cmp_bad_v_ent.iprange'
printf '%b' 'cmp_bad_v_ent.iprange\0000junk\n' > 'cmp_bad_v_list.txt'
printf '%b' '1.2.3.4\0000junk\n' > 'cmp_sp_ent.iprange'
printf '%b' 'cmp_sp_ent.iprange  \0000junk\n' > 'cmp_sp_list.txt'
printf '%b' '1.2.3.4\0000A\n' > 'cmp_two_e.iprange'
printf '%b' '5.6.7.8\0000B\n' > 'cmp_two_f.iprange'
printf '%b' 'cmp_two_e.iprange\0000junk\ncmp_two_f.iprange\0000junk\n' > 'cmp_two_list.txt'
printf '%b' '1.2.3.4\0000junk\n' > 'cmp_mix_e.iprange'
printf '%b' 'cmp_mix_e.iprange\0000junk\ncmp_mix_missing.iprange\0000junk\n' > 'cmp_mix_list.txt'
mkdir -p 'cmp_dir'
printf '%b' '1.2.3.4\0000junk\n' > 'cmp_dir/a.iprange'
mkdir -p 'cmp_dir'
printf '%b' '5.6.7.8-9.10.11.12\0000x\n' > 'cmp_dir/b.iprange'
mkdir -p 'cmp_dirv'
printf '%b' '1.2.3.4\0000junk\n' > 'cmp_dirv/a.iprange'
mkdir -p 'cmp_dirv'
printf '%b' '5.6.7.8\0000x\n' > 'cmp_dirv/b.iprange'
mkdir -p 'cmp_dirv'
printf '%b' '# comment\0000junk\n' > 'cmp_dirv/c.iprange'
printf '%b' '2001:db8::1\0000junk\n' > 'cmp6_ent.iprange'
printf '%b' 'cmp6_ent.iprange\0000junk\n' > 'cmp6_list.txt'
printf '%b' '2001:db8::/99\0000x\n' > 'cmp6_bad_ent.iprange'
printf '%b' 'cmp6_bad_ent.iprange\0000junk\n' > 'cmp6_bad_list.txt'
printf '%b' '0x7f000001\0000junk\n' > 'cmp_host_ent.iprange'
printf '%b' 'cmp_host_ent.iprange\0000junk\n' > 'cmp_host_list.txt'
printf '%b' '\t \0000junk\n' > 'n1_tab_then_nul__list.txt'
printf '%b' '1.2.3.4\n' > 'n1_two_nul_entries__e.iprange'
printf '%b' '5.6.7.8\n' > 'n1_two_nul_entries__f.iprange'
printf '%b' 'e.iprange\0000X\nf.iprange\0000Y\n' > 'n1_two_nul_entries__list.txt'
printf '%b' '1.2.3.4\n' > 'n1_valid_then_bad__e.iprange'
printf '%b' 'e.iprange\nnope\0000junk\n' > 'n1_valid_then_bad__list.txt'
printf '%b' '   \0000junk\n' > 'n1_ws_then_nul__list.txt'
printf '%b' '\0000junk\n' > 'n1_x_entry_file_nul_only__ent.iprange'
printf '%b' 'ent.iprange\0000junk\n' > 'n1_x_entry_file_nul_only__list.txt'
printf '%b' '1.2.3.4 -\0000\n' > 'n1_x_record_broken_range__ent.iprange'
printf '%b' 'ent.iprange\0000junk\n' > 'n1_x_record_broken_range__list.txt'
printf '%b' 'localhost\0000junk\n' > 'n1_x_record_hostname_cut__ent.iprange'
printf '%b' 'ent.iprange\0000junk\n' > 'n1_x_record_hostname_cut__list.txt'
printf '%b' '2001:db8::/99\0000x\n' > 'n1_x_record_nul6__ent.iprange'
printf '%b' 'ent.iprange\0000junk\n' > 'n1_x_record_nul6__list.txt'
printf '%b' '1.2.3.4/99\0000x\n5.6.7.8\n' > 'n1_x_record_nul__ent.iprange'
printf '%b' 'ent.iprange\0000junk\n' > 'n1_x_record_nul__list.txt'
printf '%b' '1.2.3.4/99\0000x\n5.6.7.8\n' > 'n1_x_record_nul_v__ent.iprange'
printf '%b' 'ent.iprange\0000junk\n' > 'n1_x_record_nul_v__list.txt'
printf '%b' 'bogus\n' > 'n1_x_record_unparsable6__ent.iprange'
printf '%b' 'ent.iprange\0000junk\n' > 'n1_x_record_unparsable6__list.txt'
printf '%b' 'bogus\n' > 'n1_x_record_unparsable__ent.iprange'
printf '%b' 'ent.iprange\0000junk\n' > 'n1_x_record_unparsable__list.txt'
printf '%b' 'iprange binary format v1.0\noptimized\0000\nrecord size 8\0000\nrecords 1\0000\nbytes 12\0000\nlines 1\0000\nunique ips 4\0000\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > 'n2_v1_all_nul__t.bin'
printf '%b' 'iprange binary format v1.0\n\0000optimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > 'n2_v1_flag_lead_nul__t.bin'
printf '%b' 'iprange binary format v1.0\nnon-optimized\0000\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > 'n2_v1_flag_nonopt_nul__t.bin'
printf '%b' 'iprange binary format v1.0\noptimized\0000\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > 'n2_v1_flag_nul__t.bin'
printf '%b' 'iprange binary format v1.0\noptimized\0000junk\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > 'n2_v1_flag_nul_junk__t.bin'
printf '%b' 'iprange binary format v1.0\nopti\0000mized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > 'n2_v1_flag_nul_mid__t.bin'
printf '%b' 'iprange binary format v1.0\noptimized\0000\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > 'n2_v1_flag_nul_v__t.bin'
printf '%b' 't.bin\0000junk\n' > 'n2_v1_in_list__list.txt'
printf '%b' 'iprange binary format v1.0\noptimized\0000\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > 'n2_v1_in_list__t.bin'
printf '%b' 't.bin\0000junk\n' > 'n2_v1_in_list_ok__list.txt'
printf '%b' 'iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > 'n2_v1_in_list_ok__t.bin'
printf '%b' 'iprange binary format v1.0\noptimized\nrecord\0000size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > 'n2_v1_reclen_key_nul__t.bin'
printf '%b' 'iprange binary format v1.0\noptimized\nrecord size \00008\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > 'n2_v1_reclen_lead_nul__t.bin'
printf '%b' 'iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips \00004\nM<+\0032\0000\0000\0000\n\0003\0000\0000\n' > 'n2_v1_unique_lead_nul__t.bin'
printf '%b' 'iprange binary format v2.0\n\0000ipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\0032\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\r\0001 \0007\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\r\0001 ' > 'n2_v2_family_lead_nul__t.bin'
printf '%b' 'iprange binary format v2.0\nipv6\0000\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\0032\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\r\0001 \0007\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\r\0001 ' > 'n2_v2_family_nul__t.bin'
printf '%b' 'iprange binary format v2.0\nipv6\noptimized\0000\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\0032\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\r\0001 \0007\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\r\0001 ' > 'n2_v2_flag_nul__t.bin'
printf '%b' 'iprange binary format v2.0\nipv6\noptimized\nrecord\0000size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\0032\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\r\0001 \0007\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\r\0001 ' > 'n2_v2_key_nul__t.bin'
printf '%b' 'iprange binary format v2.0\nipv6\noptimized\nrecord size \000032\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\0032\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\r\0001 \0007\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\r\0001 ' > 'n2_v2_reclen_lead_nul__t.bin'
printf '%b' 'iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips \00008\nM<+\0032\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\r\0001 \0007\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0000\0270\r\0001 ' > 'n2_v2_unique_lead_nul__t.bin'
printf '%b' '\00002001:db8::1\n' > 'n3_leading_true_nul6__t.iprange'
printf '%b' '\00001.2.3.4\n' > 'n3_leading_true_nul__t.iprange'
printf '%b' '2001:db8::/12\00005\n' > 'n3_prefix_true_nul6__t.iprange'
printf '%b' '1.2.3.4/2\00004\n' > 'n3_prefix_true_nul__t.iprange'
printf '%b' '1.2.3.4/24\0000junk\n' > 'nul_cidr.iprange'
printf '%b' '\0000junk\n' > 'nul_comment.iprange'
printf '%b' '   \0000junk\n' > 'nul_empty.iprange'
printf '%b' '1.2.3.4\0000junk\n' > 'nul_plain.iprange'
printf '%b' '1.2.3.4-5.6.7.8\0000junk\n' > 'nul_range.iprange'
printf '%b' '1.2.3.4 junk\0000\n' > 'p_broken_nul__t.iprange'
printf '%b' '1.2.3.4\n\0000\n5.6.7.8\n' > 'p_mid_empty__t.iprange'
printf '%b' '1.2.3.4\0000\n' > 'p_nul_before_nl__t.iprange'
printf '%b' '\0001.2.3.4\n' > 'p_nul_first_addr__t.iprange'
printf '%b' '1.2.3.4\n\0000 9.9.9.9\n' > 'p_nul_hides_addr__t.iprange'
printf '%b' '\0000\0000\0000junk\n' > 'p_nul_multi__t.iprange'
printf '%b' '\0000' > 'p_nul_only_nonl__t.iprange'
printf '%b' '\0000\n' > 'p_nul_only_rec__t.iprange'
printf '%b' '1.2.3.4\0000junk\n' > 'p_plain_nul__t.iprange'
printf '%b' '1.2.3.4\0000junk' > 'p_plain_nul_nonl__t.iprange'
printf '%b' '1.2.3.4 \0000junk\n' > 'p_plain_nul_sp__t.iprange'
printf '%b' '1.2.3.4\t\0000junk\n' > 'p_plain_nul_tab__t.iprange'
printf '%b' '1.2.3.4\n5.6.7.8\0000junk\n' > 'p_second_nul__t.iprange'
printf '%b' '\t1.2.3.4\0000junk\n' > 'p_tab_leading__t.iprange'
printf '%b' '   \0000junk\n' > 'p_ws_then_nul__t.iprange'
printf '%b' '1.2.3.4-999\0000junk\n' > 'r_bad_second__t.iprange'
printf '%b' '1.2.3.4-999 junk\n' > 'r_bad_second_noNUL__t.iprange'
printf '%b' '1.2.3.4-#junk\n' > 'r_dash_comment__t.iprange'
printf '%b' '1.2.3.4-\0000junk\n' > 'r_end_dash__t.iprange'
printf '%b' '1.2.3.4-5.6.7.8 junk\0000\n' > 'r_junk_nul__t.iprange'
printf '%b' '1.2.3.4 \0000- 5.6.7.8\n' > 'r_nul_after_sp__t.iprange'
printf '%b' '1.2.3.4\0000-5.6.7.8\n' > 'r_nul_kills__t.iprange'
printf '%b' '1.2.3.4-5.6.7.8\0000 junk\n' > 'r_nul_sp__t.iprange'
printf '%b' '1.2.3.4-5.6.7.8\0000junk\n' > 'r_ok_nul__t.iprange'
printf '%b' '1.2.3.4-5.6.7.8/24\0000junk\n' > 'r_prefix_range__t.iprange'
printf '%b' '1.2.3.4-5.6.7.8\0000\n' > 'r_second_nul_nl__t.iprange'
printf '%b' '1.2.3.4 - \0000junk\n' > 'r_sp_dash_sp__t.iprange'
printf '%b' '1.2.3.4/24\n' > 'same_cidr.iprange'
printf '%b' '\n' > 'same_comment.iprange'
printf '%b' '   \n' > 'same_empty.iprange'
printf '%b' '1.2.3.4\n' > 'same_plain.iprange'
printf '%b' '1.2.3.4-5.6.7.8\n' > 'same_range.iprange'
printf '%b' '::1\0000junk\n' > 'v6_1_nul__t.iprange'
printf '%b' '2001:db8::1\0000junk\n' > 'v6_2001__t.iprange'
printf '%b' '::1\n\0000fe80::x\n' > 'v6_addr_nul_then__t.iprange'
printf '%b' '::1-\0000junk\n' > 'v6_dash__t.iprange'
printf '%b' '\0000\n' > 'v6_empty_nul__t.iprange'
printf '%b' '# c\0000junk\n' > 'v6_hash__t.iprange'
printf '%b' 'abcd\0000junk\n' > 'v6_hex_word__t.iprange'
printf '%b' 'localhost\0000junk\n' > 'v6_localhost_nul__t.iprange'
printf '%b' '::ffff:1.2.3.4\0000junk\n' > 'v6_mapped__t.iprange'
printf '%b' '::1\n\0000\n::2\n' > 'v6_mid_empty__t.iprange'
printf '%b' '::1-1.2.3.4\0000junk\n' > 'v6_mixedfam__t.iprange'
printf '%b' 'zzz\0000junk\n' > 'v6_nonhex__t.iprange'
printf '%b' 'fe80::1/99\0000junk\n' > 'v6_prefix_bad__t.iprange'
printf '%b' '::1-::5\0000junk\n' > 'v6_range_nul__t.iprange'
printf '%b' '::1\0000junk\n' > 'v6_verbose__t.iprange'
printf '%b' '   \0000junk\n' > 'v6_ws_nul__t.iprange'
printf '%b' ':::\0000junk\n' > 'v_drop__t.iprange'
printf '%b' '1.2.3.4-\0000junk\n' > 'v_incomplete__t.iprange'
printf '%b' '::ffff:1.2.3.4\0000junk\n' > 'v_mapped__t.iprange'
printf '%b' '1.2.3.4\n\0000\n5.6.7.8\n' > 'v_mid_empty__t.iprange'
printf '%b' '1.2.3.4/99\0000junk\n' > 'v_nulmask__t.iprange'
printf '%b' '1.2.3.4\0000junk\n' > 'v_plain__t.iprange'
printf '%b' '1.2.3.4-5.6.7.8\0000junk\n' > 'v_range__t.iprange'
printf '%b' '1.2.3.4\n' > 'n1_entry_named_dir__sub/inner.iprange'
printf '%b' '1.2.3.4/99\0000x\n5.6.7.8\n' > 'n1_x_dir_entry_record_nul__sub/ent.iprange'

check 'address bytes end at the NUL' 0 \
      '1.2.3.4\n' \
      '' \
      'p_plain_nul__t.iprange'

check 'a space before the NUL is still the token end' 0 \
      '1.2.3.4\n' \
      '' \
      'p_plain_nul_sp__t.iprange'

check 'a tab before the NUL is still the token end' 0 \
      '1.2.3.4\n' \
      '' \
      'p_plain_nul_tab__t.iprange'

check 'no newline after the NUL' 0 \
      '1.2.3.4\n' \
      '' \
      'p_plain_nul_nonl__t.iprange'

check 'the second record carries the NUL' 0 \
      '1.2.3.4\n5.6.7.8\n' \
      '' \
      'p_second_nul__t.iprange'

check 'leading tab then address then NUL' 0 \
      '1.2.3.4\n' \
      '' \
      'p_tab_leading__t.iprange'

check 'NUL directly before the newline' 0 \
      '1.2.3.4\n' \
      '' \
      'p_nul_before_nl__t.iprange'

check 'a record that is only a NUL is empty' 0 \
      '' \
      '' \
      'p_nul_only_rec__t.iprange'

check 'a NUL as the whole last record is empty' 0 \
      '' \
      '' \
      'p_nul_only_nonl__t.iprange'

check 'spaces then a NUL is an empty record' 0 \
      '' \
      '' \
      'p_ws_then_nul__t.iprange'

check 'several NUL bytes, all invisible' 0 \
      '' \
      '' \
      'p_nul_multi__t.iprange'

check 'a NUL hides the address behind it' 1 \
      '' \
      'iprange: Cannot understand line No 1 from p_nul_first_addr__t.iprange: \0001.2.3.4\n\niprange: Cannot load ipset: p_nul_first_addr__t.iprange\n' \
      'p_nul_first_addr__t.iprange'

check 'empty middle record keeps the other two' 0 \
      '1.2.3.4\n5.6.7.8\n' \
      '' \
      'p_mid_empty__t.iprange'

check 'a NUL hides a second address' 0 \
      '1.2.3.4\n' \
      '' \
      'p_nul_hides_addr__t.iprange'

check 'junk after the address stays invalid' 1 \
      '' \
      'iprange: Cannot understand line No 1 from p_broken_nul__t.iprange: 1.2.3.4 junk\niprange: Cannot load ipset: p_broken_nul__t.iprange\n' \
      'p_broken_nul__t.iprange'

check 'NUL does not hide an invalid netmask' 1 \
      '' \
      'iprange: Invalid netmask 99\niprange: Cannot understand line No 1 from c_99__t.iprange: 1.2.3.4/99\niprange: Cannot load ipset: c_99__t.iprange\n' \
      'c_99__t.iprange'

check 'valid prefix then NUL' 0 \
      '1.2.3.0/24\n' \
      '' \
      'c_24__t.iprange'

check 'prefix stops at the NUL' 0 \
      '1.0.0.0/9\n' \
      '' \
      'c_9sp__t.iprange'

check 'prefix then NUL then junk' 0 \
      '10.0.0.0/8\n' \
      '' \
      'c_8__t.iprange'

check 'non-numeric prefix stays invalid' 1 \
      '' \
      'iprange: Cannot understand line No 1 from c_hex__t.iprange: 1.2.3.4/0x10\niprange: Cannot load ipset: c_hex__t.iprange\n' \
      'c_hex__t.iprange'

check 'NUL truncates the prefix digits' 1 \
      '' \
      'iprange: Cannot understand line No 1 from c_in_prefix__t.iprange: 1.2.3.4/2\0004\n\niprange: Cannot load ipset: c_in_prefix__t.iprange\n' \
      'c_in_prefix__t.iprange'

check 'trailing slash reports an invalid address' 1 \
      '' \
      'iprange: Invalid address .\niprange: Cannot understand line No 1 from c_slash_only__t.iprange: 1.2.3.4/\niprange: Cannot load ipset: c_slash_only__t.iprange\n' \
      'c_slash_only__t.iprange'

check 'double slash reports an invalid address' 1 \
      '' \
      'iprange: Invalid address /.\niprange: Cannot understand line No 1 from c_double_slash__t.iprange: 1.2.3.4//\niprange: Cannot load ipset: c_double_slash__t.iprange\n' \
      'c_double_slash__t.iprange'

check 'comment record with a NUL is skipped' 0 \
      '' \
      '' \
      'm_hash__t.iprange'

check 'semicolon comment with a NUL is skipped' 0 \
      '' \
      '' \
      'm_semi__t.iprange'

check 'address then comment then NUL' 0 \
      '1.2.3.4\n' \
      '' \
      'm_trail_hash__t.iprange'

check 'address then tab-comment then NUL' 0 \
      '1.2.3.4\n' \
      '' \
      'm_trail_semi_tab__t.iprange'

check 'a NUL hides the comment marker' 0 \
      '1.2.3.4\n' \
      '' \
      'm_nul_before_hash__t.iprange'

check 'a leading NUL makes the record empty' 0 \
      '' \
      '' \
      'm_nul_first_hash__t.iprange'

check 'indented comment with a NUL' 0 \
      '' \
      '' \
      'm_hash_ws__t.iprange'

check 'range then NUL then junk' 0 \
      '1.2.3.4/30\n1.2.3.8/29\n1.2.3.16/28\n1.2.3.32/27\n1.2.3.64/26\n1.2.3.128/25\n1.2.4.0/22\n1.2.8.0/21\n1.2.16.0/20\n1.2.32.0/19\n1.2.64.0/18\n1.2.128.0/17\n1.3.0.0/16\n1.4.0.0/14\n1.8.0.0/13\n1.16.0.0/12\n1.32.0.0/11\n1.64.0.0/10\n1.128.0.0/9\n2.0.0.0/7\n4.0.0.0/8\n5.0.0.0/14\n5.4.0.0/15\n5.6.0.0/22\n5.6.4.0/23\n5.6.6.0/24\n5.6.7.0/29\n5.6.7.8\n' \
      '' \
      'r_ok_nul__t.iprange'

check 'range ending at the NUL warns and adds one IP' 0 \
      '1.2.3.4\n' \
      'iprange: Incomplete range on line 1, expected an ip address after -, but line ended\n' \
      'r_end_dash__t.iprange'

check 'spaced dash ending at the NUL warns' 0 \
      '1.2.3.4\n' \
      'iprange: Incomplete range on line 1, expected an ip address after -, but line ended\n' \
      'r_sp_dash_sp__t.iprange'

check 'a NUL hides junk after a range' 0 \
      '1.2.3.4/30\n1.2.3.8/29\n1.2.3.16/28\n1.2.3.32/27\n1.2.3.64/26\n1.2.3.128/25\n1.2.4.0/22\n1.2.8.0/21\n1.2.16.0/20\n1.2.32.0/19\n1.2.64.0/18\n1.2.128.0/17\n1.3.0.0/16\n1.4.0.0/14\n1.8.0.0/13\n1.16.0.0/12\n1.32.0.0/11\n1.64.0.0/10\n1.128.0.0/9\n2.0.0.0/7\n4.0.0.0/8\n5.0.0.0/14\n5.4.0.0/15\n5.6.0.0/22\n5.6.4.0/23\n5.6.6.0/24\n5.6.7.0/29\n5.6.7.8\n' \
      '' \
      'r_nul_sp__t.iprange'

check 'junk before the NUL is still invalid' 1 \
      '' \
      'iprange: Cannot understand line No 1 from r_junk_nul__t.iprange: 1.2.3.4-5.6.7.8 junk\niprange: Cannot load ipset: r_junk_nul__t.iprange\n' \
      'r_junk_nul__t.iprange'

check 'a NUL before the dash ends the record' 0 \
      '1.2.3.4\n' \
      '' \
      'r_nul_kills__t.iprange'

check 'comment after the dash warns (no NUL)' 0 \
      '1.2.3.4\n' \
      'iprange: Ignoring text on line 1, expected an ip address after -, but found '\''#junk\n'\''\n' \
      'r_dash_comment__t.iprange'

check 'NUL right after the range end' 0 \
      '1.2.3.4/30\n1.2.3.8/29\n1.2.3.16/28\n1.2.3.32/27\n1.2.3.64/26\n1.2.3.128/25\n1.2.4.0/22\n1.2.8.0/21\n1.2.16.0/20\n1.2.32.0/19\n1.2.64.0/18\n1.2.128.0/17\n1.3.0.0/16\n1.4.0.0/14\n1.8.0.0/13\n1.16.0.0/12\n1.32.0.0/11\n1.64.0.0/10\n1.128.0.0/9\n2.0.0.0/7\n4.0.0.0/8\n5.0.0.0/14\n5.4.0.0/15\n5.6.0.0/22\n5.6.4.0/23\n5.6.6.0/24\n5.6.7.0/29\n5.6.7.8\n' \
      '' \
      'r_second_nul_nl__t.iprange'

check 'prefix on the range end then NUL' 0 \
      '1.2.3.4/30\n1.2.3.8/29\n1.2.3.16/28\n1.2.3.32/27\n1.2.3.64/26\n1.2.3.128/25\n1.2.4.0/22\n1.2.8.0/21\n1.2.16.0/20\n1.2.32.0/19\n1.2.64.0/18\n1.2.128.0/17\n1.3.0.0/16\n1.4.0.0/14\n1.8.0.0/13\n1.16.0.0/12\n1.32.0.0/11\n1.64.0.0/10\n1.128.0.0/9\n2.0.0.0/7\n4.0.0.0/8\n5.0.0.0/14\n5.4.0.0/15\n5.6.0.0/21\n' \
      '' \
      'r_prefix_range__t.iprange'

check 'integer range end then NUL' 0 \
      '0.0.3.231\n0.0.3.232/29\n0.0.3.240/28\n0.0.4.0/22\n0.0.8.0/21\n0.0.16.0/20\n0.0.32.0/19\n0.0.64.0/18\n0.0.128.0/17\n0.1.0.0/16\n0.2.0.0/15\n0.4.0.0/14\n0.8.0.0/13\n0.16.0.0/12\n0.32.0.0/11\n0.64.0.0/10\n0.128.0.0/9\n1.0.0.0/15\n1.2.0.0/23\n1.2.2.0/24\n1.2.3.0/30\n1.2.3.4\n' \
      '' \
      'r_bad_second__t.iprange'

check 'integer range end with junk (no NUL)' 1 \
      '' \
      'iprange: Cannot understand line No 1 from r_bad_second_noNUL__t.iprange: 1.2.3.4-999 junk\n\niprange: Cannot load ipset: r_bad_second_noNUL__t.iprange\n' \
      'r_bad_second_noNUL__t.iprange'

check 'space, NUL, then a hidden range end' 0 \
      '1.2.3.4\n' \
      '' \
      'r_nul_after_sp__t.iprange'

check 'a NUL hides a hostname behind an address' 0 \
      '10.0.0.1\n' \
      '' \
      'h_addr_nul_host__t.iprange'

check 'hostname plus junk stays invalid' 1 \
      '' \
      'iprange: Cannot understand line No 1 from h_hostname_nul_bad__t.iprange: localhost x\niprange: Cannot load ipset: h_hostname_nul_bad__t.iprange\n' \
      'h_hostname_nul_bad__t.iprange'

check 'colons behind a NUL are not IPv6' 1 \
      '' \
      'iprange: Cannot understand line No 1 from d_bang_nul_colons__t.iprange: !\niprange: Cannot load ipset: d_bang_nul_colons__t.iprange\n' \
      'd_bang_nul_colons__t.iprange'

check 'colons before the NUL drop as IPv6' 0 \
      '' \
      'iprange: d_colons_nul__t.iprange: 1 IPv6 entries dropped (use -6 for IPv6 mode)\n' \
      'd_colons_nul__t.iprange'

check 'mapped IPv6 then NUL converts to IPv4' 0 \
      '1.2.3.4\n' \
      '' \
      'd_mapped_nul__t.iprange'

check 'middle record invalid, NUL hides colons' 1 \
      '' \
      'iprange: Cannot understand line No 2 from d_mixed3__t.iprange: !\niprange: Cannot load ipset: d_mixed3__t.iprange\n' \
      'd_mixed3__t.iprange'

check 'mapped IPv6 with junk before the NUL' 0 \
      '' \
      'iprange: d_mapped_sp__t.iprange: 1 IPv6 entries dropped (use -6 for IPv6 mode)\n' \
      'd_mapped_sp__t.iprange'

check 'a single colon before the NUL is not IPv6' 1 \
      '' \
      'iprange: Cannot understand line No 1 from d_one_colon__t.iprange: :\niprange: Cannot load ipset: d_one_colon__t.iprange\n' \
      'd_one_colon__t.iprange'

check '300 digits: token cap plus NUL' 1 \
      '' \
      'iprange: Cannot understand line No 1 from cap_300digits__t.iprange: 999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999\niprange: Cannot load ipset: cap_300digits__t.iprange\n' \
      'cap_300digits__t.iprange'

check '300 digits after a slash plus NUL' 1 \
      '' \
      'iprange: Cannot understand line No 1 from cap_300prefix__t.iprange: 1.2.3.4/999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999\niprange: Cannot load ipset: cap_300prefix__t.iprange\n' \
      'cap_300prefix__t.iprange'

check '300 letters: token cap then NUL' 1 \
      '' \
      'iprange: Cannot understand line No 1 from cap_300host__t.iprange: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\niprange: Cannot load ipset: cap_300host__t.iprange\n' \
      'cap_300host__t.iprange'

check 'v6 address then NUL' 0 \
      '::1\n' \
      '' \
      '-6' 'v6_1_nul__t.iprange'

check 'v6 prefix then NUL' 0 \
      'fe80::/99\n' \
      '' \
      '-6' 'v6_prefix_bad__t.iprange'

check 'v6 hex word is an address, not a hostname' 1 \
      '' \
      'iprange: Cannot parse address: abcd\niprange: Cannot understand line No 1 from v6_hex_word__t.iprange: abcd\niprange: Cannot load ipset: v6_hex_word__t.iprange\n' \
      '-6' 'v6_hex_word__t.iprange'

check 'v6 range then NUL' 0 \
      '::1\n::2/127\n::4/127\n' \
      '' \
      '-6' 'v6_range_nul__t.iprange'

check 'v6 NUL-only record is empty' 0 \
      '' \
      '' \
      '-6' 'v6_empty_nul__t.iprange'

check 'v6 global address then NUL' 0 \
      '2001:db8::1\n' \
      '' \
      '-6' 'v6_2001__t.iprange'

check 'v6 mapped address then NUL' 0 \
      '::ffff:1.2.3.4\n' \
      '' \
      '-6' 'v6_mapped__t.iprange'

check 'v6 spaces then NUL is empty' 0 \
      '' \
      '' \
      '-6' 'v6_ws_nul__t.iprange'

check 'v6 range ending at the NUL warns' 0 \
      '::1\n' \
      'iprange: Incomplete range on line, expected an address after -\n' \
      '-6' 'v6_dash__t.iprange'

check 'v6 mixed-family range then NUL' 1 \
      '' \
      'iprange: Mixed-family range on line 1: ::1 - 1.2.3.4\niprange: Cannot load ipset: v6_mixedfam__t.iprange\n' \
      '-6' 'v6_mixedfam__t.iprange'

check 'v6 comment with a NUL is skipped' 0 \
      '' \
      '' \
      '-6' 'v6_hash__t.iprange'

check 'v6 empty middle record' 0 \
      '::1\n::2\n' \
      '' \
      '-6' 'v6_mid_empty__t.iprange'

check 'v6 NUL hides the next record'\''s address' 0 \
      '::1\n' \
      '' \
      '-6' 'v6_addr_nul_then__t.iprange'

check '-v: the load bookkeeping of a NUL record' 0 \
      '1.2.3.4\n' \
      'iprange: Loading from v_plain__t.iprange\niprange: Loaded optimized v_plain__t.iprange\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n' \
      '-v' 'v_plain__t.iprange'

check '-v: invalid netmask then the unparseable echo' 1 \
      '' \
      'iprange: Loading from v_nulmask__t.iprange\niprange: Invalid netmask 99\niprange: Cannot understand line No 1 from v_nulmask__t.iprange: 1.2.3.4/99\niprange: Cannot load ipset: v_nulmask__t.iprange\n' \
      '-v' 'v_nulmask__t.iprange'

check '-v: an empty NUL record is not a line' 0 \
      '1.2.3.4\n5.6.7.8\n' \
      'iprange: Loading from v_mid_empty__t.iprange\niprange: Loaded optimized v_mid_empty__t.iprange\niprange: Printing combined ipset with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n' \
      '-v' 'v_mid_empty__t.iprange'

check '-v: the IPv6 drop warning' 0 \
      '' \
      'iprange: Loading from v_drop__t.iprange\niprange: v_drop__t.iprange: 1 IPv6 entries dropped (use -6 for IPv6 mode)\niprange: Loaded optimized v_drop__t.iprange\niprange: Printing combined ipset with 0 ranges, 0 unique IPs\n\n0 printed CIDRs, break down by prefix:\n\ntotals: 0 lines read, 0 distinct IP ranges found, 0 CIDR prefixes, 0 CIDRs printed, 0 unique IPs\n<WALLCLOCK>\n' \
      '-v' 'v_drop__t.iprange'

check '-v: mapped IPv6 converts' 0 \
      '1.2.3.4\n' \
      'iprange: Loading from v_mapped__t.iprange\niprange: Loaded optimized v_mapped__t.iprange\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n' \
      '-v' 'v_mapped__t.iprange'

check '-v: range with a NUL' 0 \
      '1.2.3.4/30\n1.2.3.8/29\n1.2.3.16/28\n1.2.3.32/27\n1.2.3.64/26\n1.2.3.128/25\n1.2.4.0/22\n1.2.8.0/21\n1.2.16.0/20\n1.2.32.0/19\n1.2.64.0/18\n1.2.128.0/17\n1.3.0.0/16\n1.4.0.0/14\n1.8.0.0/13\n1.16.0.0/12\n1.32.0.0/11\n1.64.0.0/10\n1.128.0.0/9\n2.0.0.0/7\n4.0.0.0/8\n5.0.0.0/14\n5.4.0.0/15\n5.6.0.0/22\n5.6.4.0/23\n5.6.6.0/24\n5.6.7.0/29\n5.6.7.8\n' \
      'iprange: Loading from v_range__t.iprange\niprange: Loaded optimized v_range__t.iprange\niprange: Printing combined ipset with 1 ranges, 67372037 unique IPs\n\n28 printed CIDRs, break down by prefix:\n\t- prefix /7 counts 1 entries\n\t- prefix /8 counts 1 entries\n\t- prefix /9 counts 1 entries\n\t- prefix /10 counts 1 entries\n\t- prefix /11 counts 1 entries\n\t- prefix /12 counts 1 entries\n\t- prefix /13 counts 1 entries\n\t- prefix /14 counts 2 entries\n\t- prefix /15 counts 1 entries\n\t- prefix /16 counts 1 entries\n\t- prefix /17 counts 1 entries\n\t- prefix /18 counts 1 entries\n\t- prefix /19 counts 1 entries\n\t- prefix /20 counts 1 entries\n\t- prefix /21 counts 1 entries\n\t- prefix /22 counts 2 entries\n\t- prefix /23 counts 1 entries\n\t- prefix /24 counts 1 entries\n\t- prefix /25 counts 1 entries\n\t- prefix /26 counts 1 entries\n\t- prefix /27 counts 1 entries\n\t- prefix /28 counts 1 entries\n\t- prefix /29 counts 2 entries\n\t- prefix /30 counts 1 entries\n\t- prefix /32 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 25 CIDR prefixes, 28 CIDRs printed, 67372037 unique IPs\n<WALLCLOCK>\n' \
      '-v' 'v_range__t.iprange'

check '-v: incomplete range warning' 0 \
      '1.2.3.4\n' \
      'iprange: Loading from v_incomplete__t.iprange\niprange: Incomplete range on line 1, expected an ip address after -, but line ended\niprange: Loaded optimized v_incomplete__t.iprange\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n' \
      '-v' 'v_incomplete__t.iprange'

check '-6 -v: no load bookkeeping lines' 0 \
      '::1\n' \
      'iprange: Loading from v6_verbose__t.iprange (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /128 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n' \
      '-6' '-v' 'v6_verbose__t.iprange'

check 'two files, the NUL in the first one' 0 \
      '1.2.3.4\n10.0.0.0/30\n' \
      '' \
      'a.iprange' 'b.iprange'

check 'bytes behind a NUL are invisible (address)' 0 \
      '1.2.3.4\n' \
      '' \
      'nul_plain.iprange'

check 'twin with no NUL (address)' 0 \
      '1.2.3.4\n' \
      '' \
      'same_plain.iprange'

check 'bytes behind a NUL are invisible (range)' 0 \
      '1.2.3.4/30\n1.2.3.8/29\n1.2.3.16/28\n1.2.3.32/27\n1.2.3.64/26\n1.2.3.128/25\n1.2.4.0/22\n1.2.8.0/21\n1.2.16.0/20\n1.2.32.0/19\n1.2.64.0/18\n1.2.128.0/17\n1.3.0.0/16\n1.4.0.0/14\n1.8.0.0/13\n1.16.0.0/12\n1.32.0.0/11\n1.64.0.0/10\n1.128.0.0/9\n2.0.0.0/7\n4.0.0.0/8\n5.0.0.0/14\n5.4.0.0/15\n5.6.0.0/22\n5.6.4.0/23\n5.6.6.0/24\n5.6.7.0/29\n5.6.7.8\n' \
      '' \
      'nul_range.iprange'

check 'twin with no NUL (range)' 0 \
      '1.2.3.4/30\n1.2.3.8/29\n1.2.3.16/28\n1.2.3.32/27\n1.2.3.64/26\n1.2.3.128/25\n1.2.4.0/22\n1.2.8.0/21\n1.2.16.0/20\n1.2.32.0/19\n1.2.64.0/18\n1.2.128.0/17\n1.3.0.0/16\n1.4.0.0/14\n1.8.0.0/13\n1.16.0.0/12\n1.32.0.0/11\n1.64.0.0/10\n1.128.0.0/9\n2.0.0.0/7\n4.0.0.0/8\n5.0.0.0/14\n5.4.0.0/15\n5.6.0.0/22\n5.6.4.0/23\n5.6.6.0/24\n5.6.7.0/29\n5.6.7.8\n' \
      '' \
      'same_range.iprange'

check 'bytes behind a NUL are invisible (empty record)' 0 \
      '' \
      '' \
      'nul_comment.iprange'

check 'twin with no NUL (empty record)' 0 \
      '' \
      '' \
      'same_comment.iprange'

check 'bytes behind a NUL are invisible (indented)' 0 \
      '' \
      '' \
      'nul_empty.iprange'

check 'twin with no NUL (indented)' 0 \
      '' \
      '' \
      'same_empty.iprange'

check 'bytes behind a NUL are invisible (prefix)' 0 \
      '1.2.3.0/24\n' \
      '' \
      'nul_cidr.iprange'

check 'twin with no NUL (prefix)' 0 \
      '1.2.3.0/24\n' \
      '' \
      'same_cidr.iprange'

check '@list entry cut: the truncated name is what the C opens and reports' 1 \
      '' \
      'iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists__list.txt (line 1)\n' \
      '@n1_entry_exists__list.txt'

check 'spaces before the NUL are trimmed away' 1 \
      '' \
      'iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists_sp__list.txt (line 1)\n' \
      '@n1_entry_exists_sp__list.txt'

check 'a tab before the NUL is trimmed away' 1 \
      '' \
      'iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists_tab__list.txt (line 1)\n' \
      '@n1_entry_exists_tab__list.txt'

check 'entry name ending in the NUL' 1 \
      '' \
      'iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_nul_at_end__list.txt (line 1)\n' \
      '@n1_entry_nul_at_end__list.txt'

check 'no newline after the NUL in the entry' 1 \
      '' \
      'iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists_nonl__list.txt (line 1)\n' \
      '@n1_entry_exists_nonl__list.txt'

check 'twin shape: spaces, no NUL' 1 \
      '' \
      'iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists_ws_only__list.txt (line 1)\n' \
      '@n1_entry_exists_ws_only__list.txt'

check 'truncated entry that does not exist names the cut' 1 \
      '' \
      'iprange: nope.iprange - No such file or directory\niprange: Cannot load file nope.iprange from list n1_entry_missing__list.txt (line 1)\n' \
      '@n1_entry_missing__list.txt'

check 'missing entry, no newline after the NUL' 1 \
      '' \
      'iprange: nope.iprange - No such file or directory\niprange: Cannot load file nope.iprange from list n1_entry_missing_nonl__list.txt (line 1)\n' \
      '@n1_entry_missing_nonl__list.txt'

check 'missing entry with spaces before the NUL' 1 \
      '' \
      'iprange: nope.iprange - No such file or directory\niprange: Cannot load file nope.iprange from list n1_entry_missing_ws__list.txt (line 1)\n' \
      '@n1_entry_missing_ws__list.txt'

check 'entry name with a space, cut after it' 1 \
      '' \
      'iprange: no pe.iprange - No such file or directory\niprange: Cannot load file no pe.iprange from list n1_entry_missing_sp_name__list.txt (line 1)\n' \
      '@n1_entry_missing_sp_name__list.txt'

check 'entry naming a directory loads as an empty set' 1 \
      '' \
      'iprange: sub - No such file or directory\niprange: Cannot load file sub from list n1_entry_named_dir__list.txt (line 1)\n' \
      '@n1_entry_named_dir__list.txt'

check '-v: the list expansion echoes the cut name' 1 \
      '' \
      'iprange: Loading files from list n1_entry_exists_v__list.txt\niprange: Loading file e.iprange from list (line 1)\niprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists_v__list.txt (line 1)\n' \
      '-v' '@n1_entry_exists_v__list.txt'

check '-v: missing entry echoes the cut name' 1 \
      '' \
      'iprange: Loading files from list n1_entry_missing_v__list.txt\niprange: Loading file nope.iprange from list (line 1)\niprange: nope.iprange - No such file or directory\niprange: Cannot load file nope.iprange from list n1_entry_missing_v__list.txt (line 1)\n' \
      '-v' '@n1_entry_missing_v__list.txt'

check '-v: the successful expansion of a cut name' 1 \
      '' \
      'iprange: Loading files from list n1_entry_exists_fifo__list.txt\niprange: Loading file e.iprange from list (line 1)\niprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists_fifo__list.txt (line 1)\n' \
      '-v' '@n1_entry_exists_fifo__list.txt'

check 'an entry that is only a NUL is an empty line' 1 \
      '' \
      'iprange: No valid files found in file list: n1_only_nul__list.txt\n' \
      '@n1_only_nul__list.txt'

check 'a NUL as the whole last entry is empty' 1 \
      '' \
      'iprange: No valid files found in file list: n1_only_nul_nonl__list.txt\n' \
      '@n1_only_nul_nonl__list.txt'

check 'one NUL byte as the whole file list' 1 \
      '' \
      'iprange: No valid files found in file list: n1_only_nul_bare__list.txt\n' \
      '@n1_only_nul_bare__list.txt'

check 'spaces then a NUL is an empty entry' 1 \
      '' \
      'iprange: No valid files found in file list: n1_ws_then_nul__list.txt\n' \
      '@n1_ws_then_nul__list.txt'

check 'a tab and a space then a NUL are empty' 1 \
      '' \
      'iprange: No valid files found in file list: n1_tab_then_nul__list.txt\n' \
      '@n1_tab_then_nul__list.txt'

check 'several NUL bytes, all invisible' 1 \
      '' \
      'iprange: No valid files found in file list: n1_multi_nul__list.txt\n' \
      '@n1_multi_nul__list.txt'

check '-v: an empty entry is not a load attempt' 1 \
      '' \
      'iprange: Loading files from list n1_only_nul_v__list.txt\niprange: File list n1_only_nul_v__list.txt is empty or contains no valid entries\niprange: No valid files found in file list: n1_only_nul_v__list.txt\n' \
      '-v' '@n1_only_nul_v__list.txt'

check 'a NUL hides the whole entry name behind it' 1 \
      '' \
      'iprange: No valid files found in file list: n1_nul_hides_entry__list.txt\n' \
      '@n1_nul_hides_entry__list.txt'

check 'an empty NUL entry keeps the other two' 1 \
      '' \
      'iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_nul_line_between__list.txt (line 1)\n' \
      '@n1_nul_line_between__list.txt'

check 'a comment entry with a NUL is skipped' 1 \
      '' \
      'iprange: No valid files found in file list: n1_hash_then_nul__list.txt\n' \
      '@n1_hash_then_nul__list.txt'

check 'a semicolon entry with a NUL is skipped' 1 \
      '' \
      'iprange: No valid files found in file list: n1_semi_then_nul__list.txt\n' \
      '@n1_semi_then_nul__list.txt'

check 'a comment entry does not stop the next one' 1 \
      '' \
      'iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_hash_then_valid__list.txt (line 2)\n' \
      '@n1_hash_then_valid__list.txt'

check 'two entries, both cut at their NUL' 1 \
      '' \
      'iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_two_nul_entries__list.txt (line 1)\n' \
      '@n1_two_nul_entries__list.txt'

check 'a bad second entry names the cut name' 1 \
      '' \
      'iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_valid_then_bad__list.txt (line 1)\n' \
      '@n1_valid_then_bad__list.txt'

check 'the first bad entry stops the list' 1 \
      '' \
      'iprange: nope - No such file or directory\niprange: Cannot load file nope from list n1_bad_then_valid__list.txt (line 1)\n' \
      '@n1_bad_then_valid__list.txt'

check 'an empty second entry is skipped' 1 \
      '' \
      'iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_second_entry_empty__list.txt (line 1)\n' \
      '@n1_second_entry_empty__list.txt'

check 'a space then NUL line after a good entry' 1 \
      '' \
      'iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_then_wsline__list.txt (line 1)\n' \
      '@n1_entry_then_wsline__list.txt'

check 'an entry over the line cap is still cut' 1 \
      '' \
      'iprange: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx - File name too long\niprange: Cannot load file xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx from list n1_long_entry__list.txt (line 1)\n' \
      '@n1_long_entry__list.txt'

check 'NUL at the line-cap boundary' 1 \
      '' \
      'iprange: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx - File name too long\niprange: Cannot load file xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx from list n1_long_entry_nul_at_cut__list.txt (line 1)\n' \
      '@n1_long_entry_nul_at_cut__list.txt'

check 'entry cut alone: the truncated name is absent, so no record is read' 1 \
      '' \
      'iprange: ent.iprange - No such file or directory\niprange: Cannot load file ent.iprange from list n1_x_record_nul__list.txt (line 1)\n' \
      '@n1_x_record_nul__list.txt'

check '-v: entry cut alone: the missing truncated name is echoed' 1 \
      '' \
      'iprange: Loading files from list n1_x_record_nul_v__list.txt\niprange: Loading file ent.iprange from list (line 1)\niprange: ent.iprange - No such file or directory\niprange: Cannot load file ent.iprange from list n1_x_record_nul_v__list.txt (line 1)\n' \
      '-v' '@n1_x_record_nul_v__list.txt'

check 'entry cut alone: an incomplete range behind the absent name is never parsed' 1 \
      '' \
      'iprange: ent.iprange - No such file or directory\niprange: Cannot load file ent.iprange from list n1_x_record_broken_range__list.txt (line 1)\n' \
      '@n1_x_record_broken_range__list.txt'

check 'entry cut alone: a NUL-only record behind the absent name is never read' 1 \
      '' \
      'iprange: ent.iprange - No such file or directory\niprange: Cannot load file ent.iprange from list n1_x_entry_file_nul_only__list.txt (line 1)\n' \
      '@n1_x_entry_file_nul_only__list.txt'

check '@dir: a directory name that does not exist is reported, not expanded' 1 \
      '' \
      'iprange: Cannot access sub: No such file or directory\n' \
      '@sub'

check '-6: entry truncated at the NUL opens the file' 1 \
      '' \
      'iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists6__list.txt (line 1)\n' \
      '-6' '@n1_entry_exists6__list.txt'

check '-6: missing entry names the cut name' 1 \
      '' \
      'iprange: nope.iprange - No such file or directory\niprange: Cannot load file nope.iprange from list n1_entry_missing6__list.txt (line 1)\n' \
      '-6' '@n1_entry_missing6__list.txt'

check '-6: an entry that is only a NUL is empty' 1 \
      '' \
      'iprange: No valid files found in file list: n1_only_nul6__list.txt\n' \
      '-6' '@n1_only_nul6__list.txt'

check '-6: entry cut alone: the truncated name is absent' 1 \
      '' \
      'iprange: ent.iprange - No such file or directory\niprange: Cannot load file ent.iprange from list n1_x_record_nul6__list.txt (line 1)\n' \
      '-6' '@n1_x_record_nul6__list.txt'

check 'v1 flag line cut at the NUL' 1 \
      '' \
      'iprange: n2_v1_flag_nul__t.bin 2nd line should be the optimized flag, but found '\''optimized'\''.\niprange: Cannot fast load n2_v1_flag_nul__t.bin\niprange: Cannot load ipset: n2_v1_flag_nul__t.bin\n' \
      'n2_v1_flag_nul__t.bin'

check 'v1 flag line: junk behind the NUL is invisible' 1 \
      '' \
      'iprange: n2_v1_flag_nul_junk__t.bin 2nd line should be the optimized flag, but found '\''optimized'\''.\niprange: Cannot fast load n2_v1_flag_nul_junk__t.bin\niprange: Cannot load ipset: n2_v1_flag_nul_junk__t.bin\n' \
      'n2_v1_flag_nul_junk__t.bin'

check 'v1 flag line starting with a NUL echoes empty' 1 \
      '' \
      'iprange: n2_v1_flag_lead_nul__t.bin 2nd line should be the optimized flag, but found '\'''\''.\niprange: Cannot fast load n2_v1_flag_lead_nul__t.bin\niprange: Cannot load ipset: n2_v1_flag_lead_nul__t.bin\n' \
      'n2_v1_flag_lead_nul__t.bin'

check 'v1 flag line cut in the middle of the word' 1 \
      '' \
      'iprange: n2_v1_flag_nul_mid__t.bin 2nd line should be the optimized flag, but found '\''opti'\''.\niprange: Cannot fast load n2_v1_flag_nul_mid__t.bin\niprange: Cannot load ipset: n2_v1_flag_nul_mid__t.bin\n' \
      'n2_v1_flag_nul_mid__t.bin'

check 'v1 non-optimized flag cut at the NUL' 1 \
      '' \
      'iprange: n2_v1_flag_nonopt_nul__t.bin 2nd line should be the optimized flag, but found '\''non-optimized'\''.\niprange: Cannot fast load n2_v1_flag_nonopt_nul__t.bin\niprange: Cannot load ipset: n2_v1_flag_nonopt_nul__t.bin\n' \
      'n2_v1_flag_nonopt_nul__t.bin'

check '-v: v1 flag line cut at the NUL' 1 \
      '' \
      'iprange: Loading from n2_v1_flag_nul_v__t.bin\niprange: n2_v1_flag_nul_v__t.bin 2nd line should be the optimized flag, but found '\''optimized'\''.\niprange: Cannot fast load n2_v1_flag_nul_v__t.bin\niprange: Cannot load ipset: n2_v1_flag_nul_v__t.bin\n' \
      '-v' 'n2_v1_flag_nul_v__t.bin'

check 'v1 record size value starting with a NUL' 1 \
      '' \
      'iprange: n2_v1_reclen_lead_nul__t.bin: invalid record size value '\'''\''\niprange: Cannot fast load n2_v1_reclen_lead_nul__t.bin\niprange: Cannot load ipset: n2_v1_reclen_lead_nul__t.bin\n' \
      'n2_v1_reclen_lead_nul__t.bin'

check 'v1 record size key cut at the NUL' 1 \
      '' \
      'iprange: n2_v1_reclen_key_nul__t.bin 3rd line should be the record size, but found '\''record'\''.\niprange: Cannot fast load n2_v1_reclen_key_nul__t.bin\niprange: Cannot load ipset: n2_v1_reclen_key_nul__t.bin\n' \
      'n2_v1_reclen_key_nul__t.bin'

check 'v1 unique ips value starting with a NUL' 1 \
      '' \
      'iprange: n2_v1_unique_lead_nul__t.bin: invalid unique ips value '\'''\''\niprange: Cannot fast load n2_v1_unique_lead_nul__t.bin\niprange: Cannot load ipset: n2_v1_unique_lead_nul__t.bin\n' \
      'n2_v1_unique_lead_nul__t.bin'

check 'v1: every header line carries a NUL' 1 \
      '' \
      'iprange: n2_v1_all_nul__t.bin 2nd line should be the optimized flag, but found '\''optimized'\''.\niprange: Cannot fast load n2_v1_all_nul__t.bin\niprange: Cannot load ipset: n2_v1_all_nul__t.bin\n' \
      'n2_v1_all_nul__t.bin'

check 'v2 family line cut at the NUL' 1 \
      '' \
      'iprange: n2_v2_family_nul__t.bin expected family '\''ipv6'\'' but found '\''ipv6'\''.\niprange: Cannot load binary v2 n2_v2_family_nul__t.bin\niprange: Cannot load ipset: n2_v2_family_nul__t.bin\n' \
      '-6' 'n2_v2_family_nul__t.bin'

check 'v2 family line starting with a NUL' 1 \
      '' \
      'iprange: n2_v2_family_lead_nul__t.bin expected family '\''ipv6'\'' but found '\'''\''.\niprange: Cannot load binary v2 n2_v2_family_lead_nul__t.bin\niprange: Cannot load ipset: n2_v2_family_lead_nul__t.bin\n' \
      '-6' 'n2_v2_family_lead_nul__t.bin'

check 'v2 optimized flag line cut at the NUL' 1 \
      '' \
      'iprange: n2_v2_flag_nul__t.bin expected optimized flag but found '\''optimized'\''.\niprange: Cannot load binary v2 n2_v2_flag_nul__t.bin\niprange: Cannot load ipset: n2_v2_flag_nul__t.bin\n' \
      '-6' 'n2_v2_flag_nul__t.bin'

check 'v2 record size value starting with a NUL' 1 \
      '' \
      'iprange: n2_v2_reclen_lead_nul__t.bin: invalid record size value '\'''\''\niprange: Cannot load binary v2 n2_v2_reclen_lead_nul__t.bin\niprange: Cannot load ipset: n2_v2_reclen_lead_nul__t.bin\n' \
      '-6' 'n2_v2_reclen_lead_nul__t.bin'

check 'v2 unique ips value starting with a NUL' 1 \
      '' \
      'iprange: n2_v2_unique_lead_nul__t.bin: invalid unique ips value '\'''\''\niprange: Cannot load binary v2 n2_v2_unique_lead_nul__t.bin\niprange: Cannot load ipset: n2_v2_unique_lead_nul__t.bin\n' \
      '-6' 'n2_v2_unique_lead_nul__t.bin'

check 'v2 record size key cut at the NUL' 1 \
      '' \
      'iprange: n2_v2_key_nul__t.bin expected record size but found '\''record'\''.\niprange: Cannot load binary v2 n2_v2_key_nul__t.bin\niprange: Cannot load ipset: n2_v2_key_nul__t.bin\n' \
      '-6' 'n2_v2_key_nul__t.bin'

check '@list entry with a NUL naming a bad v1 file' 1 \
      '' \
      'iprange: t.bin - No such file or directory\niprange: Cannot load file t.bin from list n2_v1_in_list__list.txt (line 1)\n' \
      '@n2_v1_in_list__list.txt'

check '@list entry with a NUL naming a valid v1 file' 1 \
      '' \
      'iprange: t.bin - No such file or directory\niprange: Cannot load file t.bin from list n2_v1_in_list_ok__list.txt (line 1)\n' \
      '@n2_v1_in_list_ok__list.txt'

check 'a record starting with the byte 0x00' 0 \
      '' \
      '' \
      'n3_leading_true_nul__t.iprange'

check 'a NUL inside the prefix digits' 0 \
      '0.0.0.0/2\n' \
      '' \
      'n3_prefix_true_nul__t.iprange'

check '-6: a record starting with the byte 0x00' 0 \
      '' \
      '' \
      '-6' 'n3_leading_true_nul6__t.iprange'

check '-6: a NUL inside the prefix digits' 0 \
      '2000::/12\n' \
      '' \
      '-6' 'n3_prefix_true_nul6__t.iprange'

check_resolver_dependent 'hostname truncated at the NUL resolves' \
      'h_localhost_nul__t.iprange'

check_resolver_dependent 'hostname then NUL then newline' \
      'h_localhost_nul_nl__t.iprange'

check_resolver_dependent 'hostname then two NUL bytes' \
      'h_localhost_double__t.iprange'

check_resolver_dependent 'hostname with a comment then a NUL' \
      'h_localhost_comment__t.iprange'

check_resolver_dependent 'two hostname records hidden behind NULs' \
      'h_localhost_twice__t.iprange'

check_resolver_dependent 'digit-then-letter token becomes a hostname' \
      'h_num_letter__t.iprange'

check_resolver_dependent 'underscore token becomes a hostname' \
      'h_underscore__t.iprange'

check_resolver_dependent 'v6 non-hex word becomes a hostname' \
      '-6' 'v6_nonhex__t.iprange'

check_resolver_dependent 'v6 hostname truncated at the NUL resolves' \
      '-6' 'v6_localhost_nul__t.iprange'

check_resolver_dependent 'entry cut alone: a hostname behind the absent name is never resolved' \
      '@n1_x_record_unparsable__list.txt'

check_resolver_dependent 'entry cut alone: a hostname behind the absent name, hidden by a NUL' \
      '@n1_x_record_hostname_cut__list.txt'

check_resolver_dependent '-6: entry cut alone: a hostname behind the absent name' \
      '-6' '@n1_x_record_unparsable6__list.txt'

# W15: the entry cut and the record cut together. The C opens the truncated
# name (src/iprange.c:878 -> src/ipset_load.c:248), so a file whose records also
# hold NUL bytes is loaded, and the load diagnostics carry the truncated name
# (src/ipset_load.c:255, :343, :355; src/iprange.h:102-112 for the trim).

check '@list entry cut: the truncated name is opened and its records are cut' 0 \
      '1.2.3.4\n5.6.7.8/29\n5.6.7.16/28\n5.6.7.32/27\n5.6.7.64/26\n5.6.7.128/25\n5.6.8.0/21\n5.6.16.0/20\n5.6.32.0/19\n5.6.64.0/18\n5.6.128.0/17\n5.7.0.0/16\n5.8.0.0/13\n5.16.0.0/12\n5.32.0.0/11\n5.64.0.0/10\n5.128.0.0/9\n6.0.0.0/7\n8.0.0.0/8\n9.0.0.0/13\n9.8.0.0/15\n9.10.0.0/21\n9.10.8.0/23\n9.10.10.0/24\n9.10.11.0/29\n9.10.11.8/30\n9.10.11.12\n' \
      '' \
      '@cmp_ok_list.txt'

check '@list entry cut under -v: every echo names the truncated file' 0 \
      '1.2.3.4\n5.6.7.8/29\n5.6.7.16/28\n5.6.7.32/27\n5.6.7.64/26\n5.6.7.128/25\n5.6.8.0/21\n5.6.16.0/20\n5.6.32.0/19\n5.6.64.0/18\n5.6.128.0/17\n5.7.0.0/16\n5.8.0.0/13\n5.16.0.0/12\n5.32.0.0/11\n5.64.0.0/10\n5.128.0.0/9\n6.0.0.0/7\n8.0.0.0/8\n9.0.0.0/13\n9.8.0.0/15\n9.10.0.0/21\n9.10.8.0/23\n9.10.10.0/24\n9.10.11.0/29\n9.10.11.8/30\n9.10.11.12\n' \
      'iprange: Loading files from list cmp_ok_v_list.txt\niprange: Loading file cmp_ok_v_ent.iprange from list (line 1)\niprange: Loading from cmp_ok_v_ent.iprange\niprange: Loaded optimized cmp_ok_v_ent.iprange\niprange: Printing combined ipset with 2 ranges, 67372038 unique IPs\n\n27 printed CIDRs, break down by prefix:\n\t- prefix /7 counts 1 entries\n\t- prefix /8 counts 1 entries\n\t- prefix /9 counts 1 entries\n\t- prefix /10 counts 1 entries\n\t- prefix /11 counts 1 entries\n\t- prefix /12 counts 1 entries\n\t- prefix /13 counts 2 entries\n\t- prefix /15 counts 1 entries\n\t- prefix /16 counts 1 entries\n\t- prefix /17 counts 1 entries\n\t- prefix /18 counts 1 entries\n\t- prefix /19 counts 1 entries\n\t- prefix /20 counts 1 entries\n\t- prefix /21 counts 2 entries\n\t- prefix /23 counts 1 entries\n\t- prefix /24 counts 1 entries\n\t- prefix /25 counts 1 entries\n\t- prefix /26 counts 1 entries\n\t- prefix /27 counts 1 entries\n\t- prefix /28 counts 1 entries\n\t- prefix /29 counts 2 entries\n\t- prefix /30 counts 1 entries\n\t- prefix /32 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 23 CIDR prefixes, 27 CIDRs printed, 67372038 unique IPs\n<WALLCLOCK>\n' \
      '-v' \
      '@cmp_ok_v_list.txt'

check '@list entry cut: an invalid record inside the truncated name still fails' 1 \
      '' \
      'iprange: Invalid netmask 99\niprange: Cannot understand line No 1 from cmp_bad_ent.iprange: 1.2.3.4/99\niprange: Cannot load file cmp_bad_ent.iprange from list cmp_bad_list.txt (line 1)\n' \
      '@cmp_bad_list.txt'

check '@list entry cut under -v with an invalid record inside' 1 \
      '' \
      'iprange: Loading files from list cmp_bad_v_list.txt\niprange: Loading file cmp_bad_v_ent.iprange from list (line 1)\niprange: Loading from cmp_bad_v_ent.iprange\niprange: Invalid netmask 99\niprange: Cannot understand line No 1 from cmp_bad_v_ent.iprange: 1.2.3.4/99\niprange: Cannot load file cmp_bad_v_ent.iprange from list cmp_bad_v_list.txt (line 1)\n' \
      '-v' \
      '@cmp_bad_v_list.txt'

check '@list entry cut: blanks before the NUL are trimmed and the name opens' 0 \
      '1.2.3.4\n' \
      '' \
      '@cmp_sp_list.txt'

check '@list entry cut: two entries, both cut, both opened' 0 \
      '1.2.3.4\n5.6.7.8\n' \
      '' \
      '@cmp_two_list.txt'

check '@list entry cut: the first name opens, the second truncated name is missing' 1 \
      '' \
      'iprange: cmp_mix_missing.iprange - No such file or directory\niprange: Cannot load file cmp_mix_missing.iprange from list cmp_mix_list.txt (line 2)\n' \
      '@cmp_mix_list.txt'

check '@dir expansion with NUL records inside the directory' 0 \
      '1.2.3.4\n5.6.7.8/29\n5.6.7.16/28\n5.6.7.32/27\n5.6.7.64/26\n5.6.7.128/25\n5.6.8.0/21\n5.6.16.0/20\n5.6.32.0/19\n5.6.64.0/18\n5.6.128.0/17\n5.7.0.0/16\n5.8.0.0/13\n5.16.0.0/12\n5.32.0.0/11\n5.64.0.0/10\n5.128.0.0/9\n6.0.0.0/7\n8.0.0.0/8\n9.0.0.0/13\n9.8.0.0/15\n9.10.0.0/21\n9.10.8.0/23\n9.10.10.0/24\n9.10.11.0/29\n9.10.11.8/30\n9.10.11.12\n' \
      '' \
      '@cmp_dir'

check '@dir expansion under -v with NUL records inside' 0 \
      '1.2.3.4\n5.6.7.8\n' \
      'iprange: Loading files from directory cmp_dirv\niprange: Loading file cmp_dirv/a.iprange from directory cmp_dirv\niprange: Loading from cmp_dirv/a.iprange\niprange: Loaded optimized cmp_dirv/a.iprange\niprange: Loading file cmp_dirv/b.iprange from directory cmp_dirv\niprange: Loading from cmp_dirv/b.iprange\niprange: Loaded optimized cmp_dirv/b.iprange\niprange: Loading file cmp_dirv/c.iprange from directory cmp_dirv\niprange: Loading from cmp_dirv/c.iprange\niprange: Loaded optimized cmp_dirv/c.iprange\niprange: Merging cmp_dirv/b.iprange to combined ipset\niprange: Merging cmp_dirv/c.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n' \
      '-v' \
      '@cmp_dirv'

check '-6: @list entry cut opens the truncated name and cuts its records' 0 \
      '2001:db8::1\n' \
      '' \
      '-6' \
      '@cmp6_list.txt'

check '-6: @list entry cut with an invalid record inside' 0 \
      '2001:db8::/99\n' \
      '' \
      '-6' \
      '@cmp6_bad_list.txt'

check_resolver_dependent '@list entry cut with a hostname record behind the NUL' \
      '-v' \
      '@cmp_host_list.txt'


if [ "$fail" -ne 0 ]; then exit 1; fi
# OK: 160 cases pinned byte for byte against the C reference, 13 resolver-dependent cases matched to it line by line
