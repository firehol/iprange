//! The v4.1 scope table (§C.2, §D): a fixed-record B+tree keyed by `scope_id` mapping
//! each scope to its per-scope header `{version, type, name, kv_root}`.
//!
//! On disk it is a B+tree of `page_type` 4 (branch) / 5 (leaf): leaves hold sorted
//! `SCOPE_RECORD_SIZE`-byte records; branches have the **same layout as an IPv4 branch**
//! (a `u32` `scope_id` separator + `u32` child pgno), so the existing [`BranchView`] is
//! reused with `Ipv4Key` standing in for `scope_id`.
//!
//! The writer keeps the registry **in memory** (a `Vec<ScopeRec>` sorted by `scope_id`,
//! loaded by scanning the committed tree on open) and **bulk-rebuilds** the tree at commit
//! — simpler than incremental split/merge, and valid because tree shape is
//! implementation-defined (§D, conformance is cross-read behavioral). Reads descend the
//! committed tree in `O(log scopes)`. Updating a header preserves each record's `kv_root`,
//! so it never rewrites a scope's KV (§C.2).

use alloc::vec::Vec;

use crate::crc32c;
use crate::error::{Error, Result};
use crate::key::Ipv4Key;
use crate::node::BranchView;
use crate::spec::{self, PAGE_HEADER_SIZE, PAGE_SIZE};
use crate::wire::PageHeader;

/// An owned per-scope header record (the scope-table leaf payload, §C.2). `name` holds
/// `name_len` bytes (`<= SCOPE_NAME_MAX`); `type_`/`version`/`kv_root` are the seekable
/// header fields. `id` is the B+tree key.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct ScopeRec {
    pub id: u32,
    pub version: u64,
    pub type_: u8,
    pub name: Vec<u8>,
    pub kv_root: u32,
}

impl ScopeRec {
    /// Write this record into a `SCOPE_RECORD_SIZE`-byte slot (LE; name zero-padded).
    fn encode(&self, out: &mut [u8]) {
        debug_assert_eq!(out.len(), spec::SCOPE_RECORD_SIZE);
        out.fill(0);
        out[spec::SCOPE_REC_ID..spec::SCOPE_REC_ID + 4].copy_from_slice(&self.id.to_le_bytes());
        out[spec::SCOPE_REC_VERSION..spec::SCOPE_REC_VERSION + 8]
            .copy_from_slice(&self.version.to_le_bytes());
        out[spec::SCOPE_REC_TYPE] = self.type_;
        let nlen = self.name.len();
        out[spec::SCOPE_REC_NAME_LEN..spec::SCOPE_REC_NAME_LEN + 2]
            .copy_from_slice(&(nlen as u16).to_le_bytes());
        out[spec::SCOPE_REC_NAME..spec::SCOPE_REC_NAME + nlen].copy_from_slice(&self.name);
        out[spec::SCOPE_REC_KV_ROOT..spec::SCOPE_REC_KV_ROOT + 4]
            .copy_from_slice(&self.kv_root.to_le_bytes());
    }
}

/// A read view over a scope-table **leaf** page (`page_type 5`): `count` fixed records.
pub(crate) struct ScopeLeafView<'a> {
    page: &'a [u8],
    count: usize,
}

impl<'a> ScopeLeafView<'a> {
    #[inline]
    pub fn new(page: &'a [u8], count: usize) -> Self {
        ScopeLeafView { page, count }
    }
    /// The `scope_id` of record `i` (the leading 4 bytes — the B+tree key).
    #[inline]
    pub fn id(&self, i: usize) -> u32 {
        let off = PAGE_HEADER_SIZE + i * spec::SCOPE_RECORD_SIZE + spec::SCOPE_REC_ID;
        u32::from_le_bytes([
            self.page[off],
            self.page[off + 1],
            self.page[off + 2],
            self.page[off + 3],
        ])
    }
    /// Decode record `i` into an owned [`ScopeRec`] (the reader/writer validates bounds).
    pub fn record(&self, i: usize) -> Result<ScopeRec> {
        let base = PAGE_HEADER_SIZE + i * spec::SCOPE_RECORD_SIZE;
        let r = &self.page[base..base + spec::SCOPE_RECORD_SIZE];
        let id = u32::from_le_bytes([r[0], r[1], r[2], r[3]]);
        let version = {
            let mut b = [0u8; 8];
            b.copy_from_slice(&r[spec::SCOPE_REC_VERSION..spec::SCOPE_REC_VERSION + 8]);
            u64::from_le_bytes(b)
        };
        let type_ = r[spec::SCOPE_REC_TYPE];
        let name_len =
            u16::from_le_bytes([r[spec::SCOPE_REC_NAME_LEN], r[spec::SCOPE_REC_NAME_LEN + 1]])
                as usize;
        if name_len > spec::SCOPE_NAME_MAX {
            return Err(Error::Invariant("scope name_len > 256"));
        }
        // The name slot beyond name_len MUST be zero (§C.2 / §D tail-zero discipline).
        if r[spec::SCOPE_REC_NAME + name_len..spec::SCOPE_REC_NAME + spec::SCOPE_NAME_MAX]
            .iter()
            .any(|&b| b != 0)
        {
            return Err(Error::NonZeroReserved("scope name padding"));
        }
        let name = r[spec::SCOPE_REC_NAME..spec::SCOPE_REC_NAME + name_len].to_vec();
        let kv_root = u32::from_le_bytes([
            r[spec::SCOPE_REC_KV_ROOT],
            r[spec::SCOPE_REC_KV_ROOT + 1],
            r[spec::SCOPE_REC_KV_ROOT + 2],
            r[spec::SCOPE_REC_KV_ROOT + 3],
        ]);
        Ok(ScopeRec {
            id,
            version,
            type_,
            name,
            kv_root,
        })
    }

    /// Byte length of the populated body (for the tail-zero check).
    #[inline]
    pub fn body_len(&self) -> usize {
        self.count * spec::SCOPE_RECORD_SIZE
    }

    /// Validate record `i`'s `name_len`/padding/UTF-8 without decoding (alloc-free, reader
    /// §9). The `name` bytes MUST be valid UTF-8 (RFC 3629, §C.2): a hostile file otherwise
    /// delivers non-UTF-8 names via `scope_name`/`scope_list` (F3).
    pub fn validate_at(&self, i: usize) -> Result<()> {
        let base = PAGE_HEADER_SIZE + i * spec::SCOPE_RECORD_SIZE;
        let nl = u16::from_le_bytes([
            self.page[base + spec::SCOPE_REC_NAME_LEN],
            self.page[base + spec::SCOPE_REC_NAME_LEN + 1],
        ]) as usize;
        if nl > spec::SCOPE_NAME_MAX {
            return Err(Error::Invariant("scope name_len > 256"));
        }
        let name_start = base + spec::SCOPE_REC_NAME;
        if core::str::from_utf8(&self.page[name_start..name_start + nl]).is_err() {
            return Err(Error::Invariant("scope name not valid UTF-8"));
        }
        let pad = name_start + nl;
        let pad_end = base + spec::SCOPE_REC_NAME + spec::SCOPE_NAME_MAX;
        if self.page[pad..pad_end].iter().any(|&b| b != 0) {
            return Err(Error::NonZeroReserved("scope name padding"));
        }
        Ok(())
    }
}

#[inline]
fn page_at(bytes: &[u8], pgno: u32) -> &[u8] {
    let off = pgno as usize * PAGE_SIZE;
    &bytes[off..off + PAGE_SIZE]
}

/// Recursively validate the scope-table subtree (reader §9 for §D). The image geometry is
/// already checked by the caller (file size, `total_pages`). Walks by `page_type` (no
/// stored height): all leaves at the same depth, `scope_id`s globally strictly increasing,
/// child pgnos in `[2, total_pages)`, depth bounded by `TREE_HEIGHT_MAX`. Never panics/
/// loops on hostile-but-checksum-valid input.
/// `visited` is the file-wide page-disjointness bitset (F2), length `total_pages`, shared
/// with every per-scope KV tree walk: a page reached twice (a duplicate child pgno or an
/// aliased subtree) is structural corruption. Subsumes the per-node duplicate-child check.
pub(crate) fn validate(
    bytes: &[u8],
    root_pgno: u32,
    total_pages: u64,
    visited: &mut [bool],
) -> Result<()> {
    if root_pgno == 0 {
        return Ok(());
    }
    if (root_pgno as u64) < 2 || (root_pgno as u64) >= total_pages {
        return Err(Error::Structural("scope_table_root out of range"));
    }
    let mut leaf_depth: Option<u32> = None;
    let mut prev_id: Option<u32> = None;
    // Root covers the whole id space; separators narrow it per child (FILE id 0 is the min).
    validate_node(
        bytes,
        root_pgno,
        1,
        total_pages,
        visited,
        &mut leaf_depth,
        &mut prev_id,
        0,
        u32::MAX,
    )
}

#[allow(clippy::too_many_arguments)] // recursive validator threading shared walk state
fn validate_node(
    bytes: &[u8],
    pgno: u32,
    depth: u32,
    total_pages: u64,
    visited: &mut [bool],
    leaf_depth: &mut Option<u32>,
    prev_id: &mut Option<u32>,
    lo: u32,
    hi: u32,
) -> Result<()> {
    if depth > spec::TREE_HEIGHT_MAX {
        return Err(Error::Invariant("scope path deeper than TREE_HEIGHT_MAX"));
    }
    // File-wide page disjointness (F2): every page belongs to exactly one parent.
    if visited[pgno as usize] {
        return Err(Error::Structural("scope page reached twice (aliased)"));
    }
    visited[pgno as usize] = true;
    let page = page_at(bytes, pgno);
    if !crc32c::verify_page(page) {
        return Err(Error::ChecksumFailed("scope page"));
    }
    let h = PageHeader::decode(page);
    if h.reserved != 0 {
        return Err(Error::NonZeroReserved("scope page header reserved"));
    }
    if h.pgno != pgno {
        return Err(Error::Structural("scope page self-pgno mismatch"));
    }
    match h.page_type {
        spec::PAGE_TYPE_SCOPE_LEAF => {
            match *leaf_depth {
                None => *leaf_depth = Some(depth),
                Some(d) if d == depth => {}
                _ => return Err(Error::Invariant("scope leaves at differing depths")),
            }
            let count = h.entry_count as usize;
            if count < 1 || count > spec::scope_leaf_max() {
                return Err(Error::Invariant("scope leaf entry_count out of range"));
            }
            let leaf = ScopeLeafView::new(page, count);
            if page[PAGE_HEADER_SIZE + leaf.body_len()..]
                .iter()
                .any(|&b| b != 0)
            {
                return Err(Error::NonZeroReserved("scope leaf tail"));
            }
            for i in 0..count {
                leaf.validate_at(i)?;
                let id = leaf.id(i);
                if id < lo || id > hi {
                    return Err(Error::Invariant("scope id outside node bound"));
                }
                if let Some(p) = *prev_id {
                    if id <= p {
                        return Err(Error::Invariant("scope ids not sorted/disjoint"));
                    }
                }
                *prev_id = Some(id);
            }
            Ok(())
        }
        spec::PAGE_TYPE_SCOPE_BRANCH => {
            let s = h.entry_count as usize;
            if s < 1 || s > spec::scope_branch_max() {
                return Err(Error::Invariant(
                    "scope branch separator count out of range",
                ));
            }
            let b = scope_branch_view(page, s);
            if page[PAGE_HEADER_SIZE + b.body_len()..]
                .iter()
                .any(|&x| x != 0)
            {
                return Err(Error::NonZeroReserved("scope branch tail"));
            }
            // Separators in bound and strictly increasing: lo < sep[0] < … < sep[s-1] <= hi.
            let mut prev_sep: Option<u32> = None;
            for i in 0..s {
                let sep = b.sep(i).0;
                if sep <= lo {
                    return Err(Error::Invariant("scope separator <= lo"));
                }
                if sep > hi {
                    return Err(Error::Invariant("scope separator > hi"));
                }
                if let Some(p) = prev_sep {
                    if sep <= p {
                        return Err(Error::Invariant("scope separators not increasing"));
                    }
                }
                prev_sep = Some(sep);
            }
            for j in 0..b.child_count() {
                let c = b.child(j);
                if (c as u64) < 2 || (c as u64) >= total_pages {
                    return Err(Error::Structural("scope child pgno out of range"));
                }
            }
            // Recurse with separator-derived bounds, confining each child's ids to its routing
            // interval (child[0]=[lo, sep[0]-1]; child[i]=[sep[i-1], sep[i]-1]; child[s]=
            // [sep[s-1], hi]). Mirrors the IP-tree validator (reader.rs); without it a file
            // with valid CRCs but mismatched separators would misroute lookups.
            let mut lower = lo;
            for i in 0..s {
                let sep = b.sep(i).0;
                let upper = sep
                    .checked_sub(1)
                    .ok_or(Error::Invariant("scope separator has no predecessor"))?;
                validate_node(
                    bytes,
                    b.child(i),
                    depth + 1,
                    total_pages,
                    visited,
                    leaf_depth,
                    prev_id,
                    lower,
                    upper,
                )?;
                lower = sep;
            }
            validate_node(
                bytes,
                b.child(s),
                depth + 1,
                total_pages,
                visited,
                leaf_depth,
                prev_id,
                lower,
                hi,
            )?;
            Ok(())
        }
        _ => Err(Error::Structural("unexpected page_type in scope table")),
    }
}

/// Load every scope record (in `scope_id` order) from a validated committed scope tree —
/// the writer's in-memory registry on open. `root_pgno == 0` → empty.
pub(crate) fn load_all(bytes: &[u8], root_pgno: u32) -> Result<Vec<ScopeRec>> {
    let mut out = Vec::new();
    if root_pgno != 0 {
        load_node(bytes, root_pgno, 0, &mut out)?;
    }
    Ok(out)
}

/// Look up one scope record by `scope_id` in a validated committed scope tree, descending
/// in `O(log scopes)` (no full load). `root_pgno == 0` → `None`. Mirrors the byte-level
/// safety of `kv::get`: range-checks every pgno and bounds the descent by `TREE_HEIGHT_MAX`,
/// so hostile-but-checksum-valid input cannot panic or loop even though `open` already
/// validated the tree.
pub(crate) fn find(
    bytes: &[u8],
    root_pgno: u32,
    total_pages: u64,
    scope_id: u32,
) -> Result<Option<ScopeRec>> {
    if root_pgno == 0 {
        return Ok(None);
    }
    let mut pgno = root_pgno;
    for _ in 0..=spec::TREE_HEIGHT_MAX {
        if (pgno as u64) < 2 || (pgno as u64) >= total_pages {
            return Err(Error::Structural("scope child pgno out of range"));
        }
        let page = page_at(bytes, pgno);
        let h = PageHeader::decode(page);
        let count = h.entry_count as usize;
        match h.page_type {
            spec::PAGE_TYPE_SCOPE_LEAF => {
                let leaf = ScopeLeafView::new(page, count);
                // Binary search the sorted ids for an exact match.
                let (mut lo, mut hi) = (0usize, count);
                while lo < hi {
                    let mid = lo + (hi - lo) / 2;
                    if leaf.id(mid) < scope_id {
                        lo = mid + 1;
                    } else {
                        hi = mid;
                    }
                }
                if lo < count && leaf.id(lo) == scope_id {
                    return Ok(Some(leaf.record(lo)?));
                }
                return Ok(None);
            }
            spec::PAGE_TYPE_SCOPE_BRANCH => {
                let b = scope_branch_view(page, count);
                // child index = number of separators with sep <= scope_id.
                let (mut lo, mut hi) = (0usize, count);
                while lo < hi {
                    let mid = lo + (hi - lo) / 2;
                    if b.sep(mid).0 <= scope_id {
                        lo = mid + 1;
                    } else {
                        hi = mid;
                    }
                }
                pgno = b.child(lo);
            }
            _ => return Err(Error::Structural("unexpected page_type in scope table")),
        }
    }
    Err(Error::Invariant("scope path deeper than TREE_HEIGHT_MAX"))
}

fn load_node(bytes: &[u8], pgno: u32, depth: u32, out: &mut Vec<ScopeRec>) -> Result<()> {
    if depth > spec::TREE_HEIGHT_MAX {
        return Err(Error::Invariant("scope path deeper than TREE_HEIGHT_MAX"));
    }
    let page = page_at(bytes, pgno);
    let h = PageHeader::decode(page);
    match h.page_type {
        spec::PAGE_TYPE_SCOPE_LEAF => {
            let count = h.entry_count as usize;
            let leaf = ScopeLeafView::new(page, count);
            for i in 0..count {
                out.push(leaf.record(i)?);
            }
            Ok(())
        }
        spec::PAGE_TYPE_SCOPE_BRANCH => {
            let s = h.entry_count as usize;
            let b = scope_branch_view(page, s);
            for j in 0..b.child_count() {
                load_node(bytes, b.child(j), depth + 1, out)?;
            }
            Ok(())
        }
        _ => Err(Error::Structural("unexpected page_type in scope table")),
    }
}

/// Collect every page number in the scope tree (for freeing on rebuild and the allocator
/// reachable-set walk). The tree is validated, so the walk is bounded.
pub(crate) fn collect_pages(bytes: &[u8], root_pgno: u32, out: &mut Vec<u32>) {
    if root_pgno == 0 {
        return;
    }
    collect_node(bytes, root_pgno, 0, out);
}

fn collect_node(bytes: &[u8], pgno: u32, depth: u32, out: &mut Vec<u32>) {
    if depth > spec::TREE_HEIGHT_MAX {
        return;
    }
    out.push(pgno);
    let page = page_at(bytes, pgno);
    let h = PageHeader::decode(page);
    if h.page_type == spec::PAGE_TYPE_SCOPE_BRANCH {
        let s = h.entry_count as usize;
        let b = scope_branch_view(page, s);
        for j in 0..b.child_count() {
            collect_node(bytes, b.child(j), depth + 1, out);
        }
    }
}

/// Build a single scope-table leaf page in `page` from `recs` (must be `<= scope_leaf_max`).
pub(crate) fn write_scope_leaf(page: &mut [u8], pgno: u32, recs: &[ScopeRec]) {
    page.fill(0);
    PageHeader::write(page, spec::PAGE_TYPE_SCOPE_LEAF, recs.len() as u16, pgno);
    for (i, rec) in recs.iter().enumerate() {
        let off = PAGE_HEADER_SIZE + i * spec::SCOPE_RECORD_SIZE;
        rec.encode(&mut page[off..off + spec::SCOPE_RECORD_SIZE]);
    }
}

/// Build a single scope-table branch page (IPv4-branch layout: `scope_id` separators).
pub(crate) fn write_scope_branch(page: &mut [u8], pgno: u32, seps: &[u32], children: &[u32]) {
    debug_assert_eq!(children.len(), seps.len() + 1);
    page.fill(0);
    PageHeader::write(page, spec::PAGE_TYPE_SCOPE_BRANCH, seps.len() as u16, pgno);
    let c0 = PAGE_HEADER_SIZE;
    page[c0..c0 + 4].copy_from_slice(&children[0].to_le_bytes());
    for i in 0..seps.len() {
        let sep_off = PAGE_HEADER_SIZE + 4 + i * (spec::SCOPE_KEY_WIDTH + 4);
        page[sep_off..sep_off + 4].copy_from_slice(&seps[i].to_le_bytes());
        let c_off = sep_off + 4;
        page[c_off..c_off + 4].copy_from_slice(&children[i + 1].to_le_bytes());
    }
}

/// Read the scope branch as an IPv4 branch view (scope_id == Ipv4Key key).
#[inline]
pub(crate) fn scope_branch_view(page: &[u8], sep_count: usize) -> BranchView<'_, Ipv4Key> {
    BranchView::<Ipv4Key>::new(page, sep_count)
}
