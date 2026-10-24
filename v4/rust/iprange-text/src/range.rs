//! Legacy interval-set core: closed address ranges and the exact
//! ipset merge/optimize semantics of the released C implementation.
//!
//! Everything here is family-generic over [`IpNum`]; the C code
//! duplicated these rules for IPv4 (u32) and IPv6 (u128), the Rust
//! port keeps one authoritative implementation (SOW-0028, legacy
//! contract: language-local adapter, no v4 persistence logic).

/// A closed address-space region with native-width arithmetic.
pub trait IpNum: Copy + Clone + Eq + Ord + std::fmt::Debug + Send + Sync {
    /// Address width in bits (32 or 128).
    const BITS: u32;
    /// The maximum address of the family.
    const MAX: Self;

    /// Numeric value (0 ..= 2^BITS - 1) as u128.
    fn as_u128(self) -> u128;
    /// Build from a value that fits the family width.
    fn from_u128(v: u128) -> Self;
    /// True when this is the family maximum (adjacency guard).
    fn is_max(self) -> bool {
        self == Self::MAX
    }
    /// Next address; None at the family maximum (no wrap).
    fn inc(self) -> Option<Self> {
        if self.is_max() {
            None
        } else {
            Some(Self::from_u128(self.as_u128() + 1))
        }
    }
    /// Previous address; None at zero (no wrap).
    fn dec(self) -> Option<Self> {
        let v = self.as_u128();
        if v == 0 {
            None
        } else {
            Some(Self::from_u128(v - 1))
        }
    }
}

impl IpNum for u32 {
    const BITS: u32 = 32;
    const MAX: Self = u32::MAX;
    #[inline]
    fn as_u128(self) -> u128 {
        self as u128
    }
    #[inline]
    fn from_u128(v: u128) -> Self {
        v as u32
    }
}

impl IpNum for u128 {
    const BITS: u32 = 128;
    const MAX: Self = u128::MAX;
    #[inline]
    fn as_u128(self) -> u128 {
        self
    }
    #[inline]
    fn from_u128(v: u128) -> Self {
        v
    }
}

/// One closed range `[lo, hi]` with `lo <= hi` always true.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Range<T: IpNum> {
    pub lo: T,
    pub hi: T,
}

impl<T: IpNum> Range<T> {
    /// Number of addresses covered.
    ///
    /// The IPv6 universe holds 2^128 addresses, one more than `u128` can
    /// carry, so the count saturates at `u128::MAX` instead of overflowing:
    /// a plain addition panics in a debug build and wraps to zero in a
    /// release build, and both differ from the released C tool, whose
    /// bookkeeping saturates the same way (`ipset6_added_entry()` in
    /// `ipset6.h`: "2^128 doesn't fit in uint128_t, saturate at max").
    /// Every count consumer adds these sizes with `saturating_add()` for the
    /// 128-bit family, so a set of disjoint ranges that together exceed
    /// `u128::MAX` reports `u128::MAX` exactly as C does. Counts that are
    /// representable — including the whole IPv4 universe at 2^32 — are
    /// unaffected.
    pub fn size(self) -> u128 {
        self.hi
            .as_u128()
            .saturating_sub(self.lo.as_u128())
            .saturating_add(1)
    }
}

/// An ipset: an ordered/optimized-flagged collection of closed ranges
/// with the line-count and unique-IP bookkeeping of the C `ipset`.
///
/// The C load path appends entries in input order and tracks an
/// "optimized" flag exactly like this struct: adjacency merges into
/// the last range while everything else either appends cleanly or
/// clears the flag (the set then needs a full optimize pass before it
/// is trustworthy). Every output/count consumer optimizes first.
#[derive(Clone, Debug)]
pub struct IpSet<T: IpNum> {
    pub ranges: Vec<Range<T>>,
    /// Number of stored ranges (C `entries`).
    pub entries: usize,
    /// Input-line bookkeeping (C `lines`); feeds binary headers and
    /// `-v` totals only, never the CSV counts.
    pub lines: usize,
    /// Unique IP count; v4 accumulates into u128 without overflow and
    /// v6 saturates at `MAX` (full-universe representation limit).
    pub unique: u128,
    /// True when the ranges are known merged (sorted, non-overlapping,
    /// non-adjacent). Set only by [`IpSet::optimize`]; the load append
    /// path flips it off on any disorder.
    pub optimized: bool,
}

impl<T: IpNum> Default for IpSet<T> {
    fn default() -> Self {
        IpSet {
            ranges: Vec::new(),
            entries: 0,
            lines: 0,
            unique: 0,
            optimized: true,
        }
    }
}

impl<T: IpNum> IpSet<T> {
    /// Add one closed range without merging (C `ipset_add_ip_range`).
    ///
    /// Mirrors `ipset_added_entry` semantics: adjacency after the last
    /// range merges while the set stays optimized; any other disorder
    /// is appended and clears the optimized flag. The unique counter
    /// is incremented first for every added range (the C order), which
    /// transiently over-counts contained/duplicate appends; every
    /// consumer re-optimizes before reporting, so the transient value
    /// is not observable (mirrored for oracle fidelity).
    pub fn add_range(&mut self, range: Range<T>) {
        self.unique = match T::BITS {
            128 => self.unique.saturating_add(range.size()),
            _ => self.unique + range.size(),
        };
        if self.optimized && !self.ranges.is_empty() {
            let last = self.ranges.last().unwrap();
            if last.hi.is_max() {
                // Nothing can be adjacent to the family maximum.
            } else if range.lo == last.hi.inc().unwrap() {
                // Adjacent: merge into the last range, entries unchanged.
                self.ranges.last_mut().unwrap().hi = range.hi;
                return;
            } else if range.lo > last.hi {
                self.ranges.push(range);
                self.entries += 1;
                return;
            }
            // Overlap, duplicate, contained, or before: not optimized.
            self.optimized = false;
            self.ranges.push(range);
            self.entries += 1;
        } else {
            self.ranges.push(range);
            self.entries += 1;
        }
    }

    /// Sort and merge per the C `ipset_optimize` sweep:
    /// sort by lo ascending, hi descending; then merge containment,
    /// overlap, and guarded adjacency; recompute `unique` and set the
    /// optimized flag. `lines` is preserved.
    pub fn optimize(&mut self) {
        self.ranges
            .sort_by(|a, b| a.lo.cmp(&b.lo).then_with(|| b.hi.cmp(&a.hi)));
        let mut out: Vec<Range<T>> = Vec::with_capacity(self.ranges.len());
        let mut unique: u128 = 0;
        for range in self.ranges.drain(..) {
            if let Some(last) = out.last_mut() {
                if range.hi <= last.hi {
                    // Contained (including duplicates): skip entirely.
                    continue;
                }
                let adjacent = !last.hi.is_max() && range.lo == last.hi.inc().unwrap();
                if range.lo <= last.hi || adjacent {
                    // Overlap or adjacency: extend.
                    last.hi = range.hi;
                    continue;
                }
                unique = match T::BITS {
                    128 => unique.saturating_add(last.size()),
                    _ => unique + last.size(),
                };
                out.push(range);
            } else {
                out.push(range);
            }
        }
        if let Some(last) = out.last() {
            unique = match T::BITS {
                128 => unique.saturating_add(last.size()),
                _ => unique + last.size(),
            };
        }
        self.ranges = out;
        self.entries = self.ranges.len();
        self.unique = unique;
        self.optimized = true;
    }

    /// Append every range of `other` (C `ipset_merge`): concatenates,
    /// adds its lines, and clears the optimized flag. The caller runs
    /// [`IpSet::optimize`] afterwards.
    pub fn merge_from(&mut self, other: &IpSet<T>) {
        self.ranges.extend(other.ranges.iter().copied());
        self.entries += other.entries;
        self.lines += other.lines;
        self.unique = match T::BITS {
            128 => self.unique.saturating_add(other.unique),
            _ => self.unique + other.unique,
        };
        self.optimized = false;
    }
}

#[cfg(test)]
mod tests {
    use super::{IpSet, Range};

    const V6_MAX: u128 = u128::MAX;

    fn set6(ranges: &[(u128, u128)]) -> IpSet<u128> {
        let mut set = IpSet::default();
        for &(lo, hi) in ranges {
            set.add_range(Range { lo, hi });
        }
        set
    }

    /// The whole IPv6 universe holds 2^128 addresses, one more than `u128`
    /// can carry. The count of it saturates at the family maximum, which is
    /// what the released C tool reports for `::/0` (`ipset6_added_entry()`
    /// in `ipset6.h`, verified against the C binary): an overflowing addition
    /// panics in a debug build and wraps to zero in a release build, and both
    /// answers are wrong for the same input.
    #[test]
    fn size_of_the_full_ipv6_universe_saturates() {
        assert_eq!(
            Range {
                lo: 0u128,
                hi: V6_MAX
            }
            .size(),
            V6_MAX
        );
    }

    /// Saturating the one unrepresentable count must not disturb the counts
    /// that do fit: the half universe and a single address stay exact, and so
    /// does the whole IPv4 universe at 2^32, which `u128` holds easily.
    #[test]
    fn representable_sizes_stay_exact() {
        // `::/1` is the closed range `[0, 2^127 - 1]`, which holds 2^127
        // addresses; the same top address one range lower would hold one more.
        assert_eq!(
            Range {
                lo: 0u128,
                hi: (1 << 127) - 1
            }
            .size(),
            1 << 127
        );
        assert_eq!(
            Range {
                lo: V6_MAX,
                hi: V6_MAX
            }
            .size(),
            1
        );
        assert_eq!(
            Range {
                lo: 0u32,
                hi: u32::MAX
            }
            .size(),
            4_294_967_296
        );
    }

    /// Adding the full universe books `entries` and `unique` exactly as the C
    /// load path does, before any optimize sweep has run.
    #[test]
    fn adding_the_full_ipv6_universe_books_the_c_counts() {
        let set = set6(&[(0, V6_MAX)]);
        assert_eq!((set.entries, set.unique), (1, V6_MAX));
    }

    /// Two disjoint ranges that together cover the universe cannot be summed
    /// in `u128`; the sweep merges them into one entry and reports the family
    /// maximum, the count `iprange -6 -C` prints for that input.
    #[test]
    fn optimizing_a_split_universe_saturates_like_c() {
        let mut set = set6(&[(0, V6_MAX - 1), (V6_MAX, V6_MAX)]);
        set.optimize();
        assert_eq!((set.entries, set.unique), (1, V6_MAX));
        assert_eq!(
            set.ranges,
            vec![Range {
                lo: 0u128,
                hi: V6_MAX
            }]
        );
    }

    /// The per-add counter and the sweep counter must agree at the saturation
    /// point: a consumer may report either one, depending on whether the set
    /// was optimized before the count was taken.
    #[test]
    fn adding_then_optimizing_agree_at_the_saturation_point() {
        let mut set = set6(&[(0, V6_MAX), (0, V6_MAX)]);
        assert_eq!(set.unique, V6_MAX);
        set.optimize();
        assert_eq!((set.entries, set.unique), (1, V6_MAX));
    }
}
