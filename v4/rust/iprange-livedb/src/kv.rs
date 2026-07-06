//! The v4.1 per-scope KV store (§C.4, §D): a bulk-loaded B+tree behind each scope's
//! `kv_root`, mapping a UTF-8 `key` to `(type, value)`. Unlike the IP tree and the scope
//! table (both **fixed-record**), KV pages are **slot-directory** pages with
//! variable-length entries — a separate layout this module builds from scratch.
//!
//! On disk it is a B+tree of `page_type` 6 (branch) / 7 (leaf) with values inlined when
//! small and chained through `page_type` 8 overflow pages when large. Each leaf/branch is
//! a slot directory: a `u16` slot array grows from the front (byte offsets into the page),
//! the entry heap grows from the back, entries sorted by `key`.
//!
//! The writer buffers a scope's KV in memory and **bulk-rebuilds** the tree at commit
//! (§C.4: full-rewrite per scope per commit, no incremental split/merge), so this module
//! exposes page **encoders** and a **measurer** the writer drives, plus the read path
//! (`get`/`list`), recursive `validate`, and `collect_pages` — mirroring `scope.rs`.
//!
//! Tree **shape** (fanout, inline-vs-overflow threshold, bulk-load order) is
//! implementation-defined; the page/entry **encoding** here is normative (§D) so the Go
//! implementation cross-reads these pages. `value_kind` makes inline-vs-overflow
//! self-describing, so a reader parses either regardless of the writer's threshold.

use alloc::vec::Vec;

use crate::crc32c;
use crate::error::{Error, Result};
use crate::spec::{self, PAGE_HEADER_SIZE, PAGE_SIZE};
use crate::wire::{u16_le, u32_le, u64_le, PageHeader};

/// An owned KV entry, as buffered by the writer and returned by reads. `value` is the
/// **whole** reassembled value (inline or overflow-spanning), opaque to the engine
/// except for the `type == 0` UTF-8 check.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct KvEntry {
    pub key: Vec<u8>,
    pub type_: u32,
    pub value: Vec<u8>,
}

/// Validate a KV `key` per §C.4: 1..=1024 bytes, valid UTF-8, no NUL. Returns the key on
/// success so callers can chain. `InvalidInput` on any violation (caller-facing).
pub(crate) fn check_key(key: &[u8]) -> Result<()> {
    if !(spec::KV_KEY_MIN..=spec::KV_KEY_MAX).contains(&key.len()) {
        return Err(Error::InvalidInput("kv key length out of range (1..=1024)"));
    }
    if key.contains(&0) {
        return Err(Error::InvalidInput("kv key contains NUL"));
    }
    if core::str::from_utf8(key).is_err() {
        return Err(Error::InvalidInput("kv key not valid UTF-8"));
    }
    Ok(())
}

/// Validate a `type == 0` value is text (valid UTF-8, no NUL), per §C.4. A no-op for any
/// non-zero `type` (caller-defined binary). `InvalidInput` on a bad text value.
pub(crate) fn check_text_value(type_: u32, value: &[u8]) -> Result<()> {
    if type_ != spec::KV_TYPE_TEXT {
        return Ok(());
    }
    if value.contains(&0) {
        return Err(Error::InvalidInput("kv text value contains NUL"));
    }
    if core::str::from_utf8(value).is_err() {
        return Err(Error::InvalidInput("kv text value not valid UTF-8"));
    }
    Ok(())
}

#[inline]
fn page_at(bytes: &[u8], pgno: u32) -> &[u8] {
    let off = pgno as usize * PAGE_SIZE;
    &bytes[off..off + PAGE_SIZE]
}

#[inline]
fn pgno_in_range(pgno: u32, total_pages: u64) -> bool {
    (pgno as u64) >= 2 && (pgno as u64) < total_pages
}

// --- slot-directory views over an already-bounds-checked page (§D) ---

/// A read view over a KV **leaf** (`page_type 7`): `count` slots, each a `u16` byte offset
/// into the entry heap. Accessors return `Result` because offsets and lengths come from
/// untrusted bytes; every read is range-checked against the page.
pub(crate) struct KvLeafView<'a> {
    page: &'a [u8],
    count: usize,
}

/// One parsed KV leaf entry header (the value is fetched separately by descriptor).
struct LeafEntryHdr<'a> {
    key: &'a [u8],
    type_: u32,
    /// `Some(value)` if inline; `None` if overflow.
    inline: Option<&'a [u8]>,
    /// `(first_pgno, value_total_len)` if overflow.
    overflow: Option<(u32, u64)>,
}

impl<'a> KvLeafView<'a> {
    #[inline]
    fn new(page: &'a [u8], count: usize) -> Self {
        KvLeafView { page, count }
    }

    /// Byte offset of slot `i` (the entry start within the page). Range-checked.
    fn slot(&self, i: usize) -> Result<usize> {
        let so = PAGE_HEADER_SIZE + i * spec::KV_SLOT_SIZE;
        // The slot directory must lie within the page body.
        if so + spec::KV_SLOT_SIZE > PAGE_SIZE {
            return Err(Error::Structural("kv leaf slot out of page"));
        }
        let off = u16_le(self.page, so) as usize;
        // An entry must start after the slot directory and within the page.
        if off < PAGE_HEADER_SIZE + self.count * spec::KV_SLOT_SIZE || off >= PAGE_SIZE {
            return Err(Error::Structural("kv leaf entry offset out of bounds"));
        }
        Ok(off)
    }

    /// Parse entry `i`'s header (key + type + value descriptor), fully range-checked.
    fn entry_hdr(&self, i: usize) -> Result<LeafEntryHdr<'a>> {
        let mut off = self.slot(i)?;
        let key_len = read_u16(self.page, &mut off)? as usize;
        if !(spec::KV_KEY_MIN..=spec::KV_KEY_MAX).contains(&key_len) {
            return Err(Error::Invariant("kv key_len out of range"));
        }
        let key = read_bytes(self.page, &mut off, key_len)?;
        let type_ = read_u32(self.page, &mut off)?;
        let kind = read_u8(self.page, &mut off)?;
        match kind {
            spec::KV_VALUE_INLINE => {
                let vlen = read_u32(self.page, &mut off)? as usize;
                let value = read_bytes(self.page, &mut off, vlen)?;
                Ok(LeafEntryHdr {
                    key,
                    type_,
                    inline: Some(value),
                    overflow: None,
                })
            }
            spec::KV_VALUE_OVERFLOW => {
                let first = read_u32(self.page, &mut off)?;
                let total = read_u64(self.page, &mut off)?;
                Ok(LeafEntryHdr {
                    key,
                    type_,
                    inline: None,
                    overflow: Some((first, total)),
                })
            }
            _ => Err(Error::Structural("kv leaf unknown value_kind")),
        }
    }

    /// The `key` of entry `i` (for ordered descent/scan). Range-checked.
    fn key(&self, i: usize) -> Result<&'a [u8]> {
        Ok(self.entry_hdr(i)?.key)
    }
}

/// A read view over a KV **branch** (`page_type 6`): a leftmost child pgno followed by
/// `count` separators, each `(sep_key, child_pgno)`, in a slot directory.
pub(crate) struct KvBranchView<'a> {
    page: &'a [u8],
    count: usize,
}

impl<'a> KvBranchView<'a> {
    #[inline]
    fn new(page: &'a [u8], count: usize) -> Self {
        KvBranchView { page, count }
    }

    /// The leftmost child pgno (precedes every separator), at a fixed offset after the
    /// slot directory's reserved leading `u32`. Stored as the first heap field.
    fn leftmost(&self) -> Result<u32> {
        // The leftmost child is stored in the 4 bytes immediately after the page header,
        // before the slot directory (a fixed, non-slotted field — like the IP branch).
        Ok(u32_le(self.page, PAGE_HEADER_SIZE))
    }

    /// Byte offset of separator slot `i`. Range-checked.
    fn slot(&self, i: usize) -> Result<usize> {
        let so = KV_BRANCH_DIR_START + i * spec::KV_SLOT_SIZE;
        if so + spec::KV_SLOT_SIZE > PAGE_SIZE {
            return Err(Error::Structural("kv branch slot out of page"));
        }
        let off = u16_le(self.page, so) as usize;
        if off < KV_BRANCH_DIR_START + self.count * spec::KV_SLOT_SIZE || off >= PAGE_SIZE {
            return Err(Error::Structural("kv branch entry offset out of bounds"));
        }
        Ok(off)
    }

    /// Separator `i`: `(sep_key, child_pgno)`. Range-checked.
    fn sep(&self, i: usize) -> Result<(&'a [u8], u32)> {
        let mut off = self.slot(i)?;
        let sep_len = read_u16(self.page, &mut off)? as usize;
        if !(spec::KV_KEY_MIN..=spec::KV_KEY_MAX).contains(&sep_len) {
            return Err(Error::Invariant("kv sep_len out of range"));
        }
        let key = read_bytes(self.page, &mut off, sep_len)?;
        let child = read_u32(self.page, &mut off)?;
        Ok((key, child))
    }

    /// Child pgno for descent index `j` (`0` = leftmost, `j>=1` follows `sep[j-1]`).
    fn child(&self, j: usize) -> Result<u32> {
        if j == 0 {
            self.leftmost()
        } else {
            Ok(self.sep(j - 1)?.1)
        }
    }
}

/// The KV branch slot directory starts after the header **and** the leftmost-child `u32`.
const KV_BRANCH_DIR_START: usize = PAGE_HEADER_SIZE + 4;

/// Enforce canonical packing of a KV slot-directory page (§D), closing the wrong-answer hole
/// where a CRC-valid file shrinks `entry_count` (stale slot/heap bytes ignored) or shrinks an
/// inline value length (leftover heap bytes ignored) and is accepted as a different view.
/// Unlike the fixed-record IP-tree/scope-table pages (which enforce a single tail-zero region),
/// a slot-directory page has a slot directory at the front and a variable-length entry heap at
/// the back, so canonicality has two parts:
///   1. the free gap `[slot_dir_end, heap_start)` (everything between the slot directory and
///      the lowest entry) MUST be entirely zero — a shrunk `entry_count` leaves the dropped
///      slot and its entry bytes in this gap, non-zero; and
///   2. the entries MUST tile `[heap_start, PAGE_SIZE)` EXACTLY — contiguous, no gaps, ending at
///      `PAGE_SIZE` — so a shrunk key/value/separator length (a shorter entry) leaves an
///      uncovered heap byte the writer never produces.
///
/// `spans` is `(start, end)` per entry: `start` is its slot's byte offset, `end = start +`
/// encoded entry size. The caller guarantees `count >= 1` (leaf/branch occupancy), and every
/// `start >= slot_dir_end` and `end <= PAGE_SIZE` (the slot/cursor range checks), so the slice
/// and arithmetic below cannot panic.
fn check_canonical_packing(
    page: &[u8],
    slot_dir_end: usize,
    mut spans: Vec<(usize, usize)>,
    free_gap_msg: &'static str,
    tiling_msg: &'static str,
) -> Result<()> {
    let heap_start = spans.iter().map(|&(s, _)| s).min().unwrap_or(PAGE_SIZE);
    // (1) The free gap between the slot directory and the entry heap MUST be zero.
    if page[slot_dir_end..heap_start].iter().any(|&b| b != 0) {
        return Err(Error::NonZeroReserved(free_gap_msg));
    }
    // (2) The entries MUST tile [heap_start, PAGE_SIZE) with no gap or overlap.
    spans.sort_unstable_by_key(|&(s, _)| s);
    let mut expect = heap_start;
    for &(s, e) in &spans {
        if s != expect {
            return Err(Error::Structural(tiling_msg));
        }
        expect = e;
    }
    if expect != PAGE_SIZE {
        return Err(Error::Structural(tiling_msg));
    }
    Ok(())
}

// --- bounds-safe cursor reads over a single page ---

#[inline]
fn read_u8(page: &[u8], off: &mut usize) -> Result<u8> {
    if *off + 1 > PAGE_SIZE {
        return Err(Error::Structural("kv entry read past page"));
    }
    let v = page[*off];
    *off += 1;
    Ok(v)
}
#[inline]
fn read_u16(page: &[u8], off: &mut usize) -> Result<u16> {
    if *off + 2 > PAGE_SIZE {
        return Err(Error::Structural("kv entry read past page"));
    }
    let v = u16_le(page, *off);
    *off += 2;
    Ok(v)
}
#[inline]
fn read_u32(page: &[u8], off: &mut usize) -> Result<u32> {
    if *off + 4 > PAGE_SIZE {
        return Err(Error::Structural("kv entry read past page"));
    }
    let v = u32_le(page, *off);
    *off += 4;
    Ok(v)
}
#[inline]
fn read_u64(page: &[u8], off: &mut usize) -> Result<u64> {
    if *off + 8 > PAGE_SIZE {
        return Err(Error::Structural("kv entry read past page"));
    }
    let v = u64_le(page, *off);
    *off += 8;
    Ok(v)
}
#[inline]
fn read_bytes<'a>(page: &'a [u8], off: &mut usize, len: usize) -> Result<&'a [u8]> {
    if *off + len > PAGE_SIZE {
        return Err(Error::Structural("kv entry read past page"));
    }
    let s = &page[*off..*off + len];
    *off += len;
    Ok(s)
}

// --- overflow chains (read by count, never to a terminator; §C.5) ---

/// Reassemble an overflow value: concatenate payloads along the chain from `first_pgno`,
/// truncated to `value_total_len`. Reads **exactly** `ceil(total/payload)` pages; a
/// revisit, cycle, length mismatch, bad pgno, or page-type/self-pgno error → `Corruption`.
/// Never loops to a terminator (§C.5).
///
/// `visited` selects the revisit/cycle defense (F2/F4), both O(1) per page (never an O(n²)
/// scan): `Some(bitset)` is the file-wide `[bool]` of length `total_pages` shared across the
/// whole validate walk — a page seen twice (within this chain, across two KV entries, or
/// anywhere else in the metadata forest) is structural corruption. `None` is the read path
/// (`get`/`list`), where the tree is already validated (chains are disjoint), so a small
/// per-chain set suffices for within-chain cycle defense — avoiding an O(total_pages)
/// allocation per read.
fn read_overflow(
    bytes: &[u8],
    first: u32,
    total: u64,
    total_pages: u64,
    mut visited: Option<&mut [bool]>,
) -> Result<Vec<u8>> {
    if total == 0 {
        // A zero-length value uses no overflow pages (the writer stores it inline), so a
        // chain claiming total==0 is malformed.
        return Err(Error::Structural("kv overflow chain for empty value"));
    }
    let payload = spec::OVERFLOW_PAYLOAD as u64;
    let want_pages = total.div_ceil(payload);
    // Cap the page budget: a chain longer than the file cannot be valid.
    if want_pages > total_pages {
        return Err(Error::Structural("kv overflow chain longer than file"));
    }
    let mut out = Vec::with_capacity(total as usize);
    let mut pgno = first;
    // Read path (visited == None): per-chain set, O(1) lookup, no O(total_pages) allocation.
    let mut local = alloc::collections::BTreeSet::new();
    for step in 0..want_pages {
        if !pgno_in_range(pgno, total_pages) {
            return Err(Error::Structural("kv overflow pgno out of range"));
        }
        // Revisit/cycle defense (O(1), F4): a page may be reached at most once; on the
        // validate path the shared bitset also rejects a page aliased by any other entry
        // (F2). Bounded by the computed page count, never by a sentinel.
        let revisited = match visited.as_deref_mut() {
            Some(v) => {
                let was = v[pgno as usize];
                v[pgno as usize] = true;
                was
            }
            None => !local.insert(pgno),
        };
        if revisited {
            return Err(Error::Structural("kv overflow chain revisits a page"));
        }
        let page = page_at(bytes, pgno);
        if !crc32c::verify_page(page) {
            return Err(Error::ChecksumFailed("kv overflow page"));
        }
        let h = PageHeader::decode(page);
        if h.page_type != spec::PAGE_TYPE_OVERFLOW {
            return Err(Error::Structural("kv overflow wrong page_type"));
        }
        if h.reserved != 0 {
            return Err(Error::NonZeroReserved("kv overflow header reserved"));
        }
        if h.pgno != pgno {
            return Err(Error::Structural("kv overflow self-pgno mismatch"));
        }
        let next = u32_le(page, spec::OVERFLOW_NEXT_PGNO);
        let remaining = total - step * payload;
        let take = remaining.min(payload) as usize;
        let body = PAGE_HEADER_SIZE + 4;
        out.extend_from_slice(&page[body..body + take]);
        let is_last = step + 1 == want_pages;
        if is_last {
            // The last page MUST terminate the chain (next == 0) and its unused payload
            // tail MUST be zero — read-by-count means a non-zero `next` is corruption.
            if next != 0 {
                return Err(Error::Structural("kv overflow chain longer than length"));
            }
            if page[body + take..].iter().any(|&b| b != 0) {
                return Err(Error::NonZeroReserved("kv overflow last-page tail"));
            }
        } else {
            if next == 0 {
                return Err(Error::Structural("kv overflow chain shorter than length"));
            }
            pgno = next;
        }
    }
    Ok(out)
}

/// Streaming structural walk of an overflow chain for the **validate** path (§C.5): the same
/// checks as [`read_overflow`] (pgno range, revisit/cycle via the shared `visited` bitset,
/// per-page CRC, page_type, self-pgno, reserved, read-by-count budget, and a last page that
/// terminates the chain with a zero tail) but it does **not** materialize the value — peak
/// extra memory is O(1), not O(value). When `text` (type 0), each page payload chunk is
/// checked for NUL and fed to an incremental UTF-8 validator (a multibyte char may straddle a
/// page boundary), so a text value is validated without ever holding the whole value.
fn validate_overflow_value(
    bytes: &[u8],
    first: u32,
    total: u64,
    total_pages: u64,
    visited: &mut [bool],
    text: bool,
) -> Result<()> {
    if total == 0 {
        return Err(Error::Structural("kv overflow chain for empty value"));
    }
    let payload = spec::OVERFLOW_PAYLOAD as u64;
    let want_pages = total.div_ceil(payload);
    if want_pages > total_pages {
        return Err(Error::Structural("kv overflow chain longer than file"));
    }
    let mut pgno = first;
    let mut utf8 = Utf8Stream::new();
    for step in 0..want_pages {
        if !pgno_in_range(pgno, total_pages) {
            return Err(Error::Structural("kv overflow pgno out of range"));
        }
        if visited[pgno as usize] {
            return Err(Error::Structural("kv overflow chain revisits a page"));
        }
        visited[pgno as usize] = true;
        let page = page_at(bytes, pgno);
        if !crc32c::verify_page(page) {
            return Err(Error::ChecksumFailed("kv overflow page"));
        }
        let h = PageHeader::decode(page);
        if h.page_type != spec::PAGE_TYPE_OVERFLOW {
            return Err(Error::Structural("kv overflow wrong page_type"));
        }
        if h.reserved != 0 {
            return Err(Error::NonZeroReserved("kv overflow header reserved"));
        }
        if h.pgno != pgno {
            return Err(Error::Structural("kv overflow self-pgno mismatch"));
        }
        let next = u32_le(page, spec::OVERFLOW_NEXT_PGNO);
        let remaining = total - step * payload;
        let take = remaining.min(payload) as usize;
        let body = PAGE_HEADER_SIZE + 4;
        if text {
            let chunk = &page[body..body + take];
            if chunk.contains(&0) {
                return Err(Error::Invariant("kv text value contains NUL"));
            }
            utf8.feed(chunk)?;
        }
        let is_last = step + 1 == want_pages;
        if is_last {
            if next != 0 {
                return Err(Error::Structural("kv overflow chain longer than length"));
            }
            if page[body + take..].iter().any(|&b| b != 0) {
                return Err(Error::NonZeroReserved("kv overflow last-page tail"));
            }
        } else {
            if next == 0 {
                return Err(Error::Structural("kv overflow chain shorter than length"));
            }
            pgno = next;
        }
    }
    if text {
        utf8.finish()?;
    }
    Ok(())
}

/// Incremental UTF-8 validator (no allocation): feed payload chunks in order. A multibyte
/// sequence may straddle a chunk boundary, so up to 3 trailing bytes of an incomplete-but-
/// valid prefix are carried to the next chunk; [`finish`](Self::finish) rejects a value that
/// ends mid-sequence. Equivalent accept/reject to `from_utf8` over the whole value.
struct Utf8Stream {
    carry: [u8; 3],
    carry_len: usize,
}

impl Utf8Stream {
    fn new() -> Self {
        Utf8Stream {
            carry: [0; 3],
            carry_len: 0,
        }
    }

    fn feed(&mut self, chunk: &[u8]) -> Result<()> {
        let mut data = chunk;
        if self.carry_len > 0 {
            // Complete the carried partial char from the front of this chunk. `carry[0]` is a
            // valid multibyte lead (`from_utf8` flagged the prior tail as incomplete-but-valid),
            // so its expected length is known; a char is ≤4 bytes, spanning at most two chunks.
            let need = utf8_seq_len(self.carry[0])?;
            let take = (need - self.carry_len).min(data.len());
            let mut buf = [0u8; 4];
            buf[..self.carry_len].copy_from_slice(&self.carry[..self.carry_len]);
            buf[self.carry_len..self.carry_len + take].copy_from_slice(&data[..take]);
            let have = self.carry_len + take;
            if have < need {
                // This chunk was shorter than the missing bytes (a tiny last page); still partial.
                self.carry[..have].copy_from_slice(&buf[..have]);
                self.carry_len = have;
                return Ok(());
            }
            if core::str::from_utf8(&buf[..need]).is_err() {
                return Err(Error::Invariant("kv text value not valid UTF-8"));
            }
            self.carry_len = 0;
            data = &data[take..];
        }
        match core::str::from_utf8(data) {
            Ok(_) => Ok(()),
            Err(e) => {
                // A definite error (error_len Some) is invalid; otherwise the trailing bytes
                // from valid_up_to are an incomplete-but-valid prefix to carry (must be ≤3).
                if e.error_len().is_some() {
                    return Err(Error::Invariant("kv text value not valid UTF-8"));
                }
                let rest = &data[e.valid_up_to()..];
                if rest.len() > 3 {
                    return Err(Error::Invariant("kv text value not valid UTF-8"));
                }
                self.carry[..rest.len()].copy_from_slice(rest);
                self.carry_len = rest.len();
                Ok(())
            }
        }
    }

    fn finish(&self) -> Result<()> {
        if self.carry_len != 0 {
            return Err(Error::Invariant("kv text value not valid UTF-8"));
        }
        Ok(())
    }
}

/// Expected byte length (2..=4) of a UTF-8 sequence from its lead byte; error for a non-lead
/// or invalid lead.
fn utf8_seq_len(lead: u8) -> Result<usize> {
    if lead & 0b1110_0000 == 0b1100_0000 {
        Ok(2)
    } else if lead & 0b1111_0000 == 0b1110_0000 {
        Ok(3)
    } else if lead & 0b1111_1000 == 0b1111_0000 {
        Ok(4)
    } else {
        Err(Error::Invariant("kv text value not valid UTF-8"))
    }
}

// --- read path: get / list ---

/// Look up `key` in the KV tree rooted at `root_pgno` (already validated). Returns the
/// reassembled `(type, value)` or `None`. `root_pgno == 0` → `None`.
pub(crate) fn get(
    bytes: &[u8],
    root_pgno: u32,
    key: &[u8],
    total_pages: u64,
) -> Result<Option<(u32, Vec<u8>)>> {
    if root_pgno == 0 {
        return Ok(None);
    }
    let mut pgno = root_pgno;
    for _ in 0..=spec::TREE_HEIGHT_MAX {
        if !pgno_in_range(pgno, total_pages) {
            return Err(Error::Structural("kv child pgno out of range"));
        }
        let page = page_at(bytes, pgno);
        let h = PageHeader::decode(page);
        let count = h.entry_count as usize;
        match h.page_type {
            spec::PAGE_TYPE_KV_LEAF => {
                let leaf = KvLeafView::new(page, count);
                // Binary search the sorted slots.
                let (mut lo, mut hi) = (0usize, count);
                while lo < hi {
                    let mid = lo + (hi - lo) / 2;
                    if leaf.key(mid)? < key {
                        lo = mid + 1;
                    } else {
                        hi = mid;
                    }
                }
                if lo < count {
                    let hdr = leaf.entry_hdr(lo)?;
                    if hdr.key == key {
                        let value = match (hdr.inline, hdr.overflow) {
                            (Some(v), None) => v.to_vec(),
                            (None, Some((first, total))) => {
                                // Read path: per-chain revisit check (None), no O(total_pages)
                                // allocation; the tree is already validated.
                                read_overflow(bytes, first, total, total_pages, None)?
                            }
                            _ => return Err(Error::Structural("kv entry descriptor")),
                        };
                        return Ok(Some((hdr.type_, value)));
                    }
                }
                return Ok(None);
            }
            spec::PAGE_TYPE_KV_BRANCH => {
                let b = KvBranchView::new(page, count);
                // child index = number of separators with sep_key <= key.
                let (mut lo, mut hi) = (0usize, count);
                while lo < hi {
                    let mid = lo + (hi - lo) / 2;
                    if b.sep(mid)?.0 <= key {
                        lo = mid + 1;
                    } else {
                        hi = mid;
                    }
                }
                pgno = b.child(lo)?;
            }
            _ => return Err(Error::Structural("kv unexpected page_type")),
        }
    }
    Err(Error::Invariant("kv path deeper than TREE_HEIGHT_MAX"))
}

/// Append every entry in the KV tree (in key order) to `out` as full `KvEntry`s
/// (overflow values reassembled). The tree is validated, so the walk is bounded.
pub(crate) fn list(
    bytes: &[u8],
    root_pgno: u32,
    total_pages: u64,
    out: &mut Vec<KvEntry>,
) -> Result<()> {
    if root_pgno == 0 {
        return Ok(());
    }
    list_node(bytes, root_pgno, 0, total_pages, out)
}

fn list_node(
    bytes: &[u8],
    pgno: u32,
    depth: u32,
    total_pages: u64,
    out: &mut Vec<KvEntry>,
) -> Result<()> {
    if depth > spec::TREE_HEIGHT_MAX {
        return Err(Error::Invariant("kv path deeper than TREE_HEIGHT_MAX"));
    }
    let page = page_at(bytes, pgno);
    let h = PageHeader::decode(page);
    let count = h.entry_count as usize;
    match h.page_type {
        spec::PAGE_TYPE_KV_LEAF => {
            let leaf = KvLeafView::new(page, count);
            for i in 0..count {
                let hdr = leaf.entry_hdr(i)?;
                let value = match (hdr.inline, hdr.overflow) {
                    (Some(v), None) => v.to_vec(),
                    (None, Some((first, total))) => {
                        read_overflow(bytes, first, total, total_pages, None)?
                    }
                    _ => return Err(Error::Structural("kv entry descriptor")),
                };
                out.push(KvEntry {
                    key: hdr.key.to_vec(),
                    type_: hdr.type_,
                    value,
                });
            }
            Ok(())
        }
        spec::PAGE_TYPE_KV_BRANCH => {
            let b = KvBranchView::new(page, count);
            for j in 0..=count {
                list_node(bytes, b.child(j)?, depth + 1, total_pages, out)?;
            }
            Ok(())
        }
        _ => Err(Error::Structural("kv unexpected page_type")),
    }
}

// --- allocator support: collect every page reachable from a kv_root ---

/// Collect every page number in the KV tree **and** its overflow chains (for freeing on
/// rebuild and for the allocator reachable-set walk). The tree is validated, so the walk
/// is bounded. Best-effort on a not-yet-validated tree (used only on validated images).
pub(crate) fn collect_pages(bytes: &[u8], root_pgno: u32, total_pages: u64, out: &mut Vec<u32>) {
    if root_pgno == 0 {
        return;
    }
    collect_node(bytes, root_pgno, 0, total_pages, out);
}

fn collect_node(bytes: &[u8], pgno: u32, depth: u32, total_pages: u64, out: &mut Vec<u32>) {
    if depth > spec::TREE_HEIGHT_MAX || !pgno_in_range(pgno, total_pages) {
        return;
    }
    out.push(pgno);
    let page = page_at(bytes, pgno);
    let h = PageHeader::decode(page);
    let count = h.entry_count as usize;
    match h.page_type {
        spec::PAGE_TYPE_KV_LEAF => {
            let leaf = KvLeafView::new(page, count);
            for i in 0..count {
                if let Ok(hdr) = leaf.entry_hdr(i) {
                    if let Some((first, total)) = hdr.overflow {
                        collect_overflow(bytes, first, total, total_pages, out);
                    }
                }
            }
        }
        spec::PAGE_TYPE_KV_BRANCH => {
            let b = KvBranchView::new(page, count);
            for j in 0..=count {
                if let Ok(c) = b.child(j) {
                    collect_node(bytes, c, depth + 1, total_pages, out);
                }
            }
        }
        _ => {}
    }
}

/// Collect the overflow chain pages from `first`, bounded by the computed page count and
/// a revisit guard (no terminator-driven loop; §C.5).
fn collect_overflow(bytes: &[u8], first: u32, total: u64, total_pages: u64, out: &mut Vec<u32>) {
    if total == 0 {
        return;
    }
    let payload = spec::OVERFLOW_PAYLOAD as u64;
    let want_pages = total.div_ceil(payload);
    let mut pgno = first;
    // Per-chain revisit guard, O(log n) — not an O(n²) `out.contains` scan of the whole
    // cross-scope accumulator. On a validated tree chains are disjoint, so per-chain suffices.
    let mut seen = alloc::collections::BTreeSet::new();
    for _ in 0..want_pages {
        if !pgno_in_range(pgno, total_pages) || !seen.insert(pgno) {
            return;
        }
        out.push(pgno);
        let page = page_at(bytes, pgno);
        if PageHeader::decode(page).page_type != spec::PAGE_TYPE_OVERFLOW {
            return;
        }
        pgno = u32_le(page, spec::OVERFLOW_NEXT_PGNO);
        if pgno == 0 {
            return;
        }
    }
}

// --- reader validation (§C.5, never-panic) ---

/// Recursively validate a KV subtree (reader §9 for §D): per-page CRC32C, page-type +
/// self-pgno + reserved checks, slot directory in bounds, entries sorted + key-disjoint
/// across the whole tree, child pgnos in `[2, total_pages)`, depth bounded by
/// `TREE_HEIGHT_MAX`, overflow chains read-by-count, and `type == 0` text validation over
/// the **whole reassembled** value. Never panics/loops on hostile-but-checksum-valid input.
/// `visited` is the file-wide page-disjointness bitset (F2), length `total_pages`, shared
/// with the scope-table walk and every other per-scope KV tree: a page reached twice
/// (a duplicate child pgno, a shared subtree, or a shared overflow chain across two
/// entries) is structural corruption. This makes the whole v4.1 metadata page forest
/// provably disjoint and acyclic, and subsumes per-node duplicate-child checks.
pub(crate) fn validate(
    bytes: &[u8],
    root_pgno: u32,
    total_pages: u64,
    visited: &mut [bool],
) -> Result<()> {
    if root_pgno == 0 {
        return Ok(());
    }
    if !pgno_in_range(root_pgno, total_pages) {
        return Err(Error::Structural("kv_root out of range"));
    }
    let mut leaf_depth: Option<u32> = None;
    let mut prev_key: Option<Vec<u8>> = None;
    // Root covers all keys; `lo` is the inclusive lower bound (empty = the minimum, since
    // keys are non-empty), `hi` the exclusive upper bound (None = +infinity).
    validate_node(
        bytes,
        root_pgno,
        1,
        total_pages,
        visited,
        &mut leaf_depth,
        &mut prev_key,
        b"",
        None,
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
    prev_key: &mut Option<Vec<u8>>,
    lo: &[u8],
    hi: Option<&[u8]>,
) -> Result<()> {
    if depth > spec::TREE_HEIGHT_MAX {
        return Err(Error::Invariant("kv path deeper than TREE_HEIGHT_MAX"));
    }
    // File-wide page disjointness (F2): every page belongs to exactly one parent.
    if visited[pgno as usize] {
        return Err(Error::Structural("kv page reached twice (aliased)"));
    }
    visited[pgno as usize] = true;
    let page = page_at(bytes, pgno);
    if !crc32c::verify_page(page) {
        return Err(Error::ChecksumFailed("kv page"));
    }
    let h = PageHeader::decode(page);
    if h.reserved != 0 {
        return Err(Error::NonZeroReserved("kv page header reserved"));
    }
    if h.pgno != pgno {
        return Err(Error::Structural("kv page self-pgno mismatch"));
    }
    let count = h.entry_count as usize;
    match h.page_type {
        spec::PAGE_TYPE_KV_LEAF => {
            match *leaf_depth {
                None => *leaf_depth = Some(depth),
                Some(d) if d == depth => {}
                _ => return Err(Error::Invariant("kv leaves at differing depths")),
            }
            if count < 1 {
                return Err(Error::Invariant("kv leaf empty"));
            }
            // The slot directory must fit; each slot's entry must lie after it.
            if PAGE_HEADER_SIZE + count * spec::KV_SLOT_SIZE > PAGE_SIZE {
                return Err(Error::Structural("kv leaf slot directory overflows page"));
            }
            let leaf = KvLeafView::new(page, count);
            // (start, end) of each entry's heap bytes, for the canonical-packing check below.
            let mut spans: Vec<(usize, usize)> = Vec::with_capacity(count);
            for i in 0..count {
                let start = leaf.slot(i)?;
                let hdr = leaf.entry_hdr(i)?;
                check_key(hdr.key)?;
                // Within the node's routing interval [lo, hi) (hi exclusive; None = +inf).
                if hdr.key < lo {
                    return Err(Error::Invariant("kv key below node bound"));
                }
                if let Some(h) = hi {
                    if hdr.key >= h {
                        return Err(Error::Invariant("kv key at/above node bound"));
                    }
                }
                // Globally strictly increasing keys (sorted + disjoint across the tree).
                if let Some(p) = prev_key {
                    if hdr.key <= p.as_slice() {
                        return Err(Error::Invariant("kv keys not sorted/disjoint"));
                    }
                }
                *prev_key = Some(hdr.key.to_vec());
                // Validate the value WITHOUT materializing an overflow-spanning one
                // (§C.4/§C.5): text (type 0) must be NUL-free valid UTF-8. The shared
                // `visited` marks each overflow page so a chain aliased by another entry
                // (the glm PoC wrong-answer bug, F2) is rejected. Each arm yields the entry's
                // encoded size so the canonical-packing check can verify the heap tiles exactly.
                let text = hdr.type_ == spec::KV_TYPE_TEXT;
                let span = match (hdr.inline, hdr.overflow) {
                    (Some(v), None) => {
                        if text {
                            if v.contains(&0) {
                                return Err(Error::Invariant("kv text value contains NUL"));
                            }
                            if core::str::from_utf8(v).is_err() {
                                return Err(Error::Invariant("kv text value not valid UTF-8"));
                            }
                        }
                        spec::kv_inline_entry_size(hdr.key.len(), v.len())
                    }
                    (None, Some((first, total))) => {
                        validate_overflow_value(
                            bytes,
                            first,
                            total,
                            total_pages,
                            &mut *visited,
                            text,
                        )?;
                        spec::kv_overflow_entry_size(hdr.key.len())
                    }
                    _ => return Err(Error::Structural("kv entry descriptor")),
                };
                spans.push((start, start + span));
            }
            // Canonical packing: free gap zero + entries tile the heap exactly (§D). Without
            // this a CRC-valid file could shrink entry_count or an inline value and be accepted
            // as a different view.
            let slot_dir_end = PAGE_HEADER_SIZE + count * spec::KV_SLOT_SIZE;
            check_canonical_packing(
                page,
                slot_dir_end,
                spans,
                "kv leaf free gap not zero",
                "kv leaf entries not canonically packed",
            )?;
            Ok(())
        }
        spec::PAGE_TYPE_KV_BRANCH => {
            if count < 1 {
                return Err(Error::Invariant("kv branch has no separators"));
            }
            if KV_BRANCH_DIR_START + count * spec::KV_SLOT_SIZE > PAGE_SIZE {
                return Err(Error::Structural("kv branch slot directory overflows page"));
            }
            let b = KvBranchView::new(page, count);
            // Separators in bound and strictly increasing: lo < sep[0] < … < sep[count-1],
            // and (when hi is set) sep[count-1] < hi.
            let mut prev_sep: Option<Vec<u8>> = None;
            // (start, end) of each separator's heap bytes, for the canonical-packing check.
            let mut spans: Vec<(usize, usize)> = Vec::with_capacity(count);
            for i in 0..count {
                let start = b.slot(i)?;
                let (sep, _) = b.sep(i)?;
                check_key(sep)?;
                if sep <= lo {
                    return Err(Error::Invariant("kv separator <= lo"));
                }
                if let Some(h) = hi {
                    if sep >= h {
                        return Err(Error::Invariant("kv separator >= hi"));
                    }
                }
                if let Some(p) = &prev_sep {
                    if sep <= p.as_slice() {
                        return Err(Error::Invariant("kv separators not increasing"));
                    }
                }
                prev_sep = Some(sep.to_vec());
                spans.push((start, start + spec::kv_branch_sep_size(sep.len())));
            }
            // Canonical packing: free gap zero + separators tile the heap exactly (§D). The
            // leftmost-child u32 lives in [PAGE_HEADER_SIZE, KV_BRANCH_DIR_START) and is part of
            // the header region, NOT the free gap, so the gap starts at slot_dir_end.
            let slot_dir_end = KV_BRANCH_DIR_START + count * spec::KV_SLOT_SIZE;
            check_canonical_packing(
                page,
                slot_dir_end,
                spans,
                "kv branch free gap not zero",
                "kv branch entries not canonically packed",
            )?;
            // Children in range.
            for j in 0..=count {
                let c = b.child(j)?;
                if !pgno_in_range(c, total_pages) {
                    return Err(Error::Structural("kv child pgno out of range"));
                }
            }
            // Recurse with separator-derived bounds so each child's keys are confined to its
            // routing interval (child[0]=[lo, sep[0]); child[i]=[sep[i-1], sep[i]); child
            // [count]=[sep[count-1], hi)). Without this a file with valid CRCs but mismatched
            // separators would misroute lookups (mirrors the IP-tree validator, reader.rs).
            for j in 0..=count {
                let child_lo = if j == 0 { lo } else { b.sep(j - 1)?.0 };
                let child_hi = if j == count { hi } else { Some(b.sep(j)?.0) };
                validate_node(
                    bytes,
                    b.child(j)?,
                    depth + 1,
                    total_pages,
                    visited,
                    leaf_depth,
                    prev_key,
                    child_lo,
                    child_hi,
                )?;
            }
            Ok(())
        }
        _ => Err(Error::Structural("kv unexpected page_type")),
    }
}

// --- page encoders (writer-driven; §D) ---

/// The encoded body bytes (entries + slots) of one KV leaf entry, for bulk-load packing.
/// `value_kind` is decided by the caller (inline vs overflow) before this point.
#[derive(Clone)]
pub(crate) enum LeafSlot {
    /// Inline entry: stores the value bytes directly.
    Inline {
        key: Vec<u8>,
        type_: u32,
        value: Vec<u8>,
    },
    /// Overflow entry: stores the chain head + total length.
    Overflow {
        key: Vec<u8>,
        type_: u32,
        first_pgno: u32,
        total_len: u64,
    },
}

impl LeafSlot {
    /// The key (sort/separator key).
    pub(crate) fn key(&self) -> &[u8] {
        match self {
            LeafSlot::Inline { key, .. } | LeafSlot::Overflow { key, .. } => key,
        }
    }

    /// Encoded entry size in the heap (excluding the 2-byte slot), §D.
    pub(crate) fn entry_size(&self) -> usize {
        match self {
            LeafSlot::Inline { key, value, .. } => {
                spec::kv_inline_entry_size(key.len(), value.len())
            }
            LeafSlot::Overflow { key, .. } => spec::kv_overflow_entry_size(key.len()),
        }
    }

    /// Total page footprint of this entry: heap bytes + its slot, §D.
    pub(crate) fn footprint(&self) -> usize {
        self.entry_size() + spec::KV_SLOT_SIZE
    }

    /// Encode this entry's heap bytes at `out` (caller positions `out` correctly).
    fn encode(&self, out: &mut [u8]) {
        let mut p = 0usize;
        match self {
            LeafSlot::Inline { key, type_, value } => {
                put_u16(out, &mut p, key.len() as u16);
                put_bytes(out, &mut p, key);
                put_u32(out, &mut p, *type_);
                out[p] = spec::KV_VALUE_INLINE;
                p += 1;
                put_u32(out, &mut p, value.len() as u32);
                put_bytes(out, &mut p, value);
            }
            LeafSlot::Overflow {
                key,
                type_,
                first_pgno,
                total_len,
            } => {
                put_u16(out, &mut p, key.len() as u16);
                put_bytes(out, &mut p, key);
                put_u32(out, &mut p, *type_);
                out[p] = spec::KV_VALUE_OVERFLOW;
                p += 1;
                put_u32(out, &mut p, *first_pgno);
                put_u64(out, &mut p, *total_len);
            }
        }
    }
}

/// Build one KV leaf page in `page` from `slots` (sorted by key). The slot directory grows
/// from the front; entries are packed from the back. The caller has sized `slots` to fit.
pub(crate) fn write_kv_leaf(page: &mut [u8], pgno: u32, slots: &[LeafSlot]) {
    page.fill(0);
    PageHeader::write(page, spec::PAGE_TYPE_KV_LEAF, slots.len() as u16, pgno);
    let mut heap_end = PAGE_SIZE; // entries grow downward from the page end
    for (i, slot) in slots.iter().enumerate() {
        let sz = slot.entry_size();
        let start = heap_end - sz;
        slot.encode(&mut page[start..heap_end]);
        let so = PAGE_HEADER_SIZE + i * spec::KV_SLOT_SIZE;
        page[so..so + 2].copy_from_slice(&(start as u16).to_le_bytes());
        heap_end = start;
    }
}

/// A KV branch separator for bulk-load: `(sep_key, child_pgno)`.
pub(crate) struct BranchSep {
    pub sep: Vec<u8>,
    pub child: u32,
}

/// Build one KV branch page: a leftmost child pgno (fixed field) + `seps` separators in a
/// slot directory (sorted by `sep`). The caller has sized `seps` to fit.
pub(crate) fn write_kv_branch(page: &mut [u8], pgno: u32, leftmost: u32, seps: &[BranchSep]) {
    page.fill(0);
    PageHeader::write(page, spec::PAGE_TYPE_KV_BRANCH, seps.len() as u16, pgno);
    page[PAGE_HEADER_SIZE..PAGE_HEADER_SIZE + 4].copy_from_slice(&leftmost.to_le_bytes());
    let mut heap_end = PAGE_SIZE;
    for (i, s) in seps.iter().enumerate() {
        let sz = spec::kv_branch_sep_size(s.sep.len());
        let start = heap_end - sz;
        let out = &mut page[start..heap_end];
        let mut p = 0usize;
        put_u16(out, &mut p, s.sep.len() as u16);
        put_bytes(out, &mut p, &s.sep);
        put_u32(out, &mut p, s.child);
        let so = KV_BRANCH_DIR_START + i * spec::KV_SLOT_SIZE;
        page[so..so + 2].copy_from_slice(&(start as u16).to_le_bytes());
        heap_end = start;
    }
}

/// Write one overflow page: header, `next_pgno`, and `payload` (zero-padded to the page).
pub(crate) fn write_overflow(page: &mut [u8], pgno: u32, next: u32, payload: &[u8]) {
    debug_assert!(payload.len() <= spec::OVERFLOW_PAYLOAD);
    page.fill(0);
    PageHeader::write(page, spec::PAGE_TYPE_OVERFLOW, 0, pgno);
    page[spec::OVERFLOW_NEXT_PGNO..spec::OVERFLOW_NEXT_PGNO + 4]
        .copy_from_slice(&next.to_le_bytes());
    let body = PAGE_HEADER_SIZE + 4;
    page[body..body + payload.len()].copy_from_slice(payload);
}

// --- in-page write cursor helpers ---

#[inline]
fn put_u16(out: &mut [u8], p: &mut usize, v: u16) {
    out[*p..*p + 2].copy_from_slice(&v.to_le_bytes());
    *p += 2;
}
#[inline]
fn put_u32(out: &mut [u8], p: &mut usize, v: u32) {
    out[*p..*p + 4].copy_from_slice(&v.to_le_bytes());
    *p += 4;
}
#[inline]
fn put_u64(out: &mut [u8], p: &mut usize, v: u64) {
    out[*p..*p + 8].copy_from_slice(&v.to_le_bytes());
    *p += 8;
}
#[inline]
fn put_bytes(out: &mut [u8], p: &mut usize, v: &[u8]) {
    out[*p..*p + v.len()].copy_from_slice(v);
    *p += v.len();
}

#[cfg(test)]
mod tests {
    use super::*;

    // Feed each chunk in order (chunks model successive overflow-page payloads), then finish;
    // returns Err if any feed or finish rejects — i.e. the streaming verdict over the value.
    fn run(chunks: &[&[u8]]) -> Result<()> {
        let mut s = Utf8Stream::new();
        for c in chunks {
            s.feed(c)?;
        }
        s.finish()
    }

    #[test]
    fn utf8_stream_ascii_and_whole_chars() {
        assert!(run(&[&b"hello world"[..]]).is_ok());
        assert!(run(&[&b"caf\xC3\xA9"[..]]).is_ok()); // café in one chunk
        assert!(run(&["€ end".as_bytes()]).is_ok());
        assert!(run(&[&[][..]]).is_ok()); // empty chunk
    }

    #[test]
    fn utf8_stream_valid_split_2_3_4_byte() {
        // 2-byte é = C3 A9 straddling the chunk boundary.
        assert!(run(&[&b"ab\xC3"[..], &b"\xA9cd"[..]]).is_ok());
        // 3-byte € = E2 82 AC split after 1 and after 2 bytes.
        assert!(run(&[&b"x\xE2"[..], &b"\x82\xACy"[..]]).is_ok());
        assert!(run(&[&b"x\xE2\x82"[..], &b"\xACy"[..]]).is_ok());
        // 4-byte 😀 = F0 9F 98 80 split at each interior boundary.
        assert!(run(&[&b"\xF0"[..], &b"\x9F\x98\x80"[..]]).is_ok());
        assert!(run(&[&b"\xF0\x9F"[..], &b"\x98\x80"[..]]).is_ok());
        assert!(run(&[&b"\xF0\x9F\x98"[..], &b"\x80"[..]]).is_ok());
    }

    #[test]
    fn utf8_stream_invalid_split_rejected() {
        // E2 (3-byte lead) then 0x28 ('(') is not a continuation ⇒ invalid across the boundary.
        assert!(run(&[&b"x\xE2"[..], &b"\x28y"[..]]).is_err());
        // Overlong E0 80 80 (second byte must be A0..BF), split.
        assert!(run(&[&b"\xE0"[..], &b"\x80\x80"[..]]).is_err());
        // Lone continuation byte at a chunk start.
        assert!(run(&[&b"ok"[..], &b"\x80more"[..]]).is_err());
        // Surrogate ED A0 80 (invalid in UTF-8), split.
        assert!(run(&[&b"\xED"[..], &b"\xA0\x80"[..]]).is_err());
    }

    #[test]
    fn utf8_stream_incomplete_at_end_rejected() {
        // Value ends mid 3-byte char (only the lead present).
        assert!(run(&[&b"hi\xE2"[..]]).is_err());
        // Lead + one continuation across chunks, missing the third byte.
        assert!(run(&[&b"hi\xE2"[..], &b"\x82"[..]]).is_err());
        // 4-byte char missing its last byte.
        assert!(run(&[&b"\xF0\x9F\x98"[..]]).is_err());
    }
}
