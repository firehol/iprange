//! Ordered **cursor** over a validated v4 image (§v4.1.A) and the **standard SDK
//! helpers** built on it (§v4.1.B).
//!
//! The cursor is read-only: it navigates the structure that [`Reader::open`] already
//! validated, so it never reads out of bounds, never loops, never panics — it only walks
//! a known-good tree. There are no leaf sibling pointers (D3), so the cursor keeps a
//! root→leaf **path stack** (a fixed `[Frame; TREE_HEIGHT_MAX]`, no heap) and re-descends
//! at leaf boundaries.
//!
//! **`seek(key)`** positions at the *successor* — the first record with `from >= key`.
//! **`next`/`prev`** step in key order; `current` reads the positioned record. The cursor
//! is `Copy`, so callers (and the helpers here) can snapshot a position cheaply.
//!
//! Helpers take a **selector predicate** `match(scope) -> bool` over the opaque `scope`
//! bytes — the engine never interprets `scope` (D11) — and stream results to a **visitor**
//! returning [`ControlFlow`]. `query_ranges[_merged]` / `query_cidrs[_merged]` emit;
//! `count_ips` / `count_cidrs` tally. CIDR output is the canonical minimal cover.

use core::marker::PhantomData;
use core::ops::ControlFlow;

use crate::error::Result;
use crate::key::IpKey;
use crate::node::{BranchView, LeafView};
use crate::reader::Reader;
use crate::spec::TREE_HEIGHT_MAX;
use crate::wire::PageHeader;

/// One level of the root→leaf path: the page and the chosen index within it (a child
/// index in a branch, a record index in the leaf).
#[derive(Clone, Copy, Debug)]
struct Frame {
    pgno: u32,
    idx: u32,
}

/// Where the cursor sits relative to the records, in key order.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum State {
    /// The tree is empty (`root_pgno == 0`).
    Empty,
    /// Before the first record (`next` → first; `prev` → stays).
    BeforeFirst,
    /// Positioned at a record (`current` returns it).
    At,
    /// Past the last record (`prev` → last; `next` → stays).
    AfterLast,
}

/// An ordered cursor over a validated [`Reader`] (§v4.1.A). Construct with
/// [`Reader::cursor`]. `Copy`, so a position can be snapshotted by value.
#[derive(Clone, Copy, Debug)]
pub struct Cursor<'r, 'a, K: IpKey> {
    reader: &'r Reader<'a>,
    path: [Frame; TREE_HEIGHT_MAX as usize],
    state: State,
    _k: PhantomData<K>,
}

impl<'r, 'a, K: IpKey> Cursor<'r, 'a, K> {
    /// Build a cursor (caller-checked family). Starts unpositioned (`BeforeFirst`, or
    /// `Empty` if the tree has no records).
    pub(crate) fn new(reader: &'r Reader<'a>) -> Self {
        let state = if reader.root_pgno() == 0 {
            State::Empty
        } else {
            State::BeforeFirst
        };
        Cursor {
            reader,
            path: [Frame { pgno: 0, idx: 0 }; TREE_HEIGHT_MAX as usize],
            state,
            _k: PhantomData,
        }
    }

    // --- node access (over already-validated pages) ---

    #[inline]
    fn leaf(&self, pgno: u32) -> LeafView<'a, K> {
        let page = self.reader.page_bytes(pgno);
        let count = PageHeader::decode(page).entry_count as usize;
        LeafView::new(page, count)
    }

    #[inline]
    fn branch(&self, pgno: u32) -> BranchView<'a, K> {
        let page = self.reader.page_bytes(pgno);
        let count = PageHeader::decode(page).entry_count as usize;
        BranchView::new(page, count)
    }

    #[inline]
    fn leaf_level(&self) -> usize {
        // tree_height >= 1 whenever the tree is non-empty (root_pgno != 0).
        self.reader.tree_height() as usize - 1
    }

    // --- positioning ---

    /// Fill `path[level..=leaf_level]` by taking the leftmost child down to a leaf,
    /// positioning at its first record.
    fn descend_leftmost(&mut self, mut level: usize, mut pgno: u32) {
        let leaf_level = self.leaf_level();
        loop {
            if level == leaf_level {
                self.path[level] = Frame { pgno, idx: 0 };
                return;
            }
            self.path[level] = Frame { pgno, idx: 0 };
            pgno = self.branch(pgno).child(0);
            level += 1;
        }
    }

    /// Fill `path[level..=leaf_level]` by taking the rightmost child down to a leaf,
    /// positioning at its last record.
    fn descend_rightmost(&mut self, mut level: usize, mut pgno: u32) {
        let leaf_level = self.leaf_level();
        loop {
            if level == leaf_level {
                let last = (self.leaf(pgno).len() as u32).saturating_sub(1);
                self.path[level] = Frame { pgno, idx: last };
                return;
            }
            let b = self.branch(pgno);
            let last = b.child_count() - 1;
            self.path[level] = Frame {
                pgno,
                idx: last as u32,
            };
            pgno = b.child(last);
            level += 1;
        }
    }

    /// Position at the first record (key order). `false` if the tree is empty.
    pub fn first(&mut self) -> bool {
        if self.state == State::Empty {
            return false;
        }
        self.descend_leftmost(0, self.reader.root_pgno());
        self.state = State::At;
        true
    }

    /// Position at the last record (key order). `false` if the tree is empty.
    pub fn last(&mut self) -> bool {
        if self.state == State::Empty {
            return false;
        }
        self.descend_rightmost(0, self.reader.root_pgno());
        self.state = State::At;
        true
    }

    /// Position at the **successor**: the first record with `from >= key`. Returns `true`
    /// if such a record exists (then [`current`](Self::current) yields it), `false` if
    /// `key` is past every record (the cursor becomes `AfterLast`) or the tree is empty.
    ///
    /// To get the record **covering** `key` (greatest `from <= key`, present iff
    /// `key <= to`): `seek(key)`, then if `current().from != key` call `prev()` and check
    /// its `to >= key`.
    pub fn seek(&mut self, key: K) -> bool {
        if self.state == State::Empty {
            return false;
        }
        let leaf_level = self.leaf_level();
        let mut level = 0usize;
        let mut pgno = self.reader.root_pgno();
        loop {
            if level == leaf_level {
                let leaf = self.leaf(pgno);
                let idx = leaf_lower_bound(&leaf, key);
                if idx < leaf.len() {
                    self.path[level] = Frame {
                        pgno,
                        idx: idx as u32,
                    };
                    self.state = State::At;
                    return true;
                }
                // No record >= key in this leaf; the successor (if any) is the first
                // record of the next leaf. Park at the last record and step forward.
                self.path[level] = Frame {
                    pgno,
                    idx: (leaf.len() as u32).saturating_sub(1),
                };
                self.state = State::At;
                return self.next();
            }
            let b = self.branch(pgno);
            let ci = branch_descend(&b, key);
            self.path[level] = Frame {
                pgno,
                idx: ci as u32,
            };
            pgno = b.child(ci);
            level += 1;
        }
    }

    /// Step to the next record in key order. `BeforeFirst` → first; `At` → next or
    /// `AfterLast`; `Empty`/`AfterLast` → stays, `false`.
    ///
    /// Named `next` for the cursor idiom; this type is intentionally not an `Iterator`
    /// (it is bidirectional and seekable, and `current` borrows from the image).
    #[allow(clippy::should_implement_trait)]
    pub fn next(&mut self) -> bool {
        match self.state {
            State::Empty | State::AfterLast => false,
            State::BeforeFirst => self.first(),
            State::At => {
                let leaf_level = self.leaf_level();
                if (self.path[leaf_level].idx as usize) + 1
                    < self.leaf(self.path[leaf_level].pgno).len()
                {
                    self.path[leaf_level].idx += 1;
                    return true;
                }
                let mut level = leaf_level;
                loop {
                    if level == 0 {
                        self.state = State::AfterLast;
                        return false;
                    }
                    level -= 1;
                    let b = self.branch(self.path[level].pgno);
                    if (self.path[level].idx as usize) + 1 < b.child_count() {
                        self.path[level].idx += 1;
                        let child = b.child(self.path[level].idx as usize);
                        self.descend_leftmost(level + 1, child);
                        return true;
                    }
                }
            }
        }
    }

    /// Step to the previous record in key order. `AfterLast` → last; `At` → prev or
    /// `BeforeFirst`; `Empty`/`BeforeFirst` → stays, `false`.
    pub fn prev(&mut self) -> bool {
        match self.state {
            State::Empty | State::BeforeFirst => false,
            State::AfterLast => self.last(),
            State::At => {
                let leaf_level = self.leaf_level();
                if self.path[leaf_level].idx > 0 {
                    self.path[leaf_level].idx -= 1;
                    return true;
                }
                let mut level = leaf_level;
                loop {
                    if level == 0 {
                        self.state = State::BeforeFirst;
                        return false;
                    }
                    level -= 1;
                    if self.path[level].idx > 0 {
                        self.path[level].idx -= 1;
                        let child = self
                            .branch(self.path[level].pgno)
                            .child(self.path[level].idx as usize);
                        self.descend_rightmost(level + 1, child);
                        return true;
                    }
                }
            }
        }
    }

    /// The positioned record `(from, to, scope_id)`, or `None` unless `At`.
    pub fn current(&self) -> Option<(K, K, u32)> {
        if self.state != State::At {
            return None;
        }
        let leaf_level = self.leaf_level();
        let f = self.path[leaf_level];
        let r = self.leaf(f.pgno).record(f.idx as usize);
        Some((r.from(), r.to(), r.scope_id()))
    }

    // --- helpers (§v4.1.B) ---

    /// Walk records overlapping `[from, to]` in key order, calling `f(cf, ct, scope)` with
    /// each record **clamped** to the window (`cf <= ct`). Stops if `f` returns `Break`.
    /// The covering record (one whose `from < from <= to`) is included.
    fn for_each_overlap<F>(&mut self, from: K, to: K, mut f: F)
    where
        F: FnMut(K, K, u32) -> ControlFlow<()>,
    {
        if from > to {
            return;
        }
        self.seek(from);
        // Back up to a covering record if the one before the successor overlaps `from`.
        let mut probe = *self;
        if probe.prev() {
            if let Some((_, pt, _)) = probe.current() {
                if pt >= from {
                    *self = probe;
                }
            }
        }
        while let Some((rf, rt, rs)) = self.current() {
            if rf > to {
                break;
            }
            let cf = if rf > from { rf } else { from };
            let ct = if rt < to { rt } else { to };
            if f(cf, ct, rs).is_break() {
                break;
            }
            if !self.next() {
                break;
            }
        }
    }

    /// Emit each record overlapping `[from, to]` whose `scope` matches `select`, clamped
    /// to the window, as `(from, to, scope_id)`. Per-record (not merged).
    pub fn query_ranges<S, V>(&mut self, from: K, to: K, mut select: S, mut visit: V) -> Result<()>
    where
        S: FnMut(u32) -> bool,
        V: FnMut(K, K, u32) -> ControlFlow<()>,
    {
        self.for_each_overlap(from, to, |cf, ct, scope| {
            if select(scope) {
                visit(cf, ct, scope)
            } else {
                ControlFlow::Continue(())
            }
        });
        Ok(())
    }

    /// Emit the **maximal contiguous runs** of matched key-space in `[from, to]` as
    /// `(from, to)` (coalesced across scopes; a non-matching or absent span breaks a run).
    pub fn query_ranges_merged<S, V>(
        &mut self,
        from: K,
        to: K,
        mut select: S,
        mut visit: V,
    ) -> Result<()>
    where
        S: FnMut(u32) -> bool,
        V: FnMut(K, K) -> ControlFlow<()>,
    {
        let mut open: Option<(K, K)> = None;
        let mut stop = false;
        self.for_each_overlap(from, to, |cf, ct, scope| {
            if !select(scope) {
                if let Some((of, ot)) = open.take() {
                    if visit(of, ot).is_break() {
                        stop = true;
                        return ControlFlow::Break(());
                    }
                }
                return ControlFlow::Continue(());
            }
            match open {
                Some((of, ot)) if ot.checked_inc() == Some(cf) => open = Some((of, ct)),
                Some((of, ot)) => {
                    if visit(of, ot).is_break() {
                        stop = true;
                        return ControlFlow::Break(());
                    }
                    open = Some((cf, ct));
                }
                None => open = Some((cf, ct)),
            }
            ControlFlow::Continue(())
        });
        if !stop {
            if let Some((of, ot)) = open {
                let _ = visit(of, ot);
            }
        }
        Ok(())
    }

    /// Emit, for each matched record overlapping `[from, to]`, the canonical CIDR cover
    /// of its clamped range, as `(addr, prefix_len, scope_id)`.
    pub fn query_cidrs<S, V>(&mut self, from: K, to: K, mut select: S, mut visit: V) -> Result<()>
    where
        S: FnMut(u32) -> bool,
        V: FnMut(K, u8, u32) -> ControlFlow<()>,
    {
        self.for_each_overlap(from, to, |cf, ct, scope| {
            if select(scope) {
                emit_cidrs::<K, _>(cf, ct, |addr, plen| visit(addr, plen, scope))
            } else {
                ControlFlow::Continue(())
            }
        });
        Ok(())
    }

    /// Emit the canonical CIDR cover of each **merged** matched run in `[from, to]`, as
    /// `(addr, prefix_len)` — the netset view.
    pub fn query_cidrs_merged<S, V>(
        &mut self,
        from: K,
        to: K,
        select: S,
        mut visit: V,
    ) -> Result<()>
    where
        S: FnMut(u32) -> bool,
        V: FnMut(K, u8) -> ControlFlow<()>,
    {
        self.query_ranges_merged(from, to, select, |rf, rt| {
            emit_cidrs::<K, _>(rf, rt, &mut visit)
        })
    }

    /// Total distinct IPs in `[from, to]` whose `scope` matches `select`. Records are
    /// disjoint, so this is the sum of clamped sizes. Saturates at `u128::MAX` (only a
    /// fully-covered IPv6 space, `2^128`, would exceed it).
    pub fn count_ips<S>(&mut self, from: K, to: K, mut select: S) -> u128
    where
        S: FnMut(u32) -> bool,
    {
        let mut total: u128 = 0;
        self.for_each_overlap(from, to, |cf, ct, scope| {
            if select(scope) {
                let span = ct.to_u128() - cf.to_u128();
                total = total.saturating_add(span).saturating_add(1);
            }
            ControlFlow::Continue(())
        });
        total
    }

    /// Number of CIDRs the matched set in `[from, to]` decomposes to (netset entry count):
    /// the CIDR count of the **merged** runs.
    pub fn count_cidrs<S>(&mut self, from: K, to: K, select: S) -> u64
    where
        S: FnMut(u32) -> bool,
    {
        let mut total: u64 = 0;
        let _ = self.query_ranges_merged(from, to, select, |rf, rt| {
            total += cidr_count::<K>(rf, rt);
            ControlFlow::Continue(())
        });
        total
    }
}

/// First index in `leaf` whose record `from >= key` (lower bound). `len` if all `< key`.
#[inline]
fn leaf_lower_bound<K: IpKey>(leaf: &LeafView<'_, K>, key: K) -> usize {
    let (mut lo, mut hi) = (0usize, leaf.len());
    while lo < hi {
        let mid = lo + (hi - lo) / 2;
        if leaf.record(mid).from() < key {
            lo = mid + 1;
        } else {
            hi = mid;
        }
    }
    lo
}

/// Branch descent (§5.2): child index = number of separators `<= key`.
#[inline]
fn branch_descend<K: IpKey>(branch: &BranchView<'_, K>, key: K) -> usize {
    let (mut lo, mut hi) = (0usize, branch.sep_count());
    while lo < hi {
        let mid = lo + (hi - lo) / 2;
        if branch.sep(mid) <= key {
            lo = mid + 1;
        } else {
            hi = mid;
        }
    }
    lo
}

/// Largest `n` in `[0, bits]` with `2^n - 1 <= gap` (i.e. `2^n <= gap + 1`).
#[inline]
fn size_bits(gap: u128, bits: u32) -> u32 {
    if bits == 128 && gap == u128::MAX {
        return 128; // gap + 1 == 2^128
    }
    let cap = gap + 1; // safe: gap < 2^bits, and the bits==128 full case is handled above
    (127 - cap.leading_zeros()).min(bits)
}

/// Canonical minimal CIDR cover of the inclusive range `[a, b]` (§v4.1.B): repeatedly take
/// the largest aligned prefix that fits the remaining range. `emit(network, prefix_len)`.
fn emit_cidrs<K: IpKey, F: FnMut(K, u8) -> ControlFlow<()>>(
    a: K,
    b: K,
    mut emit: F,
) -> ControlFlow<()> {
    let bits = K::BITS;
    let mut cur = a.to_u128();
    let end = b.to_u128(); // inclusive; cur <= end
    loop {
        let align_bits = if cur == 0 {
            bits
        } else {
            cur.trailing_zeros().min(bits)
        };
        let nbits = align_bits.min(size_bits(end - cur, bits));
        emit(K::from_u128(cur), (bits - nbits) as u8)?;
        if nbits >= bits {
            return ControlFlow::Continue(()); // covered the whole space in one block
        }
        let block_end = cur + ((1u128 << nbits) - 1);
        if block_end >= end {
            return ControlFlow::Continue(());
        }
        cur = block_end + 1;
    }
}

/// Number of CIDRs in the canonical cover of `[a, b]`.
#[inline]
fn cidr_count<K: IpKey>(a: K, b: K) -> u64 {
    let mut n = 0u64;
    let _ = emit_cidrs::<K, _>(a, b, |_, _| {
        n += 1;
        ControlFlow::Continue(())
    });
    n
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::key::Ipv4Key;
    use crate::record;
    use crate::spec::{self, IpVersion, PAGE_HEADER_SIZE, PAGE_SIZE};
    use crate::wire::{finalize_checksum, Meta};

    fn v4(n: u32) -> Ipv4Key {
        Ipv4Key(n)
    }

    #[allow(clippy::too_many_arguments)]
    fn meta(
        pgno: u32,
        root: u32,
        height: u32,
        total_pages: u64,
        record_count: u64,
        txn: u64,
    ) -> Meta {
        Meta {
            pgno,
            version_minor: 0,
            meta_size: spec::META_SIZE,
            page_size: PAGE_SIZE as u32,
            checksum_algo: spec::CHECKSUM_ALGO_CRC32C,
            flags: IpVersion::V4.flag(),
            key_width: 4,
            scope_mode: spec::SCOPE_MODE_SCALAR,
            record_size: spec::record_size(4),
            created_unixtime: 0,
            root_pgno: root,
            tree_height: height,
            total_pages,
            record_count,
            txn_id: txn,
            updated_unixtime: 0,
            scope_table_root: 0,
            free_list_head: 0,
        }
    }

    fn put_leaf(file: &mut [u8], pgno: u32, records: &[(Ipv4Key, Ipv4Key, u32)]) {
        let rs = spec::record_size(4) as usize;
        let base = pgno as usize * PAGE_SIZE;
        let page = &mut file[base..base + PAGE_SIZE];
        PageHeader::write(page, spec::PAGE_TYPE_LEAF, records.len() as u16, pgno);
        for (i, (f, t, s)) in records.iter().enumerate() {
            let off = PAGE_HEADER_SIZE + i * rs;
            record::write::<Ipv4Key>(&mut page[off..off + rs], *f, *t, *s);
        }
        finalize_checksum(page);
    }

    fn build_single_leaf(records: &[(Ipv4Key, Ipv4Key, u32)]) -> Vec<u8> {
        let mut file = vec![0u8; 3 * PAGE_SIZE];
        put_leaf(&mut file, 2, records);
        let rc = records.len() as u64;
        meta(0, 2, 1, 3, rc, 2).encode_into(&mut file[..PAGE_SIZE]);
        meta(1, 2, 1, 3, rc, 1).encode_into(&mut file[PAGE_SIZE..2 * PAGE_SIZE]);
        file
    }

    fn build_empty() -> Vec<u8> {
        let mut file = vec![0u8; 2 * PAGE_SIZE];
        meta(0, 0, 0, 2, 0, 2).encode_into(&mut file[..PAGE_SIZE]);
        meta(1, 0, 0, 2, 0, 1).encode_into(&mut file[PAGE_SIZE..]);
        file
    }

    /// metas, root branch (pgno 2, one separator `sep`), leaves at pgno 3/4.
    fn build_two_level(
        sep: Ipv4Key,
        left: &[(Ipv4Key, Ipv4Key, u32)],
        right: &[(Ipv4Key, Ipv4Key, u32)],
    ) -> Vec<u8> {
        let mut file = vec![0u8; 5 * PAGE_SIZE];
        put_leaf(&mut file, 3, left);
        put_leaf(&mut file, 4, right);
        {
            let page = &mut file[2 * PAGE_SIZE..3 * PAGE_SIZE];
            PageHeader::write(page, spec::PAGE_TYPE_BRANCH, 1, 2);
            page[PAGE_HEADER_SIZE..PAGE_HEADER_SIZE + 4].copy_from_slice(&3u32.to_le_bytes());
            let sep_off = PAGE_HEADER_SIZE + 4;
            sep.write_le(&mut page[sep_off..sep_off + 4]);
            page[sep_off + 4..sep_off + 8].copy_from_slice(&4u32.to_le_bytes());
            finalize_checksum(page);
        }
        let rc = (left.len() + right.len()) as u64;
        meta(0, 2, 2, 5, rc, 2).encode_into(&mut file[..PAGE_SIZE]);
        meta(1, 2, 2, 5, rc, 1).encode_into(&mut file[PAGE_SIZE..2 * PAGE_SIZE]);
        file
    }

    fn collect_all(r: &Reader<'_>) -> Vec<(u32, u32, u32)> {
        let mut c = r.cursor::<Ipv4Key>().unwrap();
        let mut out = Vec::new();
        c.first();
        while let Some((f, t, s)) = c.current() {
            out.push((f.0, t.0, s));
            c.next();
        }
        out
    }

    #[test]
    fn iterate_forward_and_backward() {
        let recs: &[(Ipv4Key, Ipv4Key, u32)] = &[
            (v4(10), v4(20), 1),
            (v4(30), v4(40), 2),
            (v4(50), v4(60), 1),
        ];
        let file = build_single_leaf(recs);
        let r = Reader::open(&file).unwrap();
        assert_eq!(collect_all(&r), vec![(10, 20, 1), (30, 40, 2), (50, 60, 1)]);

        // backward from last
        let mut c = r.cursor::<Ipv4Key>().unwrap();
        assert!(c.last());
        let mut back = Vec::new();
        while let Some((f, _, _)) = c.current() {
            back.push(f.0);
            c.prev();
        }
        assert_eq!(back, vec![50, 30, 10]);
    }

    #[test]
    fn seek_semantics() {
        let recs: &[(Ipv4Key, Ipv4Key, u32)] = &[
            (v4(10), v4(20), 1),
            (v4(30), v4(40), 2),
            (v4(50), v4(60), 1),
        ];
        let file = build_single_leaf(recs);
        let r = Reader::open(&file).unwrap();
        let mut c = r.cursor::<Ipv4Key>().unwrap();

        assert!(c.seek(v4(5)));
        assert_eq!(c.current().unwrap().0 .0, 10); // before all -> first
        assert!(c.seek(v4(10)));
        assert_eq!(c.current().unwrap().0 .0, 10); // exact from
        assert!(c.seek(v4(25)));
        assert_eq!(c.current().unwrap().0 .0, 30); // gap -> successor
        assert!(c.seek(v4(30)));
        assert_eq!(c.current().unwrap().0 .0, 30);
        assert!(!c.seek(v4(61))); // past all -> AfterLast
        assert!(c.current().is_none());
        // prev from AfterLast -> last record
        assert!(c.prev());
        assert_eq!(c.current().unwrap().0 .0, 50);
    }

    #[test]
    fn empty_tree_cursor() {
        let file = build_empty();
        let r = Reader::open(&file).unwrap();
        let mut c = r.cursor::<Ipv4Key>().unwrap();
        assert!(!c.first());
        assert!(!c.last());
        assert!(!c.next());
        assert!(!c.prev());
        assert!(!c.seek(v4(5)));
        assert!(c.current().is_none());
    }

    #[test]
    fn before_first_after_last_transitions() {
        let recs: &[(Ipv4Key, Ipv4Key, u32)] = &[(v4(10), v4(20), 1)];
        let file = build_single_leaf(recs);
        let r = Reader::open(&file).unwrap();
        let mut c = r.cursor::<Ipv4Key>().unwrap();
        // starts BeforeFirst
        assert!(c.current().is_none());
        assert!(!c.prev()); // stays BeforeFirst
        assert!(c.next()); // -> first
        assert_eq!(c.current().unwrap().0 .0, 10);
        assert!(!c.next()); // -> AfterLast
        assert!(c.current().is_none());
        assert!(!c.next()); // stays AfterLast
        assert!(c.prev()); // -> last (only) record
        assert_eq!(c.current().unwrap().0 .0, 10);
    }

    #[test]
    fn two_level_iterate_and_seek_cross_leaves() {
        let left: &[(Ipv4Key, Ipv4Key, u32)] = &[(v4(10), v4(20), 1), (v4(50), v4(60), 2)];
        let right: &[(Ipv4Key, Ipv4Key, u32)] = &[(v4(100), v4(110), 3), (v4(200), v4(210), 4)];
        let file = build_two_level(v4(100), left, right);
        let r = Reader::open(&file).unwrap();
        assert_eq!(
            collect_all(&r).iter().map(|x| x.0).collect::<Vec<_>>(),
            vec![10, 50, 100, 200]
        );
        // seek that crosses from left leaf into right leaf
        let mut c = r.cursor::<Ipv4Key>().unwrap();
        assert!(c.seek(v4(70)));
        assert_eq!(c.current().unwrap().0 .0, 100);
        // next/prev across the leaf boundary
        assert!(c.prev());
        assert_eq!(c.current().unwrap().0 .0, 50);
        assert!(c.next());
        assert_eq!(c.current().unwrap().0 .0, 100);
    }

    #[test]
    fn query_ranges_merged_and_select() {
        let recs: &[(Ipv4Key, Ipv4Key, u32)] = &[
            (v4(10), v4(20), 1),
            (v4(21), v4(30), 1), // contiguous with previous (20+1 == 21)
            (v4(40), v4(50), 2),
        ];
        let file = build_single_leaf(recs);
        let r = Reader::open(&file).unwrap();
        let mut c = r.cursor::<Ipv4Key>().unwrap();

        // select all, whole space: the two contiguous [1] runs merge into 10..30.
        let mut runs = Vec::new();
        c.query_ranges_merged(
            Ipv4Key::MIN,
            Ipv4Key::MAX,
            |_| true,
            |f, t| {
                runs.push((f.0, t.0));
                ControlFlow::Continue(())
            },
        )
        .unwrap();
        assert_eq!(runs, vec![(10, 30), (40, 50)]);

        // select scope_id == 2 only.
        let mut runs2 = Vec::new();
        c.query_ranges_merged(
            Ipv4Key::MIN,
            Ipv4Key::MAX,
            |s| s == 2,
            |f, t| {
                runs2.push((f.0, t.0));
                ControlFlow::Continue(())
            },
        )
        .unwrap();
        assert_eq!(runs2, vec![(40, 50)]);
    }

    #[test]
    fn count_ips_window_and_select() {
        let recs: &[(Ipv4Key, Ipv4Key, u32)] = &[(v4(10), v4(19), 1), (v4(30), v4(39), 2)]; // 10 + 10 IPs
        let file = build_single_leaf(recs);
        let r = Reader::open(&file).unwrap();
        let mut c = r.cursor::<Ipv4Key>().unwrap();
        assert_eq!(c.count_ips(Ipv4Key::MIN, Ipv4Key::MAX, |_| true), 20);
        assert_eq!(c.count_ips(Ipv4Key::MIN, Ipv4Key::MAX, |s| s == 1), 10);
        // window clamps: [15, 34] -> 5 (15..19) + 5 (30..34) = 10
        assert_eq!(c.count_ips(v4(15), v4(34), |_| true), 10);
    }

    #[test]
    fn query_cidrs_and_count() {
        // 0.0.0.0 .. 0.0.0.255 is exactly one /24.
        let recs: &[(Ipv4Key, Ipv4Key, u32)] = &[(v4(0), v4(255), 1)];
        let file = build_single_leaf(recs);
        let r = Reader::open(&file).unwrap();
        let mut c = r.cursor::<Ipv4Key>().unwrap();
        let mut cidrs = Vec::new();
        c.query_cidrs(
            Ipv4Key::MIN,
            Ipv4Key::MAX,
            |_| true,
            |a, p, _| {
                cidrs.push((a.0, p));
                ControlFlow::Continue(())
            },
        )
        .unwrap();
        assert_eq!(cidrs, vec![(0u32, 24u8)]);
        assert_eq!(c.count_cidrs(Ipv4Key::MIN, Ipv4Key::MAX, |_| true), 1);
    }

    #[test]
    fn cidr_decomposition_units() {
        fn cover(a: u32, b: u32) -> Vec<(u32, u8)> {
            let mut v = Vec::new();
            let _ = emit_cidrs::<Ipv4Key, _>(Ipv4Key(a), Ipv4Key(b), |addr, p| {
                v.push((addr.0, p));
                ControlFlow::Continue(())
            });
            v
        }
        assert_eq!(cover(0, 255), vec![(0, 24)]);
        assert_eq!(cover(10, 20), vec![(10, 31), (12, 30), (16, 30), (20, 32)]);
        assert_eq!(cover(0, u32::MAX), vec![(0, 0)]); // whole IPv4 space -> /0
        assert_eq!(cover(42, 42), vec![(42, 32)]); // single host

        // IPv6 whole space -> ::/0 (no overflow).
        let mut v6 = Vec::new();
        let _ = emit_cidrs::<crate::key::Ipv6Key, _>(
            crate::key::Ipv6Key::MIN,
            crate::key::Ipv6Key::MAX,
            |a, p| {
                v6.push((a, p));
                ControlFlow::Continue(())
            },
        );
        assert_eq!(v6.len(), 1);
        assert_eq!(v6[0].1, 0);
    }

    #[test]
    fn visitor_stop_is_honored() {
        let recs: &[(Ipv4Key, Ipv4Key, u32)] = &[
            (v4(10), v4(20), 1),
            (v4(40), v4(50), 2),
            (v4(70), v4(80), 3),
        ];
        let file = build_single_leaf(recs);
        let r = Reader::open(&file).unwrap();
        let mut c = r.cursor::<Ipv4Key>().unwrap();
        let mut seen = Vec::new();
        c.query_ranges(
            Ipv4Key::MIN,
            Ipv4Key::MAX,
            |_| true,
            |f, _, _| {
                seen.push(f.0);
                if seen.len() == 2 {
                    ControlFlow::Break(())
                } else {
                    ControlFlow::Continue(())
                }
            },
        )
        .unwrap();
        assert_eq!(seen, vec![10, 40]); // stopped after 2
    }

    #[test]
    fn family_mismatch_errors() {
        let recs: &[(Ipv4Key, Ipv4Key, u32)] = &[(v4(10), v4(20), 1)];
        let file = build_single_leaf(recs);
        let r = Reader::open(&file).unwrap();
        assert!(r.cursor::<crate::key::Ipv6Key>().is_err());
    }
}
