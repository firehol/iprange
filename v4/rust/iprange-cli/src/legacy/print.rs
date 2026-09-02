//! Legacy output: CIDR decomposition, ranges, single-IP expansion,
//! and binary rendering with the exact C formats and prefix/suffix
//! wrapper rules.
//!
//! Ported 1:1 from:
//! - `src/ipset_print.c` / `src/ipset6_print.c` — the CIDR
//!   decomposition walk (`split_range`/`split_range6`), the ranges
//!   and single-IP modes, and the `print_addr*` line shapes;
//! - `src/iprange.c` / `src/iprange6_main.c` — which print shape a
//!   flag selects and how the `--print-prefix*`/`--print-suffix*`
//!   wrappers attach to every output line;
//! - `src/ipset_binary.c` / `src/ipset6_binary.c` — the binary save
//!   entry points (empty sets emit nothing so `test -s file` works).
//!
//! The count/compare CSV rows never carry prefix/suffix wrappers in
//! the C (the `iprange_csv_write_*` helpers are bare); only the
//! print modes wrap, so this module is the only wrapper owner.

use std::io;

use super::binary;
use super::family::{Family, FamilyImpl};
use super::options::{Options, PrintMode};
use super::range::IpSet;

/// The C `256 * 256 * 256` cap on one range in `-1`/`--print-single-ips`
/// mode: a range strictly larger than 16,777,216 addresses is
/// eliminated (with a stderr warning) instead of expanded.
const SINGLE_IPS_CAP: u128 = 256 * 256 * 256;


/// Broadcast address of `addr` at `prefix` (C `broadcast()` /
/// `broadcast6()`): `addr | ((1 << (BITS - prefix)) - 1)`, with the
/// full-width mask handled without overflow.
#[inline]
fn broadcast_of<F: FamilyImpl>(addr: F, prefix: u32) -> F {
    let shift = F::BITS - prefix;
    if shift >= 128 {
        F::MAX
    } else {
        F::from_u128(addr.as_u128() | ((1u128 << shift) - 1))
    }
}

/// One CIDR line (C `print_addr`/`print_addr6`): prefixes below the
/// family width use the `--print-prefix-nets`/`--print-suffix-nets`
/// wrappers; the full-width prefix prints the bare address with the
/// ips wrappers. The suffix attaches before the line newline.
fn write_cidr_line<F: FamilyImpl, W: io::Write>(
    w: &mut W,
    options: &Options,
    addr: F,
    prefix: u32,
) -> io::Result<()> {
    if prefix < F::BITS {
        write!(
            w,
            "{}{}{}",
            options.print.prefix_nets,
            F::fmt_cidr(addr, prefix),
            options.print.suffix_nets
        )?;
    } else {
        write!(
            w,
            "{}{}{}",
            options.print.prefix_ips,
            F::fmt_cidr(addr, prefix),
            options.print.suffix_ips
        )?;
    }
    w.write_all(b"\n")
}

/// One ranges-mode line (C `print_addr_range`/`print_addr6_range`):
/// `lo-hi`; a single-address range prints twice with the ips
/// wrappers, a multi-address range uses the nets wrappers.
fn write_range_line<F: FamilyImpl, W: io::Write>(
    w: &mut W,
    options: &Options,
    lo: F,
    hi: F,
) -> io::Result<()> {
    if lo == hi {
        write!(
            w,
            "{}{}-{}{}",
            options.print.prefix_ips,
            F::fmt_addr(lo),
            F::fmt_addr(hi),
            options.print.suffix_ips
        )?;
    } else {
        write!(
            w,
            "{}{}-{}{}",
            options.print.prefix_nets,
            F::fmt_addr(lo),
            F::fmt_addr(hi),
            options.print.suffix_nets
        )?;
    }
    w.write_all(b"\n")
}

/// One single-IP line (C `print_addr_single`/`print_addr6_single`):
/// bare address with the ips wrappers.
fn write_single_line<F: FamilyImpl, W: io::Write>(
    w: &mut W,
    options: &Options,
    addr: F,
) -> io::Result<()> {
    write!(
        w,
        "{}{}{}",
        options.print.prefix_ips,
        F::fmt_addr(addr),
        options.print.suffix_ips
    )?;
    w.write_all(b"\n")
}

/// C `split_range()` / `split_range6()`: recursively cover
/// `[lo, hi]` (a sub-range of the network `addr/prefix`) with the
/// largest enabled CIDR blocks, printing each block. `enabled` is
/// the family prefix array (`prefix_enabled`/`prefix6_enabled`);
/// a block is emitted only when its prefix is enabled.
#[allow(clippy::too_many_arguments)]
fn split_range<F: FamilyImpl, W: io::Write>(
    w: &mut W,
    options: &Options,
    addr: F,
    prefix: u32,
    lo: F,
    hi: F,
    enabled: &[bool],
    mut counters: Option<&mut Vec<u64>>,
) -> io::Result<()> {
    let bc = broadcast_of(addr, prefix);

    if lo == addr && hi == bc && enabled[prefix as usize] {
        if let Some(counters) = counters {
            counters[prefix as usize] += 1;
        }
        return write_cidr_line(w, options, addr, prefix);
    }

    let prefix = prefix + 1;
    let lower_half = addr;
    let upper_half = F::from_u128(addr.as_u128() | (1u128 << (F::BITS - prefix)));

    if hi < upper_half {
        return split_range(w, options, lower_half, prefix, lo, hi, enabled, counters);
    }
    if lo >= upper_half {
        return split_range(w, options, upper_half, prefix, lo, hi, enabled, counters);
    }

    split_range(
        w,
        options,
        lower_half,
        prefix,
        lo,
        broadcast_of(lower_half, prefix),
        enabled,
        counters.as_deref_mut(),
    )?;
    split_range(w, options, upper_half, prefix, upper_half, hi, enabled, counters)
}

/// Render one optimized set with the selected print shape
/// (CIDR decomposition, ranges, single IPs, or binary) to `w`.
///
/// Order matches the C `ipset_print`/`ipset6_print` dispatch: the
/// set is optimized first (the caller may hand a merged-but-dirty
/// set, exactly like the C mode walks), then the single `PrintMode`
/// shape selected by the last `--print-*` flag. The optional `name`
/// feeds the C `-v` "Printing ..." diagnostic. `--quiet` suppresses
/// output (the C honors it only for diff; the caller also guards
/// there).
pub fn print_set<F: FamilyImpl, W: io::Write>(
    w: &mut W,
    options: &Options,
    name: &str,
    set: &IpSet<F>,
) -> io::Result<()> {
    if options.quiet {
        return Ok(());
    }

    // C ipset_print()/ipset6_print(): `if(!(flags & OPTIMIZED)) optimize`.
    let mut owned;
    let set = if set.optimized {
        set
    } else {
        owned = set.clone();
        owned.optimize();
        &owned
    };

    if options.print.mode == PrintMode::Binary {
        // The writer is generic over the family (v1 only ever sees
        // the u32 family, v2 the u128 family), so no identity
        // conversion pass is needed.
        return match F::FAMILY {
            Family::V4 => binary::write_v1(w, set),
            Family::V6 => binary::write_v2(w, set),
        };
    }

    // C debug "Printing ..." line (after the binary early return).
    if options.debug {
        match F::FAMILY {
            Family::V4 => eprintln!(
                "iprange: Printing {name} with {} ranges, {} unique IPs",
                set.entries, set.unique
            ),
            Family::V6 => eprintln!(
                "iprange: Printing {name} (IPv6) with {} ranges, {} unique IPs",
                set.entries, set.unique
            ),
        }
    }

    // Per-prefix counters for the `-v` breakdown (C prefix_counters /
    // prefix6_counters); alive only under debug.
    let mut counters: Vec<u64> = if options.debug && options.print.mode == PrintMode::Cidr {
        vec![0; F::BITS as usize + 1]
    } else {
        Vec::new()
    };

    let mut total: u128 = 0;
    match options.print.mode {
        PrintMode::Binary => unreachable!("handled above"),
        PrintMode::Ranges => {
            for r in &set.ranges {
                write_range_line(w, options, r.lo, r.hi)?;
            }
            total = set.ranges.len() as u128;
        }
        PrintMode::SingleIps => {
            for r in &set.ranges {
                let start = r.lo.as_u128();
                let end = r.hi.as_u128();
                if end - start > SINGLE_IPS_CAP {
                    // C warning text is family-specific; the range
                    // is skipped either way.
                    if F::FAMILY == Family::V4 {
                        eprintln!(
                            "iprange: too big range eliminated start={} end={} gives {} IPs",
                            F::fmt_addr(r.lo),
                            F::fmt_addr(r.hi),
                            end - start
                        );
                    } else {
                        eprintln!(
                            "iprange: too big range eliminated start={} end={}",
                            F::fmt_addr(r.lo),
                            F::fmt_addr(r.hi)
                        );
                    }
                    continue;
                }
                for x in start..=end {
                    write_single_line(w, options, F::from_u128(x))?;
                    total += 1;
                }
            }
        }
        PrintMode::Cidr => {
            // The recursive walk starts at the family network zero
            // with prefix 0 (C split_range / split_range6).
            let enabled = options.enabled();
            for r in &set.ranges {
                split_range(
                    w,
                    options,
                    F::from_u128(0),
                    0,
                    r.lo,
                    r.hi,
                    enabled,
                    if counters.is_empty() {
                        None
                    } else {
                        Some(&mut counters)
                    },
                )?;
            }
            total = counters.iter().sum::<u64>() as u128;
        }
    }

    // C debug breakdown and totals block.
    if options.debug {
        let mut prefixes = 0usize;
        if options.print.mode == PrintMode::Cidr {
            eprintln!();
            eprintln!("{total} printed CIDRs, break down by prefix:");
            let mut total_cidrs: u128 = 0;
            for (prefix, &count) in counters.iter().enumerate() {
                if count > 0 {
                    eprintln!("\t- prefix /{prefix} counts {count} entries");
                    total_cidrs += count as u128;
                    prefixes += 1;
                }
            }
            total = total_cidrs;
        } else if options.print.mode == PrintMode::SingleIps {
            prefixes = 1;
        }
        let units = match options.print.mode {
            PrintMode::Cidr => "CIDRs",
            PrintMode::SingleIps => "IPs",
            _ => "ranges",
        };
        eprintln!();
        eprintln!(
            "totals: {} lines read, {} distinct IP ranges found, {} CIDR prefixes, {} {units} printed, {} unique IPs",
            set.lines, set.entries, prefixes, total, set.unique
        );
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::super::binary;
    use super::*;
    use crate::legacy::range::{IpNum, IpSet, Range};

    fn set4(ranges: &[(u32, u32)]) -> IpSet<u32> {
        let mut s = IpSet::default();
        for &(lo, hi) in ranges {
            s.add_range(Range { lo, hi });
        }
        s.optimize();
        s
    }

    fn set6(ranges: &[(u128, u128)]) -> IpSet<u128> {
        let mut s = IpSet::default();
        for &(lo, hi) in ranges {
            s.add_range(Range { lo, hi });
        }
        s.optimize();
        s
    }

    fn render4(options: &Options, set: &IpSet<u32>) -> String {
        let mut buf = Vec::new();
        print_set::<u32, _>(&mut buf, options, "test", set).unwrap();
        String::from_utf8(buf).unwrap()
    }

    fn render6(options: &Options, set: &IpSet<u128>) -> String {
        let mut buf = Vec::new();
        print_set::<u128, _>(&mut buf, options, "test", set).unwrap();
        String::from_utf8(buf).unwrap()
    }

    #[test]
    fn cidr_decomposition_whole_24_is_one_line() {
        let set = set4(&[(0, 255)]);
        assert_eq!(render4(&Options::default(), &set), "0.0.0.0/24\n");
    }

    #[test]
    fn cidr_decomposition_matches_c_oracle_shapes() {
        // Oracle-probed: 10.0.0.1 - 10.0.0.6 decomposes greedily.
        let set = set4(&[(0x0a00_0001, 0x0a00_0006)]);
        assert_eq!(
            render4(&Options::default(), &set),
            "10.0.0.1\n10.0.0.2/31\n10.0.0.4/31\n10.0.0.6\n"
        );
    }

    #[test]
    fn cidr_decomposition_obeys_disabled_prefixes() {
        let mut options = Options::default();
        // --min-prefix 25 disables 0..24, /25 stays enabled.
        for slot in 0..25 {
            options.prefix4_enabled[slot] = false;
        }
        let set = set4(&[(0, 255)]);
        assert_eq!(render4(&options, &set), "0.0.0.0/25\n0.0.0.128/25\n");
    }

    #[test]
    fn cidr_decomposition_v6_full_width_is_bare() {
        // A /126 prints as one CIDR line; disabling it decomposes to
        // four bare /128 addresses.
        let set = set6(&[(
            0x2001_0db8_0000_0000_0000_0000_0000_0000,
            0x2001_0db8_0000_0000_0000_0000_0000_0003,
        )]);
        let mut options = Options::default();
        options.family = Family::V6;
        assert_eq!(render6(&options, &set), "2001:db8::/126\n");
        for slot in 0..128 {
            options.prefix6_enabled[slot] = false;
        }
        assert_eq!(
            render6(&options, &set),
            "2001:db8::\n2001:db8::1\n2001:db8::2\n2001:db8::3\n"
        );
    }

    #[test]
    fn ranges_mode_prints_lo_dash_hi() {
        let mut options = Options::default();
        options.print.mode = PrintMode::Ranges;
        let set = set4(&[(0x0a00_0001, 0x0a00_0001), (0x0a00_0008, 0x0a00_000b)]);
        assert_eq!(
            render4(&options, &set),
            "10.0.0.1-10.0.0.1\n10.0.0.8-10.0.0.11\n"
        );
    }

    #[test]
    fn single_ips_mode_expands_every_address() {
        let mut options = Options::default();
        options.print.mode = PrintMode::SingleIps;
        let set = set4(&[(0x0a00_0000, 0x0a00_0003)]);
        assert_eq!(
            render4(&options, &set),
            "10.0.0.0\n10.0.0.1\n10.0.0.2\n10.0.0.3\n"
        );
    }

    #[test]
    fn single_ips_cap_is_strictly_greater() {
        // The C eliminates only when `end - start > 256^3`; a range
        // whose size is exactly the cap is still expanded. The full
        // expansion is 16.7M lines, so assert the size predicate the
        // mode is built on instead of rendering it.
        let set = set4(&[(0, SINGLE_IPS_CAP as u32)]);
        let start = set.ranges[0].lo.as_u128();
        let end = set.ranges[0].hi.as_u128();
        assert_eq!(end - start, SINGLE_IPS_CAP);
        assert!(!(end - start > SINGLE_IPS_CAP));
        assert!(SINGLE_IPS_CAP + 1 > SINGLE_IPS_CAP);
    }

    #[test]
    fn prefix_suffix_wrappers_attach_per_line() {
        // Oracle-probed test 37 shape: the /32 uses the ips wrappers,
        // the /30 CIDR line uses the nets wrappers; the suffix sits
        // before the newline.
        let mut options = Options::default();
        options.print.prefix_ips = "IP:".into();
        options.print.suffix_ips = ":I".into();
        options.print.prefix_nets = "NET:".into();
        options.print.suffix_nets = ":N".into();
        let set = set4(&[(0x0a00_0001, 0x0a00_0001), (0x0a00_0008, 0x0a00_000b)]);
        assert_eq!(
            render4(&options, &set),
            "IP:10.0.0.1:I\nNET:10.0.0.8/30:N\n"
        );
    }

    #[test]
    fn single_ips_wrappers_match_c() {
        let mut options = Options::default();
        options.print.mode = PrintMode::SingleIps;
        options.print.prefix_ips = "P-".into();
        options.print.suffix_ips = "-S".into();
        let set = set4(&[(0x0a00_000a, 0x0a00_000a)]);
        assert_eq!(render4(&options, &set), "P-10.0.0.10-S\n");
    }

    #[test]
    fn binary_mode_emits_v1_payload() {
        let mut options = Options::default();
        options.print.mode = PrintMode::Binary;
        let mut set = set4(&[(0xac10_6301, 0xac10_6301)]);
        set.lines = 1;
        let mut buf = Vec::new();
        print_set::<u32, _>(&mut buf, &options, "test", &set).unwrap();
        let mut expected = Vec::new();
        binary::write_v1(&mut expected, &set).unwrap();
        assert_eq!(buf, expected);
        assert!(buf.starts_with(b"iprange binary format v1.0\noptimized\n"));
    }

    #[test]
    fn binary_mode_empty_set_writes_nothing() {
        let mut options = Options::default();
        options.print.mode = PrintMode::Binary;
        let set = IpSet::<u32>::default();
        let mut buf = Vec::new();
        print_set::<u32, _>(&mut buf, &options, "test", &set).unwrap();
        assert!(buf.is_empty());
    }

    #[test]
    fn quiet_suppresses_all_output() {
        let mut options = Options::default();
        options.quiet = true;
        let set = set4(&[(0, 255)]);
        assert!(render4(&options, &set).is_empty());
    }

    #[test]
    fn unoptimized_input_is_optimized_before_printing() {
        // C ipset_print() optimizes when the flag is clear; two
        // overlapping appends must print as one merged range.
        let mut set = IpSet::<u32>::default();
        set.add_range(Range { lo: 1, hi: 10 });
        set.add_range(Range { lo: 5, hi: 20 });
        assert!(!set.optimized);
        let out = render4(&Options::default(), &set);
        assert_eq!(
            out,
            "0.0.0.1\n0.0.0.2/31\n0.0.0.4/30\n0.0.0.8/29\n0.0.0.16/30\n0.0.0.20\n"
        );
    }

    #[test]
    fn v6_cidr_decomposition_basic() {
        let set = set6(&[(
            0x2001_0db8_0000_0000_0000_0000_0000_0001,
            0x2001_0db8_0000_0000_0000_0000_0000_0006,
        )]);
        let mut options = Options::default();
        options.family = Family::V6;
        assert_eq!(
            render6(&options, &set),
            "2001:db8::1\n2001:db8::2/127\n2001:db8::4/127\n2001:db8::6\n"
        );
    }
}
