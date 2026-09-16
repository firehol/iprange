//! Text records that hold interior NUL bytes, classified exactly like the
//! released C tool.
//!
//! `ipset_load()` and `ipset6_load()` read a record with
//! `fgets(line, MAX_LINE, fp)` and classify it with `parse_line()`
//! (`src/ipset_load.c:137`) or `parse_line6()` (`src/ipset6_load.c:59`),
//! which test single bytes against a terminator set that includes `'\0'`
//! (`src/ipset_load.c:150,173,195,224`; `src/ipset6_load.c:68,99,127,144`)
//! and scan tokens with predicates that reject `'\0'`. The load-time IPv6
//! scan `strchr(line, ":")` (`src/ipset_load.c:309`) and the record echo
//! `fprintf(..., ": %s\n", line)` (`src/ipset_load.c:343,355,369`;
//! `src/ipset6_load.c:250,262,278`) stop there too. So for the C a record is
//! exactly the bytes before its first NUL: the hidden tail cannot make an
//! address invalid, cannot complete a range, cannot add a comment marker, and
//! cannot add a second colon. Record count and line ids are unaffected,
//! because `fgets` still splits on `'\n'`.
//!
//! Two directions are pinned, because they break differently. A record that is
//! valid up to its NUL is accepted and its hidden tail ignored (rc 0, the entry
//! on stdout). A record that is invalid up to its NUL still fails, and the
//! extra lines the C prints on the way - `Invalid netmask`, `Invalid address`,
//! `Incomplete range`, `Cannot parse address`, `Mixed-family range` - appear
//! because the truncated token reached the address parser.
//!
//! The one exception to byte-exact pinning is the class whose outcome the
//! machine resolver decides: a NUL-truncated token that is a hostname. Those
//! cases compare the engine to the reference process instead, on exit code and
//! on the stdout and masked-stderr lines as multisets, because the C writes its
//! per-reply lines from resolver threads. Every other case, including the ones
//! where a NUL merely *hides* a hostname, is pinned byte for byte. The
//! wall-clock line is the shared per-line exception
//! (`parity_support::mask_wallclock`).

#![cfg(unix)]

use std::ffi::{OsStr, OsString};
use std::path::PathBuf;

mod parity_support;
use parity_support::{
    assert_verbose_parity, mask_wallclock, raw, run_bounded_in, Scratch, PROGRAM,
};
/// Every fixture the cases read, by exact name bytes and exact content.
const FIXTURES: &[(&[u8], &[u8])] = &[
    (b"a.iprange", b"1.2.3.4\x00junk\n"),
    (b"b.iprange", b"10.0.0.0/30\n"),
    (b"c_24__t.iprange", b"1.2.3.4/24\x00junk\n"),
    (b"c_8__t.iprange", b"10.0.0.0/8\x00junk junk\n"),
    (b"c_99__t.iprange", b"1.2.3.4/99\x00junk\n"),
    (b"c_9sp__t.iprange", b"1.2.3.4/9\x00 9\n"),
    (b"c_double_slash__t.iprange", b"1.2.3.4//\x00junk\n"),
    (b"c_hex__t.iprange", b"1.2.3.4/0x10\x00junk\n"),
    (b"c_in_prefix__t.iprange", b"1.2.3.4/2\x04\n"),
    (b"c_slash_only__t.iprange", b"1.2.3.4/\x00junk\n"),
    (b"cap_300digits__t.iprange", b"999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999\x00junk\n"),
    (b"cap_300host__t.iprange", b"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\x00junk\n"),
    (b"cap_300prefix__t.iprange", b"1.2.3.4/999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999\x00junk\n"),
    (b"d_bang_nul_colons__t.iprange", b"!\x00:::bad\n"),
    (b"d_colons_nul__t.iprange", b":::\x00junk\n"),
    (b"d_mapped_nul__t.iprange", b"::ffff:1.2.3.4\x00junk\n"),
    (b"d_mapped_sp__t.iprange", b"::ffff:1.2.3.4 junk\x00\n"),
    (b"d_mixed3__t.iprange", b"1.2.3.4\n!\x00:::bad\n5.6.7.8\n"),
    (b"d_one_colon__t.iprange", b":\x00::x\n"),
    (b"h_addr_nul_host__t.iprange", b"10.0.0.1\x00localhost\n"),
    (b"h_hostname_nul_bad__t.iprange", b"localhost x\x00junk\n"),
    (b"h_localhost_comment__t.iprange", b"localhost # c\x00junk\n"),
    (b"h_localhost_double__t.iprange", b"localhost\x00\x00\n"),
    (b"h_localhost_nul__t.iprange", b"localhost\x00junk\n"),
    (b"h_localhost_nul_nl__t.iprange", b"localhost\x00\n"),
    (b"h_localhost_twice__t.iprange", b"localhost\x00A\nlocalhost\x00B\n"),
    (b"h_num_letter__t.iprange", b"1abc\x00junk\n"),
    (b"h_underscore__t.iprange", b"a_b\x00x\n"),
    (b"m_hash__t.iprange", b"# comment\x00junk\n"),
    (b"m_hash_ws__t.iprange", b"  # x\x00y\n"),
    (b"m_nul_before_hash__t.iprange", b"1.2.3.4\x00# junk\n"),
    (b"m_nul_first_hash__t.iprange", b"\x00#junk\n"),
    (b"m_semi__t.iprange", b"; comment\x00junk\n"),
    (b"m_trail_hash__t.iprange", b"1.2.3.4 # c\x00junk\n"),
    (b"m_trail_semi_tab__t.iprange", b"1.2.3.4\t;\x00junk\n"),
    (b"n1_bad_then_valid__e.iprange", b"1.2.3.4\n"),
    (b"n1_bad_then_valid__list.txt", b"nope\x00junk\ne.iprange\n"),
    (b"n1_entry_exists6__e.iprange", b"2001:db8::1\n"),
    (b"n1_entry_exists6__list.txt", b"e.iprange\x00junk\n"),
    (b"n1_entry_exists__e.iprange", b"1.2.3.4\n"),
    (b"n1_entry_exists__list.txt", b"e.iprange\x00junk\n"),
    (b"n1_entry_exists_fifo__e.iprange", b"1.2.3.4\n"),
    (b"n1_entry_exists_fifo__list.txt", b"e.iprange\x00junk\n"),
    (b"n1_entry_exists_nonl__e.iprange", b"1.2.3.4\n"),
    (b"n1_entry_exists_nonl__list.txt", b"e.iprange\x00junk"),
    (b"n1_entry_exists_sp__e.iprange", b"1.2.3.4\n"),
    (b"n1_entry_exists_sp__list.txt", b"e.iprange  \x00junk\n"),
    (b"n1_entry_exists_tab__e.iprange", b"1.2.3.4\n"),
    (b"n1_entry_exists_tab__list.txt", b"e.iprange\t\x00junk\n"),
    (b"n1_entry_exists_v__e.iprange", b"1.2.3.4\n"),
    (b"n1_entry_exists_v__list.txt", b"e.iprange\x00junk\n"),
    (b"n1_entry_exists_ws_only__e.iprange", b"1.2.3.4\n"),
    (b"n1_entry_exists_ws_only__list.txt", b"  e.iprange  \n"),
    (b"n1_entry_missing6__list.txt", b"nope.iprange\x00junk\n"),
    (b"n1_entry_missing__list.txt", b"nope.iprange\x00junk\n"),
    (b"n1_entry_missing_nonl__list.txt", b"nope.iprange\x00junk"),
    (b"n1_entry_missing_sp_name__list.txt", b"no pe.iprange\x00junk\n"),
    (b"n1_entry_missing_v__list.txt", b"nope.iprange\x00junk\n"),
    (b"n1_entry_missing_ws__list.txt", b"  nope.iprange \x00junk\n"),
    (b"n1_entry_named_dir__list.txt", b"sub\x00junk\n"),
    (b"n1_entry_named_dir__sub/inner.iprange", b"1.2.3.4\n"),
    (b"n1_entry_nul_at_end__e.iprange", b"1.2.3.4\n"),
    (b"n1_entry_nul_at_end__list.txt", b"e.iprange\x00\n"),
    (b"n1_entry_then_wsline__e.iprange", b"1.2.3.4\n"),
    (b"n1_entry_then_wsline__list.txt", b"e.iprange\n \x00junk\n"),
    (b"n1_hash_then_nul__list.txt", b"# c\x00junk\n"),
    (b"n1_hash_then_valid__e.iprange", b"1.2.3.4\n"),
    (b"n1_hash_then_valid__list.txt", b"# c\x00junk\ne.iprange\n"),
    (b"n1_long_entry__list.txt", b"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\x00junk\n"),
    (b"n1_long_entry_nul_at_cut__list.txt", b"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\x00junk\n"),
    (b"n1_multi_nul__list.txt", b"\x00\x00\x00junk\n"),
    (b"n1_nul_hides_entry__e.iprange", b"1.2.3.4\n"),
    (b"n1_nul_hides_entry__list.txt", b"\x00e.iprange\n"),
    (b"n1_nul_line_between__e.iprange", b"1.2.3.4\n"),
    (b"n1_nul_line_between__f.iprange", b"5.6.7.8\n"),
    (b"n1_nul_line_between__list.txt", b"e.iprange\n\x00junk\nf.iprange\n"),
    (b"n1_only_nul6__list.txt", b"\x00junk\n"),
    (b"n1_only_nul__list.txt", b"\x00junk\n"),
    (b"n1_only_nul_bare__list.txt", b"\x00"),
    (b"n1_only_nul_nonl__list.txt", b"\x00junk"),
    (b"n1_only_nul_v__list.txt", b"\x00junk\n"),
    (b"n1_second_entry_empty__e.iprange", b"1.2.3.4\n"),
    (b"n1_second_entry_empty__list.txt", b"e.iprange\n\x00\n"),
    (b"n1_semi_then_nul__list.txt", b"; c\x00junk\n"),
    (b"n1_tab_then_nul__list.txt", b"\t \x00junk\n"),
    (b"n1_two_nul_entries__e.iprange", b"1.2.3.4\n"),
    (b"n1_two_nul_entries__f.iprange", b"5.6.7.8\n"),
    (b"n1_two_nul_entries__list.txt", b"e.iprange\x00X\nf.iprange\x00Y\n"),
    (b"n1_valid_then_bad__e.iprange", b"1.2.3.4\n"),
    (b"n1_valid_then_bad__list.txt", b"e.iprange\nnope\x00junk\n"),
    (b"n1_ws_then_nul__list.txt", b"   \x00junk\n"),
    (b"n1_x_dir_entry_record_nul__sub/ent.iprange", b"1.2.3.4/99\x00x\n5.6.7.8\n"),
    (b"n1_x_entry_file_nul_only__ent.iprange", b"\x00junk\n"),
    (b"n1_x_entry_file_nul_only__list.txt", b"ent.iprange\x00junk\n"),
    (b"n1_x_record_broken_range__ent.iprange", b"1.2.3.4 -\x00\n"),
    (b"n1_x_record_broken_range__list.txt", b"ent.iprange\x00junk\n"),
    (b"n1_x_record_hostname_cut__ent.iprange", b"localhost\x00junk\n"),
    (b"n1_x_record_hostname_cut__list.txt", b"ent.iprange\x00junk\n"),
    (b"n1_x_record_nul6__ent.iprange", b"2001:db8::/99\x00x\n"),
    (b"n1_x_record_nul6__list.txt", b"ent.iprange\x00junk\n"),
    (b"n1_x_record_nul__ent.iprange", b"1.2.3.4/99\x00x\n5.6.7.8\n"),
    (b"n1_x_record_nul__list.txt", b"ent.iprange\x00junk\n"),
    (b"n1_x_record_nul_v__ent.iprange", b"1.2.3.4/99\x00x\n5.6.7.8\n"),
    (b"n1_x_record_nul_v__list.txt", b"ent.iprange\x00junk\n"),
    (b"n1_x_record_unparsable6__ent.iprange", b"bogus\n"),
    (b"n1_x_record_unparsable6__list.txt", b"ent.iprange\x00junk\n"),
    (b"n1_x_record_unparsable__ent.iprange", b"bogus\n"),
    (b"n1_x_record_unparsable__list.txt", b"ent.iprange\x00junk\n"),
    (b"n2_v1_all_nul__t.bin", b"iprange binary format v1.0\noptimized\x00\nrecord size 8\x00\nrecords 1\x00\nbytes 12\x00\nlines 1\x00\nunique ips 4\x00\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"n2_v1_flag_lead_nul__t.bin", b"iprange binary format v1.0\n\x00optimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"n2_v1_flag_nonopt_nul__t.bin", b"iprange binary format v1.0\nnon-optimized\x00\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"n2_v1_flag_nul__t.bin", b"iprange binary format v1.0\noptimized\x00\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"n2_v1_flag_nul_junk__t.bin", b"iprange binary format v1.0\noptimized\x00junk\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"n2_v1_flag_nul_mid__t.bin", b"iprange binary format v1.0\nopti\x00mized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"n2_v1_flag_nul_v__t.bin", b"iprange binary format v1.0\noptimized\x00\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"n2_v1_in_list__list.txt", b"t.bin\x00junk\n"),
    (b"n2_v1_in_list__t.bin", b"iprange binary format v1.0\noptimized\x00\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"n2_v1_in_list_ok__list.txt", b"t.bin\x00junk\n"),
    (b"n2_v1_in_list_ok__t.bin", b"iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"n2_v1_reclen_key_nul__t.bin", b"iprange binary format v1.0\noptimized\nrecord\x00size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"n2_v1_reclen_lead_nul__t.bin", b"iprange binary format v1.0\noptimized\nrecord size \x008\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"n2_v1_unique_lead_nul__t.bin", b"iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips \x004\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"n2_v2_family_lead_nul__t.bin", b"iprange binary format v2.0\n\x00ipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 "),
    (b"n2_v2_family_nul__t.bin", b"iprange binary format v2.0\nipv6\x00\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 "),
    (b"n2_v2_flag_nul__t.bin", b"iprange binary format v2.0\nipv6\noptimized\x00\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 "),
    (b"n2_v2_key_nul__t.bin", b"iprange binary format v2.0\nipv6\noptimized\nrecord\x00size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 "),
    (b"n2_v2_reclen_lead_nul__t.bin", b"iprange binary format v2.0\nipv6\noptimized\nrecord size \x0032\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 "),
    (b"n2_v2_unique_lead_nul__t.bin", b"iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips \x008\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 "),
    (b"n3_leading_true_nul6__t.iprange", b"\x002001:db8::1\n"),
    (b"n3_leading_true_nul__t.iprange", b"\x001.2.3.4\n"),
    (b"n3_prefix_true_nul6__t.iprange", b"2001:db8::/12\x005\n"),
    (b"n3_prefix_true_nul__t.iprange", b"1.2.3.4/2\x004\n"),
    (b"nul_cidr.iprange", b"1.2.3.4/24\x00junk\n"),
    (b"nul_comment.iprange", b"\x00junk\n"),
    (b"nul_empty.iprange", b"   \x00junk\n"),
    (b"nul_plain.iprange", b"1.2.3.4\x00junk\n"),
    (b"nul_range.iprange", b"1.2.3.4-5.6.7.8\x00junk\n"),
    (b"p_broken_nul__t.iprange", b"1.2.3.4 junk\x00\n"),
    (b"p_mid_empty__t.iprange", b"1.2.3.4\n\x00\n5.6.7.8\n"),
    (b"p_nul_before_nl__t.iprange", b"1.2.3.4\x00\n"),
    (b"p_nul_first_addr__t.iprange", b"\x01.2.3.4\n"),
    (b"p_nul_hides_addr__t.iprange", b"1.2.3.4\n\x00 9.9.9.9\n"),
    (b"p_nul_multi__t.iprange", b"\x00\x00\x00junk\n"),
    (b"p_nul_only_nonl__t.iprange", b"\x00"),
    (b"p_nul_only_rec__t.iprange", b"\x00\n"),
    (b"p_plain_nul__t.iprange", b"1.2.3.4\x00junk\n"),
    (b"p_plain_nul_nonl__t.iprange", b"1.2.3.4\x00junk"),
    (b"p_plain_nul_sp__t.iprange", b"1.2.3.4 \x00junk\n"),
    (b"p_plain_nul_tab__t.iprange", b"1.2.3.4\t\x00junk\n"),
    (b"p_second_nul__t.iprange", b"1.2.3.4\n5.6.7.8\x00junk\n"),
    (b"p_tab_leading__t.iprange", b"\t1.2.3.4\x00junk\n"),
    (b"p_ws_then_nul__t.iprange", b"   \x00junk\n"),
    (b"r_bad_second__t.iprange", b"1.2.3.4-999\x00junk\n"),
    (b"r_bad_second_noNUL__t.iprange", b"1.2.3.4-999 junk\n"),
    (b"r_dash_comment__t.iprange", b"1.2.3.4-#junk\n"),
    (b"r_end_dash__t.iprange", b"1.2.3.4-\x00junk\n"),
    (b"r_junk_nul__t.iprange", b"1.2.3.4-5.6.7.8 junk\x00\n"),
    (b"r_nul_after_sp__t.iprange", b"1.2.3.4 \x00- 5.6.7.8\n"),
    (b"r_nul_kills__t.iprange", b"1.2.3.4\x00-5.6.7.8\n"),
    (b"r_nul_sp__t.iprange", b"1.2.3.4-5.6.7.8\x00 junk\n"),
    (b"r_ok_nul__t.iprange", b"1.2.3.4-5.6.7.8\x00junk\n"),
    (b"r_prefix_range__t.iprange", b"1.2.3.4-5.6.7.8/24\x00junk\n"),
    (b"r_second_nul_nl__t.iprange", b"1.2.3.4-5.6.7.8\x00\n"),
    (b"r_sp_dash_sp__t.iprange", b"1.2.3.4 - \x00junk\n"),
    (b"same_cidr.iprange", b"1.2.3.4/24\n"),
    (b"same_comment.iprange", b"\n"),
    (b"same_empty.iprange", b"   \n"),
    (b"same_plain.iprange", b"1.2.3.4\n"),
    (b"same_range.iprange", b"1.2.3.4-5.6.7.8\n"),
    (b"v6_1_nul__t.iprange", b"::1\x00junk\n"),
    (b"v6_2001__t.iprange", b"2001:db8::1\x00junk\n"),
    (b"v6_addr_nul_then__t.iprange", b"::1\n\x00fe80::x\n"),
    (b"v6_dash__t.iprange", b"::1-\x00junk\n"),
    (b"v6_empty_nul__t.iprange", b"\x00\n"),
    (b"v6_hash__t.iprange", b"# c\x00junk\n"),
    (b"v6_hex_word__t.iprange", b"abcd\x00junk\n"),
    (b"v6_localhost_nul__t.iprange", b"localhost\x00junk\n"),
    (b"v6_mapped__t.iprange", b"::ffff:1.2.3.4\x00junk\n"),
    (b"v6_mid_empty__t.iprange", b"::1\n\x00\n::2\n"),
    (b"v6_mixedfam__t.iprange", b"::1-1.2.3.4\x00junk\n"),
    (b"v6_nonhex__t.iprange", b"zzz\x00junk\n"),
    (b"v6_prefix_bad__t.iprange", b"fe80::1/99\x00junk\n"),
    (b"v6_range_nul__t.iprange", b"::1-::5\x00junk\n"),
    (b"v6_verbose__t.iprange", b"::1\x00junk\n"),
    (b"v6_ws_nul__t.iprange", b"   \x00junk\n"),
    (b"v_drop__t.iprange", b":::\x00junk\n"),
    (b"v_incomplete__t.iprange", b"1.2.3.4-\x00junk\n"),
    (b"v_mapped__t.iprange", b"::ffff:1.2.3.4\x00junk\n"),
    (b"v_mid_empty__t.iprange", b"1.2.3.4\n\x00\n5.6.7.8\n"),
    (b"v_nulmask__t.iprange", b"1.2.3.4/99\x00junk\n"),
    (b"v_plain__t.iprange", b"1.2.3.4\x00junk\n"),
    (b"v_range__t.iprange", b"1.2.3.4-5.6.7.8\x00junk\n"),
    // The W15 cases: a `@file-list` entry name is a C string, so the bytes after
    // its first NUL are invisible to the open, the trim and the echoes
    // (src/iprange.c:863-882). Each of these fixtures is named exactly as the
    // truncated entry name, so the entry cut and the record cut apply in one run.
    (b"cmp_ok_ent.iprange", b"1.2.3.4\x00junk\n5.6.7.8-9.10.11.12\x00x\n"),
    (b"cmp_ok_list.txt", b"cmp_ok_ent.iprange\x00junk\n"),
    (b"cmp_ok_v_ent.iprange", b"1.2.3.4\x00junk\n5.6.7.8-9.10.11.12\x00x\n"),
    (b"cmp_ok_v_list.txt", b"cmp_ok_v_ent.iprange\x00junk\n"),
    (b"cmp_bad_ent.iprange", b"1.2.3.4/99\x00x\n5.6.7.8\n"),
    (b"cmp_bad_list.txt", b"cmp_bad_ent.iprange\x00junk\n"),
    (b"cmp_bad_v_ent.iprange", b"1.2.3.4/99\x00x\n5.6.7.8\n"),
    (b"cmp_bad_v_list.txt", b"cmp_bad_v_ent.iprange\x00junk\n"),
    (b"cmp_sp_ent.iprange", b"1.2.3.4\x00junk\n"),
    (b"cmp_sp_list.txt", b"cmp_sp_ent.iprange  \x00junk\n"),
    (b"cmp_two_e.iprange", b"1.2.3.4\x00A\n"),
    (b"cmp_two_f.iprange", b"5.6.7.8\x00B\n"),
    (b"cmp_two_list.txt", b"cmp_two_e.iprange\x00junk\ncmp_two_f.iprange\x00junk\n"),
    (b"cmp_mix_e.iprange", b"1.2.3.4\x00junk\n"),
    (b"cmp_mix_list.txt", b"cmp_mix_e.iprange\x00junk\ncmp_mix_missing.iprange\x00junk\n"),
    (b"cmp_dir/a.iprange", b"1.2.3.4\x00junk\n"),
    (b"cmp_dir/b.iprange", b"5.6.7.8-9.10.11.12\x00x\n"),
    (b"cmp_dirv/a.iprange", b"1.2.3.4\x00junk\n"),
    (b"cmp_dirv/b.iprange", b"5.6.7.8\x00x\n"),
    (b"cmp_dirv/c.iprange", b"# comment\x00junk\n"),
    (b"cmp6_ent.iprange", b"2001:db8::1\x00junk\n"),
    (b"cmp6_list.txt", b"cmp6_ent.iprange\x00junk\n"),
    (b"cmp6_bad_ent.iprange", b"2001:db8::/99\x00x\n"),
    (b"cmp6_bad_list.txt", b"cmp6_bad_ent.iprange\x00junk\n"),
    (b"cmp_host_ent.iprange", b"0x7f000001\x00junk\n"),
    (b"cmp_host_list.txt", b"cmp_host_ent.iprange\x00junk\n"),
];

/// Directories implied by a fixture path (an `@dir` case reads one).
const FIXTURE_DIRS: &[&[u8]] = &[
    b"cmp_dir",
    b"cmp_dirv",
    b"n1_entry_named_dir__sub",
    b"n1_x_dir_entry_record_nul__sub",
];

/// One measured C contract: argv bytes and the exit code, stdout bytes and
/// stderr bytes (`<WALLCLOCK>` marks the C's own timing line).
struct Case {
    label: &'static str,
    argv: &'static [&'static [u8]],
    rc: i32,
    stdout: &'static [u8],
    stderr: &'static [u8],
}

/// A case whose result the machine resolver decides: compared to the
/// reference process as a multiset of lines per channel, never pinned.
struct LiveCase {
    label: &'static str,
    argv: &'static [&'static [u8]],
}

/// The twin pairs: the two records differ only in the bytes hidden behind a
/// NUL, so the classification rule says they must be judged identically.
const TWINS: &[(&[u8], &[u8])] = &[
    (b"nul_plain.iprange", b"same_plain.iprange"),
    (b"nul_range.iprange", b"same_range.iprange"),
    (b"nul_comment.iprange", b"same_comment.iprange"),
    (b"nul_empty.iprange", b"same_empty.iprange"),
    (b"nul_cidr.iprange", b"same_cidr.iprange"),
];

fn fixtures() -> Scratch {
    let dir = Scratch::new("nul-text-records");
    for name in FIXTURE_DIRS {
        std::fs::create_dir_all(dir.path().join(raw(name))).expect("create the fixture directory");
    }
    for (name, payload) in FIXTURES {
        dir.file(name, payload);
    }
    dir
}

/// Every case whose result the C can decide without a resolver.
const NUL_RECORDS: &[Case] = &[
    Case {
        label: "address bytes end at the NUL",
        argv: &[b"p_plain_nul__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n",
        stderr: b"",
    },
    Case {
        label: "a space before the NUL is still the token end",
        argv: &[b"p_plain_nul_sp__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n",
        stderr: b"",
    },
    Case {
        label: "a tab before the NUL is still the token end",
        argv: &[b"p_plain_nul_tab__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n",
        stderr: b"",
    },
    Case {
        label: "no newline after the NUL",
        argv: &[b"p_plain_nul_nonl__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n",
        stderr: b"",
    },
    Case {
        label: "the second record carries the NUL",
        argv: &[b"p_second_nul__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n5.6.7.8\n",
        stderr: b"",
    },
    Case {
        label: "leading tab then address then NUL",
        argv: &[b"p_tab_leading__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n",
        stderr: b"",
    },
    Case {
        label: "NUL directly before the newline",
        argv: &[b"p_nul_before_nl__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n",
        stderr: b"",
    },
    Case {
        label: "a record that is only a NUL is empty",
        argv: &[b"p_nul_only_rec__t.iprange"],
        rc: 0,
        stdout: b"",
        stderr: b"",
    },
    Case {
        label: "a NUL as the whole last record is empty",
        argv: &[b"p_nul_only_nonl__t.iprange"],
        rc: 0,
        stdout: b"",
        stderr: b"",
    },
    Case {
        label: "spaces then a NUL is an empty record",
        argv: &[b"p_ws_then_nul__t.iprange"],
        rc: 0,
        stdout: b"",
        stderr: b"",
    },
    Case {
        label: "several NUL bytes, all invisible",
        argv: &[b"p_nul_multi__t.iprange"],
        rc: 0,
        stdout: b"",
        stderr: b"",
    },
    Case {
        label: "a NUL hides the address behind it",
        argv: &[b"p_nul_first_addr__t.iprange"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Cannot understand line No 1 from p_nul_first_addr__t.iprange: \x01.2.3.4\n\niprange: Cannot load ipset: p_nul_first_addr__t.iprange\n",
    },
    Case {
        label: "empty middle record keeps the other two",
        argv: &[b"p_mid_empty__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n5.6.7.8\n",
        stderr: b"",
    },
    Case {
        label: "a NUL hides a second address",
        argv: &[b"p_nul_hides_addr__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n",
        stderr: b"",
    },
    Case {
        label: "junk after the address stays invalid",
        argv: &[b"p_broken_nul__t.iprange"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Cannot understand line No 1 from p_broken_nul__t.iprange: 1.2.3.4 junk\niprange: Cannot load ipset: p_broken_nul__t.iprange\n",
    },
    Case {
        label: "NUL does not hide an invalid netmask",
        argv: &[b"c_99__t.iprange"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Invalid netmask 99\niprange: Cannot understand line No 1 from c_99__t.iprange: 1.2.3.4/99\niprange: Cannot load ipset: c_99__t.iprange\n",
    },
    Case {
        label: "valid prefix then NUL",
        argv: &[b"c_24__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.0/24\n",
        stderr: b"",
    },
    Case {
        label: "prefix stops at the NUL",
        argv: &[b"c_9sp__t.iprange"],
        rc: 0,
        stdout: b"1.0.0.0/9\n",
        stderr: b"",
    },
    Case {
        label: "prefix then NUL then junk",
        argv: &[b"c_8__t.iprange"],
        rc: 0,
        stdout: b"10.0.0.0/8\n",
        stderr: b"",
    },
    Case {
        label: "non-numeric prefix stays invalid",
        argv: &[b"c_hex__t.iprange"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Cannot understand line No 1 from c_hex__t.iprange: 1.2.3.4/0x10\niprange: Cannot load ipset: c_hex__t.iprange\n",
    },
    Case {
        label: "NUL truncates the prefix digits",
        argv: &[b"c_in_prefix__t.iprange"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Cannot understand line No 1 from c_in_prefix__t.iprange: 1.2.3.4/2\x04\n\niprange: Cannot load ipset: c_in_prefix__t.iprange\n",
    },
    Case {
        label: "trailing slash reports an invalid address",
        argv: &[b"c_slash_only__t.iprange"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Invalid address .\niprange: Cannot understand line No 1 from c_slash_only__t.iprange: 1.2.3.4/\niprange: Cannot load ipset: c_slash_only__t.iprange\n",
    },
    Case {
        label: "double slash reports an invalid address",
        argv: &[b"c_double_slash__t.iprange"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Invalid address /.\niprange: Cannot understand line No 1 from c_double_slash__t.iprange: 1.2.3.4//\niprange: Cannot load ipset: c_double_slash__t.iprange\n",
    },
    Case {
        label: "comment record with a NUL is skipped",
        argv: &[b"m_hash__t.iprange"],
        rc: 0,
        stdout: b"",
        stderr: b"",
    },
    Case {
        label: "semicolon comment with a NUL is skipped",
        argv: &[b"m_semi__t.iprange"],
        rc: 0,
        stdout: b"",
        stderr: b"",
    },
    Case {
        label: "address then comment then NUL",
        argv: &[b"m_trail_hash__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n",
        stderr: b"",
    },
    Case {
        label: "address then tab-comment then NUL",
        argv: &[b"m_trail_semi_tab__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n",
        stderr: b"",
    },
    Case {
        label: "a NUL hides the comment marker",
        argv: &[b"m_nul_before_hash__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n",
        stderr: b"",
    },
    Case {
        label: "a leading NUL makes the record empty",
        argv: &[b"m_nul_first_hash__t.iprange"],
        rc: 0,
        stdout: b"",
        stderr: b"",
    },
    Case {
        label: "indented comment with a NUL",
        argv: &[b"m_hash_ws__t.iprange"],
        rc: 0,
        stdout: b"",
        stderr: b"",
    },
    Case {
        label: "range then NUL then junk",
        argv: &[b"r_ok_nul__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4/30\n1.2.3.8/29\n1.2.3.16/28\n1.2.3.32/27\n1.2.3.64/26\n1.2.3.128/25\n1.2.4.0/22\n1.2.8.0/21\n1.2.16.0/20\n1.2.32.0/19\n1.2.64.0/18\n1.2.128.0/17\n1.3.0.0/16\n1.4.0.0/14\n1.8.0.0/13\n1.16.0.0/12\n1.32.0.0/11\n1.64.0.0/10\n1.128.0.0/9\n2.0.0.0/7\n4.0.0.0/8\n5.0.0.0/14\n5.4.0.0/15\n5.6.0.0/22\n5.6.4.0/23\n5.6.6.0/24\n5.6.7.0/29\n5.6.7.8\n",
        stderr: b"",
    },
    Case {
        label: "range ending at the NUL warns and adds one IP",
        argv: &[b"r_end_dash__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n",
        stderr: b"iprange: Incomplete range on line 1, expected an ip address after -, but line ended\n",
    },
    Case {
        label: "spaced dash ending at the NUL warns",
        argv: &[b"r_sp_dash_sp__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n",
        stderr: b"iprange: Incomplete range on line 1, expected an ip address after -, but line ended\n",
    },
    Case {
        label: "a NUL hides junk after a range",
        argv: &[b"r_nul_sp__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4/30\n1.2.3.8/29\n1.2.3.16/28\n1.2.3.32/27\n1.2.3.64/26\n1.2.3.128/25\n1.2.4.0/22\n1.2.8.0/21\n1.2.16.0/20\n1.2.32.0/19\n1.2.64.0/18\n1.2.128.0/17\n1.3.0.0/16\n1.4.0.0/14\n1.8.0.0/13\n1.16.0.0/12\n1.32.0.0/11\n1.64.0.0/10\n1.128.0.0/9\n2.0.0.0/7\n4.0.0.0/8\n5.0.0.0/14\n5.4.0.0/15\n5.6.0.0/22\n5.6.4.0/23\n5.6.6.0/24\n5.6.7.0/29\n5.6.7.8\n",
        stderr: b"",
    },
    Case {
        label: "junk before the NUL is still invalid",
        argv: &[b"r_junk_nul__t.iprange"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Cannot understand line No 1 from r_junk_nul__t.iprange: 1.2.3.4-5.6.7.8 junk\niprange: Cannot load ipset: r_junk_nul__t.iprange\n",
    },
    Case {
        label: "a NUL before the dash ends the record",
        argv: &[b"r_nul_kills__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n",
        stderr: b"",
    },
    Case {
        label: "comment after the dash warns (no NUL)",
        argv: &[b"r_dash_comment__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n",
        stderr: b"iprange: Ignoring text on line 1, expected an ip address after -, but found '#junk\n'\n",
    },
    Case {
        label: "NUL right after the range end",
        argv: &[b"r_second_nul_nl__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4/30\n1.2.3.8/29\n1.2.3.16/28\n1.2.3.32/27\n1.2.3.64/26\n1.2.3.128/25\n1.2.4.0/22\n1.2.8.0/21\n1.2.16.0/20\n1.2.32.0/19\n1.2.64.0/18\n1.2.128.0/17\n1.3.0.0/16\n1.4.0.0/14\n1.8.0.0/13\n1.16.0.0/12\n1.32.0.0/11\n1.64.0.0/10\n1.128.0.0/9\n2.0.0.0/7\n4.0.0.0/8\n5.0.0.0/14\n5.4.0.0/15\n5.6.0.0/22\n5.6.4.0/23\n5.6.6.0/24\n5.6.7.0/29\n5.6.7.8\n",
        stderr: b"",
    },
    Case {
        label: "prefix on the range end then NUL",
        argv: &[b"r_prefix_range__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4/30\n1.2.3.8/29\n1.2.3.16/28\n1.2.3.32/27\n1.2.3.64/26\n1.2.3.128/25\n1.2.4.0/22\n1.2.8.0/21\n1.2.16.0/20\n1.2.32.0/19\n1.2.64.0/18\n1.2.128.0/17\n1.3.0.0/16\n1.4.0.0/14\n1.8.0.0/13\n1.16.0.0/12\n1.32.0.0/11\n1.64.0.0/10\n1.128.0.0/9\n2.0.0.0/7\n4.0.0.0/8\n5.0.0.0/14\n5.4.0.0/15\n5.6.0.0/21\n",
        stderr: b"",
    },
    Case {
        label: "integer range end then NUL",
        argv: &[b"r_bad_second__t.iprange"],
        rc: 0,
        stdout: b"0.0.3.231\n0.0.3.232/29\n0.0.3.240/28\n0.0.4.0/22\n0.0.8.0/21\n0.0.16.0/20\n0.0.32.0/19\n0.0.64.0/18\n0.0.128.0/17\n0.1.0.0/16\n0.2.0.0/15\n0.4.0.0/14\n0.8.0.0/13\n0.16.0.0/12\n0.32.0.0/11\n0.64.0.0/10\n0.128.0.0/9\n1.0.0.0/15\n1.2.0.0/23\n1.2.2.0/24\n1.2.3.0/30\n1.2.3.4\n",
        stderr: b"",
    },
    Case {
        label: "integer range end with junk (no NUL)",
        argv: &[b"r_bad_second_noNUL__t.iprange"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Cannot understand line No 1 from r_bad_second_noNUL__t.iprange: 1.2.3.4-999 junk\n\niprange: Cannot load ipset: r_bad_second_noNUL__t.iprange\n",
    },
    Case {
        label: "space, NUL, then a hidden range end",
        argv: &[b"r_nul_after_sp__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n",
        stderr: b"",
    },
    Case {
        label: "a NUL hides a hostname behind an address",
        argv: &[b"h_addr_nul_host__t.iprange"],
        rc: 0,
        stdout: b"10.0.0.1\n",
        stderr: b"",
    },
    Case {
        label: "hostname plus junk stays invalid",
        argv: &[b"h_hostname_nul_bad__t.iprange"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Cannot understand line No 1 from h_hostname_nul_bad__t.iprange: localhost x\niprange: Cannot load ipset: h_hostname_nul_bad__t.iprange\n",
    },
    Case {
        label: "colons behind a NUL are not IPv6",
        argv: &[b"d_bang_nul_colons__t.iprange"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Cannot understand line No 1 from d_bang_nul_colons__t.iprange: !\niprange: Cannot load ipset: d_bang_nul_colons__t.iprange\n",
    },
    Case {
        label: "colons before the NUL drop as IPv6",
        argv: &[b"d_colons_nul__t.iprange"],
        rc: 0,
        stdout: b"",
        stderr: b"iprange: d_colons_nul__t.iprange: 1 IPv6 entries dropped (use -6 for IPv6 mode)\n",
    },
    Case {
        label: "mapped IPv6 then NUL converts to IPv4",
        argv: &[b"d_mapped_nul__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n",
        stderr: b"",
    },
    Case {
        label: "middle record invalid, NUL hides colons",
        argv: &[b"d_mixed3__t.iprange"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Cannot understand line No 2 from d_mixed3__t.iprange: !\niprange: Cannot load ipset: d_mixed3__t.iprange\n",
    },
    Case {
        label: "mapped IPv6 with junk before the NUL",
        argv: &[b"d_mapped_sp__t.iprange"],
        rc: 0,
        stdout: b"",
        stderr: b"iprange: d_mapped_sp__t.iprange: 1 IPv6 entries dropped (use -6 for IPv6 mode)\n",
    },
    Case {
        label: "a single colon before the NUL is not IPv6",
        argv: &[b"d_one_colon__t.iprange"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Cannot understand line No 1 from d_one_colon__t.iprange: :\niprange: Cannot load ipset: d_one_colon__t.iprange\n",
    },
    Case {
        label: "300 digits: token cap plus NUL",
        argv: &[b"cap_300digits__t.iprange"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Cannot understand line No 1 from cap_300digits__t.iprange: 999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999\niprange: Cannot load ipset: cap_300digits__t.iprange\n",
    },
    Case {
        label: "300 digits after a slash plus NUL",
        argv: &[b"cap_300prefix__t.iprange"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Cannot understand line No 1 from cap_300prefix__t.iprange: 1.2.3.4/999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999\niprange: Cannot load ipset: cap_300prefix__t.iprange\n",
    },
    Case {
        label: "300 letters: token cap then NUL",
        argv: &[b"cap_300host__t.iprange"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Cannot understand line No 1 from cap_300host__t.iprange: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\niprange: Cannot load ipset: cap_300host__t.iprange\n",
    },
    Case {
        label: "v6 address then NUL",
        argv: &[b"-6", b"v6_1_nul__t.iprange"],
        rc: 0,
        stdout: b"::1\n",
        stderr: b"",
    },
    Case {
        label: "v6 prefix then NUL",
        argv: &[b"-6", b"v6_prefix_bad__t.iprange"],
        rc: 0,
        stdout: b"fe80::/99\n",
        stderr: b"",
    },
    Case {
        label: "v6 hex word is an address, not a hostname",
        argv: &[b"-6", b"v6_hex_word__t.iprange"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Cannot parse address: abcd\niprange: Cannot understand line No 1 from v6_hex_word__t.iprange: abcd\niprange: Cannot load ipset: v6_hex_word__t.iprange\n",
    },
    Case {
        label: "v6 range then NUL",
        argv: &[b"-6", b"v6_range_nul__t.iprange"],
        rc: 0,
        stdout: b"::1\n::2/127\n::4/127\n",
        stderr: b"",
    },
    Case {
        label: "v6 NUL-only record is empty",
        argv: &[b"-6", b"v6_empty_nul__t.iprange"],
        rc: 0,
        stdout: b"",
        stderr: b"",
    },
    Case {
        label: "v6 global address then NUL",
        argv: &[b"-6", b"v6_2001__t.iprange"],
        rc: 0,
        stdout: b"2001:db8::1\n",
        stderr: b"",
    },
    Case {
        label: "v6 mapped address then NUL",
        argv: &[b"-6", b"v6_mapped__t.iprange"],
        rc: 0,
        stdout: b"::ffff:1.2.3.4\n",
        stderr: b"",
    },
    Case {
        label: "v6 spaces then NUL is empty",
        argv: &[b"-6", b"v6_ws_nul__t.iprange"],
        rc: 0,
        stdout: b"",
        stderr: b"",
    },
    Case {
        label: "v6 range ending at the NUL warns",
        argv: &[b"-6", b"v6_dash__t.iprange"],
        rc: 0,
        stdout: b"::1\n",
        stderr: b"iprange: Incomplete range on line, expected an address after -\n",
    },
    Case {
        label: "v6 mixed-family range then NUL",
        argv: &[b"-6", b"v6_mixedfam__t.iprange"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Mixed-family range on line 1: ::1 - 1.2.3.4\niprange: Cannot load ipset: v6_mixedfam__t.iprange\n",
    },
    Case {
        label: "v6 comment with a NUL is skipped",
        argv: &[b"-6", b"v6_hash__t.iprange"],
        rc: 0,
        stdout: b"",
        stderr: b"",
    },
    Case {
        label: "v6 empty middle record",
        argv: &[b"-6", b"v6_mid_empty__t.iprange"],
        rc: 0,
        stdout: b"::1\n::2\n",
        stderr: b"",
    },
    Case {
        label: "v6 NUL hides the next record's address",
        argv: &[b"-6", b"v6_addr_nul_then__t.iprange"],
        rc: 0,
        stdout: b"::1\n",
        stderr: b"",
    },
    Case {
        label: "-v: the load bookkeeping of a NUL record",
        argv: &[b"-v", b"v_plain__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n",
        stderr: b"iprange: Loading from v_plain__t.iprange\niprange: Loaded optimized v_plain__t.iprange\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n",
    },
    Case {
        label: "-v: invalid netmask then the unparseable echo",
        argv: &[b"-v", b"v_nulmask__t.iprange"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Loading from v_nulmask__t.iprange\niprange: Invalid netmask 99\niprange: Cannot understand line No 1 from v_nulmask__t.iprange: 1.2.3.4/99\niprange: Cannot load ipset: v_nulmask__t.iprange\n",
    },
    Case {
        label: "-v: an empty NUL record is not a line",
        argv: &[b"-v", b"v_mid_empty__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n5.6.7.8\n",
        stderr: b"iprange: Loading from v_mid_empty__t.iprange\niprange: Loaded optimized v_mid_empty__t.iprange\niprange: Printing combined ipset with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n",
    },
    Case {
        label: "-v: the IPv6 drop warning",
        argv: &[b"-v", b"v_drop__t.iprange"],
        rc: 0,
        stdout: b"",
        stderr: b"iprange: Loading from v_drop__t.iprange\niprange: v_drop__t.iprange: 1 IPv6 entries dropped (use -6 for IPv6 mode)\niprange: Loaded optimized v_drop__t.iprange\niprange: Printing combined ipset with 0 ranges, 0 unique IPs\n\n0 printed CIDRs, break down by prefix:\n\ntotals: 0 lines read, 0 distinct IP ranges found, 0 CIDR prefixes, 0 CIDRs printed, 0 unique IPs\n<WALLCLOCK>\n",
    },
    Case {
        label: "-v: mapped IPv6 converts",
        argv: &[b"-v", b"v_mapped__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n",
        stderr: b"iprange: Loading from v_mapped__t.iprange\niprange: Loaded optimized v_mapped__t.iprange\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n",
    },
    Case {
        label: "-v: range with a NUL",
        argv: &[b"-v", b"v_range__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4/30\n1.2.3.8/29\n1.2.3.16/28\n1.2.3.32/27\n1.2.3.64/26\n1.2.3.128/25\n1.2.4.0/22\n1.2.8.0/21\n1.2.16.0/20\n1.2.32.0/19\n1.2.64.0/18\n1.2.128.0/17\n1.3.0.0/16\n1.4.0.0/14\n1.8.0.0/13\n1.16.0.0/12\n1.32.0.0/11\n1.64.0.0/10\n1.128.0.0/9\n2.0.0.0/7\n4.0.0.0/8\n5.0.0.0/14\n5.4.0.0/15\n5.6.0.0/22\n5.6.4.0/23\n5.6.6.0/24\n5.6.7.0/29\n5.6.7.8\n",
        stderr: b"iprange: Loading from v_range__t.iprange\niprange: Loaded optimized v_range__t.iprange\niprange: Printing combined ipset with 1 ranges, 67372037 unique IPs\n\n28 printed CIDRs, break down by prefix:\n\t- prefix /7 counts 1 entries\n\t- prefix /8 counts 1 entries\n\t- prefix /9 counts 1 entries\n\t- prefix /10 counts 1 entries\n\t- prefix /11 counts 1 entries\n\t- prefix /12 counts 1 entries\n\t- prefix /13 counts 1 entries\n\t- prefix /14 counts 2 entries\n\t- prefix /15 counts 1 entries\n\t- prefix /16 counts 1 entries\n\t- prefix /17 counts 1 entries\n\t- prefix /18 counts 1 entries\n\t- prefix /19 counts 1 entries\n\t- prefix /20 counts 1 entries\n\t- prefix /21 counts 1 entries\n\t- prefix /22 counts 2 entries\n\t- prefix /23 counts 1 entries\n\t- prefix /24 counts 1 entries\n\t- prefix /25 counts 1 entries\n\t- prefix /26 counts 1 entries\n\t- prefix /27 counts 1 entries\n\t- prefix /28 counts 1 entries\n\t- prefix /29 counts 2 entries\n\t- prefix /30 counts 1 entries\n\t- prefix /32 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 25 CIDR prefixes, 28 CIDRs printed, 67372037 unique IPs\n<WALLCLOCK>\n",
    },
    Case {
        label: "-v: incomplete range warning",
        argv: &[b"-v", b"v_incomplete__t.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n",
        stderr: b"iprange: Loading from v_incomplete__t.iprange\niprange: Incomplete range on line 1, expected an ip address after -, but line ended\niprange: Loaded optimized v_incomplete__t.iprange\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n",
    },
    Case {
        label: "-6 -v: no load bookkeeping lines",
        argv: &[b"-6", b"-v", b"v6_verbose__t.iprange"],
        rc: 0,
        stdout: b"::1\n",
        stderr: b"iprange: Loading from v6_verbose__t.iprange (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /128 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n",
    },
    Case {
        label: "two files, the NUL in the first one",
        argv: &[b"a.iprange", b"b.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n10.0.0.0/30\n",
        stderr: b"",
    },
    Case {
        label: "bytes behind a NUL are invisible (address)",
        argv: &[b"nul_plain.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n",
        stderr: b"",
    },
    Case {
        label: "twin with no NUL (address)",
        argv: &[b"same_plain.iprange"],
        rc: 0,
        stdout: b"1.2.3.4\n",
        stderr: b"",
    },
    Case {
        label: "bytes behind a NUL are invisible (range)",
        argv: &[b"nul_range.iprange"],
        rc: 0,
        stdout: b"1.2.3.4/30\n1.2.3.8/29\n1.2.3.16/28\n1.2.3.32/27\n1.2.3.64/26\n1.2.3.128/25\n1.2.4.0/22\n1.2.8.0/21\n1.2.16.0/20\n1.2.32.0/19\n1.2.64.0/18\n1.2.128.0/17\n1.3.0.0/16\n1.4.0.0/14\n1.8.0.0/13\n1.16.0.0/12\n1.32.0.0/11\n1.64.0.0/10\n1.128.0.0/9\n2.0.0.0/7\n4.0.0.0/8\n5.0.0.0/14\n5.4.0.0/15\n5.6.0.0/22\n5.6.4.0/23\n5.6.6.0/24\n5.6.7.0/29\n5.6.7.8\n",
        stderr: b"",
    },
    Case {
        label: "twin with no NUL (range)",
        argv: &[b"same_range.iprange"],
        rc: 0,
        stdout: b"1.2.3.4/30\n1.2.3.8/29\n1.2.3.16/28\n1.2.3.32/27\n1.2.3.64/26\n1.2.3.128/25\n1.2.4.0/22\n1.2.8.0/21\n1.2.16.0/20\n1.2.32.0/19\n1.2.64.0/18\n1.2.128.0/17\n1.3.0.0/16\n1.4.0.0/14\n1.8.0.0/13\n1.16.0.0/12\n1.32.0.0/11\n1.64.0.0/10\n1.128.0.0/9\n2.0.0.0/7\n4.0.0.0/8\n5.0.0.0/14\n5.4.0.0/15\n5.6.0.0/22\n5.6.4.0/23\n5.6.6.0/24\n5.6.7.0/29\n5.6.7.8\n",
        stderr: b"",
    },
    Case {
        label: "bytes behind a NUL are invisible (empty record)",
        argv: &[b"nul_comment.iprange"],
        rc: 0,
        stdout: b"",
        stderr: b"",
    },
    Case {
        label: "twin with no NUL (empty record)",
        argv: &[b"same_comment.iprange"],
        rc: 0,
        stdout: b"",
        stderr: b"",
    },
    Case {
        label: "bytes behind a NUL are invisible (indented)",
        argv: &[b"nul_empty.iprange"],
        rc: 0,
        stdout: b"",
        stderr: b"",
    },
    Case {
        label: "twin with no NUL (indented)",
        argv: &[b"same_empty.iprange"],
        rc: 0,
        stdout: b"",
        stderr: b"",
    },
    Case {
        label: "bytes behind a NUL are invisible (prefix)",
        argv: &[b"nul_cidr.iprange"],
        rc: 0,
        stdout: b"1.2.3.0/24\n",
        stderr: b"",
    },
    Case {
        label: "twin with no NUL (prefix)",
        argv: &[b"same_cidr.iprange"],
        rc: 0,
        stdout: b"1.2.3.0/24\n",
        stderr: b"",
    },
    Case {
        label: "@list entry truncated at the NUL opens the file",
        argv: &[b"@n1_entry_exists__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists__list.txt (line 1)\n",
    },
    Case {
        label: "spaces before the NUL are trimmed away",
        argv: &[b"@n1_entry_exists_sp__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists_sp__list.txt (line 1)\n",
    },
    Case {
        label: "a tab before the NUL is trimmed away",
        argv: &[b"@n1_entry_exists_tab__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists_tab__list.txt (line 1)\n",
    },
    Case {
        label: "entry name ending in the NUL",
        argv: &[b"@n1_entry_nul_at_end__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_nul_at_end__list.txt (line 1)\n",
    },
    Case {
        label: "no newline after the NUL in the entry",
        argv: &[b"@n1_entry_exists_nonl__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists_nonl__list.txt (line 1)\n",
    },
    Case {
        label: "twin shape: spaces, no NUL",
        argv: &[b"@n1_entry_exists_ws_only__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists_ws_only__list.txt (line 1)\n",
    },
    Case {
        label: "truncated entry that does not exist names the cut",
        argv: &[b"@n1_entry_missing__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: nope.iprange - No such file or directory\niprange: Cannot load file nope.iprange from list n1_entry_missing__list.txt (line 1)\n",
    },
    Case {
        label: "missing entry, no newline after the NUL",
        argv: &[b"@n1_entry_missing_nonl__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: nope.iprange - No such file or directory\niprange: Cannot load file nope.iprange from list n1_entry_missing_nonl__list.txt (line 1)\n",
    },
    Case {
        label: "missing entry with spaces before the NUL",
        argv: &[b"@n1_entry_missing_ws__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: nope.iprange - No such file or directory\niprange: Cannot load file nope.iprange from list n1_entry_missing_ws__list.txt (line 1)\n",
    },
    Case {
        label: "entry name with a space, cut after it",
        argv: &[b"@n1_entry_missing_sp_name__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: no pe.iprange - No such file or directory\niprange: Cannot load file no pe.iprange from list n1_entry_missing_sp_name__list.txt (line 1)\n",
    },
    Case {
        label: "entry naming a directory loads as an empty set",
        argv: &[b"@n1_entry_named_dir__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: sub - No such file or directory\niprange: Cannot load file sub from list n1_entry_named_dir__list.txt (line 1)\n",
    },
    Case {
        label: "-v: the list expansion echoes the cut name",
        argv: &[b"-v", b"@n1_entry_exists_v__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Loading files from list n1_entry_exists_v__list.txt\niprange: Loading file e.iprange from list (line 1)\niprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists_v__list.txt (line 1)\n",
    },
    Case {
        label: "-v: missing entry echoes the cut name",
        argv: &[b"-v", b"@n1_entry_missing_v__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Loading files from list n1_entry_missing_v__list.txt\niprange: Loading file nope.iprange from list (line 1)\niprange: nope.iprange - No such file or directory\niprange: Cannot load file nope.iprange from list n1_entry_missing_v__list.txt (line 1)\n",
    },
    Case {
        label: "-v: the successful expansion of a cut name",
        argv: &[b"-v", b"@n1_entry_exists_fifo__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Loading files from list n1_entry_exists_fifo__list.txt\niprange: Loading file e.iprange from list (line 1)\niprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists_fifo__list.txt (line 1)\n",
    },
    Case {
        label: "an entry that is only a NUL is an empty line",
        argv: &[b"@n1_only_nul__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: No valid files found in file list: n1_only_nul__list.txt\n",
    },
    Case {
        label: "a NUL as the whole last entry is empty",
        argv: &[b"@n1_only_nul_nonl__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: No valid files found in file list: n1_only_nul_nonl__list.txt\n",
    },
    Case {
        label: "one NUL byte as the whole file list",
        argv: &[b"@n1_only_nul_bare__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: No valid files found in file list: n1_only_nul_bare__list.txt\n",
    },
    Case {
        label: "spaces then a NUL is an empty entry",
        argv: &[b"@n1_ws_then_nul__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: No valid files found in file list: n1_ws_then_nul__list.txt\n",
    },
    Case {
        label: "a tab and a space then a NUL are empty",
        argv: &[b"@n1_tab_then_nul__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: No valid files found in file list: n1_tab_then_nul__list.txt\n",
    },
    Case {
        label: "several NUL bytes, all invisible",
        argv: &[b"@n1_multi_nul__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: No valid files found in file list: n1_multi_nul__list.txt\n",
    },
    Case {
        label: "-v: an empty entry is not a load attempt",
        argv: &[b"-v", b"@n1_only_nul_v__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Loading files from list n1_only_nul_v__list.txt\niprange: File list n1_only_nul_v__list.txt is empty or contains no valid entries\niprange: No valid files found in file list: n1_only_nul_v__list.txt\n",
    },
    Case {
        label: "a NUL hides the whole entry name behind it",
        argv: &[b"@n1_nul_hides_entry__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: No valid files found in file list: n1_nul_hides_entry__list.txt\n",
    },
    Case {
        label: "an empty NUL entry keeps the other two",
        argv: &[b"@n1_nul_line_between__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_nul_line_between__list.txt (line 1)\n",
    },
    Case {
        label: "a comment entry with a NUL is skipped",
        argv: &[b"@n1_hash_then_nul__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: No valid files found in file list: n1_hash_then_nul__list.txt\n",
    },
    Case {
        label: "a semicolon entry with a NUL is skipped",
        argv: &[b"@n1_semi_then_nul__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: No valid files found in file list: n1_semi_then_nul__list.txt\n",
    },
    Case {
        label: "a comment entry does not stop the next one",
        argv: &[b"@n1_hash_then_valid__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_hash_then_valid__list.txt (line 2)\n",
    },
    Case {
        label: "two entries, both cut at their NUL",
        argv: &[b"@n1_two_nul_entries__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_two_nul_entries__list.txt (line 1)\n",
    },
    Case {
        label: "a bad second entry names the cut name",
        argv: &[b"@n1_valid_then_bad__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_valid_then_bad__list.txt (line 1)\n",
    },
    Case {
        label: "the first bad entry stops the list",
        argv: &[b"@n1_bad_then_valid__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: nope - No such file or directory\niprange: Cannot load file nope from list n1_bad_then_valid__list.txt (line 1)\n",
    },
    Case {
        label: "an empty second entry is skipped",
        argv: &[b"@n1_second_entry_empty__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_second_entry_empty__list.txt (line 1)\n",
    },
    Case {
        label: "a space then NUL line after a good entry",
        argv: &[b"@n1_entry_then_wsline__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_then_wsline__list.txt (line 1)\n",
    },
    Case {
        label: "an entry over the line cap is still cut",
        argv: &[b"@n1_long_entry__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx - File name too long\niprange: Cannot load file xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx from list n1_long_entry__list.txt (line 1)\n",
    },
    Case {
        label: "NUL at the line-cap boundary",
        argv: &[b"@n1_long_entry_nul_at_cut__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx - File name too long\niprange: Cannot load file xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx from list n1_long_entry_nul_at_cut__list.txt (line 1)\n",
    },
    Case {
        label: "entry cut and record cut compose",
        argv: &[b"@n1_x_record_nul__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: ent.iprange - No such file or directory\niprange: Cannot load file ent.iprange from list n1_x_record_nul__list.txt (line 1)\n",
    },
    Case {
        label: "-v: entry cut and record cut compose",
        argv: &[b"-v", b"@n1_x_record_nul_v__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Loading files from list n1_x_record_nul_v__list.txt\niprange: Loading file ent.iprange from list (line 1)\niprange: ent.iprange - No such file or directory\niprange: Cannot load file ent.iprange from list n1_x_record_nul_v__list.txt (line 1)\n",
    },
    Case {
        label: "entry cut with an incomplete range",
        argv: &[b"@n1_x_record_broken_range__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: ent.iprange - No such file or directory\niprange: Cannot load file ent.iprange from list n1_x_record_broken_range__list.txt (line 1)\n",
    },
    Case {
        label: "entry cut with a NUL-only record inside",
        argv: &[b"@n1_x_entry_file_nul_only__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: ent.iprange - No such file or directory\niprange: Cannot load file ent.iprange from list n1_x_entry_file_nul_only__list.txt (line 1)\n",
    },
    Case {
        label: "@dir expansion with a NUL record inside",
        argv: &[b"@sub"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Cannot access sub: No such file or directory\n",
    },
    Case {
        label: "-6: entry truncated at the NUL opens the file",
        argv: &[b"-6", b"@n1_entry_exists6__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists6__list.txt (line 1)\n",
    },
    Case {
        label: "-6: missing entry names the cut name",
        argv: &[b"-6", b"@n1_entry_missing6__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: nope.iprange - No such file or directory\niprange: Cannot load file nope.iprange from list n1_entry_missing6__list.txt (line 1)\n",
    },
    Case {
        label: "-6: an entry that is only a NUL is empty",
        argv: &[b"-6", b"@n1_only_nul6__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: No valid files found in file list: n1_only_nul6__list.txt\n",
    },
    Case {
        label: "-6: entry cut and record cut compose",
        argv: &[b"-6", b"@n1_x_record_nul6__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: ent.iprange - No such file or directory\niprange: Cannot load file ent.iprange from list n1_x_record_nul6__list.txt (line 1)\n",
    },
    Case {
        label: "v1 flag line cut at the NUL",
        argv: &[b"n2_v1_flag_nul__t.bin"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: n2_v1_flag_nul__t.bin 2nd line should be the optimized flag, but found 'optimized'.\niprange: Cannot fast load n2_v1_flag_nul__t.bin\niprange: Cannot load ipset: n2_v1_flag_nul__t.bin\n",
    },
    Case {
        label: "v1 flag line: junk behind the NUL is invisible",
        argv: &[b"n2_v1_flag_nul_junk__t.bin"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: n2_v1_flag_nul_junk__t.bin 2nd line should be the optimized flag, but found 'optimized'.\niprange: Cannot fast load n2_v1_flag_nul_junk__t.bin\niprange: Cannot load ipset: n2_v1_flag_nul_junk__t.bin\n",
    },
    Case {
        label: "v1 flag line starting with a NUL echoes empty",
        argv: &[b"n2_v1_flag_lead_nul__t.bin"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: n2_v1_flag_lead_nul__t.bin 2nd line should be the optimized flag, but found ''.\niprange: Cannot fast load n2_v1_flag_lead_nul__t.bin\niprange: Cannot load ipset: n2_v1_flag_lead_nul__t.bin\n",
    },
    Case {
        label: "v1 flag line cut in the middle of the word",
        argv: &[b"n2_v1_flag_nul_mid__t.bin"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: n2_v1_flag_nul_mid__t.bin 2nd line should be the optimized flag, but found 'opti'.\niprange: Cannot fast load n2_v1_flag_nul_mid__t.bin\niprange: Cannot load ipset: n2_v1_flag_nul_mid__t.bin\n",
    },
    Case {
        label: "v1 non-optimized flag cut at the NUL",
        argv: &[b"n2_v1_flag_nonopt_nul__t.bin"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: n2_v1_flag_nonopt_nul__t.bin 2nd line should be the optimized flag, but found 'non-optimized'.\niprange: Cannot fast load n2_v1_flag_nonopt_nul__t.bin\niprange: Cannot load ipset: n2_v1_flag_nonopt_nul__t.bin\n",
    },
    Case {
        label: "-v: v1 flag line cut at the NUL",
        argv: &[b"-v", b"n2_v1_flag_nul_v__t.bin"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Loading from n2_v1_flag_nul_v__t.bin\niprange: n2_v1_flag_nul_v__t.bin 2nd line should be the optimized flag, but found 'optimized'.\niprange: Cannot fast load n2_v1_flag_nul_v__t.bin\niprange: Cannot load ipset: n2_v1_flag_nul_v__t.bin\n",
    },
    Case {
        label: "v1 record size value starting with a NUL",
        argv: &[b"n2_v1_reclen_lead_nul__t.bin"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: n2_v1_reclen_lead_nul__t.bin: invalid record size value ''\niprange: Cannot fast load n2_v1_reclen_lead_nul__t.bin\niprange: Cannot load ipset: n2_v1_reclen_lead_nul__t.bin\n",
    },
    Case {
        label: "v1 record size key cut at the NUL",
        argv: &[b"n2_v1_reclen_key_nul__t.bin"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: n2_v1_reclen_key_nul__t.bin 3rd line should be the record size, but found 'record'.\niprange: Cannot fast load n2_v1_reclen_key_nul__t.bin\niprange: Cannot load ipset: n2_v1_reclen_key_nul__t.bin\n",
    },
    Case {
        label: "v1 unique ips value starting with a NUL",
        argv: &[b"n2_v1_unique_lead_nul__t.bin"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: n2_v1_unique_lead_nul__t.bin: invalid unique ips value ''\niprange: Cannot fast load n2_v1_unique_lead_nul__t.bin\niprange: Cannot load ipset: n2_v1_unique_lead_nul__t.bin\n",
    },
    Case {
        label: "v1: every header line carries a NUL",
        argv: &[b"n2_v1_all_nul__t.bin"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: n2_v1_all_nul__t.bin 2nd line should be the optimized flag, but found 'optimized'.\niprange: Cannot fast load n2_v1_all_nul__t.bin\niprange: Cannot load ipset: n2_v1_all_nul__t.bin\n",
    },
    Case {
        label: "v2 family line cut at the NUL",
        argv: &[b"-6", b"n2_v2_family_nul__t.bin"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: n2_v2_family_nul__t.bin expected family 'ipv6' but found 'ipv6'.\niprange: Cannot load binary v2 n2_v2_family_nul__t.bin\niprange: Cannot load ipset: n2_v2_family_nul__t.bin\n",
    },
    Case {
        label: "v2 family line starting with a NUL",
        argv: &[b"-6", b"n2_v2_family_lead_nul__t.bin"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: n2_v2_family_lead_nul__t.bin expected family 'ipv6' but found ''.\niprange: Cannot load binary v2 n2_v2_family_lead_nul__t.bin\niprange: Cannot load ipset: n2_v2_family_lead_nul__t.bin\n",
    },
    Case {
        label: "v2 optimized flag line cut at the NUL",
        argv: &[b"-6", b"n2_v2_flag_nul__t.bin"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: n2_v2_flag_nul__t.bin expected optimized flag but found 'optimized'.\niprange: Cannot load binary v2 n2_v2_flag_nul__t.bin\niprange: Cannot load ipset: n2_v2_flag_nul__t.bin\n",
    },
    Case {
        label: "v2 record size value starting with a NUL",
        argv: &[b"-6", b"n2_v2_reclen_lead_nul__t.bin"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: n2_v2_reclen_lead_nul__t.bin: invalid record size value ''\niprange: Cannot load binary v2 n2_v2_reclen_lead_nul__t.bin\niprange: Cannot load ipset: n2_v2_reclen_lead_nul__t.bin\n",
    },
    Case {
        label: "v2 unique ips value starting with a NUL",
        argv: &[b"-6", b"n2_v2_unique_lead_nul__t.bin"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: n2_v2_unique_lead_nul__t.bin: invalid unique ips value ''\niprange: Cannot load binary v2 n2_v2_unique_lead_nul__t.bin\niprange: Cannot load ipset: n2_v2_unique_lead_nul__t.bin\n",
    },
    Case {
        label: "v2 record size key cut at the NUL",
        argv: &[b"-6", b"n2_v2_key_nul__t.bin"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: n2_v2_key_nul__t.bin expected record size but found 'record'.\niprange: Cannot load binary v2 n2_v2_key_nul__t.bin\niprange: Cannot load ipset: n2_v2_key_nul__t.bin\n",
    },
    Case {
        label: "@list entry with a NUL naming a bad v1 file",
        argv: &[b"@n2_v1_in_list__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: t.bin - No such file or directory\niprange: Cannot load file t.bin from list n2_v1_in_list__list.txt (line 1)\n",
    },
    Case {
        label: "@list entry with a NUL naming a valid v1 file",
        argv: &[b"@n2_v1_in_list_ok__list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: t.bin - No such file or directory\niprange: Cannot load file t.bin from list n2_v1_in_list_ok__list.txt (line 1)\n",
    },
    Case {
        label: "a record starting with the byte 0x00",
        argv: &[b"n3_leading_true_nul__t.iprange"],
        rc: 0,
        stdout: b"",
        stderr: b"",
    },
    Case {
        label: "a NUL inside the prefix digits",
        argv: &[b"n3_prefix_true_nul__t.iprange"],
        rc: 0,
        stdout: b"0.0.0.0/2\n",
        stderr: b"",
    },
    Case {
        label: "-6: a record starting with the byte 0x00",
        argv: &[b"-6", b"n3_leading_true_nul6__t.iprange"],
        rc: 0,
        stdout: b"",
        stderr: b"",
    },
    Case {
        label: "-6: a NUL inside the prefix digits",
        argv: &[b"-6", b"n3_prefix_true_nul6__t.iprange"],
        rc: 0,
        stdout: b"2000::/12\n",
        stderr: b"",
    },
];

/// The `@file-list`/`@dir` entry cut pinned together with the record cut inside
/// the file the truncated name opened, plus the open-failure family for a
/// truncated name that is not there.
const NUL_LIST_ENTRY_CASES: &[Case] = &[
    Case {
        label: "@list entry cut: the truncated name is opened and its records are cut",
        argv: &[b"@cmp_ok_list.txt"],
        rc: 0,
        stdout: b"1.2.3.4\n5.6.7.8/29\n5.6.7.16/28\n5.6.7.32/27\n5.6.7.64/26\n5.6.7.128/25\n5.6.8.0/21\n5.6.16.0/20\n5.6.32.0/19\n5.6.64.0/18\n5.6.128.0/17\n5.7.0.0/16\n5.8.0.0/13\n5.16.0.0/12\n5.32.0.0/11\n5.64.0.0/10\n5.128.0.0/9\n6.0.0.0/7\n8.0.0.0/8\n9.0.0.0/13\n9.8.0.0/15\n9.10.0.0/21\n9.10.8.0/23\n9.10.10.0/24\n9.10.11.0/29\n9.10.11.8/30\n9.10.11.12\n",
        stderr: b"",
    },
    Case {
        label: "@list entry cut under -v: every echo names the truncated file",
        argv: &[b"-v", b"@cmp_ok_v_list.txt"],
        rc: 0,
        stdout: b"1.2.3.4\n5.6.7.8/29\n5.6.7.16/28\n5.6.7.32/27\n5.6.7.64/26\n5.6.7.128/25\n5.6.8.0/21\n5.6.16.0/20\n5.6.32.0/19\n5.6.64.0/18\n5.6.128.0/17\n5.7.0.0/16\n5.8.0.0/13\n5.16.0.0/12\n5.32.0.0/11\n5.64.0.0/10\n5.128.0.0/9\n6.0.0.0/7\n8.0.0.0/8\n9.0.0.0/13\n9.8.0.0/15\n9.10.0.0/21\n9.10.8.0/23\n9.10.10.0/24\n9.10.11.0/29\n9.10.11.8/30\n9.10.11.12\n",
        stderr: b"iprange: Loading files from list cmp_ok_v_list.txt\niprange: Loading file cmp_ok_v_ent.iprange from list (line 1)\niprange: Loading from cmp_ok_v_ent.iprange\niprange: Loaded optimized cmp_ok_v_ent.iprange\niprange: Printing combined ipset with 2 ranges, 67372038 unique IPs\n\n27 printed CIDRs, break down by prefix:\n\t- prefix /7 counts 1 entries\n\t- prefix /8 counts 1 entries\n\t- prefix /9 counts 1 entries\n\t- prefix /10 counts 1 entries\n\t- prefix /11 counts 1 entries\n\t- prefix /12 counts 1 entries\n\t- prefix /13 counts 2 entries\n\t- prefix /15 counts 1 entries\n\t- prefix /16 counts 1 entries\n\t- prefix /17 counts 1 entries\n\t- prefix /18 counts 1 entries\n\t- prefix /19 counts 1 entries\n\t- prefix /20 counts 1 entries\n\t- prefix /21 counts 2 entries\n\t- prefix /23 counts 1 entries\n\t- prefix /24 counts 1 entries\n\t- prefix /25 counts 1 entries\n\t- prefix /26 counts 1 entries\n\t- prefix /27 counts 1 entries\n\t- prefix /28 counts 1 entries\n\t- prefix /29 counts 2 entries\n\t- prefix /30 counts 1 entries\n\t- prefix /32 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 23 CIDR prefixes, 27 CIDRs printed, 67372038 unique IPs\n<WALLCLOCK>\n",
    },
    Case {
        label: "@list entry cut: an invalid record inside the truncated name still fails",
        argv: &[b"@cmp_bad_list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Invalid netmask 99\niprange: Cannot understand line No 1 from cmp_bad_ent.iprange: 1.2.3.4/99\niprange: Cannot load file cmp_bad_ent.iprange from list cmp_bad_list.txt (line 1)\n",
    },
    Case {
        label: "@list entry cut under -v with an invalid record inside",
        argv: &[b"-v", b"@cmp_bad_v_list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: Loading files from list cmp_bad_v_list.txt\niprange: Loading file cmp_bad_v_ent.iprange from list (line 1)\niprange: Loading from cmp_bad_v_ent.iprange\niprange: Invalid netmask 99\niprange: Cannot understand line No 1 from cmp_bad_v_ent.iprange: 1.2.3.4/99\niprange: Cannot load file cmp_bad_v_ent.iprange from list cmp_bad_v_list.txt (line 1)\n",
    },
    Case {
        label: "@list entry cut: blanks before the NUL are trimmed and the name opens",
        argv: &[b"@cmp_sp_list.txt"],
        rc: 0,
        stdout: b"1.2.3.4\n",
        stderr: b"",
    },
    Case {
        label: "@list entry cut: two entries, both cut, both opened",
        argv: &[b"@cmp_two_list.txt"],
        rc: 0,
        stdout: b"1.2.3.4\n5.6.7.8\n",
        stderr: b"",
    },
    Case {
        label: "@list entry cut: the first name opens, the second truncated name is missing",
        argv: &[b"@cmp_mix_list.txt"],
        rc: 1,
        stdout: b"",
        stderr: b"iprange: cmp_mix_missing.iprange - No such file or directory\niprange: Cannot load file cmp_mix_missing.iprange from list cmp_mix_list.txt (line 2)\n",
    },
    Case {
        label: "@dir expansion with NUL records inside the directory",
        argv: &[b"@cmp_dir"],
        rc: 0,
        stdout: b"1.2.3.4\n5.6.7.8/29\n5.6.7.16/28\n5.6.7.32/27\n5.6.7.64/26\n5.6.7.128/25\n5.6.8.0/21\n5.6.16.0/20\n5.6.32.0/19\n5.6.64.0/18\n5.6.128.0/17\n5.7.0.0/16\n5.8.0.0/13\n5.16.0.0/12\n5.32.0.0/11\n5.64.0.0/10\n5.128.0.0/9\n6.0.0.0/7\n8.0.0.0/8\n9.0.0.0/13\n9.8.0.0/15\n9.10.0.0/21\n9.10.8.0/23\n9.10.10.0/24\n9.10.11.0/29\n9.10.11.8/30\n9.10.11.12\n",
        stderr: b"",
    },
    Case {
        label: "@dir expansion under -v with NUL records inside",
        argv: &[b"-v", b"@cmp_dirv"],
        rc: 0,
        stdout: b"1.2.3.4\n5.6.7.8\n",
        stderr: b"iprange: Loading files from directory cmp_dirv\niprange: Loading file cmp_dirv/a.iprange from directory cmp_dirv\niprange: Loading from cmp_dirv/a.iprange\niprange: Loaded optimized cmp_dirv/a.iprange\niprange: Loading file cmp_dirv/b.iprange from directory cmp_dirv\niprange: Loading from cmp_dirv/b.iprange\niprange: Loaded optimized cmp_dirv/b.iprange\niprange: Loading file cmp_dirv/c.iprange from directory cmp_dirv\niprange: Loading from cmp_dirv/c.iprange\niprange: Loaded optimized cmp_dirv/c.iprange\niprange: Merging cmp_dirv/b.iprange to combined ipset\niprange: Merging cmp_dirv/c.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n",
    },
    Case {
        label: "-6: @list entry cut opens the truncated name and cuts its records",
        argv: &[b"-6", b"@cmp6_list.txt"],
        rc: 0,
        stdout: b"2001:db8::1\n",
        stderr: b"",
    },
    Case {
        label: "-6: @list entry cut with an invalid record inside",
        argv: &[b"-6", b"@cmp6_bad_list.txt"],
        rc: 0,
        stdout: b"2001:db8::/99\n",
        stderr: b"",
    },
];

/// The cases whose result the machine resolver decides.
const NUL_RECORDS_RESOLVER: &[LiveCase] = &[
    LiveCase {
        label: "hostname truncated at the NUL resolves",
        argv: &[b"h_localhost_nul__t.iprange"],
    },
    LiveCase {
        label: "hostname then NUL then newline",
        argv: &[b"h_localhost_nul_nl__t.iprange"],
    },
    LiveCase {
        label: "hostname then two NUL bytes",
        argv: &[b"h_localhost_double__t.iprange"],
    },
    LiveCase {
        label: "hostname with a comment then a NUL",
        argv: &[b"h_localhost_comment__t.iprange"],
    },
    LiveCase {
        label: "two hostname records hidden behind NULs",
        argv: &[b"h_localhost_twice__t.iprange"],
    },
    LiveCase {
        label: "digit-then-letter token becomes a hostname",
        argv: &[b"h_num_letter__t.iprange"],
    },
    LiveCase {
        label: "underscore token becomes a hostname",
        argv: &[b"h_underscore__t.iprange"],
    },
    LiveCase {
        label: "v6 non-hex word becomes a hostname",
        argv: &[b"-6", b"v6_nonhex__t.iprange"],
    },
    LiveCase {
        label: "v6 hostname truncated at the NUL resolves",
        argv: &[b"-6", b"v6_localhost_nul__t.iprange"],
    },
    LiveCase {
        label: "entry cut with a hostname record",
        argv: &[b"@n1_x_record_unparsable__list.txt"],
    },
    LiveCase {
        label: "entry cut with a hostname record behind a NUL",
        argv: &[b"@n1_x_record_hostname_cut__list.txt"],
    },
    LiveCase {
        label: "-6: entry cut with a hostname record",
        argv: &[b"-6", b"@n1_x_record_unparsable6__list.txt"],
    },
    LiveCase {
        label: "@list entry cut with a hostname record behind the NUL",
        argv: &[b"-v", b"@cmp_host_list.txt"],
    },
];

fn run_exact(cases: &[Case]) {
    let dir = fixtures();
    for case in cases {
        let owned: Vec<OsString> = case.argv.iter().map(|a| raw(a)).collect();
        let argv: Vec<&OsStr> = owned.iter().map(|a| a.as_os_str()).collect();
        assert_verbose_parity(
            case.label,
            dir.path(),
            &argv,
            b"",
            (case.rc, case.stdout, case.stderr),
        );
    }
}

/// Judge a resolver-dependent case against the reference process rather than
/// against pinned bytes: the same exit code, and the same stdout and
/// masked-stderr lines as a multiset.
fn run_live(cases: &[LiveCase]) {
    let Some(reference) = parity_support::reference() else {
        panic!("no C reference installed: the resolver-dependent cases cannot be judged");
    };
    let dir = fixtures();
    for case in cases {
        let owned: Vec<OsString> = case.argv.iter().map(|a| raw(a)).collect();
        let argv: Vec<&OsStr> = owned.iter().map(|a| a.as_os_str()).collect();
        let engine = run_bounded_in(&PathBuf::from(PROGRAM), dir.path(), &argv, b"");
        let oracle = run_bounded_in(&reference, dir.path(), &argv, b"");
        assert_eq!(
            (engine.code, engine.signal, engine.timed_out),
            (oracle.code, oracle.signal, oracle.timed_out),
            "{}: exit status differs from {} ({})",
            case.label,
            reference.display(),
            engine.describe()
        );
        assert_eq!(
            line_multiset(&engine.stdout),
            line_multiset(&oracle.stdout),
            "{}: stdout lines differ from the reference",
            case.label
        );
        assert_eq!(
            line_multiset(&mask_wallclock(&engine.stderr)),
            line_multiset(&mask_wallclock(&oracle.stderr)),
            "{}: stderr lines differ from the reference (engine stderr {:?})",
            case.label,
            String::from_utf8_lossy(&engine.stderr)
        );
    }
}

/// The lines of a stream as a sorted multiset. The C emits its per-reply DNS
/// lines from resolver threads, so the order within one run is not contract.
fn line_multiset(bytes: &[u8]) -> Vec<&[u8]> {
    let mut lines: Vec<&[u8]> = bytes.split(|b| *b == b'\n').collect();
    lines.sort_unstable();
    lines
}

/// The entry cut and the record cut in one run, and the open-failure family.
#[test]
fn nul_list_entry_cuts_match_c() {
    run_exact(NUL_LIST_ENTRY_CASES);
}

#[test]
fn nul_text_records_match_c() {
    run_exact(NUL_RECORDS);
}

#[test]
fn nul_text_records_resolved_by_c() {
    run_live(NUL_RECORDS_RESOLVER);
}

/// The rule at the smallest scale: each twin pair differs only in the bytes
/// hidden behind a NUL, so the engine must judge both the same way. The pairs
/// are pinned against the C separately, so this checks the rule and not just
/// the reference.
#[test]
fn nul_truncation_matches_its_twin() {
    let dir = fixtures();
    for (hidden, visible) in TWINS {
        let owned: Vec<OsString> = [*hidden].iter().map(|a| raw(a)).collect();
        let argv: Vec<&OsStr> = owned.iter().map(|a| a.as_os_str()).collect();
        let a = run_bounded_in(&PathBuf::from(PROGRAM), dir.path(), &argv, b"");
        let owned: Vec<OsString> = [*visible].iter().map(|a| raw(a)).collect();
        let argv: Vec<&OsStr> = owned.iter().map(|a| a.as_os_str()).collect();
        let b = run_bounded_in(&PathBuf::from(PROGRAM), dir.path(), &argv, b"");
        assert_eq!(
            a.code, b.code,
            "{}: rc differs from its NUL-free twin {}",
            String::from_utf8_lossy(hidden),
            String::from_utf8_lossy(visible)
        );
        assert_eq!(
            a.stdout, b.stdout,
            "{}: stdout differs from its NUL-free twin",
            String::from_utf8_lossy(hidden)
        );
        assert_eq!(
            line_multiset(&mask_wallclock(&a.stderr)),
            line_multiset(&mask_wallclock(&b.stderr)),
            "{}: stderr differs from its NUL-free twin",
            String::from_utf8_lossy(hidden)
        );
    }
}
