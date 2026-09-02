// Legacy set operations: merge, common, exclude, diff, compare,
// count, and reduce orchestration (exact C mode dispatch).
//
// Port of `src/iprange.c` mode execution and its IPv6 twin
// `src/iprange6_main.c` (the released CLI runs both twins; the union
// is implemented once here over the family-carrying IpSet). The
// interval walks below are the exact ports of `src/ipset_common.c`,
// `src/ipset6_common.c`, `src/ipset_exclude.c`,
// `src/ipset6_exclude.c`, `src/ipset_diff.c`, `src/ipset6_diff.c`,
// `src/ipset_combine.c`, `src/ipset6_combine.c`,
// `src/ipset_merge.c`, `src/ipset6_merge.c`, and
// `src/ipset_reduce.c`.

package legacy

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// LoadedSet (name + set) and Loaded (all sets + the group-B
// boundary) are defined by the parse worker (parse.go); ops.go only
// consumes them through execute.

// familySuffix is the C debug suffix for the IPv6 twins.
func familySuffix(fam Family) string {
	if fam == V6 {
		return " (IPv6)"
	}
	return ""
}

// cloneSet deep-copies one set (the Rust port's IpSet::clone): the
// shared Go sets are pointers, and the C merge folds mutate their
// first operand, which the port keeps read-only.
func cloneSet(s *IpSet) *IpSet {
	c := *s
	c.Ranges = make([]Range, len(s.Ranges))
	copy(c.Ranges, s.Ranges)
	return &c
}

// optimizeDirect is the direct ipset_optimize call (C
// ipset_optimize_all, ipset_unique_ips, ipset_print, ipset_reduce,
// and the compare modes' ipset_optimize(comips)): the IPv4 twin
// prints "Is already optimized" when the flag is already set; the
// IPv6 twin stays silent in that case.
func optimizeDirect(o *Options, set *IpSet, name string) {
	if set.Optimized {
		if o.Debug && o.Family == V4 {
			fmt.Fprintf(os.Stderr, "iprange: Is already optimized %s\n", name)
		}
		return
	}
	if o.Debug {
		fmt.Fprintf(os.Stderr, "iprange: Optimizing %s%s\n", name, familySuffix(o.Family))
	}
	set.Optimize()
}

// optimizeOperand is the operand optimization inside the
// common/exclude/diff walks (C ipset_common/ipset_exclude/
// ipset_diff only call ipset_optimize when the flag is clear, so an
// already-optimized operand produces no line at all).
func optimizeOperand(o *Options, set *IpSet, name string) {
	if !set.Optimized {
		if o.Debug {
			fmt.Fprintf(os.Stderr, "iprange: Optimizing %s%s\n", name, familySuffix(o.Family))
		}
		set.Optimize()
	}
}

// emit runs one stdout-producing body through a 64 KiB buffer and
// flushes once; any failure maps to the C-style stderr message and
// exit code 1. SIGPIPE terminates the process first, exactly like
// the C binary (the Go runtime re-raises SIGPIPE for writes to
// fds 1/2, so no action is needed here).
func emit(body func(w *bufio.Writer) error) int {
	w := bufio.NewWriterSize(os.Stdout, 64*1024)
	var err error
	if err = body(w); err == nil {
		err = w.Flush()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "iprange: %v\n", err)
		return 1
	}
	return 0
}

// printViaSet prints one set through the shared PrintSet seam (the
// print worker owns the C ipset_print shape and writes to stdout)
// with the emit-style error mapping of the Rust port.
func printViaSet(o *Options, set *IpSet, name string) int {
	if err := PrintSet(o, set, name); err != nil {
		fmt.Fprintf(os.Stderr, "iprange: %v\n", err)
		return 1
	}
	return 0
}

// csvField is the C iprange_csv_write_field (src/iprange.h): quote
// only when the field contains ',', '"', '\n' or '\r'; double
// embedded quotes.
func csvField(w *bufio.Writer, field string) error {
	if !strings.ContainsAny(field, ",\"\n\r") {
		_, err := w.WriteString(field)
		return err
	}
	if _, err := w.WriteString("\""); err != nil {
		return err
	}
	for i := 0; i < len(field); i++ {
		if field[i] == '"' {
			if _, err := w.WriteString("\"\""); err != nil {
				return err
			}
		} else if err := w.WriteByte(field[i]); err != nil {
			return err
		}
	}
	_, err := w.WriteString("\"")
	return err
}

// writeUint writes decimal digits without a leading separator (C
// iprange_write_uintmax) for a 64-bit value.
func writeUint(w *bufio.Writer, v uint64) error {
	var buf [20]byte
	n := len(buf)
	for {
		n--
		buf[n] = '0' + byte(v%10)
		v /= 10
		if v == 0 {
			break
		}
	}
	_, err := w.Write(buf[n:])
	return err
}

// divmod10 divides a 128-bit value by 10, returning the quotient and
// remainder. Uses 2^64 == 6 (mod 10) so the low limb divides without
// a 65-bit accumulator: with q1 = Lo/10, r1 = Lo%10, r = Hi%10,
// (r*2^64 + Lo)/10 = q1 + r*floor(2^64/10) + (r*6 + r1)/10; every
// partial sum stays below 2^64 because the total is exactly
// floor((r*2^64 + Lo)/10) <= 2^64-1.
func divmod10(v IP128) (IP128, uint64) {
	hiQ := v.Hi / 10
	r := v.Hi % 10
	q1 := v.Lo / 10
	r1 := v.Lo % 10
	const two64div10 = 1844674407370955161 // floor(2^64 / 10)
	loQ := q1 + r*two64div10 + (r*6+r1)/10
	return IP128{Hi: hiQ, Lo: loQ}, (r*6 + r1) % 10
}

// writeUint128 writes the decimal digits of a 128-bit value (C
// iprange_write_uintmax on the uint128 counters of the IPv6 twin).
func writeUint128(w *bufio.Writer, v IP128) error {
	var buf [40]byte
	n := len(buf)
	var d uint64
	for {
		n--
		// Plain assignment (not :=): the parameter keeps the
		// quotient so the loop terminates.
		v, d = divmod10(v)
		buf[n] = '0' + byte(d)
		if v.Hi == 0 && v.Lo == 0 {
			break
		}
	}
	_, err := w.Write(buf[n:])
	return err
}

// writeCompareRow is the
// `name1,name2,entries1,entries2,ips1,ips2,combined_ips,common_ips`
// row (C iprange_csv_write_compare_row, src/iprange.c:52).
func writeCompareRow(w *bufio.Writer, name1, name2 string, entries1, entries2 int, unique1, unique2, combinedIPs, commonIPs IP128) error {
	if err := csvField(w, name1); err != nil {
		return err
	}
	if err := w.WriteByte(','); err != nil {
		return err
	}
	if err := csvField(w, name2); err != nil {
		return err
	}
	if err := w.WriteByte(','); err != nil {
		return err
	}
	if err := writeUint(w, uint64(entries1)); err != nil {
		return err
	}
	if err := w.WriteByte(','); err != nil {
		return err
	}
	if err := writeUint(w, uint64(entries2)); err != nil {
		return err
	}
	if err := w.WriteByte(','); err != nil {
		return err
	}
	if err := writeUint128(w, unique1); err != nil {
		return err
	}
	if err := w.WriteByte(','); err != nil {
		return err
	}
	if err := writeUint128(w, unique2); err != nil {
		return err
	}
	if err := w.WriteByte(','); err != nil {
		return err
	}
	if err := writeUint128(w, combinedIPs); err != nil {
		return err
	}
	if err := w.WriteByte(','); err != nil {
		return err
	}
	if err := writeUint128(w, commonIPs); err != nil {
		return err
	}
	_, err := w.WriteString("\n")
	return err
}

// writeCountRow is the `name,entries,unique_ips,common_ips` row (C
// iprange_csv_write_count_row, src/iprange.c:64).
func writeCountRow(w *bufio.Writer, name string, entries int, uniqueIPs, commonIPs IP128) error {
	if err := csvField(w, name); err != nil {
		return err
	}
	if err := w.WriteByte(','); err != nil {
		return err
	}
	if err := writeUint(w, uint64(entries)); err != nil {
		return err
	}
	if err := w.WriteByte(','); err != nil {
		return err
	}
	if err := writeUint128(w, uniqueIPs); err != nil {
		return err
	}
	if err := w.WriteByte(','); err != nil {
		return err
	}
	if err := writeUint128(w, commonIPs); err != nil {
		return err
	}
	_, err := w.WriteString("\n")
	return err
}

// writeUniqueRow is the `name,entries,unique_ips` row (C
// iprange_csv_write_unique_row, src/iprange.c:76).
func writeUniqueRow(w *bufio.Writer, name string, entries int, uniqueIPs IP128) error {
	if err := csvField(w, name); err != nil {
		return err
	}
	if err := w.WriteByte(','); err != nil {
		return err
	}
	if err := writeUint(w, uint64(entries)); err != nil {
		return err
	}
	if err := w.WriteByte(','); err != nil {
		return err
	}
	if err := writeUint128(w, uniqueIPs); err != nil {
		return err
	}
	_, err := w.WriteString("\n")
	return err
}

// u128Add adds two 128-bit values with wraparound (the Rust u128
// release semantics used by the compare-row common-IP math).
func u128Add(a, b IP128) IP128 {
	lo := a.Lo + b.Lo
	hi := a.Hi + b.Hi
	if lo < a.Lo {
		hi++
	}
	return IP128{Hi: hi, Lo: lo}
}

// mergeGroup is the C ipset_merge fold (src/ipset_merge.c,
// src/ipset6_merge.c): the first set receives every other set by
// concatenation; the result is left unoptimized exactly like the C.
// target names the merge destination for the debug text (the C
// ipset_set_filename(root, "combined ipset") rename used by the
// combine/reduce/count-unique branches, or the first set's name when
// no rename happened).
func mergeGroup(o *Options, a []LoadedSet, target string) *IpSet {
	merged := cloneSet(a[0].Set)
	for _, set := range a[1:] {
		if o.Debug {
			fmt.Fprintf(os.Stderr, "iprange: Merging %s to %s%s\n", set.Name, target, familySuffix(o.Family))
		}
		merged.MergeFrom(set.Set)
	}
	return merged
}

// intersectOp is the C ipset_common walk (src/ipset_common.c,
// src/ipset6_common.c): one sorted sweep over both optimized inputs;
// overlapping pieces are appended in ascending order. The result is
// optimized by construction. Empty input produces an empty result
// with summed line counts.
func intersectOp(a, b *IpSet) *IpSet {
	// The C walkers create the result with no OPTIMIZED flag (plain
	// appends, no adjacency merging) and set the flag at the end.
	out := &IpSet{Fam: a.Fam, Optimized: false}
	n1, n2 := len(a.Ranges), len(b.Ranges)
	if n1 == 0 || n2 == 0 {
		out.Lines = a.Lines + b.Lines
		out.Optimized = true
		return out
	}
	i1, i2 := 0, 0
	lo1, hi1 := a.Ranges[0].Lo, a.Ranges[0].Hi
	lo2, hi2 := b.Ranges[0].Lo, b.Ranges[0].Hi
	for i1 < n1 && i2 < n2 {
		if lo1.Compare(hi2) > 0 {
			i2++
			if i2 < n2 {
				lo2, hi2 = b.Ranges[i2].Lo, b.Ranges[i2].Hi
			}
			continue
		}
		if lo2.Compare(hi1) > 0 {
			i1++
			if i1 < n1 {
				lo1, hi1 = a.Ranges[i1].Lo, a.Ranges[i1].Hi
			}
			continue
		}
		var lo, hi IP128
		if lo1.Compare(lo2) > 0 {
			lo = lo1
		} else {
			lo = lo2
		}
		if hi1.Compare(hi2) < 0 {
			hi = hi1
			i1++
			if i1 < n1 {
				lo1, hi1 = a.Ranges[i1].Lo, a.Ranges[i1].Hi
			}
		} else {
			hi = hi2
			i2++
			if i2 < n2 {
				lo2, hi2 = b.Ranges[i2].Lo, b.Ranges[i2].Hi
			}
		}
		out.AddRange(Range{Lo: lo, Hi: hi})
	}
	out.Lines = a.Lines + b.Lines
	out.Optimized = true
	return out
}

// subtractOp is the C ipset_exclude walk (src/ipset_exclude.c,
// src/ipset6_exclude.c): a minus b over two optimized inputs. The
// result is optimized by construction; an empty b copies a, an empty
// a yields an empty result (the C also tracks the summed line
// counts, used only by the -v totals of the printer).
func subtractOp(a, b *IpSet) *IpSet {
	// C ipset_exclude result: created unoptimized, flagged at the end.
	out := &IpSet{Fam: a.Fam, Optimized: false}
	n1, n2 := len(a.Ranges), len(b.Ranges)
	if n1 == 0 {
		out.Lines = a.Lines + b.Lines
		out.Optimized = true
		return out
	}
	if n2 == 0 {
		for _, r := range a.Ranges {
			out.AddRange(r)
		}
		out.Lines = a.Lines + b.Lines
		out.Optimized = true
		return out
	}
	i1, i2 := 0, 0
	lo1, hi1 := a.Ranges[0].Lo, a.Ranges[0].Hi
	lo2, hi2 := b.Ranges[0].Lo, b.Ranges[0].Hi
	for i1 < n1 && i2 < n2 {
		if lo1.Compare(hi2) > 0 {
			i2++
			if i2 < n2 {
				lo2, hi2 = b.Ranges[i2].Lo, b.Ranges[i2].Hi
			}
			continue
		}
		if lo2.Compare(hi1) > 0 {
			out.AddRange(Range{Lo: lo1, Hi: hi1})
			i1++
			if i1 < n1 {
				lo1, hi1 = a.Ranges[i1].Lo, a.Ranges[i1].Hi
			}
			continue
		}
		if lo1.Compare(lo2) < 0 {
			// lo1 < lo2 implies lo2 > 0, so Dec is safe.
			hi, _ := lo2.Dec()
			out.AddRange(Range{Lo: lo1, Hi: hi})
			lo1 = lo2
		}
		if hi1 == hi2 {
			i1++
			if i1 < n1 {
				lo1, hi1 = a.Ranges[i1].Lo, a.Ranges[i1].Hi
			}
			i2++
			if i2 < n2 {
				lo2, hi2 = b.Ranges[i2].Lo, b.Ranges[i2].Hi
			}
		} else if hi1.Compare(hi2) < 0 {
			i1++
			if i1 < n1 {
				lo1, hi1 = a.Ranges[i1].Lo, a.Ranges[i1].Hi
			}
		} else {
			// hi1 > hi2, so hi2 is below the family maximum (the
			// IPv6 twin guards hi2 == MAX separately; Inc is the
			// same guard and leaves lo1 untouched there).
			if next, ok := hi2.Inc(a.Fam); ok {
				lo1 = next
			} else {
				i1++
				if i1 < n1 {
					lo1, hi1 = a.Ranges[i1].Lo, a.Ranges[i1].Hi
				}
			}
			i2++
			if i2 < n2 {
				lo2, hi2 = b.Ranges[i2].Lo, b.Ranges[i2].Hi
			}
		}
	}
	if i1 < n1 {
		out.AddRange(Range{Lo: lo1, Hi: hi1})
		i1++
		for i1 < n1 {
			out.AddRange(a.Ranges[i1])
			i1++
		}
	}
	out.Lines = a.Lines + b.Lines
	out.Optimized = true
	return out
}

// diffOp is the C ipset_diff walk (src/ipset_diff.c,
// src/ipset6_diff.c): the symmetric difference of two optimized
// inputs, appended in ascending order. The result is optimized by
// construction. The numeric Dec/Inc guards match the IPv6 twin's
// overflow handling for the family maximum.
func diffOp(a, b *IpSet) *IpSet {
	// C ipset_diff result: created unoptimized, flagged at the end
	// (adjacent diff pieces stay separate entries, exactly like C).
	out := &IpSet{Fam: a.Fam, Optimized: false}
	n1, n2 := len(a.Ranges), len(b.Ranges)
	if n1 == 0 && n2 == 0 {
		out.Lines = a.Lines + b.Lines
		out.Optimized = true
		return out
	}
	if n1 == 0 {
		for _, r := range b.Ranges {
			out.AddRange(r)
		}
		out.Lines = a.Lines + b.Lines
		out.Optimized = true
		return out
	}
	if n2 == 0 {
		for _, r := range a.Ranges {
			out.AddRange(r)
		}
		out.Lines = a.Lines + b.Lines
		out.Optimized = true
		return out
	}
	i1, i2 := 0, 0
	lo1, hi1 := a.Ranges[0].Lo, a.Ranges[0].Hi
	lo2, hi2 := b.Ranges[0].Lo, b.Ranges[0].Hi
	for i1 < n1 && i2 < n2 {
		if lo1.Compare(hi2) > 0 {
			out.AddRange(Range{Lo: lo2, Hi: hi2})
			i2++
			if i2 < n2 {
				lo2, hi2 = b.Ranges[i2].Lo, b.Ranges[i2].Hi
			}
			continue
		}
		if lo2.Compare(hi1) > 0 {
			out.AddRange(Range{Lo: lo1, Hi: hi1})
			i1++
			if i1 < n1 {
				lo1, hi1 = a.Ranges[i1].Lo, a.Ranges[i1].Hi
			}
			continue
		}
		if lo1.Compare(lo2) > 0 {
			// lo1 > lo2 >= 0 implies lo1 >= 1, so Dec is safe.
			hi, _ := lo1.Dec()
			out.AddRange(Range{Lo: lo2, Hi: hi})
		} else if lo2.Compare(lo1) > 0 {
			hi, _ := lo2.Dec()
			out.AddRange(Range{Lo: lo1, Hi: hi})
		}
		if hi1.Compare(hi2) > 0 {
			// hi1 > hi2 implies hi2 < MAX, so Inc is safe.
			lo1, _ = hi2.Inc(a.Fam)
			i2++
			if i2 < n2 {
				lo2, hi2 = b.Ranges[i2].Lo, b.Ranges[i2].Hi
			}
			continue
		}
		if hi2.Compare(hi1) > 0 {
			lo2, _ = hi1.Inc(a.Fam)
			i1++
			if i1 < n1 {
				lo1, hi1 = a.Ranges[i1].Lo, a.Ranges[i1].Hi
			}
			continue
		}
		i1++
		if i1 < n1 {
			lo1, hi1 = a.Ranges[i1].Lo, a.Ranges[i1].Hi
		}
		i2++
		if i2 < n2 {
			lo2, hi2 = b.Ranges[i2].Lo, b.Ranges[i2].Hi
		}
	}
	for i1 < n1 {
		out.AddRange(Range{Lo: lo1, Hi: hi1})
		i1++
		if i1 < n1 {
			lo1, hi1 = a.Ranges[i1].Lo, a.Ranges[i1].Hi
		}
	}
	for i2 < n2 {
		out.AddRange(Range{Lo: lo2, Hi: hi2})
		i2++
		if i2 < n2 {
			lo2, hi2 = b.Ranges[i2].Lo, b.Ranges[i2].Hi
		}
	}
	out.Lines = a.Lines + b.Lines
	out.Optimized = true
	return out
}

// combineOp is the C ipset_combine (src/ipset_combine.c,
// src/ipset6_combine.c): concatenation of both inputs, never
// optimized; the compare modes call ipset_optimize(comips) right
// after.
func combineOp(a, b *IpSet) *IpSet {
	combined := NewIpSet(a.Fam)
	combined.MergeFrom(a)
	combined.MergeFrom(b)
	return combined
}

// cidrBroadcast is the broadcast address of a network-aligned
// addr/prefix (C broadcast()/broadcast6(): addr | (MAX >> prefix)).
func cidrBroadcast(addr IP128, fam Family, prefix uint32) IP128 {
	if fam == V4 {
		if prefix >= 32 {
			return addr
		}
		return IP128{Lo: addr.Lo | (0xFFFF_FFFF >> prefix)}
	}
	switch {
	case prefix >= 128:
		return addr
	case prefix == 0:
		return ipMax6
	case prefix >= 64:
		return IP128{Lo: 0xFFFF_FFFF_FFFF_FFFF >> (prefix - 64)}
	default:
		return IP128{Hi: 0xFFFF_FFFF_FFFF_FFFF >> prefix, Lo: 0xFFFF_FFFF_FFFF_FFFF}
	}
}

// cidrSetBit sets the (BITS - prefix)-th bit (C
// set_bit()/set_bit6(): addr | (1 << (BITS - prefix))). The caller
// only uses prefixes 1..=BITS, so the shift is never a full-width
// overflow.
func cidrSetBit(addr IP128, fam Family, prefix uint32) IP128 {
	shift := uint32(32)
	if fam == V6 {
		shift = 128
	}
	shift -= prefix
	switch {
	case shift >= 64:
		return IP128{Hi: addr.Hi | (1 << (shift - 64)), Lo: addr.Lo}
	case shift >= 0:
		return IP128{Hi: addr.Hi, Lo: addr.Lo | (1 << shift)}
	}
	return addr
}

// countSplit is the C split_range with a counting callback (the
// exact port used by ipset_reduce): recursively decompose [lo, hi]
// inside the CIDR cell addr/prefix; each emitted CIDR increments its
// prefix counter. Disabled prefixes split further instead of
// emitting, exactly like the C.
func countSplit(addr IP128, prefix uint32, lo, hi IP128, fam Family, enabled []bool, counters []uint64) {
	bc := cidrBroadcast(addr, fam, prefix)
	if lo == addr && hi == bc && (prefix >= bitsOf(fam) || enabled[prefix]) {
		counters[prefix]++
		return
	}
	p := prefix + 1
	lower := addr
	upper := cidrSetBit(addr, fam, p)
	if hi.Compare(upper) < 0 {
		countSplit(lower, p, lo, hi, fam, enabled, counters)
	} else if lo.Compare(upper) >= 0 {
		countSplit(upper, p, lo, hi, fam, enabled, counters)
	} else {
		countSplit(lower, p, lo, cidrBroadcast(lower, fam, p), fam, enabled, counters)
		countSplit(upper, p, upper, hi, fam, enabled, counters)
	}
}

func bitsOf(fam Family) uint32 {
	if fam == V6 {
		return 128
	}
	return 32
}

// applyReduce is the C ipset_reduce (src/ipset_reduce.c): disable
// prefixes so that the later CIDR print produces fewer subnets while
// allowing the configured percentage increase (or the minimum
// accepted entries) in total CIDR count. It mutates enabled in
// place; the set itself is left unchanged (only optimized). IPv4
// only in the C; the IPv6 twin rejects reduce before this point.
func applyReduce(o *Options, set *IpSet, enabled []bool) {
	maxPrefix := len(enabled) - 1

	// C ipset_reduce() optimizes through the guarded
	// if(!(flags & OPTIMIZED)) form, which is silent when the set is
	// already optimized (unlike the direct ipset_optimize calls,
	// which print "Is already optimized" on the IPv4 side).
	optimizeOperand(o, set, "combined ipset")

	if o.Debug {
		fmt.Fprintln(os.Stderr, "\nCounting prefixes in combined ipset")
	}

	counters := make([]uint64, len(enabled))
	for _, r := range set.Ranges {
		countSplit(IP128{}, 0, r.Lo, r.Hi, set.Fam, enabled, counters)
	}

	if o.Debug {
		fmt.Fprintln(os.Stderr, "Break down by prefix:")
	}

	var total, initial uint64
	for i := 0; i <= maxPrefix; i++ {
		if counters[i] > 0 {
			if o.Debug {
				fmt.Fprintf(os.Stderr, "\t- prefix /%d counts %d entries\n", i, counters[i])
			}
			total += counters[i]
			initial++
		} else {
			// The C also disables prefixes that produced no entries.
			enabled[i] = false
		}
	}
	if o.Debug {
		fmt.Fprintf(os.Stderr, "Total %d entries generated\n", total)
	}

	acceptable := total * o.ReduceFactor / 100
	if acceptable < o.ReduceEntries {
		acceptable = o.ReduceEntries
	}
	if o.Debug {
		fmt.Fprintf(os.Stderr, "Acceptable is to reach %d entries by reducing prefixes\n", acceptable)
	}

	var eliminated uint64
	for total < acceptable {
		min, to := -1, -1
		minIncrease := acceptable * 10

		for i := 0; i < maxPrefix; i++ {
			if counters[i] == 0 || !enabled[i] {
				continue
			}
			multiplier := uint64(2)
			for j := i + 1; j <= maxPrefix; j++ {
				if counters[j] > 0 {
					increase := counters[i] * (multiplier - 1)
					if o.Debug {
						fmt.Fprintf(os.Stderr, "\t\t> Examining merging prefix %d to %d (increase by %d)\n", i, j, increase)
					}
					if increase < minIncrease {
						minIncrease = increase
						min, to = i, j
					}
					break
				}
				multiplier *= 2
			}
		}

		if min == -1 || to == -1 || min == to {
			if o.Debug {
				fmt.Fprintln(os.Stderr, "\tNothing more to reduce")
			}
			break
		}

		multiplier := uint64(1)
		for x := min; x < to; x++ {
			multiplier *= 2
		}
		increase := counters[min]*multiplier - counters[min]
		if o.Debug {
			fmt.Fprintf(os.Stderr, "\t\t> Selected prefix %d (%d entries) to be merged in %d (total increase by %d)\n", min, counters[min], to, increase)
		}

		if total+increase > acceptable {
			if o.Debug {
				fmt.Fprintf(os.Stderr, "\tCannot proceed to increase total %d by %d, above acceptable %d.\n", total, increase, acceptable)
			}
			break
		}

		oldToCounters := counters[to]
		total += increase
		counters[to] += increase + counters[min]
		counters[min] = 0
		enabled[min] = false
		eliminated++
		if o.Debug {
			fmt.Fprintf(os.Stderr, "\t\tEliminating prefix %d in %d (had %d, now has %d entries), total is now %d (increased by %d)\n", min, to, oldToCounters, counters[to], total, increase)
		}
	}

	if o.Debug {
		fmt.Fprintf(os.Stderr, "\nEliminated %d out of %d prefixes (%d remain in the final set).\n\n", eliminated, initial, initial-eliminated)
	}
}

// execute runs the selected mode over the loaded sets and prints the
// result; it returns the process exit code (0, or 1 for a diff with
// differences).
func execute(o *Options, loaded *Loaded) int {
	if len(loaded.Sets) == 0 {
		// Defensive: loadAll always produces at least one set
		// (stdin fallback); the C texts are kept for parity.
		if o.Family == V6 {
			fmt.Fprintln(os.Stderr, "iprange: No valid ipsets to process.")
		} else {
			fmt.Fprintln(os.Stderr, "iprange: No valid ipsets to merge from the provided inputs.")
		}
		return 1
	}

	split := loaded.GroupB
	if split < 0 {
		split = 0
	}
	if split > len(loaded.Sets) {
		split = len(loaded.Sets)
	}
	a := loaded.Sets[:split]
	b := loaded.Sets[split:]

	// Both C twins check !root before the mode dispatch; the IPv6
	// twin can reach it with an empty group A (all files after the
	// positional operator).
	if o.Family == V6 && len(a) == 0 {
		fmt.Fprintln(os.Stderr, "iprange: No valid ipsets to process.")
		return 1
	}

	switch o.Mode {
	case ModeMerge:
		merged := mergeGroup(o, a, "combined ipset")
		return printViaSet(o, merged, "combined ipset")

	case ModeReduce:
		merged := mergeGroup(o, a, "combined ipset")
		if o.Family == V6 {
			fmt.Fprintln(os.Stderr, "iprange: --ipset-reduce is not supported in IPv6 mode")
			return 1
		}
		reduced := *o
		applyReduce(o, merged, reduced.Enabled())
		return printViaSet(&reduced, merged, "combined ipset")

	case ModeCommon:
		if len(a) < 2 {
			if o.Family == V6 {
				fmt.Fprintln(os.Stderr, "iprange: two ipsets at least are needed to find common IPs.")
			} else {
				fmt.Fprintln(os.Stderr, "iprange: two ipsets at least are needed to be compared to find their common IPs.")
			}
			return 1
		}
		optimizeOperand(o, a[0].Set, a[0].Name)
		optimizeOperand(o, a[1].Set, a[1].Name)
		if o.Debug {
			fmt.Fprintf(os.Stderr, "iprange: Finding common IPs in %s and %s%s\n", a[0].Name, a[1].Name, familySuffix(o.Family))
		}
		common := intersectOp(a[0].Set, a[1].Set)
		for _, set := range a[2:] {
			optimizeOperand(o, set.Set, set.Name)
			if o.Debug {
				fmt.Fprintf(os.Stderr, "iprange: Finding common IPs in common and %s%s\n", set.Name, familySuffix(o.Family))
			}
			common = intersectOp(common, set.Set)
		}
		return printViaSet(o, common, "common")

	case ModeExcludeNext:
		if len(b) == 0 {
			fmt.Fprintln(os.Stderr, "iprange: no files given after the --exclude-next parameter.")
			return 1
		}
		excluded := mergeGroup(o, a, a[0].Name)
		excludedName := a[0].Name
		for _, set := range b {
			optimizeOperand(o, excluded, excludedName)
			optimizeOperand(o, set.Set, set.Name)
			if o.Debug {
				fmt.Fprintf(os.Stderr, "iprange: Removing IPs in %s from %s%s\n", set.Name, excludedName, familySuffix(o.Family))
			}
			excluded = subtractOp(excluded, set.Set)
		}
		return printViaSet(o, excluded, "exclude")

	case ModeDiff:
		if len(a) == 0 || len(b) == 0 {
			fmt.Fprintln(os.Stderr, "iprange: two ipsets at least are needed to be diffed.")
			return 1
		}
		mergedA := mergeGroup(o, a, a[0].Name)
		mergedB := mergeGroup(o, b, b[0].Name)
		// C renames the merged chains for the diff diagnostics only
		// when more than one set was merged.
		nameA := a[0].Name
		if len(a) > 1 {
			nameA = "ipset A"
		}
		nameB := b[0].Name
		if len(b) > 1 {
			nameB = "ipset B"
		}
		optimizeOperand(o, mergedA, nameA)
		optimizeOperand(o, mergedB, nameB)
		if o.Debug {
			fmt.Fprintf(os.Stderr, "iprange: Finding diff IPs in %s and %s%s\n", nameA, nameB, familySuffix(o.Family))
		}
		result := diffOp(mergedA, mergedB)
		if !o.Quiet {
			// The C prints before computing the exit code; a print
			// failure only reports, the diff result decides the exit.
			if err := PrintSet(o, result, "diff"); err != nil {
				fmt.Fprintf(os.Stderr, "iprange: %v\n", err)
			}
		}
		if result.Unique.Hi != 0 || result.Unique.Lo != 0 {
			return 1
		}
		return 0

	case ModeCompare:
		if len(a) < 2 {
			fmt.Fprintln(os.Stderr, "iprange: two ipsets at least are needed to be compared.")
			return 1
		}
		return emit(func(w *bufio.Writer) error {
			if o.Header {
				if _, err := w.WriteString("name1,name2,entries1,entries2,ips1,ips2,combined_ips,common_ips\n"); err != nil {
					return err
				}
			}
			for _, set := range a {
				optimizeDirect(o, set.Set, set.Name)
			}
			for i := 0; i < len(a); i++ {
				for j := i + 1; j < len(a); j++ {
					if o.Debug {
						fmt.Fprintf(os.Stderr, "iprange: Combining %s and %s%s\n", a[i].Name, a[j].Name, familySuffix(o.Family))
					}
					combined := combineOp(a[i].Set, a[j].Set)
					optimizeDirect(o, combined, "combined")
					unique1 := a[i].Set.Unique
					unique2 := a[j].Set.Unique
					combinedIPs := combined.Unique
					if err := writeCompareRow(w, a[i].Name, a[j].Name, a[i].Set.Entries, a[j].Set.Entries, unique1, unique2, combinedIPs, Sub128(u128Add(unique1, unique2), combinedIPs)); err != nil {
						return err
					}
				}
			}
			return nil
		})

	case ModeCompareNext:
		if len(b) == 0 {
			fmt.Fprintln(os.Stderr, "iprange: no files given after the --compare-next parameter.")
			return 1
		}
		return emit(func(w *bufio.Writer) error {
			if o.Header {
				if _, err := w.WriteString("name1,name2,entries1,entries2,ips1,ips2,combined_ips,common_ips\n"); err != nil {
					return err
				}
			}
			for _, set := range a {
				optimizeDirect(o, set.Set, set.Name)
			}
			for _, set := range b {
				optimizeDirect(o, set.Set, set.Name)
			}
			for _, x := range a {
				for _, y := range b {
					if o.Debug {
						fmt.Fprintf(os.Stderr, "iprange: Combining %s and %s%s\n", x.Name, y.Name, familySuffix(o.Family))
					}
					combined := combineOp(x.Set, y.Set)
					optimizeDirect(o, combined, "combined")
					unique1 := x.Set.Unique
					unique2 := y.Set.Unique
					combinedIPs := combined.Unique
					if err := writeCompareRow(w, x.Name, y.Name, x.Set.Entries, y.Set.Entries, unique1, unique2, combinedIPs, Sub128(u128Add(unique1, unique2), combinedIPs)); err != nil {
						return err
					}
				}
			}
			return nil
		})

	case ModeCompareFirst:
		if len(a) < 2 {
			fmt.Fprintln(os.Stderr, "iprange: two ipsets at least are needed to be compared.")
			return 1
		}
		return emit(func(w *bufio.Writer) error {
			if o.Header {
				if _, err := w.WriteString("name,entries,unique_ips,common_ips\n"); err != nil {
					return err
				}
			}
			for _, set := range a {
				optimizeDirect(o, set.Set, set.Name)
			}
			for i := 1; i < len(a); i++ {
				if o.Debug {
					fmt.Fprintf(os.Stderr, "iprange: Combining %s and %s%s\n", a[i].Name, a[0].Name, familySuffix(o.Family))
				}
				combined := combineOp(a[i].Set, a[0].Set)
				optimizeDirect(o, combined, "combined")
				uniqueIPs := a[i].Set.Unique
				commonIPs := Sub128(u128Add(uniqueIPs, a[0].Set.Unique), combined.Unique)
				if err := writeCountRow(w, a[i].Name, a[i].Set.Entries, uniqueIPs, commonIPs); err != nil {
					return err
				}
			}
			return nil
		})

	case ModeCountUnique:
		merged := mergeGroup(o, a, "combined ipset")
		optimizeDirect(o, merged, "combined ipset")
		return emit(func(w *bufio.Writer) error {
			if o.Header {
				if _, err := w.WriteString("entries,unique_ips\n"); err != nil {
					return err
				}
			}
			if err := writeUint(w, uint64(merged.Entries)); err != nil {
				return err
			}
			if err := w.WriteByte(','); err != nil {
				return err
			}
			if err := writeUint128(w, merged.Unique); err != nil {
				return err
			}
			_, err := w.WriteString("\n")
			return err
		})

	case ModeCountUniqueAll:
		return emit(func(w *bufio.Writer) error {
			if o.Header {
				if _, err := w.WriteString("name,entries,unique_ips\n"); err != nil {
					return err
				}
			}
			for _, set := range a {
				optimizeDirect(o, set.Set, set.Name)
			}
			for _, set := range a {
				if err := writeUniqueRow(w, set.Name, set.Set.Entries, set.Set.Unique); err != nil {
					return err
				}
			}
			return nil
		})
	}
	return 0
}
