//! Interval algebra: the mathematical foundation for correct migration,
//! extsort normalization, and feed-bit range operations.
//!
//! All operations work on sorted, disjoint interval sequences keyed by `from`.
//! The core primitive is a **sweep-line merge** that walks two sequences in
//! lock-step, splitting at every boundary to produce exact coverage segments.

use crate::key::IpKey;
use alloc::vec::Vec;

/// A segment produced by the sweep-line diff between two interval sequences.
/// Each segment represents a maximal range where old and desired agree or differ.
#[derive(Clone, Copy, Debug)]
pub struct DiffSegment<K: IpKey> {
    pub from: K,
    pub to: K,
    pub old_scope: Option<u32>,  // None = not in old
    pub desired_scope: Option<u32>, // None = not in desired
}

impl<K: IpKey> DiffSegment<K> {
    /// What changed in this segment.
    pub fn kind(&self) -> SegmentKind {
        match (self.old_scope, self.desired_scope) {
            (None, Some(_)) => SegmentKind::Added,
            (Some(_), None) => SegmentKind::Removed,
            (Some(o), Some(d)) if o == d => SegmentKind::Unchanged,
            (Some(_), Some(_)) => SegmentKind::Changed,
            (None, None) => SegmentKind::Unchanged, // impossible but safe
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SegmentKind {
    Added,
    Removed,
    Changed,
    Unchanged,
}

// ──────────────────────────────────────────────────────────────────────────
// Sweep-line diff: the core algorithm
// ──────────────────────────────────────────────────────────────────────────

/// Compute the exact diff between two sorted, disjoint interval sequences.
///
/// Produces a list of maximal segments where old and desired coverage agrees
/// or differs. Each segment is either:
/// - Added (in desired, not in old)
/// - Removed (in old, not in desired)
/// - Changed (in both, different scope)
/// - Unchanged (in both, same scope)
///
/// This handles ALL overlap cases: identical, partial, one-to-many, many-to-one,
/// complete separation, and boundary adjacency.
///
/// **This is the algorithm that fixes blocker #3.**
pub fn interval_diff<K: IpKey>(
    old: &[(K, K, u32)],
    desired: &[(K, K, u32)],
) -> Vec<DiffSegment<K>> {
    let mut segments = Vec::new();
    let mut oi = 0usize; // old cursor
    let mut di = 0usize; // desired cursor

    // Trimmed views: when a partial overlap consumes part of an interval,
    // we advance the cursor's effective start without advancing the array index.
    let mut old_from = old.first().map(|r| r.0);
    let mut des_from = desired.first().map(|r| r.0);

    loop {
        let old_cur = if oi < old.len() {
            Some((old_from.unwrap(), old[oi].1, old[oi].2))
        } else {
            None
        };
        let des_cur = if di < desired.len() {
            Some((des_from.unwrap(), desired[di].1, desired[di].2))
        } else {
            None
        };

        match (old_cur, des_cur) {
            (None, None) => break,

            (Some((of, ot, os)), None) => {
                // Only old remains → removed
                segments.push(DiffSegment { from: of, to: ot, old_scope: Some(os), desired_scope: None });
                oi += 1;
                if oi < old.len() { old_from = Some(old[oi].0); }
            }

            (None, Some((df, dt, ds))) => {
                // Only desired remains → added
                segments.push(DiffSegment { from: df, to: dt, old_scope: None, desired_scope: Some(ds) });
                di += 1;
                if di < desired.len() { des_from = Some(desired[di].0); }
            }

            (Some((of, ot, os)), Some((df, dt, ds))) => {
                if ot < df {
                    // Old entirely before desired → removed
                    segments.push(DiffSegment { from: of, to: ot, old_scope: Some(os), desired_scope: None });
                    oi += 1;
                    if oi < old.len() { old_from = Some(old[oi].0); }
                } else if dt < of {
                    // Desired entirely before old → added
                    segments.push(DiffSegment { from: df, to: dt, old_scope: None, desired_scope: Some(ds) });
                    di += 1;
                    if di < desired.len() { des_from = Some(desired[di].0); }
                } else {
                    // Overlap! Split at boundaries.
                    let _seg_start = if of < df { of } else { df };

                    // Emit any old-only prefix [of, df-1]
                    if of < df {
                        let prefix_end = df.checked_dec().unwrap_or(df);
                        segments.push(DiffSegment {
                            from: of, to: prefix_end,
                            old_scope: Some(os), desired_scope: None,
                        });
                    }

                    // Emit any desired-only prefix [df, of-1]
                    if df < of {
                        let prefix_end = of.checked_dec().unwrap_or(of);
                        segments.push(DiffSegment {
                            from: df, to: prefix_end,
                            old_scope: None, desired_scope: Some(ds),
                        });
                    }

                    // Now both start at the same point: overlap_start
                    let overlap_start = if of < df { df } else { of };

                    if ot == dt {
                        // Same end → emit the overlap segment, advance both
                        segments.push(DiffSegment {
                            from: overlap_start, to: ot,
                            old_scope: Some(os), desired_scope: Some(ds),
                        });
                        oi += 1;
                        di += 1;
                        if oi < old.len() { old_from = Some(old[oi].0); }
                        if di < desired.len() { des_from = Some(desired[di].0); }
                    } else if ot < dt {
                        // Old ends first → overlap is [overlap_start, ot]
                        segments.push(DiffSegment {
                            from: overlap_start, to: ot,
                            old_scope: Some(os), desired_scope: Some(ds),
                        });
                        // Advance old, trim desired's start to ot+1
                        oi += 1;
                        if oi < old.len() { old_from = Some(old[oi].0); }
                        des_from = ot.checked_inc();
                        if des_from.is_none() {
                            // Desired's start overflows → desired is fully consumed
                            di += 1;
                        }
                    } else {
                        // Desired ends first → overlap is [overlap_start, dt]
                        segments.push(DiffSegment {
                            from: overlap_start, to: dt,
                            old_scope: Some(os), desired_scope: Some(ds),
                        });
                        // Advance desired, trim old's start to dt+1
                        di += 1;
                        if di < desired.len() { des_from = Some(desired[di].0); }
                        old_from = dt.checked_inc();
                        if old_from.is_none() {
                            oi += 1;
                        }
                    }
                }
            }
        }
    }

    // Merge adjacent segments with the same (old_scope, desired_scope) pair.
    merge_adjacent_segments(&mut segments);
    segments
}

/// Merge adjacent segments where old_scope and desired_scope both match.
fn merge_adjacent_segments<K: IpKey>(segs: &mut Vec<DiffSegment<K>>) {
    if segs.len() <= 1 { return; }
    let mut out: Vec<DiffSegment<K>> = Vec::with_capacity(segs.len());
    out.push(segs[0]);
    for &curr in segs.iter().skip(1) {
        let last = out.len() - 1;
        let prev = out[last];
        let adjacent = prev.to.checked_inc() == Some(curr.from);
        if adjacent && prev.old_scope == curr.old_scope && prev.desired_scope == curr.desired_scope {
            out[last].to = curr.to;
        } else {
            out.push(curr);
        }
    }
    *segs = out;
}

// ──────────────────────────────────────────────────────────────────────────
// Normalize: split overlapping intervals into disjoint segments
// ──────────────────────────────────────────────────────────────────────────

/// A coverage segment from normalizing overlapping input.
#[derive(Clone, Debug)]
pub struct CoverageSegment<K: IpKey> {
    pub from: K,
    pub to: K,
    /// All scope_ids that cover this segment (one entry per overlapping input record).
    pub scopes: Vec<u32>,
}

/// Normalize a sequence of (possibly overlapping) intervals into disjoint
/// coverage segments. Each segment lists ALL scope_ids that cover it.
///
/// **This fixes blocker #4:** overlapping input is properly split.
///
/// Example:
///   Input:  [(10, 20, A), (15, 25, B)]
///   Output: [(10, 14, [A]), (15, 20, [A, B]), (21, 25, [B])]
pub fn normalize_overlapping<K: IpKey>(
    input: &[(K, K, u32)],
) -> Vec<CoverageSegment<K>> {
    if input.is_empty() { return Vec::new(); }

    // Collect all boundary points.
    let mut boundaries: Vec<K> = Vec::new();
    for &(f, t, _) in input {
        boundaries.push(f);
        if let Some(after) = t.checked_inc() {
            boundaries.push(after);
        }
    }
    boundaries.sort();
    boundaries.dedup();

    // For each consecutive pair of boundaries, find all covering scopes.
    let mut segments: Vec<CoverageSegment<K>> = Vec::new();
    for i in 0..boundaries.len().saturating_sub(1) {
        let seg_from = boundaries[i];
        // The segment end is the next boundary - 1, or boundaries[i+1] - 1.
        // But we need to be careful with the boundary semantics.
        // boundaries[i] is a start point; the segment goes until boundaries[i+1]-1.
        let seg_to = boundaries[i + 1].checked_dec().unwrap_or(boundaries[i + 1]);
        if seg_from > seg_to { continue; }

        let mut scopes: Vec<u32> = Vec::new();
        for &(f, t, s) in input {
            if f <= seg_from && t >= seg_to {
                scopes.push(s);
            }
        }
        if !scopes.is_empty() {
            // Merge with previous segment if same scope set and adjacent.
            if let Some(last) = segments.last_mut() {
                let adjacent = last.to.checked_inc() == Some(seg_from);
                if adjacent && last.scopes == scopes {
                    last.to = seg_to;
                    continue;
                }
            }
            segments.push(CoverageSegment { from: seg_from, to: seg_to, scopes });
        }
    }

    segments
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::key::Ipv4Key;

    fn r(f: u32, t: u32, s: u32) -> (Ipv4Key, Ipv4Key, u32) {
        (Ipv4Key(f), Ipv4Key(t), s)
    }

    // ── interval_diff tests ──

    #[test]
    fn diff_empty_to_full() {
        let old: Vec<(Ipv4Key, Ipv4Key, u32)> = vec![];
        let desired = vec![r(10, 20, 1)];
        let segs = interval_diff(&old, &desired);
        assert_eq!(segs.len(), 1);
        assert_eq!(segs[0].kind(), SegmentKind::Added);
    }

    #[test]
    fn diff_full_to_empty() {
        let old = vec![r(10, 20, 1)];
        let desired: Vec<(Ipv4Key, Ipv4Key, u32)> = vec![];
        let segs = interval_diff(&old, &desired);
        assert_eq!(segs.len(), 1);
        assert_eq!(segs[0].kind(), SegmentKind::Removed);
    }

    #[test]
    fn diff_identical() {
        let old = vec![r(10, 20, 1)];
        let desired = vec![r(10, 20, 1)];
        let segs = interval_diff(&old, &desired);
        assert_eq!(segs.len(), 1);
        assert_eq!(segs[0].kind(), SegmentKind::Unchanged);
    }

    #[test]
    fn diff_change_scope() {
        let old = vec![r(10, 20, 1)];
        let desired = vec![r(10, 20, 2)];
        let segs = interval_diff(&old, &desired);
        assert_eq!(segs.len(), 1);
        assert_eq!(segs[0].kind(), SegmentKind::Changed);
    }

    #[test]
    fn diff_partial_overlap_old_extends() {
        // old: [10-20], desired: [10-15]
        // Expected: [10-15] unchanged, [16-20] removed
        let old = vec![r(10, 20, 1)];
        let desired = vec![r(10, 15, 1)];
        let segs = interval_diff(&old, &desired);
        eprintln!("segs: {:?}", segs);
        // Should produce: unchanged [10-15], removed [16-20]
        assert_eq!(segs.len(), 2);
        assert_eq!(segs[0].kind(), SegmentKind::Unchanged);
        assert_eq!(segs[0].from, Ipv4Key(10));
        assert_eq!(segs[0].to, Ipv4Key(15));
        assert_eq!(segs[1].kind(), SegmentKind::Removed);
        assert_eq!(segs[1].from, Ipv4Key(16));
        assert_eq!(segs[1].to, Ipv4Key(20));
    }

    #[test]
    fn diff_partial_overlap_desired_extends() {
        // old: [10-15], desired: [10-20]
        // Expected: [10-15] unchanged, [16-20] added
        let old = vec![r(10, 15, 1)];
        let desired = vec![r(10, 20, 1)];
        let segs = interval_diff(&old, &desired);
        eprintln!("segs: {:?}", segs);
        assert_eq!(segs.len(), 2);
        assert_eq!(segs[0].kind(), SegmentKind::Unchanged);
        assert_eq!(segs[1].kind(), SegmentKind::Added);
        assert_eq!(segs[1].from, Ipv4Key(16));
        assert_eq!(segs[1].to, Ipv4Key(20));
    }

    #[test]
    fn diff_one_to_many() {
        // old: [10-30], desired: [10-15], [20-30]
        // Expected: [10-15] unchanged, [16-19] removed, [20-30] unchanged
        let old = vec![r(10, 30, 1)];
        let desired = vec![r(10, 15, 1), r(20, 30, 1)];
        let segs = interval_diff(&old, &desired);
        eprintln!("segs: {:?}", segs);
        assert_eq!(segs.len(), 3);
        assert_eq!(segs[0].kind(), SegmentKind::Unchanged); // [10-15]
        assert_eq!(segs[1].kind(), SegmentKind::Removed);   // [16-19]
        assert_eq!(segs[1].from, Ipv4Key(16));
        assert_eq!(segs[1].to, Ipv4Key(19));
        assert_eq!(segs[2].kind(), SegmentKind::Unchanged); // [20-30]
    }

    #[test]
    fn diff_many_to_one() {
        // old: [10-15], [20-30], desired: [10-30]
        // Expected: [10-15] unchanged, [16-19] added, [20-30] unchanged
        let old = vec![r(10, 15, 1), r(20, 30, 1)];
        let desired = vec![r(10, 30, 1)];
        let segs = interval_diff(&old, &desired);
        eprintln!("segs: {:?}", segs);
        assert_eq!(segs.len(), 3);
        assert_eq!(segs[0].kind(), SegmentKind::Unchanged);
        assert_eq!(segs[1].kind(), SegmentKind::Added);
        assert_eq!(segs[1].from, Ipv4Key(16));
        assert_eq!(segs[1].to, Ipv4Key(19));
        assert_eq!(segs[2].kind(), SegmentKind::Unchanged);
    }

    #[test]
    fn diff_disjoint() {
        // old: [10-20], desired: [30-40]
        // Expected: [10-20] removed, [30-40] added
        let old = vec![r(10, 20, 1)];
        let desired = vec![r(30, 40, 1)];
        let segs = interval_diff(&old, &desired);
        eprintln!("segs: {:?}", segs);
        assert_eq!(segs.len(), 2);
        assert_eq!(segs[0].kind(), SegmentKind::Removed);
        assert_eq!(segs[1].kind(), SegmentKind::Added);
    }

    #[test]
    fn diff_overlapping_different_scope() {
        // old: [10-20] scope=1, desired: [15-25] scope=2
        // Expected: [10-14] removed(1), [15-20] changed(1→2), [21-25] added(2)
        let old = vec![r(10, 20, 1)];
        let desired = vec![r(15, 25, 2)];
        let segs = interval_diff(&old, &desired);
        eprintln!("segs: {:?}", segs);
        assert_eq!(segs.len(), 3);
        assert_eq!(segs[0].kind(), SegmentKind::Removed);
        assert_eq!(segs[0].from, Ipv4Key(10));
        assert_eq!(segs[0].to, Ipv4Key(14));
        assert_eq!(segs[1].kind(), SegmentKind::Changed);
        assert_eq!(segs[1].from, Ipv4Key(15));
        assert_eq!(segs[1].to, Ipv4Key(20));
        assert_eq!(segs[2].kind(), SegmentKind::Added);
        assert_eq!(segs[2].from, Ipv4Key(21));
        assert_eq!(segs[2].to, Ipv4Key(25));
    }

    // ── normalize_overlapping tests ──

    #[test]
    fn normalize_no_overlap() {
        let input = vec![r(10, 20, 1), r(30, 40, 2)];
        let segs = normalize_overlapping(&input);
        assert_eq!(segs.len(), 2);
        assert_eq!(segs[0].scopes, vec![1]);
        assert_eq!(segs[1].scopes, vec![2]);
    }

    #[test]
    fn normalize_partial_overlap() {
        let input = vec![r(10, 20, 1), r(15, 25, 2)];
        let segs = normalize_overlapping(&input);
        eprintln!("normalize: {:?}", segs);
        assert_eq!(segs.len(), 3);
        assert_eq!(segs[0].from, Ipv4Key(10));
        assert_eq!(segs[0].to, Ipv4Key(14));
        assert_eq!(segs[0].scopes, vec![1]);
        assert_eq!(segs[1].from, Ipv4Key(15));
        assert_eq!(segs[1].to, Ipv4Key(20));
        assert_eq!(segs[1].scopes, vec![1, 2]);
        assert_eq!(segs[2].from, Ipv4Key(21));
        assert_eq!(segs[2].to, Ipv4Key(25));
        assert_eq!(segs[2].scopes, vec![2]);
    }

    #[test]
    fn normalize_full_containment() {
        let input = vec![r(10, 30, 1), r(15, 25, 2)];
        let segs = normalize_overlapping(&input);
        eprintln!("normalize: {:?}", segs);
        assert_eq!(segs.len(), 3);
        assert_eq!(segs[0].scopes, vec![1]);          // [10-14]
        assert_eq!(segs[1].scopes, vec![1, 2]);       // [15-25]
        assert_eq!(segs[2].scopes, vec![1]);          // [26-30]
    }

    #[test]
    fn normalize_triple_overlap() {
        let input = vec![r(10, 30, 1), r(15, 25, 2), r(20, 35, 3)];
        let segs = normalize_overlapping(&input);
        eprintln!("normalize: {:?}", segs);
        // [10-14]=1, [15-19]=1+2, [20-25]=1+2+3, [26-30]=1+3, [31-35]=3
        assert_eq!(segs.len(), 5);
        assert_eq!(segs[0].scopes, vec![1]);
        assert_eq!(segs[1].scopes, vec![1, 2]);
        assert_eq!(segs[2].scopes, vec![1, 2, 3]);
        assert_eq!(segs[3].scopes, vec![1, 3]);
        assert_eq!(segs[4].scopes, vec![3]);
    }
}
