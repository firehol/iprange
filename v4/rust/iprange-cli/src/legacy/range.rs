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
    /// Number of addresses covered, as u128 (2^BITS - 1 max for v6
    /// full universe; v4 maximum is 2^32 which always fits u128).
    pub fn size(self) -> u128 {
        self.hi.as_u128() - self.lo.as_u128() + 1
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
