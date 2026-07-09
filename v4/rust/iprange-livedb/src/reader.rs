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
        let meta = select_active_meta(bytes, true)?;
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
        Ok(())
    }

    /// Validate the v4.1 metadata (§C.5) before exposing the reader: the scope table, then
    /// every scope's per-scope KV tree (incl. its overflow chains). A v4.0 file has
    /// `scope_table_root == 0` (no metadata) and this is a no-op. Without `alloc` the reader
    /// fails closed on a metadata-bearing file (it cannot walk the scope structures).
    fn validate_scope_table(&self) -> Result<()> {
        let root = self.meta.scope_table_root;
        if root == 0 {
            return Ok(());
        }
        #[cfg(feature = "alloc")]
        {
            // One file-wide page-disjointness bitset (F2) shared by the scope-table walk,
            // every per-scope KV tree, and every overflow chain: a page reached twice — a
            // duplicate child pgno, an aliased subtree, or a shared overflow chain across two
            // KV entries (the glm PoC wrong-answer bug) — is structural corruption. This
            // makes the whole v4.1 metadata page forest provably disjoint and acyclic.
            let mut visited = alloc::vec![false; self.meta.total_pages as usize];
            crate::scope::validate(self.bytes, root, self.meta.total_pages, &mut visited)?;
            // Each scope record's `kv_root` is a separate B+tree (§C.4): walk and validate
            // it (range-check pgnos, sorted+disjoint keys, height bound, per-page CRC32C,
            // overflow read-by-count, type==0 text), sharing `visited`. The scope table is
            // now validated, so loading the records is bounded and safe.
            let recs = crate::scope::load_all(self.bytes, root)?;
            for rec in &recs {
                crate::kv::validate(self.bytes, rec.kv_root, self.meta.total_pages, &mut visited)?;
            }
            Ok(())
        }
        #[cfg(not(feature = "alloc"))]
        {
            Err(Error::Incompatible("v4.1 metadata requires alloc"))
        }
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
    #[inline]
    pub(crate) fn record_size_bytes(&self) -> usize {
        self.record_size
    }


    /// The validated active meta (for the writer's `open_image`, §6.2).
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
            self.scan_node::<K, F>(self.meta.root_pgno, 1, &mut f);
        }
        Ok(())
    }


    // --- v4.1 metadata reads (§C, mirror the Writer's read API) ---
    //
    // Read-only, shared-lock consumers (the `MmapReader` path) descend the **on-disk
    // committed tree** validated at `open` (no in-memory registry). A v4.0 file has
    // `scope_table_root == 0` ⇒ no metadata ⇒ empty/`None`, never an error or a panic. All
    // descents go through the existing bounds-safe byte functions in `scope`/`kv`.

    /// All defined scopes as `(scope_id, name)`, ascending by `scope_id`. The FILE target
    /// (`scope_id 0`) is a dataset-metadata target, not a defined scope, so it is excluded
    /// even when it carries KV (§C.2). Mirrors [`Writer::scope_list`].
    #[cfg(feature = "alloc")]
    pub fn scope_list(&self) -> alloc::vec::Vec<(u32, alloc::vec::Vec<u8>)> {
        let root = self.meta.scope_table_root;
        if root == 0 {
            return alloc::vec::Vec::new();
        }
        // The tree was validated at `open`, so `load_all` cannot fail here; treat any error
        // as no metadata rather than panicking.
        match crate::scope::load_all(self.bytes, root) {
            Ok(recs) => recs
                .into_iter()
                .filter(|r| r.id != spec::FILE_SCOPE_ID)
                .map(|r| (r.id, r.name))
                .collect(),
            Err(_) => alloc::vec::Vec::new(),
        }
    }


    /// The scope's `name` (UTF-8 bytes), or `None` if it does not exist. The FILE target
    /// (`scope_id 0`) is never a defined scope, so it returns `None`. Mirrors
    /// [`Writer::scope_name`].
    #[cfg(feature = "alloc")]
    pub fn scope_name(&self, scope_id: u32) -> Option<alloc::vec::Vec<u8>> {
        self.scope_rec(scope_id).map(|r| r.name)
    }


    /// The scope's `version`, or `None` if it does not exist. Mirrors [`Writer::scope_version`].
    #[cfg(feature = "alloc")]
    pub fn scope_version(&self, scope_id: u32) -> Option<u64> {
        self.scope_rec(scope_id).map(|r| r.version)
    }


    /// The scope's opaque `type` byte, or `None` if it does not exist. Mirrors
    /// [`Writer::scope_type`].
    #[cfg(feature = "alloc")]
    pub fn scope_type(&self, scope_id: u32) -> Option<u8> {
        self.scope_rec(scope_id).map(|r| r.type_)
    }


    /// Get `key` on `target` as `(type, value)` (the whole reassembled value), or
    /// `Ok(None)` if absent (§C.7). Resolves the target's committed `kv_root` (`target == 0`
    /// ⇒ the FILE record), then descends its KV tree. A non-existent target or
    /// `kv_root == 0` ⇒ `Ok(None)`. Mirrors [`Writer::meta_get`].
    #[cfg(feature = "alloc")]
    pub fn meta_get(&self, target: u32, key: &[u8]) -> Result<Option<(u32, alloc::vec::Vec<u8>)>> {
        crate::kv::check_key(key)?;
        let root = match self.target_kv_root(target)? {
            Some(r) => r,
            None => return Ok(None),
        };
        crate::kv::get(self.bytes, root, key, self.meta.total_pages)
    }


    /// List every `(key, type, value)` on `target`, ordered by `key` (§C.4). A non-existent
    /// target or `kv_root == 0` ⇒ an empty list. Mirrors [`Writer::meta_list`].
    #[cfg(feature = "alloc")]
    pub fn meta_list(&self, target: u32) -> Result<alloc::vec::Vec<crate::writer::MetaEntry>> {
        let mut out = alloc::vec::Vec::new();
        if let Some(root) = self.target_kv_root(target)? {
            let mut entries = alloc::vec::Vec::new();
            crate::kv::list(self.bytes, root, self.meta.total_pages, &mut entries)?;
            out = entries
                .into_iter()
                .map(|e| (e.key, e.type_, e.value))
                .collect();
        }
        Ok(out)
    }


    /// The committed per-scope record for a **defined** scope (the FILE target `scope_id 0`
    /// is excluded, mirroring the Writer's `scope_pos`). `scope_table_root == 0` (a v4.0
    /// file) ⇒ `None`. The tree was validated at `open`, so any descent error here is
    /// treated as "not found" rather than panicking.
    #[cfg(feature = "alloc")]
    fn scope_rec(&self, scope_id: u32) -> Option<crate::scope::ScopeRec> {
        if scope_id == spec::FILE_SCOPE_ID {
            return None;
        }
        crate::scope::find(
            self.bytes,
            self.meta.scope_table_root,
            self.meta.total_pages,
            scope_id,
        )
        .ok()
        .flatten()
    }


    /// The committed `kv_root` of `target` (`None` if the target has no record or no KV).
    /// FILE (`scope_id 0`) is looked up like any scope record. Mirrors the Writer's
    /// `target_kv_root` over the on-disk tree.
    #[cfg(feature = "alloc")]
    fn target_kv_root(&self, target: u32) -> Result<Option<u32>> {
        let root = self.meta.scope_table_root;
        if root == 0 {
            return Ok(None);
        }
        match crate::scope::find(self.bytes, root, self.meta.total_pages, target)? {
            Some(rec) if rec.kv_root != 0 => Ok(Some(rec.kv_root)),
            _ => Ok(None),
        }
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
        let height = self.meta.tree_height;
        let mut pgno = self.meta.root_pgno;
        let mut depth = 1u32;
        loop {
            let page = self.page_bytes(pgno);
            let count = PageHeader::decode(page).entry_count as usize;
            if depth == height {
                let leaf = LeafView::<K>::new(page, count);
                return leaf_lookup(&leaf, ip);
            }
            let branch = BranchView::<K>::new(page, count);
            pgno = branch.child(branch_descend(&branch, ip));
            depth += 1;
        }
    }


    fn scan_node<K: IpKey, F: FnMut(K, K, u32)>(&self, pgno: u32, depth: u32, f: &mut F) {
        let page = self.page_bytes(pgno);
        let count = PageHeader::decode(page).entry_count as usize;
        if depth == self.meta.tree_height {
            let leaf = LeafView::<K>::new(page, count);
            for i in 0..leaf.len() {
                let r = leaf.record(i);
                f(r.from(), r.to(), r.scope_id());
            }
        } else {
            let branch = BranchView::<K>::new(page, count);
            for j in 0..branch.child_count() {
                self.scan_node::<K, F>(branch.child(j), depth + 1, f);
            }
        }
    }


    fn validate_tree(&self) -> Result<()> {
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
            if r < 1 || r > self.leaf_max {
                return Err(Error::Invariant("leaf entry_count out of range"));
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
        r.scan_v4(|f, t, s| seen.push((f.0, t.0, s)))
            .unwrap();
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
        let right: &[(Ipv4Key, Ipv4Key, u32)] =
            &[(v4(100), v4(110), 3), (v4(200), v4(210), 4)];
        let file = build_two_level::<Ipv4Key>(IpVersion::V4, spec::SCOPE_MODE_SCALAR, v4(100), left, right);
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
        // Trusted open: magic intact, so file opens OK. validate() catches
        // the CRC corruption in the unused meta body bytes.
        match Reader::open(&file) {
            Ok(r) => assert!(r.validate().is_err(), "validate must catch corrupt metas"),
            Err(e) => panic!("trusted open should succeed (magic intact), got {e:?}"),
        }
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
        // Trusted open accepts the file (tail check skipped). validate() catches it.
        let r = Reader::open(&file).expect("trusted open accepts non-zero tail");
        assert!(
            matches!(r.validate(), Err(Error::NonZeroReserved(m)) if m == "meta tail"),
            "validate must reject non-zero meta tail"
        );
    }


    // --- v4.1 metadata reads on the Reader (descend the on-disk committed tree) ---

    #[cfg(feature = "alloc")]
    mod metadata {
        use super::*;
        use crate::writer::Writer;

        // TODO: re-enable when scope/KV metadata APIs are re-implemented (SOW-0014 Phase 4c)
        /*
        /// Build a v4.1 image with scopes (names/versions/types) + KV on a scope and on
        /// FILE(0), one IP record, and a large overflow-spanning value. Returns the committed
        /// image and the two scope ids so the test can read them back via both APIs.
        fn build_meta_image() -> (Vec<u8>, u32, u32) {
            let mut w = Writer::<Ipv4Key>::create(1, 0);
            let a = w.scope_define(b"feed-a").unwrap();
            let b = w.scope_define(b"feed-b").unwrap();
            w.scope_set_type(a, 7).unwrap();
            w.scope_bump_version(a).unwrap();
            w.scope_bump_version(a).unwrap();
            w.scope_set_version(b, 100).unwrap();
            // KV on scope a (text + binary types) and on FILE(0).
            w.meta_set(a, b"region", spec::KV_TYPE_TEXT, b"eu-west")
                .unwrap();
            w.meta_set(a, b"weight", 9, &[0xde, 0xad, 0xbe, 0xef])
                .unwrap();
            // A value larger than the inline cap forces an overflow chain (§C.5).
            let big = vec![0x5au8; spec::KV_INLINE_MAX + spec::OVERFLOW_PAYLOAD + 17];
            w.meta_set(a, b"blob", 1, &big).unwrap();
            w.meta_set(
                spec::FILE_SCOPE_ID,
                b"source",
                spec::KV_TYPE_TEXT,
                b"firehol",
            )
            .unwrap();
            w.set(v4(10), v4(20), &[7]).unwrap(); // IP tree coexists
            w.commit(1).unwrap();
            (w.into_image(), a, b)
        }
        */

        // TODO: re-enable when scope/KV metadata APIs are re-implemented (SOW-0014 Phase 4c)
        /*
        /// The Reader's scope/metadata reads must return exactly what the Writer returns for
        /// the same committed image (API symmetry, on-disk vs in-memory registry).
        #[test]
        fn reader_matches_writer() {
            let (img, a, b) = build_meta_image();
            let w = Writer::<Ipv4Key>::open_image(img.clone()).unwrap();
            let r = Reader::open(&img).unwrap();

            // scope_list: same (id, name) set, FILE(0) excluded by both.
            assert_eq!(r.scope_list(), w.scope_list());
            assert_eq!(
                r.scope_list(),
                vec![(a, b"feed-a".to_vec()), (b, b"feed-b".to_vec())]
            );

            // Per-scope header fields.
            for id in [a, b] {
                assert_eq!(r.scope_name(id), w.scope_name(id));
                assert_eq!(r.scope_version(id), w.scope_version(id));
                assert_eq!(r.scope_type(id), w.scope_type(id));
            }
            assert_eq!(r.scope_name(a), Some(b"feed-a".to_vec()));
            assert_eq!(r.scope_type(a), Some(7));
            assert_eq!(r.scope_version(a), Some(2));
            assert_eq!(r.scope_version(b), Some(100));

            // meta_get on a scope: text, binary, and overflow-spanning values.
            for key in [&b"region"[..], b"weight", b"blob"] {
                assert_eq!(r.meta_get(a, key).unwrap(), w.meta_get(a, key).unwrap());
            }
            assert_eq!(
                r.meta_get(a, b"region").unwrap(),
                Some((spec::KV_TYPE_TEXT, b"eu-west".to_vec()))
            );
            // The big value round-trips identically across the overflow chain.
            let big = vec![0x5au8; spec::KV_INLINE_MAX + spec::OVERFLOW_PAYLOAD + 17];
            assert_eq!(r.meta_get(a, b"blob").unwrap(), Some((1u32, big)));

            // meta_list on the scope: same ordered set as the Writer.
            assert_eq!(r.meta_list(a).unwrap(), w.meta_list(a).unwrap());
            let keys: Vec<Vec<u8>> = r
                .meta_list(a)
                .unwrap()
                .into_iter()
                .map(|(k, _, _)| k)
                .collect();
            assert_eq!(
                keys,
                vec![b"blob".to_vec(), b"region".to_vec(), b"weight".to_vec()]
            );

            // FILE(0) target works on both APIs (and is not a "defined scope").
            assert_eq!(
                r.meta_get(spec::FILE_SCOPE_ID, b"source").unwrap(),
                Some((spec::KV_TYPE_TEXT, b"firehol".to_vec()))
            );
            assert_eq!(
                r.meta_get(spec::FILE_SCOPE_ID, b"source").unwrap(),
                w.meta_get(spec::FILE_SCOPE_ID, b"source").unwrap()
            );
            assert_eq!(
                r.meta_list(spec::FILE_SCOPE_ID).unwrap(),
                w.meta_list(spec::FILE_SCOPE_ID).unwrap()
            );
            assert_eq!(r.scope_name(spec::FILE_SCOPE_ID), None); // FILE is not a defined scope
        }
        */

        // TODO: re-enable when scope/KV metadata APIs are re-implemented (SOW-0014 Phase 4c)
        /*
        /// A missing scope id / missing key ⇒ None / empty (mirrors the Writer + §C.7).
        #[test]
        fn missing_scope_and_key() {
            let (img, a, _b) = build_meta_image();
            let r = Reader::open(&img).unwrap();

            assert_eq!(r.scope_name(999), None);
            assert_eq!(r.scope_version(999), None);
            assert_eq!(r.scope_type(999), None);
            // Missing key on an existing scope ⇒ None / no entry.
            assert_eq!(r.meta_get(a, b"nope").unwrap(), None);
            // Missing target ⇒ None / empty list.
            assert_eq!(r.meta_get(999, b"region").unwrap(), None);
            assert!(r.meta_list(999).unwrap().is_empty());
        }
        */

        /// A v4.0 image (no metadata) ⇒ scope_list empty, meta_get None — never a panic.
        #[test]
        fn v40_image_has_no_metadata() {
            let mut w = Writer::<Ipv4Key>::create(1, 0).unwrap();
            w.set(v4(1), v4(2), 1).unwrap();
            w.commit(1).unwrap();
            let img = w.into_image().unwrap();
            let r = Reader::open(&img).unwrap();
            assert!(r.scope_list().is_empty());
            assert_eq!(r.scope_name(1), None);
            assert_eq!(r.scope_version(1), None);
            assert_eq!(r.scope_type(1), None);
            assert_eq!(r.meta_get(1, b"k").unwrap(), None);
            assert_eq!(r.meta_get(spec::FILE_SCOPE_ID, b"k").unwrap(), None);
            assert!(r.meta_list(1).unwrap().is_empty());
            assert!(r.meta_list(spec::FILE_SCOPE_ID).unwrap().is_empty());
        }

        // TODO: re-enable when scope/KV metadata APIs are re-implemented (SOW-0014 Phase 4c)
        /*
        /// A scope with no KV ⇒ meta_get None / meta_list empty (kv_root == 0), even though
        /// the scope itself exists in the table.
        #[test]
        fn scope_without_kv() {
            let mut w = Writer::<Ipv4Key>::create(1, 0);
            let a = w.scope_define(b"noKV").unwrap();
            w.commit(1).unwrap();
            let img = w.into_image();
            let r = Reader::open(&img).unwrap();
            assert_eq!(r.scope_name(a), Some(b"noKV".to_vec()));
            assert_eq!(r.meta_get(a, b"region").unwrap(), None);
            assert!(r.meta_list(a).unwrap().is_empty());
        }
        */
    }

}
