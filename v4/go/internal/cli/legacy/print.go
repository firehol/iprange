// Legacy output rendering: CIDR decomposition, ranges, single-IP
// expansion, and binary rendering with the exact C line shapes and
// the prefix/suffix wrapper rules. The CIDR walk, the ranges and
// single-IP modes, and the print_addr* line shapes are the exact
// ports of src/ipset_print.c / src/ipset6_print.c; the wrapper
// attachment rules mirror src/iprange.c / src/iprange6_main.c.
// Binary mode delegates to the released v1.0/v2.0 payload writers
// (WriteV1/WriteV2); empty sets emit nothing so `test -s file`
// works, exactly like the C savers.

package legacy

import (
	"bufio"
	"fmt"
	"io"
	"math/bits"
	"os"
	"strconv"
	"strings"
)

// singleIPsCap is the C `256 * 256 * 256` cap of the -1 /
// --print-single-ips mode: a range strictly larger than 16,777,216
// addresses is eliminated (with a stderr warning) instead of
// expanded, for both families.
const singleIPsCap uint64 = 256 * 256 * 256

// stdoutBufferSize is the print-path buffer scale (the Rust port
// uses the same 64 KiB): lines and binary payloads coalesce into one
// write syscall per buffer instead of one per line; the C uses
// stdio's internal buffering for the same effect.
const stdoutBufferSize = 64 * 1024

// familyBits returns the address width of the family (the C BITS
// distinction between ipset_print and ipset6_print).
func familyBits(fam Family) uint32 {
	if fam == V6 {
		return 128
	}
	return 32
}

// fmtAddr renders one address in the run's family (C ip2str_r /
// ip6str_r).
func fmtAddr(o *Options, addr IP128) string {
	if o.Family == V6 {
		return FmtAddrV6(addr)
	}
	return FmtAddrV4(addr)
}

// fmtCIDR renders addr/prefix in the run's family; the full-width
// prefix prints the bare address (the C print_addr rule).
func fmtCIDR(o *Options, addr IP128, prefix uint32) string {
	if o.Family == V6 {
		return FmtCIDRV6(addr, prefix)
	}
	return FmtCIDRV4(addr, prefix)
}

// broadcastAddr returns the broadcast address of addr/prefix (C
// broadcast()/broadcast6()): addr | ((1 << (BITS - prefix)) - 1);
// the full-width mask (IPv6 prefix 0) is the family maximum.
func broadcastAddr(fam Family, addr IP128, prefix uint32) IP128 {
	if fam == V6 {
		shift := 128 - prefix
		if shift >= 64 {
			if shift == 128 {
				return Max(V6)
			}
			return IP128{Hi: addr.Hi | ((1 << (shift - 64)) - 1), Lo: 0xFFFF_FFFF_FFFF_FFFF}
		}
		return IP128{Hi: addr.Hi, Lo: addr.Lo | ((1 << shift) - 1)}
	}
	return IP128{Lo: addr.Lo | ((1 << (32 - prefix)) - 1)}
}

// setBitAddr sets one bit of addr (C set_bit()/set_bit6()): pos
// counts from the most significant bit of the family (0 is the
// 2^(BITS-1) bit). The split walk calls it with the incremented
// prefix, so pos is 0..BITS-1.
func setBitAddr(fam Family, addr IP128, pos uint32) IP128 {
	if fam == V6 {
		if pos >= 64 {
			return IP128{Hi: addr.Hi | (1 << (pos - 64)), Lo: addr.Lo}
		}
		return IP128{Hi: addr.Hi, Lo: addr.Lo | (1 << pos)}
	}
	return IP128{Lo: addr.Lo | (1 << pos)}
}

// formatU128 renders a u128 value (hi, lo) in decimal. IPv6
// unique-IP counters and totals can exceed 64 bits; the C renders
// them with u128_to_dec. IPv4 values always live in lo.
func formatU128(hi, lo uint64) string {
	if hi == 0 {
		return strconv.FormatUint(lo, 10)
	}
	// Repeatedly divide the pair by 10, collecting digits from
	// least to most significant (u128 needs at most 39 digits).
	var buf [40]byte
	n := len(buf)
	for hi != 0 || lo != 0 {
		qHi := hi / 10
		rem := hi % 10
		qLo, digit := bits.Div64(rem, lo, 10)
		hi, lo = qHi, qLo
		n--
		buf[n] = '0' + byte(digit)
	}
	return string(buf[n:])
}

// writeCIDRLine emits one CIDR line (C print_addr/print_addr6):
// prefixes below the family width use the nets wrappers; the
// full-width prefix prints the bare address with the ips wrappers.
// The suffix attaches before the line newline.
func writeCIDRLine(w io.Writer, o *Options, addr IP128, prefix uint32) error {
	var b strings.Builder
	if prefix < familyBits(o.Family) {
		b.WriteString(o.Print.PrefixNets)
		b.WriteString(fmtCIDR(o, addr, prefix))
		b.WriteString(o.Print.SuffixNets)
	} else {
		b.WriteString(o.Print.PrefixIps)
		b.WriteString(fmtCIDR(o, addr, prefix))
		b.WriteString(o.Print.SuffixIps)
	}
	b.WriteByte('\n')
	_, err := io.WriteString(w, b.String())
	return err
}

// writeRangeLine emits one ranges-mode line (C
// print_addr_range/print_addr6_range): lo-hi; a single-address
// range prints twice with the ips wrappers, a multi-address range
// uses the nets wrappers.
func writeRangeLine(w io.Writer, o *Options, lo, hi IP128) error {
	var b strings.Builder
	if lo == hi {
		b.WriteString(o.Print.PrefixIps)
		b.WriteString(fmtAddr(o, lo))
		b.WriteByte('-')
		b.WriteString(fmtAddr(o, hi))
		b.WriteString(o.Print.SuffixIps)
	} else {
		b.WriteString(o.Print.PrefixNets)
		b.WriteString(fmtAddr(o, lo))
		b.WriteByte('-')
		b.WriteString(fmtAddr(o, hi))
		b.WriteString(o.Print.SuffixNets)
	}
	b.WriteByte('\n')
	_, err := io.WriteString(w, b.String())
	return err
}

// writeSingleLine emits one single-IP line (C
// print_addr_single/print_addr6_single): the bare address with the
// ips wrappers.
func writeSingleLine(w io.Writer, o *Options, addr IP128) error {
	var b strings.Builder
	b.WriteString(o.Print.PrefixIps)
	b.WriteString(fmtAddr(o, addr))
	b.WriteString(o.Print.SuffixIps)
	b.WriteByte('\n')
	_, err := io.WriteString(w, b.String())
	return err
}

// splitRange is the C split_range()/split_range6() walk: recursively
// cover [lo, hi] (a sub-range of the network addr/prefix) with the
// largest enabled CIDR blocks, printing each block. enabled is the
// family prefix array (Prefix4Enabled/Prefix6Enabled); a block is
// emitted only when its prefix is enabled. counters, when non-nil,
// receives one increment per emitted block per prefix (the -v
// breakdown).
func splitRange(w io.Writer, o *Options, addr IP128, prefix uint32, lo, hi IP128, enabled []bool, counters []uint64) error {
	bc := broadcastAddr(o.Family, addr, prefix)

	if lo == addr && hi == bc && enabled[prefix] {
		if counters != nil {
			counters[prefix]++
		}
		return writeCIDRLine(w, o, addr, prefix)
	}

	prefix++
	lowerHalf := addr
	upperHalf := setBitAddr(o.Family, addr, familyBits(o.Family)-prefix)

	if hi.Compare(upperHalf) < 0 {
		return splitRange(w, o, lowerHalf, prefix, lo, hi, enabled, counters)
	}
	if lo.Compare(upperHalf) >= 0 {
		return splitRange(w, o, upperHalf, prefix, lo, hi, enabled, counters)
	}
	if err := splitRange(w, o, lowerHalf, prefix, lo, broadcastAddr(o.Family, lowerHalf, prefix), enabled, counters); err != nil {
		return err
	}
	return splitRange(w, o, upperHalf, prefix, upperHalf, hi, enabled, counters)
}

// printSet renders one set with the selected print shape to w. The
// dispatch order is the C ipset_print/ipset6_print sequence: quiet
// first, an optimize-if-needed pass (on a copy, so the caller's set
// is never mutated), the binary early return, the -v "Printing"
// line, then the single PrintMode shape selected by the last
// --print-* flag. name feeds the -v diagnostic.
func printSet(w io.Writer, o *Options, set *IpSet, name string) error {
	if o.Quiet {
		return nil
	}

	// C ipset_print()/ipset6_print(): `if(!(flags & OPTIMIZED))
	// optimize`. The copy keeps the caller's set untouched (Rust
	// clone+optimize); the ops layer decides whether a set reaches
	// this point already optimized.
	var owned IpSet
	if !set.Optimized {
		owned = *set
		owned.Ranges = append([]Range(nil), set.Ranges...)
		owned.Optimize()
		set = &owned
	}

	if o.Print.Mode == PrintBinary {
		// v1 is the 32-bit payload, v2 the 128-bit one; the set's
		// family selects the writer (empty sets write nothing).
		if set.Fam == V6 {
			return WriteV2(w, set)
		}
		return WriteV1(w, set)
	}

	if o.Debug {
		if set.Fam == V6 {
			fmt.Fprintf(os.Stderr, "iprange: Printing %s (IPv6) with %d ranges, %s unique IPs\n",
				name, set.Entries, formatU128(set.Unique.Hi, set.Unique.Lo))
		} else {
			fmt.Fprintf(os.Stderr, "iprange: Printing %s with %d ranges, %s unique IPs\n",
				name, set.Entries, formatU128(set.Unique.Hi, set.Unique.Lo))
		}
	}

	// Per-prefix counters for the -v breakdown (C prefix_counters /
	// prefix6_counters); alive only under debug in CIDR mode.
	var counters []uint64
	if o.Debug && o.Print.Mode == PrintCidr {
		counters = make([]uint64, familyBits(set.Fam)+1)
	}

	var total uint64
	switch o.Print.Mode {
	case PrintRanges:
		for i := range set.Ranges {
			if err := writeRangeLine(w, o, set.Ranges[i].Lo, set.Ranges[i].Hi); err != nil {
				return err
			}
			total++
		}
	case PrintSingleIps:
		for i := range set.Ranges {
			r := &set.Ranges[i]
			start, end := r.Lo, r.Hi
			delta := Sub128(end, start)
			if delta.Hi != 0 || delta.Lo > singleIPsCap {
				// The C warning text is family-specific; the
				// range is skipped either way.
				if set.Fam == V6 {
					fmt.Fprintf(os.Stderr, "iprange: too big range eliminated start=%s end=%s\n",
						fmtAddr(o, start), fmtAddr(o, end))
				} else {
					fmt.Fprintf(os.Stderr, "iprange: too big range eliminated start=%s end=%s gives %d IPs\n",
						fmtAddr(o, start), fmtAddr(o, end), delta.Lo)
				}
				continue
			}
			for x := start; ; x = x.Add(1) {
				if err := writeSingleLine(w, o, x); err != nil {
					return err
				}
				total++
				if x == end {
					break
				}
			}
		}
	case PrintCidr:
		// The recursive walk starts at the family network zero
		// with prefix 0 (C split_range / split_range6).
		enabled := o.Enabled()
		for i := range set.Ranges {
			if err := splitRange(w, o, IP128{}, 0, set.Ranges[i].Lo, set.Ranges[i].Hi, enabled, counters); err != nil {
				return err
			}
		}
		for _, c := range counters {
			total += c
		}
	}

	// C debug breakdown and totals block (stderr).
	if o.Debug {
		prefixes := 0
		if o.Print.Mode == PrintCidr {
			fmt.Fprintln(os.Stderr)
			fmt.Fprintf(os.Stderr, "%d printed CIDRs, break down by prefix:\n", total)
			var totalCIDRs uint64
			for prefix, count := range counters {
				if count > 0 {
					fmt.Fprintf(os.Stderr, "\t- prefix /%d counts %d entries\n", prefix, count)
					totalCIDRs += count
					prefixes++
				}
			}
			total = totalCIDRs
		} else if o.Print.Mode == PrintSingleIps {
			prefixes = 1
		}
		units := "ranges"
		switch o.Print.Mode {
		case PrintCidr:
			units = "CIDRs"
		case PrintSingleIps:
			units = "IPs"
		}
		fmt.Fprintln(os.Stderr)
		fmt.Fprintf(os.Stderr,
			"totals: %d lines read, %d distinct IP ranges found, %d CIDR prefixes, %d %s printed, %s unique IPs\n",
			set.Lines, set.Entries, prefixes, total, units, formatU128(set.Unique.Hi, set.Unique.Lo))
	}
	return nil
}

// PrintSet prints one set to stdout per o.Print.Mode with the
// prefix/suffix wrappers; the ops layer calls it where the C calls
// ipset_print/ipset6_print. Output is buffered with a 64 KiB writer
// so bulk text and binary output coalesce into large writes instead
// of one syscall per line, and the buffer is flushed before
// returning. A closed stdout dies of SIGPIPE exactly like the C
// binary (the Go runtime re-raises SIGPIPE for fd-1 writes); other
// write failures surface as errors.
func PrintSet(o *Options, set *IpSet, name string) error {
	w := bufio.NewWriterSize(os.Stdout, stdoutBufferSize)
	if err := printSet(w, o, set, name); err != nil {
		return err
	}
	return w.Flush()
}
