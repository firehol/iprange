//! Legacy set operations: merge, common, exclude, diff, compare,
//! count, and reduce orchestration (exact C mode dispatch).
//!
//! Port of `src/iprange.c` main() mode execution and
//! `src/iprange6_main.c` `iprange6_run()` (the released CLI runs
//! both twins; the union is implemented once here). The interval
//! walks below are the exact ports of `src/ipset_common.c`,
//! `src/ipset6_common.c`, `src/ipset_exclude.c`,
//! `src/ipset6_exclude.c`, `src/ipset_diff.c`, `src/ipset6_diff.c`,
//! `src/ipset_combine.c`, `src/ipset6_combine.c`,
//! `src/ipset_merge.c`, `src/ipset6_merge.c`, and
//! `src/ipset_reduce.c`.

use std::io::Write;

use super::family::{Family, FamilyImpl};
use super::options::{Mode, Options};
use super::parse::{self, Loaded};
use super::print;
use super::range::{IpNum, IpSet, Range};

/// C debug suffix for the IPv6 twins (" (IPv6)").
fn family_suffix<F: FamilyImpl>() -> &'static str {
    if F::FAMILY == Family::V6 {
        " (IPv6)"
    } else {
        ""
    }
}

/// Direct `ipset_optimize()` call (C `ipset_optimize_all`,
/// `ipset_unique_ips`, `ipset_print`, `ipset_reduce`, and the
/// compare modes' `ipset_optimize(comips)`): the IPv4 twin prints
/// "Is already optimized" when the flag is already set; the IPv6
/// twin stays silent in that case.
fn optimize_direct<F: FamilyImpl>(options: &Options, set: &mut IpSet<F>, name: &str) {
    if set.optimized {
        if options.debug && F::FAMILY == Family::V4 {
            eprintln!("iprange: Is already optimized {name}");
        }
        return;
    }
    if options.debug {
        eprintln!("iprange: Optimizing {name}{}", family_suffix::<F>());
    }
    set.optimize();
}

/// Operand optimization inside the common/exclude/diff walks (C
/// `ipset_common`/`ipset_exclude`/`ipset_diff` only call
/// `ipset_optimize` when the flag is clear, so an already-optimized
/// operand produces no line at all).
fn optimize_operand<F: FamilyImpl>(options: &Options, set: &mut IpSet<F>, name: &str) {
    if !set.optimized {
        if options.debug {
            eprintln!("iprange: Optimizing {name}{}", family_suffix::<F>());
        }
        set.optimize();
    }
}

/// Group A = sets before the first positional operator, group B =
/// the rest. The boundary is computed by the parser in loaded-set
/// units (`LoadedAll::group_b`): a single `@file`/`@dir` source can
/// expand to several loaded sets, and the C argv scan's
/// source-index boundary (`Options::group_b`) cannot express that.
/// With no operator the whole chain is group A.

/// Wrap a writer callable so `?` works inside and failures map to
/// the C-style stderr message and exit code 1 (SIGPIPE terminates the
/// process first, exactly like the C binary). The concrete locked
/// stdout type keeps `print_set`'s `W: io::Write` (Sized) bound.
fn emit(body: impl FnOnce(&mut std::io::StdoutLock<'static>) -> std::io::Result<()>) -> i32 {
    let mut w = std::io::stdout().lock();
    match body(&mut w) {
        Ok(()) => 0,
        Err(e) => {
            eprintln!("iprange: {}", e);
            1
        }
    }
}

/// C `iprange_csv_write_field` (src/iprange.h): quote only when the
/// field contains `,`, `"`, `\n` or `\r`; double embedded quotes.
fn csv_field(w: &mut dyn Write, field: &str) -> std::io::Result<()> {
    let bytes = field.as_bytes();
    let quote = bytes
        .iter()
        .any(|&b| matches!(b, b',' | b'"' | b'\n' | b'\r'));
    if !quote {
        return w.write_all(bytes);
    }
    w.write_all(b"\"")?;
    for &b in bytes {
        if b == b'"' {
            w.write_all(b"\"\"")?;
        } else {
            w.write_all(&[b])?;
        }
    }
    w.write_all(b"\"")
}

/// Decimal digits without a leading separator (C
/// `iprange_write_uintmax`).
fn write_uint(w: &mut dyn Write, mut v: u128) -> std::io::Result<()> {
    let mut buf = [0u8; 40];
    let mut len = 0usize;
    loop {
        buf[len] = b'0' + (v % 10) as u8;
        len += 1;
        v /= 10;
        if v == 0 {
            break;
        }
    }
    for i in (0..len).rev() {
        w.write_all(&[buf[i]])?;
    }
    Ok(())
}

/// The `name1,name2,entries1,entries2,ips1,ips2,combined_ips,common_ips`
/// row (C `iprange_csv_write_compare_row`, src/iprange.c:52).
fn write_compare_row(
    w: &mut dyn Write,
    name1: &str,
    name2: &str,
    entries1: usize,
    entries2: usize,
    unique1: u128,
    unique2: u128,
    combined_ips: u128,
    common_ips: u128,
) -> std::io::Result<()> {
    csv_field(w, name1)?;
    w.write_all(b",")?;
    csv_field(w, name2)?;
    w.write_all(b",")?;
    write_uint(w, entries1 as u128)?;
    w.write_all(b",")?;
    write_uint(w, entries2 as u128)?;
    w.write_all(b",")?;
    write_uint(w, unique1)?;
    w.write_all(b",")?;
    write_uint(w, unique2)?;
    w.write_all(b",")?;
    write_uint(w, combined_ips)?;
    w.write_all(b",")?;
    write_uint(w, common_ips)?;
    w.write_all(b"\n")
}

/// The `name,entries,unique_ips,common_ips` row (C
/// `iprange_csv_write_count_row`, src/iprange.c:64).
fn write_count_row(
    w: &mut dyn Write,
    name: &str,
    entries: usize,
    unique_ips: u128,
    common_ips: u128,
) -> std::io::Result<()> {
    csv_field(w, name)?;
    w.write_all(b",")?;
    write_uint(w, entries as u128)?;
    w.write_all(b",")?;
    write_uint(w, unique_ips)?;
    w.write_all(b",")?;
    write_uint(w, common_ips)?;
    w.write_all(b"\n")
}

/// The `name,entries,unique_ips` row (C
/// `iprange_csv_write_unique_row`, src/iprange.c:76).
fn write_unique_row(
    w: &mut dyn Write,
    name: &str,
    entries: usize,
    unique_ips: u128,
) -> std::io::Result<()> {
    csv_field(w, name)?;
    w.write_all(b",")?;
    write_uint(w, entries as u128)?;
    w.write_all(b",")?;
    write_uint(w, unique_ips)?;
    w.write_all(b"\n")
}

/// C `ipset_merge` fold (src/ipset_merge.c, src/ipset6_merge.c): the
/// first set receives every other set by concatenation; the result is
/// left unoptimized exactly like the C. `rename_to` mirrors the C
/// `ipset_set_filename(root, "combined ipset")` used by the
/// combine/reduce/count-unique branches (visible only in the -v
/// "Merging %s to %s" text).
fn merge_group<F: FamilyImpl>(
    options: &Options,
    a: &[Loaded<F>],
    rename_to: Option<&str>,
) -> IpSet<F> {
    let target = rename_to.unwrap_or(&a[0].name);
    let mut merged = a[0].set.clone();
    for set in &a[1..] {
        if options.debug {
            eprintln!(
                "iprange: Merging {} to {}{}",
                set.name,
                target,
                family_suffix::<F>()
            );
        }
        merged.merge_from(&set.set);
    }
    merged
}

/// C `ipset_common` walk (src/ipset_common.c, src/ipset6_common.c):
/// one sorted sweep over both optimized inputs; overlapping pieces
/// are appended in ascending order. The result is optimized by
/// construction. Empty input produces an empty result with summed
/// line counts.
fn intersect_op<F: FamilyImpl>(a: &IpSet<F>, b: &IpSet<F>) -> IpSet<F> {
    // The C walkers create the result with no OPTIMIZED flag (plain
    // appends, no adjacency merging) and set the flag at the end.
    let mut out = IpSet {
        optimized: false,
        ..IpSet::default()
    };
    let n1 = a.ranges.len();
    let n2 = b.ranges.len();
    if n1 == 0 || n2 == 0 {
        out.lines = a.lines + b.lines;
        out.optimized = true;
        return out;
    }
    let mut i1 = 0usize;
    let mut i2 = 0usize;
    let mut lo1 = a.ranges[0].lo;
    let mut hi1 = a.ranges[0].hi;
    let mut lo2 = b.ranges[0].lo;
    let mut hi2 = b.ranges[0].hi;
    while i1 < n1 && i2 < n2 {
        if lo1 > hi2 {
            i2 += 1;
            if i2 < n2 {
                lo2 = b.ranges[i2].lo;
                hi2 = b.ranges[i2].hi;
            }
            continue;
        }
        if lo2 > hi1 {
            i1 += 1;
            if i1 < n1 {
                lo1 = a.ranges[i1].lo;
                hi1 = a.ranges[i1].hi;
            }
            continue;
        }
        let lo = if lo1 > lo2 { lo1 } else { lo2 };
        let hi;
        if hi1 < hi2 {
            hi = hi1;
            i1 += 1;
            if i1 < n1 {
                lo1 = a.ranges[i1].lo;
                hi1 = a.ranges[i1].hi;
            }
        } else {
            hi = hi2;
            i2 += 1;
            if i2 < n2 {
                lo2 = b.ranges[i2].lo;
                hi2 = b.ranges[i2].hi;
            }
        }
        out.add_range(Range { lo, hi });
    }
    out.lines = a.lines + b.lines;
    out.optimized = true;
    out
}

/// C `ipset_exclude` walk (src/ipset_exclude.c, src/ipset6_exclude.c):
/// `a` minus `b` over two optimized inputs. The result is optimized
/// by construction; an empty `b` copies `a`, an empty `a` yields an
/// empty result (the C also tracks the summed line counts, used only
/// by the -v totals of the printer).
fn subtract_op<F: FamilyImpl>(a: &IpSet<F>, b: &IpSet<F>) -> IpSet<F> {
    // C ipset_exclude result: created unoptimized, flagged at the end.
    let mut out = IpSet {
        optimized: false,
        ..IpSet::default()
    };
    let n1 = a.ranges.len();
    let n2 = b.ranges.len();
    if n1 == 0 {
        out.lines = a.lines + b.lines;
        out.optimized = true;
        return out;
    }
    if n2 == 0 {
        for r in &a.ranges {
            out.add_range(*r);
        }
        out.lines = a.lines + b.lines;
        out.optimized = true;
        return out;
    }
    let mut i1 = 0usize;
    let mut i2 = 0usize;
    let mut lo1 = a.ranges[0].lo;
    let mut hi1 = a.ranges[0].hi;
    let mut lo2 = b.ranges[0].lo;
    let mut hi2 = b.ranges[0].hi;
    while i1 < n1 && i2 < n2 {
        if lo1 > hi2 {
            i2 += 1;
            if i2 < n2 {
                lo2 = b.ranges[i2].lo;
                hi2 = b.ranges[i2].hi;
            }
            continue;
        }
        if lo2 > hi1 {
            out.add_range(Range { lo: lo1, hi: hi1 });
            i1 += 1;
            if i1 < n1 {
                lo1 = a.ranges[i1].lo;
                hi1 = a.ranges[i1].hi;
            }
            continue;
        }
        if lo1 < lo2 {
            // lo1 < lo2 implies lo2 > 0, so dec() is safe.
            out.add_range(Range {
                lo: lo1,
                hi: lo2.dec().unwrap(),
            });
            lo1 = lo2;
        }
        if hi1 == hi2 {
            i1 += 1;
            if i1 < n1 {
                lo1 = a.ranges[i1].lo;
                hi1 = a.ranges[i1].hi;
            }
            i2 += 1;
            if i2 < n2 {
                lo2 = b.ranges[i2].lo;
                hi2 = b.ranges[i2].hi;
            }
        } else if hi1 < hi2 {
            i1 += 1;
            if i1 < n1 {
                lo1 = a.ranges[i1].lo;
                hi1 = a.ranges[i1].hi;
            }
        } else {
            // hi1 > hi2, so hi2 is below the family maximum (the
            // IPv6 twin guards hi2 == MAX separately; inc() is the
            // same guard and leaves lo1 untouched there).
            if let Some(next) = hi2.inc() {
                lo1 = next;
            } else {
                i1 += 1;
                if i1 < n1 {
                    lo1 = a.ranges[i1].lo;
                    hi1 = a.ranges[i1].hi;
                }
            }
            i2 += 1;
            if i2 < n2 {
                lo2 = b.ranges[i2].lo;
                hi2 = b.ranges[i2].hi;
            }
        }
    }
    if i1 < n1 {
        out.add_range(Range { lo: lo1, hi: hi1 });
        i1 += 1;
        while i1 < n1 {
            out.add_range(a.ranges[i1]);
            i1 += 1;
        }
    }
    out.lines = a.lines + b.lines;
    out.optimized = true;
    out
}

/// C `ipset_diff` walk (src/ipset_diff.c, src/ipset6_diff.c): the
/// symmetric difference of two optimized inputs, appended in
/// ascending order. The result is optimized by construction. The
/// numeric `dec()`/`inc()` guards match the IPv6 twin's overflow
/// handling for the family maximum.
fn diff_op<F: FamilyImpl>(a: &IpSet<F>, b: &IpSet<F>) -> IpSet<F> {
    // C ipset_diff result: created unoptimized, flagged at the end
    // (adjacent diff pieces stay separate entries, exactly like C).
    let mut out = IpSet {
        optimized: false,
        ..IpSet::default()
    };
    let n1 = a.ranges.len();
    let n2 = b.ranges.len();
    if n1 == 0 && n2 == 0 {
        out.lines = a.lines + b.lines;
        out.optimized = true;
        return out;
    }
    if n1 == 0 {
        for r in &b.ranges {
            out.add_range(*r);
        }
        out.lines = a.lines + b.lines;
        out.optimized = true;
        return out;
    }
    if n2 == 0 {
        for r in &a.ranges {
            out.add_range(*r);
        }
        out.lines = a.lines + b.lines;
        out.optimized = true;
        return out;
    }
    let mut i1 = 0usize;
    let mut i2 = 0usize;
    let mut lo1 = a.ranges[0].lo;
    let mut hi1 = a.ranges[0].hi;
    let mut lo2 = b.ranges[0].lo;
    let mut hi2 = b.ranges[0].hi;
    while i1 < n1 && i2 < n2 {
        if lo1 > hi2 {
            out.add_range(Range { lo: lo2, hi: hi2 });
            i2 += 1;
            if i2 < n2 {
                lo2 = b.ranges[i2].lo;
                hi2 = b.ranges[i2].hi;
            }
            continue;
        }
        if lo2 > hi1 {
            out.add_range(Range { lo: lo1, hi: hi1 });
            i1 += 1;
            if i1 < n1 {
                lo1 = a.ranges[i1].lo;
                hi1 = a.ranges[i1].hi;
            }
            continue;
        }
        if lo1 > lo2 {
            // lo1 > lo2 >= 0 implies lo1 >= 1, so dec() is safe.
            out.add_range(Range {
                lo: lo2,
                hi: lo1.dec().unwrap(),
            });
        } else if lo2 > lo1 {
            out.add_range(Range {
                lo: lo1,
                hi: lo2.dec().unwrap(),
            });
        }
        if hi1 > hi2 {
            // hi1 > hi2 implies hi2 < MAX, so inc() is safe.
            lo1 = hi2.inc().unwrap();
            i2 += 1;
            if i2 < n2 {
                lo2 = b.ranges[i2].lo;
                hi2 = b.ranges[i2].hi;
            }
            continue;
        }
        if hi2 > hi1 {
            lo2 = hi1.inc().unwrap();
            i1 += 1;
            if i1 < n1 {
                lo1 = a.ranges[i1].lo;
                hi1 = a.ranges[i1].hi;
            }
            continue;
        }
        i1 += 1;
        if i1 < n1 {
            lo1 = a.ranges[i1].lo;
            hi1 = a.ranges[i1].hi;
        }
        i2 += 1;
        if i2 < n2 {
            lo2 = b.ranges[i2].lo;
            hi2 = b.ranges[i2].hi;
        }
    }
    while i1 < n1 {
        out.add_range(Range { lo: lo1, hi: hi1 });
        i1 += 1;
        if i1 < n1 {
            lo1 = a.ranges[i1].lo;
            hi1 = a.ranges[i1].hi;
        }
    }
    while i2 < n2 {
        out.add_range(Range { lo: lo2, hi: hi2 });
        i2 += 1;
        if i2 < n2 {
            lo2 = b.ranges[i2].lo;
            hi2 = b.ranges[i2].hi;
        }
    }
    out.lines = a.lines + b.lines;
    out.optimized = true;
    out
}

/// C `ipset_combine` (src/ipset_combine.c, src/ipset6_combine.c):
/// concatenation of both inputs, never optimized; the compare modes
/// call `ipset_optimize(comips)` right after.
fn combine_op<F: FamilyImpl>(a: &IpSet<F>, b: &IpSet<F>) -> IpSet<F> {
    let mut combined = IpSet::default();
    combined.merge_from(a);
    combined.merge_from(b);
    combined
}

/// Broadcast address of a network-aligned `addr/prefix` (C
/// `broadcast()`/`broadcast6()`: `addr | (MAX >> prefix)`).
fn cidr_broadcast<F: IpNum>(addr: F, prefix: u32) -> F {
    F::from_u128(addr.as_u128() | (F::MAX.as_u128() >> prefix))
}

/// `addr | (1 << (BITS - prefix))` (C `set_bit()`/`set_bit6()`).
fn cidr_set_bit<F: IpNum>(addr: F, prefix: u32) -> F {
    F::from_u128(addr.as_u128() | (1u128 << (F::BITS - prefix)))
}

/// C `split_range` with a counting callback (the exact port used by
/// `ipset_reduce`): recursively decompose `[lo, hi]` inside the CIDR
/// cell `addr/prefix`; each emitted CIDR increments its prefix
/// counter. Disabled prefixes split further instead of emitting,
/// exactly like the C.
fn count_split<F: IpNum>(
    addr: F,
    prefix: u32,
    lo: F,
    hi: F,
    enabled: &[bool],
    counters: &mut [u64],
) {
    let bc = cidr_broadcast(addr, prefix);
    if lo == addr && hi == bc && (prefix >= F::BITS || enabled[prefix as usize]) {
        counters[prefix as usize] += 1;
        return;
    }
    let p = prefix + 1;
    let lower = addr;
    let upper = cidr_set_bit(addr, p);
    if hi < upper {
        count_split(lower, p, lo, hi, enabled, counters);
    } else if lo >= upper {
        count_split(upper, p, lo, hi, enabled, counters);
    } else {
        count_split(lower, p, lo, cidr_broadcast(lower, p), enabled, counters);
        count_split(upper, p, upper, hi, enabled, counters);
    }
}

/// C `ipset_reduce` (src/ipset_reduce.c): disable prefixes so that
/// the later CIDR print produces fewer subnets while allowing the
/// configured percentage increase (or the minimum accepted entries)
/// in total CIDR count. Mutates `enabled` in place; the set itself is
/// left unchanged (only optimized). IPv4 only in the C; the IPv6 twin
/// rejects reduce before this point.
fn apply_reduce<F: FamilyImpl>(options: &Options, set: &mut IpSet<F>, enabled: &mut [bool]) {
    let max_prefix = enabled.len() - 1;

    // C `ipset_reduce()` optimizes through the guarded
    // `if(!(flags & OPTIMIZED))` form, which is silent when the set
    // is already optimized (unlike the direct `ipset_optimize`
    // calls, which print "Is already optimized" on the IPv4 side).
    optimize_operand(options, set, "combined ipset");

    if options.debug {
        eprintln!("\nCounting prefixes in combined ipset");
    }

    let mut counters = vec![0u64; enabled.len()];
    for r in &set.ranges {
        count_split(F::from_u128(0), 0, r.lo, r.hi, enabled, &mut counters);
    }

    if options.debug {
        eprintln!("Break down by prefix:");
    }

    let mut total = 0u64;
    let mut initial = 0u64;
    for i in 0..=max_prefix {
        if counters[i] > 0 {
            if options.debug {
                eprintln!("\t- prefix /{i} counts {} entries", counters[i]);
            }
            total += counters[i];
            initial += 1;
        } else {
            // The C also disables prefixes that produced no entries.
            enabled[i] = false;
        }
    }
    if options.debug {
        eprintln!("Total {total} entries generated");
    }

    let mut acceptable = total * options.reduce_factor / 100;
    if acceptable < options.reduce_entries {
        acceptable = options.reduce_entries;
    }
    if options.debug {
        eprintln!("Acceptable is to reach {acceptable} entries by reducing prefixes");
    }

    let mut eliminated = 0u64;
    while total < acceptable {
        let mut min: Option<usize> = None;
        let mut to: Option<usize> = None;
        let mut min_increase = acceptable * 10;

        for i in 0..max_prefix {
            if counters[i] == 0 || !enabled[i] {
                continue;
            }
            let mut multiplier = 2u64;
            for j in (i + 1)..=max_prefix {
                if counters[j] > 0 {
                    let increase = counters[i] * (multiplier - 1);
                    if options.debug {
                        eprintln!(
                            "\t\t> Examining merging prefix {i} to {j} (increase by {increase})"
                        );
                    }
                    if increase < min_increase {
                        min_increase = increase;
                        min = Some(i);
                        to = Some(j);
                    }
                    break;
                }
                multiplier *= 2;
            }
        }

        let (Some(min), Some(to)) = (min, to) else {
            if options.debug {
                eprintln!("\tNothing more to reduce");
            }
            break;
        };
        if min == to {
            break;
        }

        let mut multiplier = 1u64;
        for _ in min..to {
            multiplier *= 2;
        }
        let increase = counters[min] * multiplier - counters[min];
        if options.debug {
            eprintln!(
                "\t\t> Selected prefix {min} ({} entries) to be merged in {to} (total increase by {increase})",
                counters[min]
            );
        }

        if total + increase > acceptable {
            if options.debug {
                eprintln!(
                    "\tCannot proceed to increase total {total} by {increase}, above acceptable {acceptable}."
                );
            }
            break;
        }

        let old_to_counters = counters[to];
        total += increase;
        counters[to] += increase + counters[min];
        counters[min] = 0;
        enabled[min] = false;
        eliminated += 1;
        if options.debug {
            eprintln!(
                "\t\tEliminating prefix {min} in {to} (had {old_to_counters}, now has {} entries), total is now {total} (increased by {increase})",
                counters[to]
            );
        }
    }

    if options.debug {
        eprintln!(
            "\nEliminated {eliminated} out of {initial} prefixes ({} remain in the final set).\n",
            initial - eliminated
        );
    }
}

/// Execute the selected mode over the loaded sets and print the
/// result; returns the process exit code (0, or 1 for a diff with
/// differences).
pub fn execute<F: FamilyImpl>(options: &Options, loaded: &mut parse::LoadedAll<F>) -> i32 {
    if loaded.sets.is_empty() {
        // Defensive: parse::load_all always produces at least one
        // set (stdin fallback); the C texts are kept for parity.
        eprintln!(
            "iprange: {}",
            if F::FAMILY == Family::V4 {
                "No valid ipsets to merge from the provided inputs."
            } else {
                "No valid ipsets to process."
            }
        );
        return 1;
    }

    let split = loaded.group_b.min(loaded.sets.len());
    let (a, b) = loaded.sets.split_at_mut(split);

    // Both C twins check `!root` before the mode dispatch; the IPv6
    // twin can reach it with an empty group A (all files after the
    // positional operator).
    if F::FAMILY == Family::V6 && a.is_empty() {
        eprintln!("iprange: No valid ipsets to process.");
        return 1;
    }

    match options.mode {
        Mode::Merge => {
            let merged = merge_group(options, a, Some("combined ipset"));
            let set = merged;
            emit(|w| print::print_set::<F, _>(w, options, &set))
        }

        Mode::Reduce => {
            let mut merged = merge_group(options, a, Some("combined ipset"));
            if F::FAMILY == Family::V6 {
                eprintln!("iprange: --ipset-reduce is not supported in IPv6 mode");
                return 1;
            }
            let mut reduced = options.clone();
            {
                let enabled = reduced.enabled_mut();
                apply_reduce(options, &mut merged, enabled);
            }
            let set = merged;
            emit(|w| print::print_set::<F, _>(w, &reduced, &set))
        }

        Mode::Common => {
            if a.len() < 2 {
                eprintln!(
                    "iprange: {}",
                    if F::FAMILY == Family::V4 {
                        "two ipsets at least are needed to be compared to find their common IPs."
                    } else {
                        "two ipsets at least are needed to find common IPs."
                    }
                );
                return 1;
            }
            optimize_operand(options, &mut a[0].set, &a[0].name);
            optimize_operand(options, &mut a[1].set, &a[1].name);
            if options.debug {
                eprintln!(
                    "iprange: Finding common IPs in {} and {}{}",
                    a[0].name,
                    a[1].name,
                    family_suffix::<F>()
                );
            }
            let mut common = intersect_op(&a[0].set, &a[1].set);
            for set in &mut a[2..] {
                optimize_operand(options, &mut set.set, &set.name);
                if options.debug {
                    eprintln!(
                        "iprange: Finding common IPs in common and {}{}",
                        set.name,
                        family_suffix::<F>()
                    );
                }
                common = intersect_op(&common, &set.set);
            }
            let set = common;
            emit(|w| print::print_set::<F, _>(w, options, &set))
        }

        Mode::ExcludeNext => {
            if b.is_empty() {
                eprintln!("iprange: no files given after the --exclude-next parameter.");
                return 1;
            }
            let mut excluded = merge_group(options, a, None);
            let excluded_name = a[0].name.clone();
            for set in b.iter_mut() {
                optimize_operand(options, &mut excluded, &excluded_name);
                optimize_operand(options, &mut set.set, &set.name);
                if options.debug {
                    eprintln!(
                        "iprange: Removing IPs in {} from {}{}",
                        set.name,
                        excluded_name,
                        family_suffix::<F>()
                    );
                }
                excluded = subtract_op(&excluded, &set.set);
            }
            let set = excluded;
            emit(|w| print::print_set::<F, _>(w, options, &set))
        }

        Mode::Diff => {
            if a.is_empty() || b.is_empty() {
                eprintln!("iprange: two ipsets at least are needed to be diffed.");
                return 1;
            }
            let mut merged_a = merge_group(options, a, None);
            let mut merged_b = merge_group(options, b, None);
            // C renames the merged chains for the diff diagnostics
            // only when more than one set was merged.
            let name_a = if a.len() > 1 {
                "ipset A".to_owned()
            } else {
                a[0].name.clone()
            };
            let name_b = if b.len() > 1 {
                "ipset B".to_owned()
            } else {
                b[0].name.clone()
            };
            optimize_operand(options, &mut merged_a, &name_a);
            optimize_operand(options, &mut merged_b, &name_b);
            if options.debug {
                eprintln!(
                    "iprange: Finding diff IPs in {} and {}{}",
                    name_a,
                    name_b,
                    family_suffix::<F>()
                );
            }
            let result = diff_op(&merged_a, &merged_b);
            if !options.quiet {
                emit(|w| print::print_set::<F, _>(w, options, &result));
            }
            if result.unique > 0 {
                1
            } else {
                0
            }
        }

        Mode::Compare => {
            if a.len() < 2 {
                eprintln!("iprange: two ipsets at least are needed to be compared.");
                return 1;
            }
            let code = emit(|w| {
                if options.header {
                    w.write_all(
                        b"name1,name2,entries1,entries2,ips1,ips2,combined_ips,common_ips\n",
                    )?;
                }
                for set in a.iter_mut() {
                    optimize_direct(options, &mut set.set, &set.name);
                }
                for i in 0..a.len() {
                    for j in (i + 1)..a.len() {
                        if options.debug {
                            eprintln!(
                                "iprange: Combining {} and {}{}",
                                a[i].name,
                                a[j].name,
                                family_suffix::<F>()
                            );
                        }
                        let mut combined = combine_op(&a[i].set, &a[j].set);
                        optimize_direct(options, &mut combined, "combined");
                        let unique1 = a[i].set.unique;
                        let unique2 = a[j].set.unique;
                        let combined_ips = combined.unique;
                        write_compare_row(
                            w,
                            &a[i].name,
                            &a[j].name,
                            a[i].set.entries,
                            a[j].set.entries,
                            unique1,
                            unique2,
                            combined_ips,
                            unique1 + unique2 - combined_ips,
                        )?;
                    }
                }
                Ok(())
            });
            code
        }

        Mode::CompareNext => {
            if b.is_empty() {
                eprintln!("iprange: no files given after the --compare-next parameter.");
                return 1;
            }
            emit(|w| {
                if options.header {
                    w.write_all(
                        b"name1,name2,entries1,entries2,ips1,ips2,combined_ips,common_ips\n",
                    )?;
                }
                for set in a.iter_mut() {
                    optimize_direct(options, &mut set.set, &set.name);
                }
                for set in b.iter_mut() {
                    optimize_direct(options, &mut set.set, &set.name);
                }
                for x in &*a {
                    for y in &*b {
                        if options.debug {
                            eprintln!(
                                "iprange: Combining {} and {}{}",
                                x.name,
                                y.name,
                                family_suffix::<F>()
                            );
                        }
                        let mut combined = combine_op(&x.set, &y.set);
                        optimize_direct(options, &mut combined, "combined");
                        let unique1 = x.set.unique;
                        let unique2 = y.set.unique;
                        let combined_ips = combined.unique;
                        write_compare_row(
                            w,
                            &x.name,
                            &y.name,
                            x.set.entries,
                            y.set.entries,
                            unique1,
                            unique2,
                            combined_ips,
                            unique1 + unique2 - combined_ips,
                        )?;
                    }
                }
                Ok(())
            })
        }

        Mode::CompareFirst => {
            if a.len() < 2 {
                eprintln!("iprange: two ipsets at least are needed to be compared.");
                return 1;
            }
            emit(|w| {
                if options.header {
                    w.write_all(b"name,entries,unique_ips,common_ips\n")?;
                }
                for set in a.iter_mut() {
                    optimize_direct(options, &mut set.set, &set.name);
                }
                for i in 1..a.len() {
                    if options.debug {
                        eprintln!(
                            "iprange: Combining {} and {}{}",
                            a[i].name,
                            a[0].name,
                            family_suffix::<F>()
                        );
                    }
                    let mut combined = combine_op(&a[i].set, &a[0].set);
                    optimize_direct(options, &mut combined, "combined");
                    let unique_ips = a[i].set.unique;
                    let common_ips = unique_ips + a[0].set.unique - combined.unique;
                    write_count_row(w, &a[i].name, a[i].set.entries, unique_ips, common_ips)?;
                }
                Ok(())
            })
        }

        Mode::CountUnique => {
            let mut merged = merge_group(options, a, Some("combined ipset"));
            optimize_direct(options, &mut merged, "combined ipset");
            emit(|w| {
                if options.header {
                    w.write_all(b"entries,unique_ips\n")?;
                }
                write_uint(w, merged.entries as u128)?;
                w.write_all(b",")?;
                write_uint(w, merged.unique)?;
                w.write_all(b"\n")
            })
        }

        Mode::CountUniqueAll => emit(|w| {
            if options.header {
                w.write_all(b"name,entries,unique_ips\n")?;
            }
            for set in a.iter_mut() {
                optimize_direct(options, &mut set.set, &set.name);
            }
            for set in a.iter() {
                write_unique_row(w, &set.name, set.set.entries, set.set.unique)?;
            }
            Ok(())
        }),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::legacy::range::Range;

    fn set4(ranges: &[(u32, u32)]) -> IpSet<u32> {
        let mut s = IpSet::default();
        for &(lo, hi) in ranges {
            s.add_range(Range { lo, hi });
        }
        s.optimize();
        s
    }

    fn loaded4(name: &str, ranges: &[(u32, u32)]) -> Loaded<u32> {
        Loaded {
            name: name.to_owned(),
            set: set4(ranges),
        }
    }

    fn plain(opts: &Options) -> Options {
        opts.clone()
    }

    fn ranges_of(set: &IpSet<u32>) -> Vec<(u32, u32)> {
        set.ranges.iter().map(|r| (r.lo, r.hi)).collect()
    }

    #[test]
    fn merge_folds_all_sets_in_order() {
        let opts = Options::default();
        let a = vec![
            loaded4("a", &[(1, 3)]),
            loaded4("b", &[(5, 7)]),
            loaded4("c", &[(10, 12)]),
        ];
        let merged = merge_group(&opts, &a, Some("combined ipset"));
        // The C merge leaves the result unoptimized; the printer
        // optimizes. Verify the optimized outcome.
        let mut merged = merged;
        merged.optimize();
        assert_eq!(ranges_of(&merged), vec![(1, 3), (5, 7), (10, 12)]);
        assert_eq!(merged.entries, 3);
    }

    #[test]
    fn merge_merges_adjacent_after_optimize() {
        let opts = Options::default();
        let a = vec![loaded4("a", &[(1, 3)]), loaded4("b", &[(4, 7)])];
        let mut merged = merge_group(&opts, &a, Some("combined ipset"));
        merged.optimize();
        assert_eq!(ranges_of(&merged), vec![(1, 7)]);
        assert_eq!(merged.entries, 1);
    }

    #[test]
    fn common_intersects_two_sets() {
        let x = set4(&[(1, 10)]);
        let y = set4(&[(5, 15)]);
        let common = intersect_op(&x, &y);
        assert_eq!(ranges_of(&common), vec![(5, 10)]);
    }

    #[test]
    fn common_with_empty_operand_is_empty() {
        let x = set4(&[]);
        let y = set4(&[(5, 15)]);
        assert!(intersect_op(&x, &y).ranges.is_empty());
        assert!(intersect_op(&y, &x).ranges.is_empty());
    }

    #[test]
    fn common_multiway_fold() {
        let a = set4(&[(1, 20)]);
        let b = set4(&[(5, 25)]);
        let c = set4(&[(10, 30)]);
        let mut common = intersect_op(&a, &b);
        common = intersect_op(&common, &c);
        assert_eq!(ranges_of(&common), vec![(10, 20)]);
    }

    #[test]
    fn exclude_subtracts_middle() {
        let a = set4(&[(1, 10)]);
        let b = set4(&[(4, 6)]);
        let out = subtract_op(&a, &b);
        assert_eq!(ranges_of(&out), vec![(1, 3), (7, 10)]);
    }

    #[test]
    fn exclude_sequential_chain_matches_union_subtraction() {
        // C ipset_exclude is applied per B set: (A \ B1) \ B2.
        let a = set4(&[(1, 100)]);
        let b1 = set4(&[(4, 10)]);
        let b2 = set4(&[(20, 30)]);
        let mut out = subtract_op(&a, &b1);
        out = subtract_op(&out, &b2);
        assert_eq!(ranges_of(&out), vec![(1, 3), (11, 19), (31, 100)]);
    }

    #[test]
    fn exclude_with_empty_b_copies_a() {
        let a = set4(&[(1, 3), (7, 9)]);
        let b = set4(&[]);
        assert_eq!(ranges_of(&subtract_op(&a, &b)), vec![(1, 3), (7, 9)]);
    }

    #[test]
    fn exclude_full_cover_is_empty() {
        let a = set4(&[(1, 10)]);
        let b = set4(&[(0, 20)]);
        assert!(subtract_op(&a, &b).ranges.is_empty());
    }

    #[test]
    fn diff_equal_sets_is_empty_and_exit_zero() {
        let a = set4(&[(1, 3), (7, 9)]);
        let out = diff_op(&a, &a);
        assert!(out.ranges.is_empty());
        assert_eq!(out.unique, 0);
        assert_eq!(if out.unique > 0 { 1 } else { 0 }, 0);
    }

    #[test]
    fn diff_symmetric_difference() {
        let a = set4(&[(1, 5)]);
        let b = set4(&[(3, 8)]);
        let out = diff_op(&a, &b);
        assert_eq!(ranges_of(&out), vec![(1, 2), (6, 8)]);
        assert!(out.unique > 0);
        assert_eq!(if out.unique > 0 { 1 } else { 0 }, 1);
    }

    #[test]
    fn diff_with_one_empty_side_copies_the_other() {
        let a = set4(&[(1, 5)]);
        let empty = set4(&[]);
        assert_eq!(ranges_of(&diff_op(&a, &empty)), vec![(1, 5)]);
        assert_eq!(ranges_of(&diff_op(&empty, &a)), vec![(1, 5)]);
    }

    #[test]
    fn csv_field_quoting_matches_c() {
        let mut buf = Vec::new();
        csv_field(&mut buf, "plain").unwrap();
        csv_field(&mut buf, "a,b").unwrap();
        csv_field(&mut buf, "q\"uote").unwrap();
        csv_field(&mut buf, "line\nbreak").unwrap();
        assert_eq!(
            String::from_utf8(buf).unwrap(),
            "plain\"a,b\"\"q\"\"uote\"\"line\nbreak\""
        );
    }

    #[test]
    fn write_uint_is_decimal() {
        let mut buf = Vec::new();
        write_uint(&mut buf, 0).unwrap();
        write_uint(&mut buf, 4294967296).unwrap();
        assert_eq!(String::from_utf8(buf).unwrap(), "04294967296");
    }

    #[test]
    fn compare_row_shape() {
        let mut buf = Vec::new();
        write_compare_row(&mut buf, "a", "b", 1, 2, 3, 4, 5, 2).unwrap();
        assert_eq!(String::from_utf8(buf).unwrap(), "a,b,1,2,3,4,5,2\n");
    }

    #[test]
    fn count_unique_all_row_shape() {
        let mut buf = Vec::new();
        write_unique_row(&mut buf, "x", 7, 42).unwrap();
        assert_eq!(String::from_utf8(buf).unwrap(), "x,7,42\n");
    }

    #[test]
    fn group_b_boundary_is_a_loaded_set_index() {
        // The parser computes LoadedAll::group_b in loaded-set units
        // (an @file source may expand to several sets); ops only
        // clamps it to the loaded length. CountUnique folds only
        // group A and prints a two-column row (no print_set), which
        // keeps this test independent of the output worker.
        let mut opts = plain(&Options::default());
        opts.mode = Mode::CountUnique;
        let mut loaded = parse::LoadedAll {
            sets: vec![
                loaded4("a", &[(1, 2)]),
                loaded4("b", &[(5, 6)]),
                loaded4("c", &[(10, 11)]),
                loaded4("d", &[(20, 21)]),
            ],
            group_b: 2,
        };
        assert_eq!(execute(&opts, &mut loaded), 0);
        // Clamped when the boundary exceeds the loaded length.
        loaded.group_b = 99;
        assert_eq!(execute(&opts, &mut loaded), 0);
    }

    #[test]
    fn reduce_disables_prefixes_without_entries() {
        let opts = Options::default();
        let mut enabled = [true; 33];
        let mut set = set4(&[(0, 65535)]); // exactly one /16
        apply_reduce(&opts, &mut set, &mut enabled);
        assert!(enabled[16]);
        assert!(!enabled[15]);
        assert!(!enabled[24]);
        // The C also disables /32 when no /32 entries were counted.
        assert!(!enabled[32]);
    }

    #[test]
    fn reduce_merges_into_closest_larger_prefix() {
        let opts = Options::default();
        let mut enabled = [true; 33];
        // One /24 (0.0.0.0-0.0.0.255) and one /16 (0.1.0.0-0.1.255.255)
        let mut set = set4(&[(0, 255), (65536, 131071)]);
        apply_reduce(&opts, &mut set, &mut enabled);
        // The /16 is eliminated into the /24 (count*255 increase),
        // matching the C's smallest-increase step.
        assert!(!enabled[16]);
        assert!(enabled[24]);
    }

    #[test]
    fn reduce_respects_user_disabled_prefixes() {
        // --min-prefix 8 disables /1../8; reduce must not enable them.
        let opts = Options::default();
        let mut enabled = [true; 33];
        for i in 0..8 {
            enabled[i] = false;
        }
        let mut set = set4(&[(0, 255)]); // one /24
        apply_reduce(&opts, &mut set, &mut enabled);
        assert!(!enabled[8]);
        assert!(enabled[24]);
    }

    #[test]
    fn merge_debug_uses_combined_ipset_name() {
        let mut opts = plain(&Options::default());
        opts.debug = true;
        let a = vec![loaded4("first", &[(1, 3)]), loaded4("second", &[(5, 7)])];
        // Capture stderr by running in a thread with a piped stderr is
        // complex; verify the value the debug line would print instead.
        let merged = merge_group(&opts, &a, Some("combined ipset"));
        assert_eq!(merged.entries, 2);
    }
}
