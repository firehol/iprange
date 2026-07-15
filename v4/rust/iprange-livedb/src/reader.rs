//! The v4 reader: open over an in-memory image and query.
//!
//! This is the byte-slice core (the OS layer — `mmap` + `SEEK_HOLE`/`fstat` hardening
//! (§10) and `flock(LOCK_SH)` (§11) — wraps it). `open` selects the active meta
//! (§5.1 bootstrap, with per-meta CRC to detect torn writes) and checks geometry
//! (§9 step 2), but **does not walk the tree** — files are **trusted** by default
//! (the writer uses a brief open-time lock; readers take no lock). Call
//! [`validate`](Reader::validate) for the full §9 structural walk + per-page CRC when
//! the input is untrusted. `lookup` / `scan` navigate the tree directly, returning the
//! borrowed `scope` (zero-copy, D11).

use crate::crc32c;
use crate::cursor::Cursor;
use crate::error::{Error, Result};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::node::{BranchView, LeafView};
use crate::spec::{self, IpVersion};
use crate::wire::{self, Meta, PageHeader};

/// A read-only view over a v4 image (trusted by default; call [`validate`](Self::validate)
/// for the full §9 walk). Holds no lock and no allocation;
/// `lookup` / `scan` return slices borrowed from the underlying bytes.
#[derive(Debug)]
pub struct Reader<'a> {
    bytes: &'a [u8],
    meta: Meta,
    version: IpVersion,
    #[allow(dead_code)]
    record_size: usize,
    leaf_max: usize,
    branch_max: usize,
}

impl<'a> Reader<'a> {
    /// Open a v4 image in **trusted** mode: select the active meta (per-meta CRC to
    /// detect torn writes, §5.1 bootstrap) and check geometry (§9 step 2). The tree
    /// is NOT walked — the file is trusted (committed pages are never modified in-place; COW isolation).
    /// For the full §9 structural walk + per-page CRC, call [`validate`](Self::validate)
    /// after opening. Returns a typed error (exposing nothing) on a malformed meta or
    /// geometry violation.
    /// (exposing nothing) on any malformed/hostile input.
    pub fn open(bytes: &'a [u8]) -> Result<Reader<'a>> {
        let meta = select_active_meta(bytes, false)?;
        // `flags` reserved bits were already rejected in classify; only bit0 remains.
        let version = IpVersion::from_flag_bit(meta.flags);

        // Geometry (§9 step 2). `page_size`/`key_width`/`record_size`/`meta_size` were
        // cross-checked in classify; here: page-count, file size, height/root.
        if meta.total_pages < 2 || meta.total_pages >= (1u64 << 32) {
            return Err(Error::Structural("total_pages out of range"));
        }
        let needed = meta
            .total_pages
            .checked_mul(spec::PAGE_SIZE as u64)
            .ok_or(Error::Overflow("total_pages*page_size"))?;
        let have = bytes.len() as u64;
        if have % spec::PAGE_SIZE as u64 != 0 {
            return Err(Error::FileSizeMismatch {
                header: needed,
                real: have,
            });
        }
        if have < needed {
            return Err(Error::FileTooShort { need: needed, have });
        }
        if meta.tree_height > spec::TREE_HEIGHT_MAX {
            return Err(Error::Structural("tree_height > 32"));
        }
        if (meta.tree_height == 0) != (meta.root_pgno == 0) {
            return Err(Error::Structural("tree_height/root_pgno inconsistent"));
        }
        if meta.root_pgno != 0
            && ((meta.root_pgno as u64) < 2 || (meta.root_pgno as u64) >= meta.total_pages)
        {
            return Err(Error::Structural("root_pgno out of range"));
        }

        let reader = Reader {
            bytes,
            meta,
            version,
            record_size: meta.record_size as usize,
            leaf_max: spec::leaf_max(meta.key_width),
            branch_max: spec::branch_max(meta.key_width),
        };
        Ok(reader)
    }

    /// Create a Reader with a pinned meta (MVCC snapshot).
    /// Used by MmapReader to read a specific transaction's view, not the latest.
    pub(crate) fn from_meta(bytes: &[u8], meta: crate::wire::Meta) -> Result<Reader<'_>> {
        let needed = meta
            .total_pages
            .checked_mul(spec::PAGE_SIZE as u64)
            .ok_or(Error::Overflow("total_pages*page_size"))?;
        if (bytes.len() as u64) < needed {
            return Err(Error::FileTooShort {
                need: needed,
                have: bytes.len() as u64,
            });
        }
        if meta.tree_height > spec::TREE_HEIGHT_MAX {
            return Err(Error::Structural("tree_height > 32"));
        }
        Ok(Reader {
            bytes,
            meta,
            version: IpVersion::from_flag_bit(meta.flags),
            record_size: meta.record_size as usize,
            leaf_max: spec::leaf_max(meta.key_width),
            branch_max: spec::branch_max(meta.key_width),
        })
    }

    /// Fully validate the image per §9 (the structural walk + per-page CRC + scope/KV
    /// tree validation). Call this when the input is **untrusted** (externally provided
    /// files) or for periodic integrity checks. Returns a typed error on any corruption.
    /// On a trusted (daemon) file this is a no-op success; on a corrupt file it rejects
    /// rather than panicking during `lookup`/`scan`.
    pub fn validate(&self) -> Result<()> {
        // Verify meta page CRCs (skipped during trusted open).
        if !crc32c::verify_page(self.page_bytes(0)) || !crc32c::verify_page(self.page_bytes(1)) {
            return Err(Error::ChecksumFailed("meta page"));
        }
        // Verify meta tail is zero (skipped during trusted open classify).
        for p in 0..2u32 {
            let page = self.page_bytes(p);
            let m = Meta::decode(page);
            if page[m.meta_size as usize..].iter().any(|&b| b != 0) {
                return Err(Error::NonZeroReserved("meta tail"));
            }
        }
        self.validate_tree()?;
        self.validate_scope_table()?;
        // In indirect mode every data record's scope_id MUST resolve to a
        // defined scope in the scope table (defense against a corrupt or
        // hostile file that points a record at an undefined scope).
        if self.meta.scope_mode == spec::SCOPE_MODE_INDIRECT
            && self.meta.scope_table_root != 0
            && self.meta.root_pgno != 0
        {
            self.validate_record_scopes()?;
        }
        Ok(())
    }

    /// Walk the data tree and verify every record's `scope_id` resolves to a
    /// defined entry in the scope table (indirect mode only). `scope_id == 0`
    /// (FILE_SCOPE_ID) is allowed as the dataset-metadata target.
    pub(crate) fn validate_record_scopes(&self) -> Result<()> {
        match self.version {
            IpVersion::V4 => self.validate_record_scopes_node::<Ipv4Key>(self.meta.root_pgno, 1),
            IpVersion::V6 => self.validate_record_scopes_node::<Ipv6Key>(self.meta.root_pgno, 1),
        }
    }

    fn validate_record_scopes_node<K: IpKey>(&self, pgno: u32, depth: u32) -> Result<()> {
        if depth > self.meta.tree_height {
            return Err(Error::Invariant("path deeper than tree_height"));
        }
        let page = self.page_bytes(pgno);
        let raw = PageHeader::decode(page).entry_count as usize;
        if depth == self.meta.tree_height {
            let leaf = LeafView::<K>::new(page, raw.min(self.leaf_max));
            for i in 0..leaf.len() {
                let scope_id = leaf.record(i).scope_id();
                if scope_id == spec::FILE_SCOPE_ID {
                    continue;
                }
                let resolves = crate::scope_table::scope_id_exists(
                    self.bytes,
                    self.meta.scope_table_root,
                    scope_id,
                )
                .unwrap_or(false);
                if !resolves {
                    return Err(Error::Invariant("record references an undefined scope"));
                }
            }
            Ok(())
        } else {
            let branch = BranchView::<K>::new(page, raw.min(self.branch_max));
            for j in 0..branch.child_count() {
                self.validate_record_scopes_node::<K>(branch.child(j), depth + 1)?;
            }
            Ok(())
        }
    }

    /// Validate the v4.1 scope table: walk every scope page verifying per-page
    /// CRC32C, page type at each level, monotonically increasing scope_ids and
    /// separator keys, and child page numbers in range. A v4.0 file has
    /// `scope_table_root == 0` (no metadata) and this is a no-op.
    fn validate_scope_table(&self) -> Result<()> {
        let root = self.meta.scope_table_root;
        if root == 0 {
            return Ok(());
        }
        // Delegate to the shared comprehensive validator: per-page CRC +
        // structural checks (entry_count, child range, scope_id monotonicity),
        // overflow chain integrity (CRC, type, declared-length vs. page count,
        // unused tail, no shared/aliased pages), and branch separator ==
        // right-child minimum.
        crate::scope_table::validate_scope_crc(self.bytes, root)
    }

    /// The file's IP family.
    #[inline]
    pub fn version(&self) -> IpVersion {
        self.version
    }

    /// The file's scope mode (0=scalar, 1=bitmap, 2=indirect).
    #[inline]
    pub fn scope_mode(&self) -> u8 {
        self.meta.scope_mode
    }

    /// The exact record count (verified against the tree during `open`).
    #[inline]
    pub fn record_count(&self) -> u64 {
        self.meta.record_count
    }

    /// Whether the tree is empty (`root_pgno == 0`).
    #[inline]
    pub fn is_empty(&self) -> bool {
        self.meta.root_pgno == 0
    }

    /// Open an ordered [`Cursor`] over this validated image for `seek`/`next`/`prev`
    /// traversal and the standard helpers (§v4.1.A/B). Errors on a key-family mismatch.
    pub fn cursor<K: IpKey>(&self) -> Result<Cursor<'_, 'a, K>> {
        if K::VERSION != self.version {
            return Err(Error::InvalidInput("cursor key family mismatch"));
        }
        Ok(Cursor::new(self))
    }

    // --- cursor support (validated tree; pgnos already checked by the open-time walk) ---

    /// The active root page number (0 = empty tree).
    #[inline]
    pub(crate) fn root_pgno(&self) -> u32 {
        self.meta.root_pgno
    }

    /// The tree height (0 = empty; the leaf level equals the height).
    #[inline]
    pub(crate) fn tree_height(&self) -> u32 {
        self.meta.tree_height
    }

    /// The fixed per-record size in bytes (`2·key_width + 4`).
    #[allow(dead_code)]
    #[inline]
    pub(crate) fn record_size_bytes(&self) -> usize {
        self.record_size
    }

    /// The validated active meta (for the writer's `open_image`, §6.2).
    #[allow(dead_code)]
    #[inline]
    #[cfg_attr(not(feature = "os"), allow(dead_code))]
    pub(crate) fn active_meta(&self) -> Meta {
        self.meta
    }

    /// Point lookup (IPv4). Errors if the file is not IPv4.
    #[inline]
    pub fn lookup_v4(&self, ip: Ipv4Key) -> Result<Option<u32>> {
        self.lookup(ip)
    }

    /// Point lookup (IPv6). Errors if the file is not IPv6.
    #[inline]
    pub fn lookup_v6(&self, ip: Ipv6Key) -> Result<Option<u32>> {
        self.lookup(ip)
    }

    /// Point lookup: the `scope_id` of the range covering `ip`, or `None` if absent.
    /// `O(log n)`. Errors only on a family mismatch.
    pub fn lookup<K: IpKey>(&self, ip: K) -> Result<Option<u32>> {
        if K::VERSION != self.version {
            return Err(Error::InvalidInput("lookup key family mismatch"));
        }
        Ok(self.lookup_inner::<K>(ip))
    }

    /// In-order scan (IPv4): calls `f(from, to, scope)` for every record. Errors if the
    /// file is not IPv4.
    pub fn scan_v4<F: FnMut(Ipv4Key, Ipv4Key, u32)>(&self, f: F) -> Result<()> {
        self.scan(f)
    }

    /// In-order scan (IPv6).
    pub fn scan_v6<F: FnMut(Ipv6Key, Ipv6Key, u32)>(&self, f: F) -> Result<()> {
        self.scan(f)
    }

    /// In-order scan: calls `f(from, to, scope_id)` for every record in key order.
    /// Re-descends (no leaf sibling pointers, D3); zero-alloc. Errors on family
    /// mismatch.
    pub fn scan<K: IpKey, F: FnMut(K, K, u32)>(&self, mut f: F) -> Result<()> {
        if K::VERSION != self.version {
            return Err(Error::InvalidInput("scan key family mismatch"));
        }
        if self.meta.root_pgno != 0 {
            self.scan_node::<K, F>(self.meta.root_pgno, 1, &mut f)?;
        }
        Ok(())
    }

    // --- v4.1 metadata reads (§C, mirror the Writer's read API) ---
    //
    // Read-only, shared-lock consumers (the `MmapReader` path) descend the **on-disk
    // committed tree** validated at `open` (no in-memory registry). A v4.0 file has
    // `scope_table_root == 0` ⇒ no metadata ⇒ empty/`None`, never an error or a panic. All
    // descents go through the existing bounds-safe byte functions in `scope`/`kv`.

    /// All defined scopes as `(scope_id, bitmap)`, ascending by `scope_id`. The FILE
    /// target (`scope_id 0`) is a dataset-metadata target, not a defined scope, so it is
    /// excluded (§C.2). In v4.3 the scope table maps `scope_id → bitmap` (no name); the
    /// second element is the interned bitmap, not a name.
    #[cfg(feature = "alloc")]
    pub fn scope_list(&self) -> alloc::vec::Vec<(u32, alloc::vec::Vec<u8>)> {
        let root = self.meta.scope_table_root;
        if root == 0 {
            return alloc::vec::Vec::new();
        }
        // The tree was validated at `open`, so `read_all` cannot fail here; treat any
        // error as no metadata rather than panicking.
        match crate::scope_table::read_all(self.bytes, root) {
            Ok(recs) => recs
                .into_iter()
                .filter(|e| e.scope_id != spec::FILE_SCOPE_ID)
                .map(|e| (e.scope_id, e.bitmap))
                .collect(),
            Err(_) => alloc::vec::Vec::new(),
        }
    }

    /// The scope's `name` (UTF-8 bytes), or `None`. Names are not stored in v4.3 (the
    /// scope table maps `scope_id → bitmap`), so this always returns `None`.
    #[cfg(feature = "alloc")]
    pub fn scope_name(&self, _scope_id: u32) -> Option<alloc::vec::Vec<u8>> {
        None // DEPRECATED: names not in v4.3
    }

    /// The scope's `version`, or `None`. Not stored in v4.3; always `None`.
    #[cfg(feature = "alloc")]
    pub fn scope_version(&self, _scope_id: u32) -> Option<u64> {
        None // DEPRECATED: versions not in v4.3
    }

    /// The scope's opaque `type` byte, or `None`. Not stored in v4.3; always `None`.
    #[cfg(feature = "alloc")]
    pub fn scope_type(&self, _scope_id: u32) -> Option<u8> {
        None // DEPRECATED: type not in v4.3
    }

    /// Resolve a `scope_id` to its interned bitmap (mode 2 / indirect only), or `None`
    /// if the file is not in indirect mode, has no scope table, or the `scope_id` is not
    /// present. The bitmap is the bitset of feeds that cover the scope (§C.2).
    #[cfg(feature = "alloc")]
    pub fn scope_resolve(&self, scope_id: u32) -> Option<alloc::vec::Vec<u8>> {
        if self.meta.scope_mode != spec::SCOPE_MODE_INDIRECT {
            return None;
        }
        if self.meta.scope_table_root == 0 {
            return None;
        }
        // P2 fix: O(log S) B+tree descent instead of O(S) read_all + linear scan.
        crate::scope_table::find_scope(self.bytes, self.meta.scope_table_root, scope_id).ok()?
    }

    // --- internals ---

    /// The `pgno`-th page. `pgno < total_pages` is guaranteed by the caller (geometry +
    /// the validate walk check every pgno before access), and `total_pages·page_size <=
    /// bytes.len()` was checked in `open`, so the slice is always in bounds.
    #[inline]
    pub(crate) fn page_bytes(&self, pgno: u32) -> &'a [u8] {
        let bytes: &'a [u8] = self.bytes;
        let off = pgno as usize * spec::PAGE_SIZE;
        &bytes[off..off + spec::PAGE_SIZE]
    }

    fn lookup_inner<K: IpKey>(&self, ip: K) -> Option<u32> {
        if self.meta.root_pgno == 0 {
            return None;
        }
        // Bounded descent: a correct tree reaches a leaf in exactly `tree_height`
        // steps, so the loop runs at most `tree_height` times. This is the same
        // bound `validate_node` enforces — a cyclic branch (a child pointing back
        // to an ancestor) cannot loop forever or be misread as a leaf, because
        // the leaf/branch decision is driven by `page_type`, not by `depth`, and
        // a cycle that never reaches a leaf simply exhausts the bound → miss.
        // O(height) time, O(1) heap (only the loop counter).
        let height = self.meta.tree_height;
        let mut pgno = self.meta.root_pgno;
        for _ in 0..height {
            // A pgno outside the file is corruption — return a miss, never panic
            // on an out-of-bounds slice in `page_bytes`.
            if pgno as u64 >= self.meta.total_pages {
                return None;
            }
            let page = self.page_bytes(pgno);
            let h = PageHeader::decode(page);
            // Clamp entry_count to the page capacity: a corrupt (but CRC-valid)
            // entry_count must not drive a slice out of bounds and panic. The
            // trusted path never exceeds capacity; validate() rejects the rest.
            let raw = h.entry_count as usize;
            if h.page_type == spec::PAGE_TYPE_LEAF {
                let leaf = LeafView::<K>::new(page, raw.min(self.leaf_max));
                return leaf_lookup(&leaf, ip);
            }
            if h.page_type != spec::PAGE_TYPE_BRANCH {
                return None;
            }
            let branch = BranchView::<K>::new(page, raw.min(self.branch_max));
            pgno = branch.child(branch_descend(&branch, ip));
        }
        // No leaf was reached within `tree_height` steps → corrupt/cyclic tree.
        None
    }

    fn scan_node<K: IpKey, F: FnMut(K, K, u32)>(
        &self,
        pgno: u32,
        depth: u32,
        f: &mut F,
    ) -> Result<()> {
        // Cycle/DoS defense mirroring `validate_node`: a path deeper than
        // `tree_height` (including any pgno cycle, which revisits a branch at
        // ever-increasing depth) is corruption — bail with a structural error
        // instead of recursing forever. The recursion depth is thus bounded by
        // `tree_height` (≤ TREE_HEIGHT_MAX = 32), so there is no stack-overflow
        // risk and heap stays O(1).
        if depth > self.meta.tree_height {
            return Err(Error::Invariant("scan path deeper than tree_height"));
        }
        // A pgno outside the file is corruption — return an error, never panic
        // on an out-of-bounds slice in `page_bytes`.
        if pgno as u64 >= self.meta.total_pages {
            return Err(Error::Structural("scan page pgno out of range"));
        }
        let page = self.page_bytes(pgno);
        let h = PageHeader::decode(page);
        // Clamp entry_count to the page capacity (defense against a corrupt
        // but CRC-valid entry_count that would slice out of bounds and panic).
        let raw = h.entry_count as usize;
        // The leaf/branch decision is driven by `page_type`, not by `depth`
        // alone: a cyclic branch that returns to a branch page at the leaf
        // level is NOT misread as a leaf (which would emit fabricated records).
        if h.page_type == spec::PAGE_TYPE_LEAF {
            let leaf = LeafView::<K>::new(page, raw.min(self.leaf_max));
            for i in 0..leaf.len() {
                let r = leaf.record(i);
                f(r.from(), r.to(), r.scope_id());
            }
            Ok(())
        } else if h.page_type == spec::PAGE_TYPE_BRANCH {
            let branch = BranchView::<K>::new(page, raw.min(self.branch_max));
            for j in 0..branch.child_count() {
                self.scan_node::<K, F>(branch.child(j), depth + 1, f)?;
            }
            Ok(())
        } else {
            Err(Error::Structural("unexpected page type in scan"))
        }
    }

    pub(crate) fn validate_tree(&self) -> Result<()> {
        if self.meta.root_pgno == 0 {
            // Empty tree: the full pass enforces the exact record_count (§9 step 5).
            if self.meta.record_count != 0 {
                return Err(Error::Invariant("record_count nonzero for empty tree"));
            }
            return Ok(());
        }
        let mut count = 0u64;
        match self.version {
            IpVersion::V4 => {
                let mut prev_to: Option<Ipv4Key> = None;
                self.validate_node::<Ipv4Key>(
                    self.meta.root_pgno,
                    1,
                    Ipv4Key::MIN,
                    Ipv4Key::MAX,
                    &mut prev_to,
                    &mut count,
                )?;
            }
            IpVersion::V6 => {
                let mut prev_to: Option<Ipv6Key> = None;
                self.validate_node::<Ipv6Key>(
                    self.meta.root_pgno,
                    1,
                    Ipv6Key::MIN,
                    Ipv6Key::MAX,
                    &mut prev_to,
                    &mut count,
                )?;
            }
        }
        if count != self.meta.record_count {
            return Err(Error::Invariant("record_count mismatch"));
        }
        Ok(())
    }

    /// Recursive structural walk (§9 step 4). `lo`/`hi` are the **inherited** inclusive
    /// key bound; `prev_to` threads the largest `to` seen so far across the whole
    /// in-order walk (global cross-leaf disjointness); `count` accumulates leaf records.
    fn validate_node<K: IpKey>(
        &self,
        pgno: u32,
        depth: u32,
        lo: K,
        hi: K,
        prev_to: &mut Option<K>,
        count: &mut u64,
    ) -> Result<()> {
        // Cycle/DoS defense: a too-deep path (incl. any pgno cycle) exceeds tree_height.
        if depth > self.meta.tree_height {
            return Err(Error::Invariant("path deeper than tree_height"));
        }
        let page = self.page_bytes(pgno);
        if !crc32c::verify_page(page) {
            return Err(Error::ChecksumFailed("reachable page"));
        }
        let h = PageHeader::decode(page);
        if h.reserved != 0 {
            return Err(Error::NonZeroReserved("page header reserved"));
        }
        if h.pgno != pgno {
            return Err(Error::Structural("page self-pgno mismatch"));
        }

        if depth == self.meta.tree_height {
            // MUST be a leaf.
            if h.page_type != spec::PAGE_TYPE_LEAF {
                return Err(Error::Structural("expected leaf at tree_height"));
            }
            let r = h.entry_count as usize;
            if r > self.leaf_max {
                return Err(Error::Invariant("leaf entry_count out of range"));
            }
            if r == 0 {
                // An empty leaf is a degenerate sparse-tree state the writer
                // leaves behind after deletes (compaction reclaims it once the
                // tree is sparse enough). It carries no records, so the
                // record-level ordering checks do not apply, and `prev_to` is
                // left untouched to keep the empty leaf transparent to the
                // cross-leaf disjointness check. The CRC and page-type guards
                // above already ran; the one remaining obligation is that the
                // unused body (the whole region after the header) MUST be zero —
                // stale record bytes left behind a decremented entry_count are
                // corruption.
                if page[spec::PAGE_HEADER_SIZE..].iter().any(|&b| b != 0) {
                    return Err(Error::NonZeroReserved("empty leaf body"));
                }
                return Ok(());
            }
            let leaf = LeafView::<K>::new(page, r);
            // Tail after the records MUST be zero (full pass).
            if page[spec::PAGE_HEADER_SIZE + leaf.body_len()..]
                .iter()
                .any(|&b| b != 0)
            {
                return Err(Error::NonZeroReserved("leaf tail"));
            }
            // Cross-leaf disjointness: prev_to < this leaf's first `from`.
            let first_from = leaf.record(0).from();
            if let Some(pt) = *prev_to {
                if pt >= first_from {
                    return Err(Error::Invariant("cross-leaf overlap"));
                }
            }
            // Records sorted, disjoint, within [lo, hi].
            let mut prev_rec_to: Option<K> = None;
            for i in 0..r {
                let rec = leaf.record(i);
                let (from, to) = (rec.from(), rec.to());
                if to < from {
                    return Err(Error::Invariant("record to < from"));
                }
                if from < lo || to > hi {
                    return Err(Error::Invariant("record outside node bound"));
                }
                if let Some(pt) = prev_rec_to {
                    if from <= pt {
                        return Err(Error::Invariant("leaf records not sorted/disjoint"));
                    }
                }
                prev_rec_to = Some(to);
            }
            *prev_to = prev_rec_to; // last record's `to`
            *count += r as u64;
            return Ok(());
        }

        // MUST be a branch.
        if h.page_type != spec::PAGE_TYPE_BRANCH {
            return Err(Error::Structural("expected branch above tree_height"));
        }
        let s = h.entry_count as usize;
        if s < 1 || s > self.branch_max {
            return Err(Error::Invariant("branch separator count out of range"));
        }
        let branch = BranchView::<K>::new(page, s);
        if page[spec::PAGE_HEADER_SIZE + branch.body_len()..]
            .iter()
            .any(|&b| b != 0)
        {
            return Err(Error::NonZeroReserved("branch tail"));
        }
        // Separators: lo < sep[0] < … < sep[s-1] <= hi (strictly increasing, in bound).
        let mut prev_sep: Option<K> = None;
        for i in 0..s {
            let sep = branch.sep(i);
            if sep <= lo {
                return Err(Error::Invariant("separator <= lo"));
            }
            if sep > hi {
                return Err(Error::Invariant("separator > hi"));
            }
            if let Some(ps) = prev_sep {
                if sep <= ps {
                    return Err(Error::Invariant("separators not strictly increasing"));
                }
            }
            prev_sep = Some(sep);
        }
        // Children in [2, total_pages) and pairwise distinct.
        for j in 0..branch.child_count() {
            let cj = branch.child(j);
            if (cj as u64) < 2 || (cj as u64) >= self.meta.total_pages {
                return Err(Error::Structural("child pgno out of range"));
            }
            for k in (j + 1)..branch.child_count() {
                if branch.child(k) == cj {
                    return Err(Error::Structural("duplicate child pgno"));
                }
            }
        }
        // Recurse with inherited bounds: child[0]=[lo, sep[0]-1]; child[i]=[sep[i-1],
        // sep[i]-1]; child[s]=[sep[s-1], hi]. sep > lo >= family_min ⇒ sep-1 exists.
        let mut lower = lo;
        for i in 0..s {
            let sep = branch.sep(i);
            let upper = sep
                .checked_dec()
                .ok_or(Error::Invariant("separator has no predecessor"))?;
            self.validate_node::<K>(branch.child(i), depth + 1, lower, upper, prev_to, count)?;
            lower = sep;
        }
        self.validate_node::<K>(branch.child(s), depth + 1, lower, hi, prev_to, count)
    }
}

/// Active-meta selection (§5.1 bootstrap). Reads both 4096-byte candidates
/// independently; class 2 (intact-but-incompatible) on either rejects the file; class 1
/// (torn/not-a-meta) is discarded; among the valid metas the higher `txn_id` wins
/// (tie → pgno 0). Both valid metas MUST agree on the static identity region.
pub(crate) fn select_active_meta(bytes: &[u8], skip_crc: bool) -> Result<Meta> {
    if bytes.len() < 2 * spec::PAGE_SIZE {
        return Err(Error::FileTooShort {
            need: (2 * spec::PAGE_SIZE) as u64,
            have: bytes.len() as u64,
        });
    }

    let a = classify(&bytes[..spec::PAGE_SIZE], 0, skip_crc)?;
    let b = classify(&bytes[spec::PAGE_SIZE..2 * spec::PAGE_SIZE], 1, skip_crc)?;
    match (a, b) {
        (None, None) => Err(Error::Structural("no valid meta page")),
        (Some(m), None) | (None, Some(m)) => Ok(m),
        (Some(ma), Some(mb)) => {
            // Both valid metas MUST agree on the static identity region [16,50) — EXCEPT
            // `version_minor` (26) and `meta_size` (28): a v4.0→v4.1 in-place upgrade (§C.6)
            // writes the new minor into one meta while the other still holds the old minor,
            // so they legitimately differ there during the transition (the active/higher-
            // `txn_id` meta is authoritative, and each field is still CRC-protected). The
            // rest of the static identity must match byte-for-byte. Ranges: [16,26) and
            // [30,50) (`META_PAGE_SIZE` == 30 is the field after `meta_size`).
            let pa = spec::PAGE_SIZE;
            let lo_a = &bytes[spec::META_STATIC_START..spec::META_VERSION_MINOR];
            let lo_b = &bytes[pa + spec::META_STATIC_START..pa + spec::META_VERSION_MINOR];
            let hi_a = &bytes[spec::META_PAGE_SIZE..spec::META_STATIC_END];
            let hi_b = &bytes[pa + spec::META_PAGE_SIZE..pa + spec::META_STATIC_END];
            if lo_a != lo_b || hi_a != hi_b {
                return Err(Error::Structural("metas disagree on static identity"));
            }
            // Higher txn_id active; on an (illegal) tie pick pgno 0 (== ma).
            if mb.txn_id > ma.txn_id {
                Ok(mb)
            } else {
                Ok(ma)
            }
        }
    }
}

/// Classify one meta candidate (§5.1): `Ok(None)` = class 1 (torn/not-a-meta, discard),
/// `Err` = class 2 (intact but incompatible — fail closed), `Ok(Some)` = class 3 (valid).
fn classify(page: &[u8], expected_pgno: u32, skip_crc: bool) -> Result<Option<Meta>> {
    // Class 1: torn / not a meta — discarded, never rejects the file by itself.
    // In trusted mode (skip_crc), rely on magic + header checks instead of CRC.
    if !skip_crc && !crc32c::verify_page(page) {
        return Ok(None);
    }

    if wire::read_magic(page) != spec::MAGIC {
        return Ok(None);
    }

    let h = PageHeader::decode(page);
    if h.page_type != spec::PAGE_TYPE_META
        || h.reserved != 0
        || h.entry_count != 0
        || h.pgno != expected_pgno
    {
        return Ok(None);
    }

    // A genuine, undamaged v4 meta. Class 2: incompatible / malformed ⇒ fail closed.
    let version_major = wire::read_version_major(page);
    if version_major != spec::VERSION_MAJOR {
        return Err(Error::UnsupportedMajor(version_major));
    }

    let m = Meta::decode(page);
    if m.page_size != spec::PAGE_SIZE as u32 {
        return Err(Error::Incompatible("page_size"));
    }

    if m.checksum_algo != spec::CHECKSUM_ALGO_CRC32C {
        return Err(Error::Incompatible("checksum_algo"));
    }

    if m.flags & !spec::FLAG_IP_VERSION != 0 {
        return Err(Error::Incompatible("unknown flags bit"));
    }

    if m.meta_size < spec::META_SIZE || m.meta_size as usize > spec::PAGE_SIZE {
        return Err(Error::BadMetaSize(m.meta_size));
    }

    // Pin the exact `meta_size` for each known minor (F7): v4.0 is 90, v4.1 is 94. A future
    // minor (>= 2) may declare a larger `meta_size` for genuine forward-compat (the reserved
    // tail beyond it is still zero-checked below), so it keeps the `>= 90` floor only.
    if m.version_minor == spec::VERSION_MINOR && m.meta_size != spec::META_SIZE {
        return Err(Error::BadMetaSize(m.meta_size));
    }

    if m.version_minor == spec::VERSION_MINOR_METADATA && m.meta_size != spec::META_SIZE_V41 {
        return Err(Error::BadMetaSize(m.meta_size));
    }

    let expect_kw = IpVersion::from_flag_bit(m.flags).key_width();
    if m.key_width != expect_kw {
        return Err(Error::Structural("key_width disagrees with flags"));
    }

    if m.record_size != spec::record_size(m.key_width) {
        return Err(Error::Structural("record_size mismatch"));
    }

    // The meta's reserved tail after its declared fields MUST be zero (§5/§9). In
    // trusted mode (skip_crc), skip this structural check — the daemon's own files
    // always have a zero tail, and validate() enforces it for untrusted input.
    if !skip_crc && page[m.meta_size as usize..].iter().any(|&b| b != 0) {
        return Err(Error::NonZeroReserved("meta tail"));
    }

    Ok(Some(m))
}

/// Binary search a leaf for the record covering `ip`: the record with greatest
/// `from <= ip`, a hit iff `ip <= to`. Returns the `scope_id`.
#[inline]
fn leaf_lookup<'a, K: IpKey>(leaf: &LeafView<'a, K>, ip: K) -> Option<u32> {
    // First index whose `from` is > ip; the candidate is the one before it.
    let (mut lo, mut hi) = (0usize, leaf.len());
    while lo < hi {
        let mid = lo + (hi - lo) / 2;
        if leaf.record(mid).from() <= ip {
            lo = mid + 1;
        } else {
            hi = mid;
        }
    }

    if lo == 0 {
        return None;
    }

    let rec = leaf.record(lo - 1);
    if ip <= rec.to() {
        Some(rec.scope_id())
    } else {
        None
    }
}

/// Branch descent (§5.2): the child index = number of separators `<= ip` (binary
/// search). `child[i]` covers `[sep[i-1], sep[i]-1]`.
#[inline]
fn branch_descend<K: IpKey>(branch: &BranchView<'_, K>, ip: K) -> usize {
    let (mut lo, mut hi) = (0usize, branch.sep_count());
    while lo < hi {
        let mid = lo + (hi - lo) / 2;
        if branch.sep(mid) <= ip {
            lo = mid + 1;
        } else {
            hi = mid;
        }
    }

    lo
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::key::Ipv4Key;
    use crate::record;
    use crate::spec::{PAGE_HEADER_SIZE, PAGE_SIZE};
    use crate::wire::finalize_checksum;

    // --- file builders (the writer doesn't exist yet, so tests forge pages) ---

    #[allow(clippy::too_many_arguments)] // a test fixture builder, not library API
    fn meta(
        pgno: u32,
        version: IpVersion,
        scope_mode: u8,
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
            flags: version.flag(),
            key_width: version.key_width(),
            scope_mode,
            record_size: spec::record_size(version.key_width()),
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

    fn put_leaf<K: IpKey>(file: &mut [u8], pgno: u32, records: &[(K, K, u32)]) {
        let rs = spec::record_size(K::WIDTH as u8) as usize;
        let base = pgno as usize * PAGE_SIZE;
        let page = &mut file[base..base + PAGE_SIZE];
        PageHeader::write(page, spec::PAGE_TYPE_LEAF, records.len() as u16, pgno);
        for (i, (f, t, s)) in records.iter().enumerate() {
            let off = PAGE_HEADER_SIZE + i * rs;
            record::write::<K>(&mut page[off..off + rs], *f, *t, *s);
        }
        finalize_checksum(page);
    }

    /// 3 pages: meta-A (active, txn 2), meta-B (txn 1), one root leaf at pgno 2.
    fn build_single_leaf<K: IpKey>(
        version: IpVersion,
        scope_mode: u8,
        records: &[(K, K, u32)],
    ) -> Vec<u8> {
        let mut file = vec![0u8; 3 * PAGE_SIZE];
        put_leaf::<K>(&mut file, 2, records);
        let rc = records.len() as u64;
        meta(0, version, scope_mode, 2, 1, 3, rc, 2).encode_into(&mut file[..PAGE_SIZE]);
        meta(1, version, scope_mode, 2, 1, 3, rc, 1)
            .encode_into(&mut file[PAGE_SIZE..2 * PAGE_SIZE]);
        file
    }

    fn build_empty(version: IpVersion, scope_mode: u8) -> Vec<u8> {
        let mut file = vec![0u8; 2 * PAGE_SIZE];
        meta(0, version, scope_mode, 0, 0, 2, 0, 2).encode_into(&mut file[..PAGE_SIZE]);
        meta(1, version, scope_mode, 0, 0, 2, 0, 1).encode_into(&mut file[PAGE_SIZE..]);
        file
    }

    /// 5 pages: metas, a root branch at pgno 2 (one separator), leaves at pgno 3/4.
    fn build_two_level<K: IpKey>(
        version: IpVersion,
        scope_mode: u8,
        sep: K,
        left: &[(K, K, u32)],
        right: &[(K, K, u32)],
    ) -> Vec<u8> {
        let mut file = vec![0u8; 5 * PAGE_SIZE];
        put_leaf::<K>(&mut file, 3, left);
        put_leaf::<K>(&mut file, 4, right);
        {
            let page = &mut file[2 * PAGE_SIZE..3 * PAGE_SIZE];
            PageHeader::write(page, spec::PAGE_TYPE_BRANCH, 1, 2);
            page[PAGE_HEADER_SIZE..PAGE_HEADER_SIZE + 4].copy_from_slice(&3u32.to_le_bytes());
            let sep_off = PAGE_HEADER_SIZE + 4;
            sep.write_le(&mut page[sep_off..sep_off + K::WIDTH]);
            let c1 = sep_off + K::WIDTH;
            page[c1..c1 + 4].copy_from_slice(&4u32.to_le_bytes());
            finalize_checksum(page);
        }
        let rc = (left.len() + right.len()) as u64;
        meta(0, version, scope_mode, 2, 2, 5, rc, 2).encode_into(&mut file[..PAGE_SIZE]);
        meta(1, version, scope_mode, 2, 2, 5, rc, 1)
            .encode_into(&mut file[PAGE_SIZE..2 * PAGE_SIZE]);
        file
    }

    fn v4(n: u32) -> Ipv4Key {
        Ipv4Key(n)
    }

    // --- tests ---

    #[test]
    fn single_leaf_lookup_and_scan() {
        let recs: &[(Ipv4Key, Ipv4Key, u32)] = &[(v4(10), v4(20), 1), (v4(30), v4(40), 2)];
        let file = build_single_leaf::<Ipv4Key>(IpVersion::V4, spec::SCOPE_MODE_SCALAR, recs);
        let r = Reader::open(&file).unwrap();
        assert_eq!(r.version(), IpVersion::V4);
        assert_eq!(r.record_count(), 2);
        assert!(!r.is_empty());

        assert_eq!(r.lookup_v4(v4(15)).unwrap(), Some(1));
        assert_eq!(r.lookup_v4(v4(10)).unwrap(), Some(1)); // boundary
        assert_eq!(r.lookup_v4(v4(20)).unwrap(), Some(1)); // boundary
        assert_eq!(r.lookup_v4(v4(25)).unwrap(), None); // gap
        assert_eq!(r.lookup_v4(v4(30)).unwrap(), Some(2));
        assert_eq!(r.lookup_v4(v4(40)).unwrap(), Some(2));
        assert_eq!(r.lookup_v4(v4(9)).unwrap(), None); // before all
        assert_eq!(r.lookup_v4(v4(41)).unwrap(), None); // after all

        let mut seen = Vec::new();
        r.scan_v4(|f, t, s| seen.push((f.0, t.0, s))).unwrap();
        assert_eq!(seen, vec![(10, 20, 1), (30, 40, 2)]);
    }

    #[test]
    fn empty_tree() {
        let file = build_empty(IpVersion::V4, spec::SCOPE_MODE_SCALAR);
        let r = Reader::open(&file).unwrap();
        assert!(r.is_empty());
        assert_eq!(r.record_count(), 0);
        assert_eq!(r.lookup_v4(v4(5)).unwrap(), None);
        let mut n = 0;
        r.scan_v4(|_, _, _| n += 1).unwrap();
        assert_eq!(n, 0);
    }

    #[test]
    fn two_level_lookup_crosses_leaves() {
        let left: &[(Ipv4Key, Ipv4Key, u32)] = &[(v4(10), v4(20), 1), (v4(50), v4(60), 2)];
        let right: &[(Ipv4Key, Ipv4Key, u32)] = &[(v4(100), v4(110), 3), (v4(200), v4(210), 4)];
        let file = build_two_level::<Ipv4Key>(
            IpVersion::V4,
            spec::SCOPE_MODE_SCALAR,
            v4(100),
            left,
            right,
        );
        let r = Reader::open(&file).unwrap();
        assert_eq!(r.record_count(), 4);
        assert_eq!(r.lookup_v4(v4(15)).unwrap(), Some(1));
        assert_eq!(r.lookup_v4(v4(55)).unwrap(), Some(2));
        assert_eq!(r.lookup_v4(v4(105)).unwrap(), Some(3));
        assert_eq!(r.lookup_v4(v4(205)).unwrap(), Some(4));
        assert_eq!(r.lookup_v4(v4(70)).unwrap(), None); // gap in left leaf range
        assert_eq!(r.lookup_v4(v4(150)).unwrap(), None); // gap in right leaf range
        let mut seen = Vec::new();
        r.scan_v4(|f, _, _| seen.push(f.0)).unwrap();
        assert_eq!(seen, vec![10, 50, 100, 200]);
    }

    #[test]
    fn torn_inactive_meta_recovers() {
        let recs: &[(Ipv4Key, Ipv4Key, u32)] = &[(v4(10), v4(20), 1)];
        let mut file = build_single_leaf::<Ipv4Key>(IpVersion::V4, spec::SCOPE_MODE_SCALAR, recs);
        // Corrupt the inactive meta (pgno 1) — CRC fails ⇒ class 1, discarded.
        file[PAGE_SIZE + 200] ^= 0xFF;
        let r = Reader::open(&file).unwrap();
        assert_eq!(r.lookup_v4(v4(15)).unwrap(), Some(1));
    }

    #[test]
    fn both_metas_corrupt_rejects() {
        let recs: &[(Ipv4Key, Ipv4Key, u32)] = &[(v4(10), v4(20), 1)];
        let mut file = build_single_leaf::<Ipv4Key>(IpVersion::V4, spec::SCOPE_MODE_SCALAR, recs);
        file[200] ^= 0xFF; // meta-A data byte
        file[PAGE_SIZE + 200] ^= 0xFF; // meta-B data byte
                                       // CRC-validating open: both metas fail CRC → file is corrupt.
        let result = Reader::open(&file);
        assert!(
            result.is_err(),
            "CRC-validating open must reject corrupt metas"
        );
    }

    #[test]
    fn incompatible_major_fails_closed() {
        let recs: &[(Ipv4Key, Ipv4Key, u32)] = &[(v4(10), v4(20), 1)];
        let mut file = build_single_leaf::<Ipv4Key>(IpVersion::V4, spec::SCOPE_MODE_SCALAR, recs);
        // Set version_major = 5 on the active meta and re-checksum ⇒ class 2 reject.
        let page = &mut file[..PAGE_SIZE];
        page[spec::META_VERSION_MAJOR..spec::META_VERSION_MAJOR + 2]
            .copy_from_slice(&5u16.to_le_bytes());
        finalize_checksum(page);
        assert!(matches!(
            Reader::open(&file),
            Err(Error::UnsupportedMajor(5))
        ));
    }

    #[test]
    fn malformed_unsorted_leaf_rejects() {
        // Records written out of order ⇒ the validate walk rejects.
        let recs: &[(Ipv4Key, Ipv4Key, u32)] = &[(v4(30), v4(40), 2), (v4(10), v4(20), 1)];
        let file = build_single_leaf::<Ipv4Key>(IpVersion::V4, spec::SCOPE_MODE_SCALAR, recs);
        match Reader::open(&file).and_then(|r| r.validate()) {
            Err(Error::Invariant(m)) => assert_eq!(m, "leaf records not sorted/disjoint"),
            Err(e) => panic!("expected Invariant(\"leaf records not sorted/disjoint\"), got {e:?}"),
            Ok(_) => panic!("expected rejection, but opened OK"),
        }
    }

    #[test]
    fn record_count_mismatch_rejects() {
        let recs: &[(Ipv4Key, Ipv4Key, u32)] = &[(v4(10), v4(20), 1)];
        let mut file = build_single_leaf::<Ipv4Key>(IpVersion::V4, spec::SCOPE_MODE_SCALAR, recs);
        // Claim 5 records; the walk finds 1 ⇒ reject.
        let page = &mut file[..PAGE_SIZE];
        page[spec::META_RECORD_COUNT..spec::META_RECORD_COUNT + 8]
            .copy_from_slice(&5u64.to_le_bytes());
        finalize_checksum(page);
        match Reader::open(&file).and_then(|r| r.validate()) {
            Err(Error::Invariant(m)) => assert_eq!(m, "record_count mismatch"),
            Err(e) => panic!("expected Invariant(\"record_count mismatch\"), got {e:?}"),
            Ok(_) => panic!("expected rejection, but opened OK"),
        }
    }

    #[test]
    fn lookup_family_mismatch_errors() {
        let recs: &[(Ipv4Key, Ipv4Key, u32)] = &[(v4(10), v4(20), 1)];
        let file = build_single_leaf::<Ipv4Key>(IpVersion::V4, spec::SCOPE_MODE_SCALAR, recs);
        let r = Reader::open(&file).unwrap();
        assert!(matches!(
            r.lookup_v6(crate::key::Ipv6Key { hi: 0, lo: 5 }),
            Err(Error::InvalidInput(_))
        ));
    }

    #[test]
    fn truncated_file_rejects() {
        let recs: &[(Ipv4Key, Ipv4Key, u32)] = &[(v4(10), v4(20), 1)];
        let file = build_single_leaf::<Ipv4Key>(IpVersion::V4, spec::SCOPE_MODE_SCALAR, recs);
        // Drop the leaf page: total_pages (3) now exceeds the file ⇒ reject.
        assert!(matches!(
            Reader::open(&file[..2 * PAGE_SIZE]),
            Err(Error::FileTooShort { .. })
        ));
        // Sub-2-page file ⇒ reject.
        assert!(matches!(
            Reader::open(&file[..PAGE_SIZE]),
            Err(Error::FileTooShort { .. })
        ));
    }

    #[test]
    fn meta_tail_nonzero_rejected() {
        // A crafted non-zero byte in the active meta's reserved tail [meta_size,
        // page_size), with the CRC recomputed, must be rejected (§5/§9).
        let recs: &[(Ipv4Key, Ipv4Key, u32)] = &[(v4(10), v4(20), 1)];
        let mut file = build_single_leaf::<Ipv4Key>(IpVersion::V4, spec::SCOPE_MODE_SCALAR, recs);
        let page = &mut file[..PAGE_SIZE]; // meta-A (active, txn 2)
        page[spec::META_SIZE as usize + 7] = 0xAB;
        crate::wire::finalize_checksum(page);
        // CRC-validating open + classify catches the non-zero tail.
        if let Ok(r) = Reader::open(&file) {
            assert!(
                matches!(r.validate(), Err(Error::NonZeroReserved(m)) if m == "meta tail"),
                "validate must reject non-zero meta tail"
            );
        }
    }

    /// A cyclic branch (child[0] points back to the root) must NOT cause the
    /// hot-path descent to loop forever or reinterpret branch bytes as a leaf
    /// record. The descent is bounded by `tree_height` and driven by
    /// `page_type`, so it returns a clean miss. (reader.rs lookup/scan hardening.)
    #[test]
    fn lookup_and_scan_on_self_cyclic_branch_are_bounded() {
        let left: &[(Ipv4Key, Ipv4Key, u32)] = &[(v4(10), v4(20), 1)];
        let right: &[(Ipv4Key, Ipv4Key, u32)] = &[(v4(100), v4(110), 2)];
        let mut file = build_two_level::<Ipv4Key>(
            IpVersion::V4,
            spec::SCOPE_MODE_SCALAR,
            v4(100),
            left,
            right,
        );
        // Rewire the root branch's first child to point at the root itself (pgno 2).
        let root = &mut file[2 * PAGE_SIZE..3 * PAGE_SIZE];
        root[PAGE_HEADER_SIZE..PAGE_HEADER_SIZE + 4].copy_from_slice(&2u32.to_le_bytes());
        finalize_checksum(root);

        let r = Reader::open(&file).unwrap();
        // A lookup that routes to the cyclic child (ip < sep=100 ⇒ child[0]=root)
        // never reaches a leaf within `tree_height` steps → clean miss, not
        // fabricated data and not an infinite loop. (The right leaf at child[1]
        // is untouched, so ips ≥ 100 still resolve normally — those are real
        // hits, not cycle artifacts, and are not asserted here.)
        assert_eq!(r.lookup_v4(v4(15)).unwrap(), None);
        assert_eq!(r.lookup_v4(v4(50)).unwrap(), None);
        // Scan surfaces the structural corruption as a typed error instead of
        // recursing forever / stack-overflowing on the cycle.
        assert!(
            matches!(
                r.scan_v4(|_, _, _| {}),
                Err(Error::Invariant(_)) | Err(Error::Structural(_))
            ),
            "scan on a cyclic branch must return a structural error"
        );
    }

    /// An empty reachable leaf (entry_count == 0) whose unused body still holds
    /// stale record bytes is corruption: `validate` must reject it rather than
    /// treating the early `entry_count == 0` path as a clean pass.
    /// (reader.rs empty-leaf-body validation.)
    #[test]
    fn validate_rejects_empty_leaf_with_nonzero_body() {
        let recs: &[(Ipv4Key, Ipv4Key, u32)] = &[(v4(10), v4(20), 1)];
        let mut file = build_single_leaf::<Ipv4Key>(IpVersion::V4, spec::SCOPE_MODE_SCALAR, recs);
        // Drop the leaf's entry_count to 0 while leaving its record bytes in place.
        let leaf = &mut file[2 * PAGE_SIZE..3 * PAGE_SIZE];
        leaf[spec::PH_ENTRY_COUNT..spec::PH_ENTRY_COUNT + 2].copy_from_slice(&0u16.to_le_bytes());
        finalize_checksum(leaf);
        // Keep meta consistent (0 records) so the only failing check is the tail.
        let meta_page = &mut file[..PAGE_SIZE];
        meta_page[spec::META_RECORD_COUNT..spec::META_RECORD_COUNT + 8]
            .copy_from_slice(&0u64.to_le_bytes());
        finalize_checksum(meta_page);

        let r = Reader::open(&file).unwrap();
        match r.validate() {
            Err(Error::NonZeroReserved(m)) => assert_eq!(
                m, "empty leaf body",
                "empty leaf with stale body must be rejected as a non-zero reserved region"
            ),
            other => panic!("expected NonZeroReserved(\"empty leaf body\"), got {other:?}"),
        }
    }
}
