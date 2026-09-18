// Text records that hold interior NUL bytes, classified exactly like the
// released C tool.
//
// ipset_load() and ipset6_load() read a record with fgets(line, MAX_LINE, fp)
// and classify it with parse_line() (src/ipset_load.c:137) or parse_line6()
// (src/ipset6_load.c:59), which test single bytes against a terminator set
// that includes '\0' (src/ipset_load.c:150,173,195,224;
// src/ipset6_load.c:68,99,127,144) and scan tokens with predicates that
// reject '\0'. The load-time IPv6 scan strchr(line, ":") (src/ipset_load.c:309)
// and the record echo fprintf(..., ": %s\n", line) (src/ipset_load.c:343,355,
// 369; src/ipset6_load.c:250,262,278) stop there too. So for the C a record is
// exactly the bytes before its first NUL: the hidden tail cannot make an
// address invalid, cannot complete a range, cannot add a comment marker, and
// cannot add a second colon. Record count and line ids are unaffected, because
// fgets still splits on '\n'.
//
// Two directions are pinned, because they break differently. A record that is
// valid up to its NUL is accepted and its hidden tail ignored (rc 0, the entry
// on stdout). A record that is invalid up to its NUL still fails, and the extra
// lines the C prints on the way - "Invalid netmask", "Invalid address",
// "Incomplete range", "Cannot parse address", "Mixed-family range" - appear
// because the truncated token reached the address parser.
//
// The one exception to byte-exact pinning is the class whose outcome the
// machine resolver decides: a NUL-truncated token that is a hostname. Those
// cases compare the engine to the reference process instead, on exit code and
// on the stdout and masked-stderr lines as multisets, because the C writes its
// per-reply lines from resolver threads. Every other case, including the ones
// where a NUL merely hides a hostname, is pinned byte for byte. The wall-clock
// line is the shared per-line exception (maskWallclock).

package legacy

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// nulFixtures is the directory every case reads, by exact name and content.
var nulFixtures = map[string]string{
	"a.iprange":                                  "1.2.3.4\x00junk\n",
	"b.iprange":                                  "10.0.0.0/30\n",
	"c_24__t.iprange":                            "1.2.3.4/24\x00junk\n",
	"c_8__t.iprange":                             "10.0.0.0/8\x00junk junk\n",
	"c_99__t.iprange":                            "1.2.3.4/99\x00junk\n",
	"c_9sp__t.iprange":                           "1.2.3.4/9\x00 9\n",
	"c_double_slash__t.iprange":                  "1.2.3.4//\x00junk\n",
	"c_hex__t.iprange":                           "1.2.3.4/0x10\x00junk\n",
	"c_in_prefix__t.iprange":                     "1.2.3.4/2\x04\n",
	"c_slash_only__t.iprange":                    "1.2.3.4/\x00junk\n",
	"cap_300digits__t.iprange":                   "999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999\x00junk\n",
	"cap_300host__t.iprange":                     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\x00junk\n",
	"cap_300prefix__t.iprange":                   "1.2.3.4/999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999\x00junk\n",
	"d_bang_nul_colons__t.iprange":               "!\x00:::bad\n",
	"d_colons_nul__t.iprange":                    ":::\x00junk\n",
	"d_mapped_nul__t.iprange":                    "::ffff:1.2.3.4\x00junk\n",
	"d_mapped_sp__t.iprange":                     "::ffff:1.2.3.4 junk\x00\n",
	"d_mixed3__t.iprange":                        "1.2.3.4\n!\x00:::bad\n5.6.7.8\n",
	"d_one_colon__t.iprange":                     ":\x00::x\n",
	"h_addr_nul_host__t.iprange":                 "10.0.0.1\x00localhost\n",
	"h_hostname_nul_bad__t.iprange":              "localhost x\x00junk\n",
	"h_localhost_comment__t.iprange":             "localhost # c\x00junk\n",
	"h_localhost_double__t.iprange":              "localhost\x00\x00\n",
	"h_localhost_nul__t.iprange":                 "localhost\x00junk\n",
	"h_localhost_nul_nl__t.iprange":              "localhost\x00\n",
	"h_localhost_twice__t.iprange":               "localhost\x00A\nlocalhost\x00B\n",
	"h_num_letter__t.iprange":                    "1abc\x00junk\n",
	"h_underscore__t.iprange":                    "a_b\x00x\n",
	"m_hash__t.iprange":                          "# comment\x00junk\n",
	"m_hash_ws__t.iprange":                       "  # x\x00y\n",
	"m_nul_before_hash__t.iprange":               "1.2.3.4\x00# junk\n",
	"m_nul_first_hash__t.iprange":                "\x00#junk\n",
	"m_semi__t.iprange":                          "; comment\x00junk\n",
	"m_trail_hash__t.iprange":                    "1.2.3.4 # c\x00junk\n",
	"m_trail_semi_tab__t.iprange":                "1.2.3.4\t;\x00junk\n",
	"n1_bad_then_valid__e.iprange":               "1.2.3.4\n",
	"n1_bad_then_valid__list.txt":                "nope\x00junk\ne.iprange\n",
	"n1_entry_exists6__e.iprange":                "2001:db8::1\n",
	"n1_entry_exists6__list.txt":                 "e.iprange\x00junk\n",
	"n1_entry_exists__e.iprange":                 "1.2.3.4\n",
	"n1_entry_exists__list.txt":                  "e.iprange\x00junk\n",
	"n1_entry_exists_fifo__e.iprange":            "1.2.3.4\n",
	"n1_entry_exists_fifo__list.txt":             "e.iprange\x00junk\n",
	"n1_entry_exists_nonl__e.iprange":            "1.2.3.4\n",
	"n1_entry_exists_nonl__list.txt":             "e.iprange\x00junk",
	"n1_entry_exists_sp__e.iprange":              "1.2.3.4\n",
	"n1_entry_exists_sp__list.txt":               "e.iprange  \x00junk\n",
	"n1_entry_exists_tab__e.iprange":             "1.2.3.4\n",
	"n1_entry_exists_tab__list.txt":              "e.iprange\t\x00junk\n",
	"n1_entry_exists_v__e.iprange":               "1.2.3.4\n",
	"n1_entry_exists_v__list.txt":                "e.iprange\x00junk\n",
	"n1_entry_exists_ws_only__e.iprange":         "1.2.3.4\n",
	"n1_entry_exists_ws_only__list.txt":          "  e.iprange  \n",
	"n1_entry_missing6__list.txt":                "nope.iprange\x00junk\n",
	"n1_entry_missing__list.txt":                 "nope.iprange\x00junk\n",
	"n1_entry_missing_nonl__list.txt":            "nope.iprange\x00junk",
	"n1_entry_missing_sp_name__list.txt":         "no pe.iprange\x00junk\n",
	"n1_entry_missing_v__list.txt":               "nope.iprange\x00junk\n",
	"n1_entry_missing_ws__list.txt":              "  nope.iprange \x00junk\n",
	"n1_entry_named_dir__list.txt":               "sub\x00junk\n",
	"n1_entry_named_dir__sub/inner.iprange":      "1.2.3.4\n",
	"n1_entry_nul_at_end__e.iprange":             "1.2.3.4\n",
	"n1_entry_nul_at_end__list.txt":              "e.iprange\x00\n",
	"n1_entry_then_wsline__e.iprange":            "1.2.3.4\n",
	"n1_entry_then_wsline__list.txt":             "e.iprange\n \x00junk\n",
	"n1_hash_then_nul__list.txt":                 "# c\x00junk\n",
	"n1_hash_then_valid__e.iprange":              "1.2.3.4\n",
	"n1_hash_then_valid__list.txt":               "# c\x00junk\ne.iprange\n",
	"n1_long_entry__list.txt":                    "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\x00junk\n",
	"n1_long_entry_nul_at_cut__list.txt":         "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\x00junk\n",
	"n1_multi_nul__list.txt":                     "\x00\x00\x00junk\n",
	"n1_nul_hides_entry__e.iprange":              "1.2.3.4\n",
	"n1_nul_hides_entry__list.txt":               "\x00e.iprange\n",
	"n1_nul_line_between__e.iprange":             "1.2.3.4\n",
	"n1_nul_line_between__f.iprange":             "5.6.7.8\n",
	"n1_nul_line_between__list.txt":              "e.iprange\n\x00junk\nf.iprange\n",
	"n1_only_nul6__list.txt":                     "\x00junk\n",
	"n1_only_nul__list.txt":                      "\x00junk\n",
	"n1_only_nul_bare__list.txt":                 "\x00",
	"n1_only_nul_nonl__list.txt":                 "\x00junk",
	"n1_only_nul_v__list.txt":                    "\x00junk\n",
	"n1_second_entry_empty__e.iprange":           "1.2.3.4\n",
	"n1_second_entry_empty__list.txt":            "e.iprange\n\x00\n",
	"n1_semi_then_nul__list.txt":                 "; c\x00junk\n",
	"n1_tab_then_nul__list.txt":                  "\t \x00junk\n",
	"n1_two_nul_entries__e.iprange":              "1.2.3.4\n",
	"n1_two_nul_entries__f.iprange":              "5.6.7.8\n",
	"n1_two_nul_entries__list.txt":               "e.iprange\x00X\nf.iprange\x00Y\n",
	"n1_valid_then_bad__e.iprange":               "1.2.3.4\n",
	"n1_valid_then_bad__list.txt":                "e.iprange\nnope\x00junk\n",
	"n1_ws_then_nul__list.txt":                   "   \x00junk\n",
	"n1_x_dir_entry_record_nul__sub/ent.iprange": "1.2.3.4/99\x00x\n5.6.7.8\n",
	"n1_x_entry_file_nul_only__ent.iprange":      "\x00junk\n",
	"n1_x_entry_file_nul_only__list.txt":         "ent.iprange\x00junk\n",
	"n1_x_record_broken_range__ent.iprange":      "1.2.3.4 -\x00\n",
	"n1_x_record_broken_range__list.txt":         "ent.iprange\x00junk\n",
	"n1_x_record_hostname_cut__ent.iprange":      "localhost\x00junk\n",
	"n1_x_record_hostname_cut__list.txt":         "ent.iprange\x00junk\n",
	"n1_x_record_nul6__ent.iprange":              "2001:db8::/99\x00x\n",
	"n1_x_record_nul6__list.txt":                 "ent.iprange\x00junk\n",
	"n1_x_record_nul__ent.iprange":               "1.2.3.4/99\x00x\n5.6.7.8\n",
	"n1_x_record_nul__list.txt":                  "ent.iprange\x00junk\n",
	"n1_x_record_nul_v__ent.iprange":             "1.2.3.4/99\x00x\n5.6.7.8\n",
	"n1_x_record_nul_v__list.txt":                "ent.iprange\x00junk\n",
	"n1_x_record_unparsable6__ent.iprange":       "bogus\n",
	"n1_x_record_unparsable6__list.txt":          "ent.iprange\x00junk\n",
	"n1_x_record_unparsable__ent.iprange":        "bogus\n",
	"n1_x_record_unparsable__list.txt":           "ent.iprange\x00junk\n",
	"n2_v1_all_nul__t.bin":                       "iprange binary format v1.0\noptimized\x00\nrecord size 8\x00\nrecords 1\x00\nbytes 12\x00\nlines 1\x00\nunique ips 4\x00\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"n2_v1_flag_lead_nul__t.bin":                 "iprange binary format v1.0\n\x00optimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"n2_v1_flag_nonopt_nul__t.bin":               "iprange binary format v1.0\nnon-optimized\x00\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"n2_v1_flag_nul__t.bin":                      "iprange binary format v1.0\noptimized\x00\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"n2_v1_flag_nul_junk__t.bin":                 "iprange binary format v1.0\noptimized\x00junk\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"n2_v1_flag_nul_mid__t.bin":                  "iprange binary format v1.0\nopti\x00mized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"n2_v1_flag_nul_v__t.bin":                    "iprange binary format v1.0\noptimized\x00\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"n2_v1_in_list__list.txt":                    "t.bin\x00junk\n",
	"n2_v1_in_list__t.bin":                       "iprange binary format v1.0\noptimized\x00\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"n2_v1_in_list_ok__list.txt":                 "t.bin\x00junk\n",
	"n2_v1_in_list_ok__t.bin":                    "iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"n2_v1_reclen_key_nul__t.bin":                "iprange binary format v1.0\noptimized\nrecord\x00size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"n2_v1_reclen_lead_nul__t.bin":               "iprange binary format v1.0\noptimized\nrecord size \x008\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"n2_v1_unique_lead_nul__t.bin":               "iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips \x004\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"n2_v2_family_lead_nul__t.bin":               "iprange binary format v2.0\n\x00ipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 ",
	"n2_v2_family_nul__t.bin":                    "iprange binary format v2.0\nipv6\x00\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 ",
	"n2_v2_flag_nul__t.bin":                      "iprange binary format v2.0\nipv6\noptimized\x00\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 ",
	"n2_v2_key_nul__t.bin":                       "iprange binary format v2.0\nipv6\noptimized\nrecord\x00size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 ",
	"n2_v2_reclen_lead_nul__t.bin":               "iprange binary format v2.0\nipv6\noptimized\nrecord size \x0032\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 ",
	"n2_v2_unique_lead_nul__t.bin":               "iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips \x008\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 ",
	"n3_leading_true_nul6__t.iprange":            "\x002001:db8::1\n",
	"n3_leading_true_nul__t.iprange":             "\x001.2.3.4\n",
	"n3_prefix_true_nul6__t.iprange":             "2001:db8::/12\x005\n",
	"n3_prefix_true_nul__t.iprange":              "1.2.3.4/2\x004\n",
	"nul_cidr.iprange":                           "1.2.3.4/24\x00junk\n",
	"nul_comment.iprange":                        "\x00junk\n",
	"nul_empty.iprange":                          "   \x00junk\n",
	"nul_plain.iprange":                          "1.2.3.4\x00junk\n",
	"nul_range.iprange":                          "1.2.3.4-5.6.7.8\x00junk\n",
	"p_broken_nul__t.iprange":                    "1.2.3.4 junk\x00\n",
	"p_mid_empty__t.iprange":                     "1.2.3.4\n\x00\n5.6.7.8\n",
	"p_nul_before_nl__t.iprange":                 "1.2.3.4\x00\n",
	"p_nul_first_addr__t.iprange":                "\x01.2.3.4\n",
	"p_nul_hides_addr__t.iprange":                "1.2.3.4\n\x00 9.9.9.9\n",
	"p_nul_multi__t.iprange":                     "\x00\x00\x00junk\n",
	"p_nul_only_nonl__t.iprange":                 "\x00",
	"p_nul_only_rec__t.iprange":                  "\x00\n",
	"p_plain_nul__t.iprange":                     "1.2.3.4\x00junk\n",
	"p_plain_nul_nonl__t.iprange":                "1.2.3.4\x00junk",
	"p_plain_nul_sp__t.iprange":                  "1.2.3.4 \x00junk\n",
	"p_plain_nul_tab__t.iprange":                 "1.2.3.4\t\x00junk\n",
	"p_second_nul__t.iprange":                    "1.2.3.4\n5.6.7.8\x00junk\n",
	"p_tab_leading__t.iprange":                   "\t1.2.3.4\x00junk\n",
	"p_ws_then_nul__t.iprange":                   "   \x00junk\n",
	"r_bad_second__t.iprange":                    "1.2.3.4-999\x00junk\n",
	"r_bad_second_noNUL__t.iprange":              "1.2.3.4-999 junk\n",
	"r_dash_comment__t.iprange":                  "1.2.3.4-#junk\n",
	"r_end_dash__t.iprange":                      "1.2.3.4-\x00junk\n",
	"r_junk_nul__t.iprange":                      "1.2.3.4-5.6.7.8 junk\x00\n",
	"r_nul_after_sp__t.iprange":                  "1.2.3.4 \x00- 5.6.7.8\n",
	"r_nul_kills__t.iprange":                     "1.2.3.4\x00-5.6.7.8\n",
	"r_nul_sp__t.iprange":                        "1.2.3.4-5.6.7.8\x00 junk\n",
	"r_ok_nul__t.iprange":                        "1.2.3.4-5.6.7.8\x00junk\n",
	"r_prefix_range__t.iprange":                  "1.2.3.4-5.6.7.8/24\x00junk\n",
	"r_second_nul_nl__t.iprange":                 "1.2.3.4-5.6.7.8\x00\n",
	"r_sp_dash_sp__t.iprange":                    "1.2.3.4 - \x00junk\n",
	"same_cidr.iprange":                          "1.2.3.4/24\n",
	"same_comment.iprange":                       "\n",
	"same_empty.iprange":                         "   \n",
	"same_plain.iprange":                         "1.2.3.4\n",
	"same_range.iprange":                         "1.2.3.4-5.6.7.8\n",
	"v6_1_nul__t.iprange":                        "::1\x00junk\n",
	"v6_2001__t.iprange":                         "2001:db8::1\x00junk\n",
	"v6_addr_nul_then__t.iprange":                "::1\n\x00fe80::x\n",
	"v6_dash__t.iprange":                         "::1-\x00junk\n",
	"v6_empty_nul__t.iprange":                    "\x00\n",
	"v6_hash__t.iprange":                         "# c\x00junk\n",
	"v6_hex_word__t.iprange":                     "abcd\x00junk\n",
	"v6_localhost_nul__t.iprange":                "localhost\x00junk\n",
	"v6_mapped__t.iprange":                       "::ffff:1.2.3.4\x00junk\n",
	"v6_mid_empty__t.iprange":                    "::1\n\x00\n::2\n",
	"v6_mixedfam__t.iprange":                     "::1-1.2.3.4\x00junk\n",
	"v6_nonhex__t.iprange":                       "zzz\x00junk\n",
	"v6_prefix_bad__t.iprange":                   "fe80::1/99\x00junk\n",
	"v6_range_nul__t.iprange":                    "::1-::5\x00junk\n",
	"v6_verbose__t.iprange":                      "::1\x00junk\n",
	"v6_ws_nul__t.iprange":                       "   \x00junk\n",
	"v_drop__t.iprange":                          ":::\x00junk\n",
	"v_incomplete__t.iprange":                    "1.2.3.4-\x00junk\n",
	"v_mapped__t.iprange":                        "::ffff:1.2.3.4\x00junk\n",
	"v_mid_empty__t.iprange":                     "1.2.3.4\n\x00\n5.6.7.8\n",
	"v_nulmask__t.iprange":                       "1.2.3.4/99\x00junk\n",
	"v_plain__t.iprange":                         "1.2.3.4\x00junk\n",
	"v_range__t.iprange":                         "1.2.3.4-5.6.7.8\x00junk\n",

	// The W15 cases: a `@file-list` entry name is a C string, so the bytes after

	// its first NUL are invisible to the open, the trim and the echoes
	// (src/iprange.c:863-882). Each of these fixtures is named exactly as the
	// truncated entry name, so the entry cut and the record cut apply in one run.
	"cmp_ok_ent.iprange":    "1.2.3.4\x00junk\n5.6.7.8-9.10.11.12\x00x\n",
	"cmp_ok_list.txt":       "cmp_ok_ent.iprange\x00junk\n",
	"cmp_ok_v_ent.iprange":  "1.2.3.4\x00junk\n5.6.7.8-9.10.11.12\x00x\n",
	"cmp_ok_v_list.txt":     "cmp_ok_v_ent.iprange\x00junk\n",
	"cmp_bad_ent.iprange":   "1.2.3.4/99\x00x\n5.6.7.8\n",
	"cmp_bad_list.txt":      "cmp_bad_ent.iprange\x00junk\n",
	"cmp_bad_v_ent.iprange": "1.2.3.4/99\x00x\n5.6.7.8\n",
	"cmp_bad_v_list.txt":    "cmp_bad_v_ent.iprange\x00junk\n",
	"cmp_sp_ent.iprange":    "1.2.3.4\x00junk\n",
	"cmp_sp_list.txt":       "cmp_sp_ent.iprange  \x00junk\n",
	"cmp_two_e.iprange":     "1.2.3.4\x00A\n",
	"cmp_two_f.iprange":     "5.6.7.8\x00B\n",
	"cmp_two_list.txt":      "cmp_two_e.iprange\x00junk\ncmp_two_f.iprange\x00junk\n",
	"cmp_mix_e.iprange":     "1.2.3.4\x00junk\n",
	"cmp_mix_list.txt":      "cmp_mix_e.iprange\x00junk\ncmp_mix_missing.iprange\x00junk\n",
	"cmp_dir/a.iprange":     "1.2.3.4\x00junk\n",
	"cmp_dir/b.iprange":     "5.6.7.8-9.10.11.12\x00x\n",
	"cmp_dirv/a.iprange":    "1.2.3.4\x00junk\n",
	"cmp_dirv/b.iprange":    "5.6.7.8\x00x\n",
	"cmp_dirv/c.iprange":    "# comment\x00junk\n",
	"cmp6_ent.iprange":      "2001:db8::1\x00junk\n",
	"cmp6_list.txt":         "cmp6_ent.iprange\x00junk\n",
	"cmp6_bad_ent.iprange":  "2001:db8::/99\x00x\n",
	"cmp6_bad_list.txt":     "cmp6_bad_ent.iprange\x00junk\n",
	"cmp_host_ent.iprange":  "0x7f000001\x00junk\n",
	"cmp_host_list.txt":     "cmp_host_ent.iprange\x00junk\n",
}

// nulCases are the contracts the C can decide without a resolver.
var nulCases = []cCase{
	{
		label:  "address bytes end at the NUL",
		argv:   []string{"p_plain_nul__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n",
		stderr: "",
	},
	{
		label:  "a space before the NUL is still the token end",
		argv:   []string{"p_plain_nul_sp__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n",
		stderr: "",
	},
	{
		label:  "a tab before the NUL is still the token end",
		argv:   []string{"p_plain_nul_tab__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n",
		stderr: "",
	},
	{
		label:  "no newline after the NUL",
		argv:   []string{"p_plain_nul_nonl__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n",
		stderr: "",
	},
	{
		label:  "the second record carries the NUL",
		argv:   []string{"p_second_nul__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n5.6.7.8\n",
		stderr: "",
	},
	{
		label:  "leading tab then address then NUL",
		argv:   []string{"p_tab_leading__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n",
		stderr: "",
	},
	{
		label:  "NUL directly before the newline",
		argv:   []string{"p_nul_before_nl__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n",
		stderr: "",
	},
	{
		label:  "a record that is only a NUL is empty",
		argv:   []string{"p_nul_only_rec__t.iprange"},
		rc:     0,
		stdout: "",
		stderr: "",
	},
	{
		label:  "a NUL as the whole last record is empty",
		argv:   []string{"p_nul_only_nonl__t.iprange"},
		rc:     0,
		stdout: "",
		stderr: "",
	},
	{
		label:  "spaces then a NUL is an empty record",
		argv:   []string{"p_ws_then_nul__t.iprange"},
		rc:     0,
		stdout: "",
		stderr: "",
	},
	{
		label:  "several NUL bytes, all invisible",
		argv:   []string{"p_nul_multi__t.iprange"},
		rc:     0,
		stdout: "",
		stderr: "",
	},
	{
		label:  "a NUL hides the address behind it",
		argv:   []string{"p_nul_first_addr__t.iprange"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Cannot understand line No 1 from p_nul_first_addr__t.iprange: \x01.2.3.4\n\niprange: Cannot load ipset: p_nul_first_addr__t.iprange\n",
	},
	{
		label:  "empty middle record keeps the other two",
		argv:   []string{"p_mid_empty__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n5.6.7.8\n",
		stderr: "",
	},
	{
		label:  "a NUL hides a second address",
		argv:   []string{"p_nul_hides_addr__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n",
		stderr: "",
	},
	{
		label:  "junk after the address stays invalid",
		argv:   []string{"p_broken_nul__t.iprange"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Cannot understand line No 1 from p_broken_nul__t.iprange: 1.2.3.4 junk\niprange: Cannot load ipset: p_broken_nul__t.iprange\n",
	},
	{
		label:  "NUL does not hide an invalid netmask",
		argv:   []string{"c_99__t.iprange"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Invalid netmask 99\niprange: Cannot understand line No 1 from c_99__t.iprange: 1.2.3.4/99\niprange: Cannot load ipset: c_99__t.iprange\n",
	},
	{
		label:  "valid prefix then NUL",
		argv:   []string{"c_24__t.iprange"},
		rc:     0,
		stdout: "1.2.3.0/24\n",
		stderr: "",
	},
	{
		label:  "prefix stops at the NUL",
		argv:   []string{"c_9sp__t.iprange"},
		rc:     0,
		stdout: "1.0.0.0/9\n",
		stderr: "",
	},
	{
		label:  "prefix then NUL then junk",
		argv:   []string{"c_8__t.iprange"},
		rc:     0,
		stdout: "10.0.0.0/8\n",
		stderr: "",
	},
	{
		label:  "non-numeric prefix stays invalid",
		argv:   []string{"c_hex__t.iprange"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Cannot understand line No 1 from c_hex__t.iprange: 1.2.3.4/0x10\niprange: Cannot load ipset: c_hex__t.iprange\n",
	},
	{
		label:  "NUL truncates the prefix digits",
		argv:   []string{"c_in_prefix__t.iprange"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Cannot understand line No 1 from c_in_prefix__t.iprange: 1.2.3.4/2\x04\n\niprange: Cannot load ipset: c_in_prefix__t.iprange\n",
	},
	{
		label:  "trailing slash reports an invalid address",
		argv:   []string{"c_slash_only__t.iprange"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Invalid address .\niprange: Cannot understand line No 1 from c_slash_only__t.iprange: 1.2.3.4/\niprange: Cannot load ipset: c_slash_only__t.iprange\n",
	},
	{
		label:  "double slash reports an invalid address",
		argv:   []string{"c_double_slash__t.iprange"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Invalid address /.\niprange: Cannot understand line No 1 from c_double_slash__t.iprange: 1.2.3.4//\niprange: Cannot load ipset: c_double_slash__t.iprange\n",
	},
	{
		label:  "comment record with a NUL is skipped",
		argv:   []string{"m_hash__t.iprange"},
		rc:     0,
		stdout: "",
		stderr: "",
	},
	{
		label:  "semicolon comment with a NUL is skipped",
		argv:   []string{"m_semi__t.iprange"},
		rc:     0,
		stdout: "",
		stderr: "",
	},
	{
		label:  "address then comment then NUL",
		argv:   []string{"m_trail_hash__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n",
		stderr: "",
	},
	{
		label:  "address then tab-comment then NUL",
		argv:   []string{"m_trail_semi_tab__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n",
		stderr: "",
	},
	{
		label:  "a NUL hides the comment marker",
		argv:   []string{"m_nul_before_hash__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n",
		stderr: "",
	},
	{
		label:  "a leading NUL makes the record empty",
		argv:   []string{"m_nul_first_hash__t.iprange"},
		rc:     0,
		stdout: "",
		stderr: "",
	},
	{
		label:  "indented comment with a NUL",
		argv:   []string{"m_hash_ws__t.iprange"},
		rc:     0,
		stdout: "",
		stderr: "",
	},
	{
		label:  "range then NUL then junk",
		argv:   []string{"r_ok_nul__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4/30\n1.2.3.8/29\n1.2.3.16/28\n1.2.3.32/27\n1.2.3.64/26\n1.2.3.128/25\n1.2.4.0/22\n1.2.8.0/21\n1.2.16.0/20\n1.2.32.0/19\n1.2.64.0/18\n1.2.128.0/17\n1.3.0.0/16\n1.4.0.0/14\n1.8.0.0/13\n1.16.0.0/12\n1.32.0.0/11\n1.64.0.0/10\n1.128.0.0/9\n2.0.0.0/7\n4.0.0.0/8\n5.0.0.0/14\n5.4.0.0/15\n5.6.0.0/22\n5.6.4.0/23\n5.6.6.0/24\n5.6.7.0/29\n5.6.7.8\n",
		stderr: "",
	},
	{
		label:  "range ending at the NUL warns and adds one IP",
		argv:   []string{"r_end_dash__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n",
		stderr: "iprange: Incomplete range on line 1, expected an ip address after -, but line ended\n",
	},
	{
		label:  "spaced dash ending at the NUL warns",
		argv:   []string{"r_sp_dash_sp__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n",
		stderr: "iprange: Incomplete range on line 1, expected an ip address after -, but line ended\n",
	},
	{
		label:  "a NUL hides junk after a range",
		argv:   []string{"r_nul_sp__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4/30\n1.2.3.8/29\n1.2.3.16/28\n1.2.3.32/27\n1.2.3.64/26\n1.2.3.128/25\n1.2.4.0/22\n1.2.8.0/21\n1.2.16.0/20\n1.2.32.0/19\n1.2.64.0/18\n1.2.128.0/17\n1.3.0.0/16\n1.4.0.0/14\n1.8.0.0/13\n1.16.0.0/12\n1.32.0.0/11\n1.64.0.0/10\n1.128.0.0/9\n2.0.0.0/7\n4.0.0.0/8\n5.0.0.0/14\n5.4.0.0/15\n5.6.0.0/22\n5.6.4.0/23\n5.6.6.0/24\n5.6.7.0/29\n5.6.7.8\n",
		stderr: "",
	},
	{
		label:  "junk before the NUL is still invalid",
		argv:   []string{"r_junk_nul__t.iprange"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Cannot understand line No 1 from r_junk_nul__t.iprange: 1.2.3.4-5.6.7.8 junk\niprange: Cannot load ipset: r_junk_nul__t.iprange\n",
	},
	{
		label:  "a NUL before the dash ends the record",
		argv:   []string{"r_nul_kills__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n",
		stderr: "",
	},
	{
		label:  "comment after the dash warns (no NUL)",
		argv:   []string{"r_dash_comment__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n",
		stderr: "iprange: Ignoring text on line 1, expected an ip address after -, but found '#junk\n'\n",
	},
	{
		label:  "NUL right after the range end",
		argv:   []string{"r_second_nul_nl__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4/30\n1.2.3.8/29\n1.2.3.16/28\n1.2.3.32/27\n1.2.3.64/26\n1.2.3.128/25\n1.2.4.0/22\n1.2.8.0/21\n1.2.16.0/20\n1.2.32.0/19\n1.2.64.0/18\n1.2.128.0/17\n1.3.0.0/16\n1.4.0.0/14\n1.8.0.0/13\n1.16.0.0/12\n1.32.0.0/11\n1.64.0.0/10\n1.128.0.0/9\n2.0.0.0/7\n4.0.0.0/8\n5.0.0.0/14\n5.4.0.0/15\n5.6.0.0/22\n5.6.4.0/23\n5.6.6.0/24\n5.6.7.0/29\n5.6.7.8\n",
		stderr: "",
	},
	{
		label:  "prefix on the range end then NUL",
		argv:   []string{"r_prefix_range__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4/30\n1.2.3.8/29\n1.2.3.16/28\n1.2.3.32/27\n1.2.3.64/26\n1.2.3.128/25\n1.2.4.0/22\n1.2.8.0/21\n1.2.16.0/20\n1.2.32.0/19\n1.2.64.0/18\n1.2.128.0/17\n1.3.0.0/16\n1.4.0.0/14\n1.8.0.0/13\n1.16.0.0/12\n1.32.0.0/11\n1.64.0.0/10\n1.128.0.0/9\n2.0.0.0/7\n4.0.0.0/8\n5.0.0.0/14\n5.4.0.0/15\n5.6.0.0/21\n",
		stderr: "",
	},
	{
		label:  "integer range end then NUL",
		argv:   []string{"r_bad_second__t.iprange"},
		rc:     0,
		stdout: "0.0.3.231\n0.0.3.232/29\n0.0.3.240/28\n0.0.4.0/22\n0.0.8.0/21\n0.0.16.0/20\n0.0.32.0/19\n0.0.64.0/18\n0.0.128.0/17\n0.1.0.0/16\n0.2.0.0/15\n0.4.0.0/14\n0.8.0.0/13\n0.16.0.0/12\n0.32.0.0/11\n0.64.0.0/10\n0.128.0.0/9\n1.0.0.0/15\n1.2.0.0/23\n1.2.2.0/24\n1.2.3.0/30\n1.2.3.4\n",
		stderr: "",
	},
	{
		label:  "integer range end with junk (no NUL)",
		argv:   []string{"r_bad_second_noNUL__t.iprange"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Cannot understand line No 1 from r_bad_second_noNUL__t.iprange: 1.2.3.4-999 junk\n\niprange: Cannot load ipset: r_bad_second_noNUL__t.iprange\n",
	},
	{
		label:  "space, NUL, then a hidden range end",
		argv:   []string{"r_nul_after_sp__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n",
		stderr: "",
	},
	{
		label:  "a NUL hides a hostname behind an address",
		argv:   []string{"h_addr_nul_host__t.iprange"},
		rc:     0,
		stdout: "10.0.0.1\n",
		stderr: "",
	},
	{
		label:  "hostname plus junk stays invalid",
		argv:   []string{"h_hostname_nul_bad__t.iprange"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Cannot understand line No 1 from h_hostname_nul_bad__t.iprange: localhost x\niprange: Cannot load ipset: h_hostname_nul_bad__t.iprange\n",
	},
	{
		label:  "colons behind a NUL are not IPv6",
		argv:   []string{"d_bang_nul_colons__t.iprange"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Cannot understand line No 1 from d_bang_nul_colons__t.iprange: !\niprange: Cannot load ipset: d_bang_nul_colons__t.iprange\n",
	},
	{
		label:  "colons before the NUL drop as IPv6",
		argv:   []string{"d_colons_nul__t.iprange"},
		rc:     0,
		stdout: "",
		stderr: "iprange: d_colons_nul__t.iprange: 1 IPv6 entries dropped (use -6 for IPv6 mode)\n",
	},
	{
		label:  "mapped IPv6 then NUL converts to IPv4",
		argv:   []string{"d_mapped_nul__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n",
		stderr: "",
	},
	{
		label:  "middle record invalid, NUL hides colons",
		argv:   []string{"d_mixed3__t.iprange"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Cannot understand line No 2 from d_mixed3__t.iprange: !\niprange: Cannot load ipset: d_mixed3__t.iprange\n",
	},
	{
		label:  "mapped IPv6 with junk before the NUL",
		argv:   []string{"d_mapped_sp__t.iprange"},
		rc:     0,
		stdout: "",
		stderr: "iprange: d_mapped_sp__t.iprange: 1 IPv6 entries dropped (use -6 for IPv6 mode)\n",
	},
	{
		label:  "a single colon before the NUL is not IPv6",
		argv:   []string{"d_one_colon__t.iprange"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Cannot understand line No 1 from d_one_colon__t.iprange: :\niprange: Cannot load ipset: d_one_colon__t.iprange\n",
	},
	{
		label:  "300 digits: token cap plus NUL",
		argv:   []string{"cap_300digits__t.iprange"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Cannot understand line No 1 from cap_300digits__t.iprange: 999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999\niprange: Cannot load ipset: cap_300digits__t.iprange\n",
	},
	{
		label:  "300 digits after a slash plus NUL",
		argv:   []string{"cap_300prefix__t.iprange"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Cannot understand line No 1 from cap_300prefix__t.iprange: 1.2.3.4/999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999\niprange: Cannot load ipset: cap_300prefix__t.iprange\n",
	},
	{
		label:  "300 letters: token cap then NUL",
		argv:   []string{"cap_300host__t.iprange"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Cannot understand line No 1 from cap_300host__t.iprange: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\niprange: Cannot load ipset: cap_300host__t.iprange\n",
	},
	{
		label:  "v6 address then NUL",
		argv:   []string{"-6", "v6_1_nul__t.iprange"},
		rc:     0,
		stdout: "::1\n",
		stderr: "",
	},
	{
		label:  "v6 prefix then NUL",
		argv:   []string{"-6", "v6_prefix_bad__t.iprange"},
		rc:     0,
		stdout: "fe80::/99\n",
		stderr: "",
	},
	{
		label:  "v6 hex word is an address, not a hostname",
		argv:   []string{"-6", "v6_hex_word__t.iprange"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Cannot parse address: abcd\niprange: Cannot understand line No 1 from v6_hex_word__t.iprange: abcd\niprange: Cannot load ipset: v6_hex_word__t.iprange\n",
	},
	{
		label:  "v6 range then NUL",
		argv:   []string{"-6", "v6_range_nul__t.iprange"},
		rc:     0,
		stdout: "::1\n::2/127\n::4/127\n",
		stderr: "",
	},
	{
		label:  "v6 NUL-only record is empty",
		argv:   []string{"-6", "v6_empty_nul__t.iprange"},
		rc:     0,
		stdout: "",
		stderr: "",
	},
	{
		label:  "v6 global address then NUL",
		argv:   []string{"-6", "v6_2001__t.iprange"},
		rc:     0,
		stdout: "2001:db8::1\n",
		stderr: "",
	},
	{
		label:  "v6 mapped address then NUL",
		argv:   []string{"-6", "v6_mapped__t.iprange"},
		rc:     0,
		stdout: "::ffff:1.2.3.4\n",
		stderr: "",
	},
	{
		label:  "v6 spaces then NUL is empty",
		argv:   []string{"-6", "v6_ws_nul__t.iprange"},
		rc:     0,
		stdout: "",
		stderr: "",
	},
	{
		label:  "v6 range ending at the NUL warns",
		argv:   []string{"-6", "v6_dash__t.iprange"},
		rc:     0,
		stdout: "::1\n",
		stderr: "iprange: Incomplete range on line, expected an address after -\n",
	},
	{
		label:  "v6 mixed-family range then NUL",
		argv:   []string{"-6", "v6_mixedfam__t.iprange"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Mixed-family range on line 1: ::1 - 1.2.3.4\niprange: Cannot load ipset: v6_mixedfam__t.iprange\n",
	},
	{
		label:  "v6 comment with a NUL is skipped",
		argv:   []string{"-6", "v6_hash__t.iprange"},
		rc:     0,
		stdout: "",
		stderr: "",
	},
	{
		label:  "v6 empty middle record",
		argv:   []string{"-6", "v6_mid_empty__t.iprange"},
		rc:     0,
		stdout: "::1\n::2\n",
		stderr: "",
	},
	{
		label:  "v6 NUL hides the next record's address",
		argv:   []string{"-6", "v6_addr_nul_then__t.iprange"},
		rc:     0,
		stdout: "::1\n",
		stderr: "",
	},
	{
		label:  "-v: the load bookkeeping of a NUL record",
		argv:   []string{"-v", "v_plain__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n",
		stderr: "iprange: Loading from v_plain__t.iprange\niprange: Loaded optimized v_plain__t.iprange\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label:  "-v: invalid netmask then the unparseable echo",
		argv:   []string{"-v", "v_nulmask__t.iprange"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Loading from v_nulmask__t.iprange\niprange: Invalid netmask 99\niprange: Cannot understand line No 1 from v_nulmask__t.iprange: 1.2.3.4/99\niprange: Cannot load ipset: v_nulmask__t.iprange\n",
	},
	{
		label:  "-v: an empty NUL record is not a line",
		argv:   []string{"-v", "v_mid_empty__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n5.6.7.8\n",
		stderr: "iprange: Loading from v_mid_empty__t.iprange\niprange: Loaded optimized v_mid_empty__t.iprange\niprange: Printing combined ipset with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label:  "-v: the IPv6 drop warning",
		argv:   []string{"-v", "v_drop__t.iprange"},
		rc:     0,
		stdout: "",
		stderr: "iprange: Loading from v_drop__t.iprange\niprange: v_drop__t.iprange: 1 IPv6 entries dropped (use -6 for IPv6 mode)\niprange: Loaded optimized v_drop__t.iprange\niprange: Printing combined ipset with 0 ranges, 0 unique IPs\n\n0 printed CIDRs, break down by prefix:\n\ntotals: 0 lines read, 0 distinct IP ranges found, 0 CIDR prefixes, 0 CIDRs printed, 0 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label:  "-v: mapped IPv6 converts",
		argv:   []string{"-v", "v_mapped__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n",
		stderr: "iprange: Loading from v_mapped__t.iprange\niprange: Loaded optimized v_mapped__t.iprange\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label:  "-v: range with a NUL",
		argv:   []string{"-v", "v_range__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4/30\n1.2.3.8/29\n1.2.3.16/28\n1.2.3.32/27\n1.2.3.64/26\n1.2.3.128/25\n1.2.4.0/22\n1.2.8.0/21\n1.2.16.0/20\n1.2.32.0/19\n1.2.64.0/18\n1.2.128.0/17\n1.3.0.0/16\n1.4.0.0/14\n1.8.0.0/13\n1.16.0.0/12\n1.32.0.0/11\n1.64.0.0/10\n1.128.0.0/9\n2.0.0.0/7\n4.0.0.0/8\n5.0.0.0/14\n5.4.0.0/15\n5.6.0.0/22\n5.6.4.0/23\n5.6.6.0/24\n5.6.7.0/29\n5.6.7.8\n",
		stderr: "iprange: Loading from v_range__t.iprange\niprange: Loaded optimized v_range__t.iprange\niprange: Printing combined ipset with 1 ranges, 67372037 unique IPs\n\n28 printed CIDRs, break down by prefix:\n\t- prefix /7 counts 1 entries\n\t- prefix /8 counts 1 entries\n\t- prefix /9 counts 1 entries\n\t- prefix /10 counts 1 entries\n\t- prefix /11 counts 1 entries\n\t- prefix /12 counts 1 entries\n\t- prefix /13 counts 1 entries\n\t- prefix /14 counts 2 entries\n\t- prefix /15 counts 1 entries\n\t- prefix /16 counts 1 entries\n\t- prefix /17 counts 1 entries\n\t- prefix /18 counts 1 entries\n\t- prefix /19 counts 1 entries\n\t- prefix /20 counts 1 entries\n\t- prefix /21 counts 1 entries\n\t- prefix /22 counts 2 entries\n\t- prefix /23 counts 1 entries\n\t- prefix /24 counts 1 entries\n\t- prefix /25 counts 1 entries\n\t- prefix /26 counts 1 entries\n\t- prefix /27 counts 1 entries\n\t- prefix /28 counts 1 entries\n\t- prefix /29 counts 2 entries\n\t- prefix /30 counts 1 entries\n\t- prefix /32 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 25 CIDR prefixes, 28 CIDRs printed, 67372037 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label:  "-v: incomplete range warning",
		argv:   []string{"-v", "v_incomplete__t.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n",
		stderr: "iprange: Loading from v_incomplete__t.iprange\niprange: Incomplete range on line 1, expected an ip address after -, but line ended\niprange: Loaded optimized v_incomplete__t.iprange\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label:  "-6 -v: no load bookkeeping lines",
		argv:   []string{"-6", "-v", "v6_verbose__t.iprange"},
		rc:     0,
		stdout: "::1\n",
		stderr: "iprange: Loading from v6_verbose__t.iprange (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /128 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n",
	},
	{
		label:  "two files, the NUL in the first one",
		argv:   []string{"a.iprange", "b.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n10.0.0.0/30\n",
		stderr: "",
	},
	{
		label:  "bytes behind a NUL are invisible (address)",
		argv:   []string{"nul_plain.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n",
		stderr: "",
	},
	{
		label:  "twin with no NUL (address)",
		argv:   []string{"same_plain.iprange"},
		rc:     0,
		stdout: "1.2.3.4\n",
		stderr: "",
	},
	{
		label:  "bytes behind a NUL are invisible (range)",
		argv:   []string{"nul_range.iprange"},
		rc:     0,
		stdout: "1.2.3.4/30\n1.2.3.8/29\n1.2.3.16/28\n1.2.3.32/27\n1.2.3.64/26\n1.2.3.128/25\n1.2.4.0/22\n1.2.8.0/21\n1.2.16.0/20\n1.2.32.0/19\n1.2.64.0/18\n1.2.128.0/17\n1.3.0.0/16\n1.4.0.0/14\n1.8.0.0/13\n1.16.0.0/12\n1.32.0.0/11\n1.64.0.0/10\n1.128.0.0/9\n2.0.0.0/7\n4.0.0.0/8\n5.0.0.0/14\n5.4.0.0/15\n5.6.0.0/22\n5.6.4.0/23\n5.6.6.0/24\n5.6.7.0/29\n5.6.7.8\n",
		stderr: "",
	},
	{
		label:  "twin with no NUL (range)",
		argv:   []string{"same_range.iprange"},
		rc:     0,
		stdout: "1.2.3.4/30\n1.2.3.8/29\n1.2.3.16/28\n1.2.3.32/27\n1.2.3.64/26\n1.2.3.128/25\n1.2.4.0/22\n1.2.8.0/21\n1.2.16.0/20\n1.2.32.0/19\n1.2.64.0/18\n1.2.128.0/17\n1.3.0.0/16\n1.4.0.0/14\n1.8.0.0/13\n1.16.0.0/12\n1.32.0.0/11\n1.64.0.0/10\n1.128.0.0/9\n2.0.0.0/7\n4.0.0.0/8\n5.0.0.0/14\n5.4.0.0/15\n5.6.0.0/22\n5.6.4.0/23\n5.6.6.0/24\n5.6.7.0/29\n5.6.7.8\n",
		stderr: "",
	},
	{
		label:  "bytes behind a NUL are invisible (empty record)",
		argv:   []string{"nul_comment.iprange"},
		rc:     0,
		stdout: "",
		stderr: "",
	},
	{
		label:  "twin with no NUL (empty record)",
		argv:   []string{"same_comment.iprange"},
		rc:     0,
		stdout: "",
		stderr: "",
	},
	{
		label:  "bytes behind a NUL are invisible (indented)",
		argv:   []string{"nul_empty.iprange"},
		rc:     0,
		stdout: "",
		stderr: "",
	},
	{
		label:  "twin with no NUL (indented)",
		argv:   []string{"same_empty.iprange"},
		rc:     0,
		stdout: "",
		stderr: "",
	},
	{
		label:  "bytes behind a NUL are invisible (prefix)",
		argv:   []string{"nul_cidr.iprange"},
		rc:     0,
		stdout: "1.2.3.0/24\n",
		stderr: "",
	},
	{
		label:  "twin with no NUL (prefix)",
		argv:   []string{"same_cidr.iprange"},
		rc:     0,
		stdout: "1.2.3.0/24\n",
		stderr: "",
	},
	{
		label:  "@list entry truncated at the NUL opens the file",
		argv:   []string{"@n1_entry_exists__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists__list.txt (line 1)\n",
	},
	{
		label:  "spaces before the NUL are trimmed away",
		argv:   []string{"@n1_entry_exists_sp__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists_sp__list.txt (line 1)\n",
	},
	{
		label:  "a tab before the NUL is trimmed away",
		argv:   []string{"@n1_entry_exists_tab__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists_tab__list.txt (line 1)\n",
	},
	{
		label:  "entry name ending in the NUL",
		argv:   []string{"@n1_entry_nul_at_end__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_nul_at_end__list.txt (line 1)\n",
	},
	{
		label:  "no newline after the NUL in the entry",
		argv:   []string{"@n1_entry_exists_nonl__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists_nonl__list.txt (line 1)\n",
	},
	{
		label:  "twin shape: spaces, no NUL",
		argv:   []string{"@n1_entry_exists_ws_only__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists_ws_only__list.txt (line 1)\n",
	},
	{
		label:  "truncated entry that does not exist names the cut",
		argv:   []string{"@n1_entry_missing__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: nope.iprange - No such file or directory\niprange: Cannot load file nope.iprange from list n1_entry_missing__list.txt (line 1)\n",
	},
	{
		label:  "missing entry, no newline after the NUL",
		argv:   []string{"@n1_entry_missing_nonl__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: nope.iprange - No such file or directory\niprange: Cannot load file nope.iprange from list n1_entry_missing_nonl__list.txt (line 1)\n",
	},
	{
		label:  "missing entry with spaces before the NUL",
		argv:   []string{"@n1_entry_missing_ws__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: nope.iprange - No such file or directory\niprange: Cannot load file nope.iprange from list n1_entry_missing_ws__list.txt (line 1)\n",
	},
	{
		label:  "entry name with a space, cut after it",
		argv:   []string{"@n1_entry_missing_sp_name__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: no pe.iprange - No such file or directory\niprange: Cannot load file no pe.iprange from list n1_entry_missing_sp_name__list.txt (line 1)\n",
	},
	{
		label:  "entry naming a directory loads as an empty set",
		argv:   []string{"@n1_entry_named_dir__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: sub - No such file or directory\niprange: Cannot load file sub from list n1_entry_named_dir__list.txt (line 1)\n",
	},
	{
		label:  "-v: the list expansion echoes the cut name",
		argv:   []string{"-v", "@n1_entry_exists_v__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Loading files from list n1_entry_exists_v__list.txt\niprange: Loading file e.iprange from list (line 1)\niprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists_v__list.txt (line 1)\n",
	},
	{
		label:  "-v: missing entry echoes the cut name",
		argv:   []string{"-v", "@n1_entry_missing_v__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Loading files from list n1_entry_missing_v__list.txt\niprange: Loading file nope.iprange from list (line 1)\niprange: nope.iprange - No such file or directory\niprange: Cannot load file nope.iprange from list n1_entry_missing_v__list.txt (line 1)\n",
	},
	{
		label:  "-v: the successful expansion of a cut name",
		argv:   []string{"-v", "@n1_entry_exists_fifo__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Loading files from list n1_entry_exists_fifo__list.txt\niprange: Loading file e.iprange from list (line 1)\niprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists_fifo__list.txt (line 1)\n",
	},
	{
		label:  "an entry that is only a NUL is an empty line",
		argv:   []string{"@n1_only_nul__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: No valid files found in file list: n1_only_nul__list.txt\n",
	},
	{
		label:  "a NUL as the whole last entry is empty",
		argv:   []string{"@n1_only_nul_nonl__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: No valid files found in file list: n1_only_nul_nonl__list.txt\n",
	},
	{
		label:  "one NUL byte as the whole file list",
		argv:   []string{"@n1_only_nul_bare__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: No valid files found in file list: n1_only_nul_bare__list.txt\n",
	},
	{
		label:  "spaces then a NUL is an empty entry",
		argv:   []string{"@n1_ws_then_nul__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: No valid files found in file list: n1_ws_then_nul__list.txt\n",
	},
	{
		label:  "a tab and a space then a NUL are empty",
		argv:   []string{"@n1_tab_then_nul__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: No valid files found in file list: n1_tab_then_nul__list.txt\n",
	},
	{
		label:  "several NUL bytes, all invisible",
		argv:   []string{"@n1_multi_nul__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: No valid files found in file list: n1_multi_nul__list.txt\n",
	},
	{
		label:  "-v: an empty entry is not a load attempt",
		argv:   []string{"-v", "@n1_only_nul_v__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Loading files from list n1_only_nul_v__list.txt\niprange: File list n1_only_nul_v__list.txt is empty or contains no valid entries\niprange: No valid files found in file list: n1_only_nul_v__list.txt\n",
	},
	{
		label:  "a NUL hides the whole entry name behind it",
		argv:   []string{"@n1_nul_hides_entry__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: No valid files found in file list: n1_nul_hides_entry__list.txt\n",
	},
	{
		label:  "an empty NUL entry keeps the other two",
		argv:   []string{"@n1_nul_line_between__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_nul_line_between__list.txt (line 1)\n",
	},
	{
		label:  "a comment entry with a NUL is skipped",
		argv:   []string{"@n1_hash_then_nul__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: No valid files found in file list: n1_hash_then_nul__list.txt\n",
	},
	{
		label:  "a semicolon entry with a NUL is skipped",
		argv:   []string{"@n1_semi_then_nul__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: No valid files found in file list: n1_semi_then_nul__list.txt\n",
	},
	{
		label:  "a comment entry does not stop the next one",
		argv:   []string{"@n1_hash_then_valid__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_hash_then_valid__list.txt (line 2)\n",
	},
	{
		label:  "two entries, both cut at their NUL",
		argv:   []string{"@n1_two_nul_entries__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_two_nul_entries__list.txt (line 1)\n",
	},
	{
		label:  "a bad second entry names the cut name",
		argv:   []string{"@n1_valid_then_bad__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_valid_then_bad__list.txt (line 1)\n",
	},
	{
		label:  "the first bad entry stops the list",
		argv:   []string{"@n1_bad_then_valid__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: nope - No such file or directory\niprange: Cannot load file nope from list n1_bad_then_valid__list.txt (line 1)\n",
	},
	{
		label:  "an empty second entry is skipped",
		argv:   []string{"@n1_second_entry_empty__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_second_entry_empty__list.txt (line 1)\n",
	},
	{
		label:  "a space then NUL line after a good entry",
		argv:   []string{"@n1_entry_then_wsline__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_then_wsline__list.txt (line 1)\n",
	},
	{
		label:  "an entry over the line cap is still cut",
		argv:   []string{"@n1_long_entry__list.txt"},
		rc:     1,
		stdout: "",
		stderr: platformPin("iprange: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx - File name too long\niprange: Cannot load file xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx from list n1_long_entry__list.txt (line 1)\n"),
	},
	{
		label:  "NUL at the line-cap boundary",
		argv:   []string{"@n1_long_entry_nul_at_cut__list.txt"},
		rc:     1,
		stdout: "",
		stderr: platformPin("iprange: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx - File name too long\niprange: Cannot load file xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx from list n1_long_entry_nul_at_cut__list.txt (line 1)\n"),
	},
	{
		label:  "entry cut and record cut compose",
		argv:   []string{"@n1_x_record_nul__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: ent.iprange - No such file or directory\niprange: Cannot load file ent.iprange from list n1_x_record_nul__list.txt (line 1)\n",
	},
	{
		label:  "-v: entry cut and record cut compose",
		argv:   []string{"-v", "@n1_x_record_nul_v__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Loading files from list n1_x_record_nul_v__list.txt\niprange: Loading file ent.iprange from list (line 1)\niprange: ent.iprange - No such file or directory\niprange: Cannot load file ent.iprange from list n1_x_record_nul_v__list.txt (line 1)\n",
	},
	{
		label:  "entry cut with an incomplete range",
		argv:   []string{"@n1_x_record_broken_range__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: ent.iprange - No such file or directory\niprange: Cannot load file ent.iprange from list n1_x_record_broken_range__list.txt (line 1)\n",
	},
	{
		label:  "entry cut with a NUL-only record inside",
		argv:   []string{"@n1_x_entry_file_nul_only__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: ent.iprange - No such file or directory\niprange: Cannot load file ent.iprange from list n1_x_entry_file_nul_only__list.txt (line 1)\n",
	},
	{
		label:  "@dir expansion with a NUL record inside",
		argv:   []string{"@sub"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Cannot access sub: No such file or directory\n",
	},
	{
		label:  "-6: entry truncated at the NUL opens the file",
		argv:   []string{"-6", "@n1_entry_exists6__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: e.iprange - No such file or directory\niprange: Cannot load file e.iprange from list n1_entry_exists6__list.txt (line 1)\n",
	},
	{
		label:  "-6: missing entry names the cut name",
		argv:   []string{"-6", "@n1_entry_missing6__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: nope.iprange - No such file or directory\niprange: Cannot load file nope.iprange from list n1_entry_missing6__list.txt (line 1)\n",
	},
	{
		label:  "-6: an entry that is only a NUL is empty",
		argv:   []string{"-6", "@n1_only_nul6__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: No valid files found in file list: n1_only_nul6__list.txt\n",
	},
	{
		label:  "-6: entry cut and record cut compose",
		argv:   []string{"-6", "@n1_x_record_nul6__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: ent.iprange - No such file or directory\niprange: Cannot load file ent.iprange from list n1_x_record_nul6__list.txt (line 1)\n",
	},
	{
		label:  "v1 flag line cut at the NUL",
		argv:   []string{"n2_v1_flag_nul__t.bin"},
		rc:     1,
		stdout: "",
		stderr: "iprange: n2_v1_flag_nul__t.bin 2nd line should be the optimized flag, but found 'optimized'.\niprange: Cannot fast load n2_v1_flag_nul__t.bin\niprange: Cannot load ipset: n2_v1_flag_nul__t.bin\n",
	},
	{
		label:  "v1 flag line: junk behind the NUL is invisible",
		argv:   []string{"n2_v1_flag_nul_junk__t.bin"},
		rc:     1,
		stdout: "",
		stderr: "iprange: n2_v1_flag_nul_junk__t.bin 2nd line should be the optimized flag, but found 'optimized'.\niprange: Cannot fast load n2_v1_flag_nul_junk__t.bin\niprange: Cannot load ipset: n2_v1_flag_nul_junk__t.bin\n",
	},
	{
		label:  "v1 flag line starting with a NUL echoes empty",
		argv:   []string{"n2_v1_flag_lead_nul__t.bin"},
		rc:     1,
		stdout: "",
		stderr: "iprange: n2_v1_flag_lead_nul__t.bin 2nd line should be the optimized flag, but found ''.\niprange: Cannot fast load n2_v1_flag_lead_nul__t.bin\niprange: Cannot load ipset: n2_v1_flag_lead_nul__t.bin\n",
	},
	{
		label:  "v1 flag line cut in the middle of the word",
		argv:   []string{"n2_v1_flag_nul_mid__t.bin"},
		rc:     1,
		stdout: "",
		stderr: "iprange: n2_v1_flag_nul_mid__t.bin 2nd line should be the optimized flag, but found 'opti'.\niprange: Cannot fast load n2_v1_flag_nul_mid__t.bin\niprange: Cannot load ipset: n2_v1_flag_nul_mid__t.bin\n",
	},
	{
		label:  "v1 non-optimized flag cut at the NUL",
		argv:   []string{"n2_v1_flag_nonopt_nul__t.bin"},
		rc:     1,
		stdout: "",
		stderr: "iprange: n2_v1_flag_nonopt_nul__t.bin 2nd line should be the optimized flag, but found 'non-optimized'.\niprange: Cannot fast load n2_v1_flag_nonopt_nul__t.bin\niprange: Cannot load ipset: n2_v1_flag_nonopt_nul__t.bin\n",
	},
	{
		label:  "-v: v1 flag line cut at the NUL",
		argv:   []string{"-v", "n2_v1_flag_nul_v__t.bin"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Loading from n2_v1_flag_nul_v__t.bin\niprange: n2_v1_flag_nul_v__t.bin 2nd line should be the optimized flag, but found 'optimized'.\niprange: Cannot fast load n2_v1_flag_nul_v__t.bin\niprange: Cannot load ipset: n2_v1_flag_nul_v__t.bin\n",
	},
	{
		label:  "v1 record size value starting with a NUL",
		argv:   []string{"n2_v1_reclen_lead_nul__t.bin"},
		rc:     1,
		stdout: "",
		stderr: "iprange: n2_v1_reclen_lead_nul__t.bin: invalid record size value ''\niprange: Cannot fast load n2_v1_reclen_lead_nul__t.bin\niprange: Cannot load ipset: n2_v1_reclen_lead_nul__t.bin\n",
	},
	{
		label:  "v1 record size key cut at the NUL",
		argv:   []string{"n2_v1_reclen_key_nul__t.bin"},
		rc:     1,
		stdout: "",
		stderr: "iprange: n2_v1_reclen_key_nul__t.bin 3rd line should be the record size, but found 'record'.\niprange: Cannot fast load n2_v1_reclen_key_nul__t.bin\niprange: Cannot load ipset: n2_v1_reclen_key_nul__t.bin\n",
	},
	{
		label:  "v1 unique ips value starting with a NUL",
		argv:   []string{"n2_v1_unique_lead_nul__t.bin"},
		rc:     1,
		stdout: "",
		stderr: "iprange: n2_v1_unique_lead_nul__t.bin: invalid unique ips value ''\niprange: Cannot fast load n2_v1_unique_lead_nul__t.bin\niprange: Cannot load ipset: n2_v1_unique_lead_nul__t.bin\n",
	},
	{
		label:  "v1: every header line carries a NUL",
		argv:   []string{"n2_v1_all_nul__t.bin"},
		rc:     1,
		stdout: "",
		stderr: "iprange: n2_v1_all_nul__t.bin 2nd line should be the optimized flag, but found 'optimized'.\niprange: Cannot fast load n2_v1_all_nul__t.bin\niprange: Cannot load ipset: n2_v1_all_nul__t.bin\n",
	},
	{
		label:  "v2 family line cut at the NUL",
		argv:   []string{"-6", "n2_v2_family_nul__t.bin"},
		rc:     1,
		stdout: "",
		stderr: "iprange: n2_v2_family_nul__t.bin expected family 'ipv6' but found 'ipv6'.\niprange: Cannot load binary v2 n2_v2_family_nul__t.bin\niprange: Cannot load ipset: n2_v2_family_nul__t.bin\n",
	},
	{
		label:  "v2 family line starting with a NUL",
		argv:   []string{"-6", "n2_v2_family_lead_nul__t.bin"},
		rc:     1,
		stdout: "",
		stderr: "iprange: n2_v2_family_lead_nul__t.bin expected family 'ipv6' but found ''.\niprange: Cannot load binary v2 n2_v2_family_lead_nul__t.bin\niprange: Cannot load ipset: n2_v2_family_lead_nul__t.bin\n",
	},
	{
		label:  "v2 optimized flag line cut at the NUL",
		argv:   []string{"-6", "n2_v2_flag_nul__t.bin"},
		rc:     1,
		stdout: "",
		stderr: "iprange: n2_v2_flag_nul__t.bin expected optimized flag but found 'optimized'.\niprange: Cannot load binary v2 n2_v2_flag_nul__t.bin\niprange: Cannot load ipset: n2_v2_flag_nul__t.bin\n",
	},
	{
		label:  "v2 record size value starting with a NUL",
		argv:   []string{"-6", "n2_v2_reclen_lead_nul__t.bin"},
		rc:     1,
		stdout: "",
		stderr: "iprange: n2_v2_reclen_lead_nul__t.bin: invalid record size value ''\niprange: Cannot load binary v2 n2_v2_reclen_lead_nul__t.bin\niprange: Cannot load ipset: n2_v2_reclen_lead_nul__t.bin\n",
	},
	{
		label:  "v2 unique ips value starting with a NUL",
		argv:   []string{"-6", "n2_v2_unique_lead_nul__t.bin"},
		rc:     1,
		stdout: "",
		stderr: "iprange: n2_v2_unique_lead_nul__t.bin: invalid unique ips value ''\niprange: Cannot load binary v2 n2_v2_unique_lead_nul__t.bin\niprange: Cannot load ipset: n2_v2_unique_lead_nul__t.bin\n",
	},
	{
		label:  "v2 record size key cut at the NUL",
		argv:   []string{"-6", "n2_v2_key_nul__t.bin"},
		rc:     1,
		stdout: "",
		stderr: "iprange: n2_v2_key_nul__t.bin expected record size but found 'record'.\niprange: Cannot load binary v2 n2_v2_key_nul__t.bin\niprange: Cannot load ipset: n2_v2_key_nul__t.bin\n",
	},
	{
		label:  "@list entry with a NUL naming a bad v1 file",
		argv:   []string{"@n2_v1_in_list__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: t.bin - No such file or directory\niprange: Cannot load file t.bin from list n2_v1_in_list__list.txt (line 1)\n",
	},
	{
		label:  "@list entry with a NUL naming a valid v1 file",
		argv:   []string{"@n2_v1_in_list_ok__list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: t.bin - No such file or directory\niprange: Cannot load file t.bin from list n2_v1_in_list_ok__list.txt (line 1)\n",
	},
	{
		label:  "a record starting with the byte 0x00",
		argv:   []string{"n3_leading_true_nul__t.iprange"},
		rc:     0,
		stdout: "",
		stderr: "",
	},
	{
		label:  "a NUL inside the prefix digits",
		argv:   []string{"n3_prefix_true_nul__t.iprange"},
		rc:     0,
		stdout: "0.0.0.0/2\n",
		stderr: "",
	},
	{
		label:  "-6: a record starting with the byte 0x00",
		argv:   []string{"-6", "n3_leading_true_nul6__t.iprange"},
		rc:     0,
		stdout: "",
		stderr: "",
	},
	{
		label:  "-6: a NUL inside the prefix digits",
		argv:   []string{"-6", "n3_prefix_true_nul6__t.iprange"},
		rc:     0,
		stdout: "2000::/12\n",
		stderr: "",
	},
}

// nulResolverCases depend on the machine resolver, so they are judged by
// comparing the engine to the reference process, one line multiset per channel.
// nulListEntryCases pin the `@file-list`/`@dir` entry cut together with the record
// cut inside the file the truncated name opened, and the open-failure family for
// a truncated name that is not there.
var nulListEntryCases = []cCase{
	{
		label:  "@list entry cut: the truncated name is opened and its records are cut",
		argv:   []string{"@cmp_ok_list.txt"},
		rc:     0,
		stdout: "1.2.3.4\n5.6.7.8/29\n5.6.7.16/28\n5.6.7.32/27\n5.6.7.64/26\n5.6.7.128/25\n5.6.8.0/21\n5.6.16.0/20\n5.6.32.0/19\n5.6.64.0/18\n5.6.128.0/17\n5.7.0.0/16\n5.8.0.0/13\n5.16.0.0/12\n5.32.0.0/11\n5.64.0.0/10\n5.128.0.0/9\n6.0.0.0/7\n8.0.0.0/8\n9.0.0.0/13\n9.8.0.0/15\n9.10.0.0/21\n9.10.8.0/23\n9.10.10.0/24\n9.10.11.0/29\n9.10.11.8/30\n9.10.11.12\n",
		stderr: "",
	},
	{
		label:  "@list entry cut under -v: every echo names the truncated file",
		argv:   []string{"-v", "@cmp_ok_v_list.txt"},
		rc:     0,
		stdout: "1.2.3.4\n5.6.7.8/29\n5.6.7.16/28\n5.6.7.32/27\n5.6.7.64/26\n5.6.7.128/25\n5.6.8.0/21\n5.6.16.0/20\n5.6.32.0/19\n5.6.64.0/18\n5.6.128.0/17\n5.7.0.0/16\n5.8.0.0/13\n5.16.0.0/12\n5.32.0.0/11\n5.64.0.0/10\n5.128.0.0/9\n6.0.0.0/7\n8.0.0.0/8\n9.0.0.0/13\n9.8.0.0/15\n9.10.0.0/21\n9.10.8.0/23\n9.10.10.0/24\n9.10.11.0/29\n9.10.11.8/30\n9.10.11.12\n",
		stderr: "iprange: Loading files from list cmp_ok_v_list.txt\niprange: Loading file cmp_ok_v_ent.iprange from list (line 1)\niprange: Loading from cmp_ok_v_ent.iprange\niprange: Loaded optimized cmp_ok_v_ent.iprange\niprange: Printing combined ipset with 2 ranges, 67372038 unique IPs\n\n27 printed CIDRs, break down by prefix:\n\t- prefix /7 counts 1 entries\n\t- prefix /8 counts 1 entries\n\t- prefix /9 counts 1 entries\n\t- prefix /10 counts 1 entries\n\t- prefix /11 counts 1 entries\n\t- prefix /12 counts 1 entries\n\t- prefix /13 counts 2 entries\n\t- prefix /15 counts 1 entries\n\t- prefix /16 counts 1 entries\n\t- prefix /17 counts 1 entries\n\t- prefix /18 counts 1 entries\n\t- prefix /19 counts 1 entries\n\t- prefix /20 counts 1 entries\n\t- prefix /21 counts 2 entries\n\t- prefix /23 counts 1 entries\n\t- prefix /24 counts 1 entries\n\t- prefix /25 counts 1 entries\n\t- prefix /26 counts 1 entries\n\t- prefix /27 counts 1 entries\n\t- prefix /28 counts 1 entries\n\t- prefix /29 counts 2 entries\n\t- prefix /30 counts 1 entries\n\t- prefix /32 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 23 CIDR prefixes, 27 CIDRs printed, 67372038 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label:  "@list entry cut: an invalid record inside the truncated name still fails",
		argv:   []string{"@cmp_bad_list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Invalid netmask 99\niprange: Cannot understand line No 1 from cmp_bad_ent.iprange: 1.2.3.4/99\niprange: Cannot load file cmp_bad_ent.iprange from list cmp_bad_list.txt (line 1)\n",
	},
	{
		label:  "@list entry cut under -v with an invalid record inside",
		argv:   []string{"-v", "@cmp_bad_v_list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: Loading files from list cmp_bad_v_list.txt\niprange: Loading file cmp_bad_v_ent.iprange from list (line 1)\niprange: Loading from cmp_bad_v_ent.iprange\niprange: Invalid netmask 99\niprange: Cannot understand line No 1 from cmp_bad_v_ent.iprange: 1.2.3.4/99\niprange: Cannot load file cmp_bad_v_ent.iprange from list cmp_bad_v_list.txt (line 1)\n",
	},
	{
		label:  "@list entry cut: blanks before the NUL are trimmed and the name opens",
		argv:   []string{"@cmp_sp_list.txt"},
		rc:     0,
		stdout: "1.2.3.4\n",
		stderr: "",
	},
	{
		label:  "@list entry cut: two entries, both cut, both opened",
		argv:   []string{"@cmp_two_list.txt"},
		rc:     0,
		stdout: "1.2.3.4\n5.6.7.8\n",
		stderr: "",
	},
	{
		label:  "@list entry cut: the first name opens, the second truncated name is missing",
		argv:   []string{"@cmp_mix_list.txt"},
		rc:     1,
		stdout: "",
		stderr: "iprange: cmp_mix_missing.iprange - No such file or directory\niprange: Cannot load file cmp_mix_missing.iprange from list cmp_mix_list.txt (line 2)\n",
	},
	{
		label:  "@dir expansion with NUL records inside the directory",
		argv:   []string{"@cmp_dir"},
		rc:     0,
		stdout: "1.2.3.4\n5.6.7.8/29\n5.6.7.16/28\n5.6.7.32/27\n5.6.7.64/26\n5.6.7.128/25\n5.6.8.0/21\n5.6.16.0/20\n5.6.32.0/19\n5.6.64.0/18\n5.6.128.0/17\n5.7.0.0/16\n5.8.0.0/13\n5.16.0.0/12\n5.32.0.0/11\n5.64.0.0/10\n5.128.0.0/9\n6.0.0.0/7\n8.0.0.0/8\n9.0.0.0/13\n9.8.0.0/15\n9.10.0.0/21\n9.10.8.0/23\n9.10.10.0/24\n9.10.11.0/29\n9.10.11.8/30\n9.10.11.12\n",
		stderr: "",
	},
	{
		label:  "@dir expansion under -v with NUL records inside",
		argv:   []string{"-v", "@cmp_dirv"},
		rc:     0,
		stdout: "1.2.3.4\n5.6.7.8\n",
		stderr: platformPin("iprange: Loading files from directory cmp_dirv\niprange: Loading file cmp_dirv/a.iprange from directory cmp_dirv\niprange: Loading from cmp_dirv/a.iprange\niprange: Loaded optimized cmp_dirv/a.iprange\niprange: Loading file cmp_dirv/b.iprange from directory cmp_dirv\niprange: Loading from cmp_dirv/b.iprange\niprange: Loaded optimized cmp_dirv/b.iprange\niprange: Loading file cmp_dirv/c.iprange from directory cmp_dirv\niprange: Loading from cmp_dirv/c.iprange\niprange: Loaded optimized cmp_dirv/c.iprange\niprange: Merging cmp_dirv/b.iprange to combined ipset\niprange: Merging cmp_dirv/c.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n"),
	},
	{
		label:  "-6: @list entry cut opens the truncated name and cuts its records",
		argv:   []string{"-6", "@cmp6_list.txt"},
		rc:     0,
		stdout: "2001:db8::1\n",
		stderr: "",
	},
	{
		label:  "-6: @list entry cut with an invalid record inside",
		argv:   []string{"-6", "@cmp6_bad_list.txt"},
		rc:     0,
		stdout: "2001:db8::/99\n",
		stderr: "",
	},
}

var nulResolverCases = [][]string{
	{"h_localhost_nul__t.iprange"},
	{"h_localhost_nul_nl__t.iprange"},
	{"h_localhost_double__t.iprange"},
	{"h_localhost_comment__t.iprange"},
	{"h_localhost_twice__t.iprange"},
	{"h_num_letter__t.iprange"},
	{"h_underscore__t.iprange"},
	{"-6", "v6_nonhex__t.iprange"},
	{"-6", "v6_localhost_nul__t.iprange"},
	{"@n1_x_record_unparsable__list.txt"},
	{"@n1_x_record_hostname_cut__list.txt"},
	{"-6", "@n1_x_record_unparsable6__list.txt"},
	{"-v", "@cmp_host_list.txt"},
}

// nulTwins are record pairs that differ only in the bytes hidden behind a
// NUL, so the classification rule says they must be judged identically.
var nulTwins = [][2]string{
	{"nul_plain.iprange", "same_plain.iprange"},
	{"nul_range.iprange", "same_range.iprange"},
	{"nul_comment.iprange", "same_comment.iprange"},
	{"nul_empty.iprange", "same_empty.iprange"},
	{"nul_cidr.iprange", "same_cidr.iprange"},
}

// writeNulFixtures recreates the measured fixture directory byte for byte.
func writeNulFixtures(t *testing.T, dir string) {
	t.Helper()
	for name, payload := range nulFixtures {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

func TestNulTextRecordsMatchC(t *testing.T) {
	dir := t.TempDir()
	writeNulFixtures(t, dir)
	for _, c := range nulCases {
		c := c
		t.Run(c.label, func(t *testing.T) {
			assertCParity(t, dir, c)
		})
	}
}

func TestNulListEntryCutsMatchC(t *testing.T) {
	dir := t.TempDir()
	writeNulFixtures(t, dir)
	for _, c := range nulListEntryCases {
		c := c
		t.Run(c.label, func(t *testing.T) {
			assertCParity(t, dir, c)
		})
	}
}

// lineMultiset is a stream's lines sorted: the C writes its per-reply DNS
// lines from resolver threads, so the order within a run is not contract.
func lineMultiset(s string) []string {
	lines := strings.Split(s, "\n")
	sort.Strings(lines)
	return lines
}

func sameLineMultiset(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestNulTextRecordsResolvedByC judges the cases whose outcome the machine
// resolver decides: the engine must agree with the reference on the exit code
// and on the multiset of stdout and masked-stderr lines. Pinning those bytes
// would test this host's resolver instead of the classification rule.
func TestNulTextRecordsResolvedByC(t *testing.T) {
	if _, err := os.Stat(cReference); err != nil {
		t.Skipf("C-ORACLE-UNAVAILABLE at %s: every case in this table is a comparison against the "+
			"released tool rather than a pinned expectation, so with no native build of C for this "+
			"platform there is nothing to compare and no engine assertion is being skipped "+
			"(%v); the engine's own pinned expectations in TestNulTextRecordsMatchC and "+
			"TestNulListEntryCutsMatchC ran here regardless", cReference, err)
	}
	dir := t.TempDir()
	writeNulFixtures(t, dir)
	for _, argv := range nulResolverCases {
		argv := argv
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			erc, eout, eerr := runLegacyChild(t, dir, argv)
			crc, cout, cerrs := runCChild(t, dir, argv)
			if erc != crc {
				t.Errorf("%v: rc = %d, reference %d (engine stderr %q)", argv, erc, crc, eerr)
			}
			if !sameLineMultiset(lineMultiset(eout), lineMultiset(cout)) {
				t.Errorf("%v: stdout lines %q, reference %q", argv, eout, cout)
			}
			em, cm := maskWallclock(eerr), maskWallclock(cerrs)
			if !sameLineMultiset(lineMultiset(em), lineMultiset(cm)) {
				t.Errorf("%v: stderr lines %q, reference %q", argv, em, cm)
			}
		})
	}
}

// TestNulTruncationMatchesItsTwin proves the rule at the smallest scale: each
// pair differs only in the bytes hidden behind a NUL, so both must be judged
// the same way. The pairs are also pinned against the C, so this checks the
// rule and not only the reference.
func TestNulTruncationMatchesItsTwin(t *testing.T) {
	dir := t.TempDir()
	writeNulFixtures(t, dir)
	for _, pair := range nulTwins {
		erc, eout, eerr := runLegacyChild(t, dir, []string{pair[0]})
		vrc, vout, verr := runLegacyChild(t, dir, []string{pair[1]})
		if erc != vrc {
			t.Errorf("%s: rc %d, its NUL-free twin %s gave %d", pair[0], erc, pair[1], vrc)
		}
		if eout != vout {
			t.Errorf("%s: stdout %q, its NUL-free twin %s gave %q", pair[0], eout, pair[1], vout)
		}
		em, vm := maskWallclock(eerr), maskWallclock(verr)
		if !sameLineMultiset(lineMultiset(em), lineMultiset(vm)) {
			t.Errorf("%s: stderr %q, its NUL-free twin %s gave %q", pair[0], em, pair[1], vm)
		}
	}
}
