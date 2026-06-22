//! The v4 reader: open over an in-memory image, validate per §9, and query.
//!
//! This is the byte-slice core (the OS layer — `mmap` + `SEEK_HOLE`/`fstat` hardening
//! (§10) and `flock(LOCK_SH)` (§11) — wraps it). `open` selects the active meta
//! (§5.1 bootstrap), checks geometry (§9 step 2), and **fully validates the reachable
//! tree before exposing any result** (§9 step 4 — the default): a hostile but
//! checksum-valid structure cannot leak a wrong answer, and a corrupt/truncated image
//! is rejected, never `SIGBUS`/loops/reads OOB. `lookup` / `scan` then navigate the
//! validated structure, returning the borrowed `scope` (zero-copy, D11).

use crate::crc32c;
use crate::error::{Error, Result};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::node::{BranchView, LeafView};
use crate::spec::{self, IpVersion};
use crate::wire::{self, Meta, PageHeader};

/// A read-only view over a **validated** v4 image. Holds no lock and no allocation;
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
    /// Open and **fully validate** a v4 image (the default, §9). Returns a typed error
    /// (exposing nothing) on any malformed/hostile input.
    pub fn open(bytes: &'a [u8]) -> Result<Reader<'a>> {
        let meta = select_active_meta(bytes)?;
        // `flags` reserved bits were already rejected in classify; only bit0 remains.
        let version = IpVersion::from_flag_bit(meta.flags);

        // Geometry (§9 step 2). `page_size`/`key_width`/`record_size`/`meta_size` were
        // cross-checked in classify; here: page-count, file size, height/root.
        if meta.total_pages < 2 || meta.total_pages > (1u64 << 32) {
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
            return Err(Error::FileTooShort {
                need: needed,
                have,
            });
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
            leaf_max: spec::leaf_max(meta.record_size),
            branch_max: spec::branch_max(meta.key_width),
        };
        reader.validate_tree()?;
        Ok(reader)
    }

    /// The file's IP family.
    #[inline]
    pub fn version(&self) -> IpVersion {
        self.version
    }

    /// The fixed per-record scope width (bytes); 0 = presence map (§4).
    #[inline]
    pub fn scope_width(&self) -> usize {
        self.meta.scope_width as usize
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

    /// The validated active meta (for the writer's `open_image`, §6.2).
    #[inline]
    #[cfg_attr(not(feature = "os"), allow(dead_code))]
    pub(crate) fn active_meta(&self) -> Meta {
        self.meta
    }

    /// Point lookup (IPv4). Errors if the file is not IPv4.
    #[inline]
    pub fn lookup_v4(&self, ip: Ipv4Key) -> Result<Option<&'a [u8]>> {
        self.lookup(ip)
    }

    /// Point lookup (IPv6). Errors if the file is not IPv6.
    #[inline]
    pub fn lookup_v6(&self, ip: Ipv6Key) -> Result<Option<&'a [u8]>> {
        self.lookup(ip)
    }

    /// Point lookup: the `scope` of the range covering `ip`, or `None` if absent.
    /// `O(log n)`. Errors only on a family mismatch.
    pub fn lookup<K: IpKey>(&self, ip: K) -> Result<Option<&'a [u8]>> {
        if K::VERSION != self.version {
            return Err(Error::InvalidInput("lookup key family mismatch"));
        }
        Ok(self.lookup_inner::<K>(ip))
    }

    /// In-order scan (IPv4): calls `f(from, to, scope)` for every record. Errors if the
    /// file is not IPv4.
    pub fn scan_v4<F: FnMut(Ipv4Key, Ipv4Key, &[u8])>(&self, f: F) -> Result<()> {
        self.scan(f)
    }

    /// In-order scan (IPv6).
    pub fn scan_v6<F: FnMut(Ipv6Key, Ipv6Key, &[u8])>(&self, f: F) -> Result<()> {
        self.scan(f)
    }

    /// In-order scan: calls `f(from, to, scope)` for every record in key order.
    /// Re-descends (no leaf sibling pointers, D3); zero-alloc. Errors on family
    /// mismatch.
    pub fn scan<K: IpKey, F: FnMut(K, K, &[u8])>(&self, mut f: F) -> Result<()> {
        if K::VERSION != self.version {
            return Err(Error::InvalidInput("scan key family mismatch"));
        }
        if self.meta.root_pgno != 0 {
            self.scan_node::<K, F>(self.meta.root_pgno, 1, &mut f);
        }
        Ok(())
    }

    // --- internals ---

    /// The `pgno`-th page. `pgno < total_pages` is guaranteed by the caller (geometry +
    /// the validate walk check every pgno before access), and `total_pages·page_size <=
    /// bytes.len()` was checked in `open`, so the slice is always in bounds.
    #[inline]
    fn page_bytes(&self, pgno: u32) -> &'a [u8] {
        let bytes: &'a [u8] = self.bytes;
        let off = pgno as usize * spec::PAGE_SIZE;
        &bytes[off..off + spec::PAGE_SIZE]
    }

    fn lookup_inner<K: IpKey>(&self, ip: K) -> Option<&'a [u8]> {
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
                let leaf = LeafView::<K>::new(page, count, self.record_size);
                return leaf_lookup(&leaf, ip);
            }
            let branch = BranchView::<K>::new(page, count);
            pgno = branch.child(branch_descend(&branch, ip));
            depth += 1;
        }
    }

    fn scan_node<K: IpKey, F: FnMut(K, K, &[u8])>(&self, pgno: u32, depth: u32, f: &mut F) {
        let page = self.page_bytes(pgno);
        let count = PageHeader::decode(page).entry_count as usize;
        if depth == self.meta.tree_height {
            let leaf = LeafView::<K>::new(page, count, self.record_size);
            for i in 0..leaf.len() {
                let r = leaf.record(i);
                f(r.from(), r.to(), r.scope());
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
            let leaf = LeafView::<K>::new(page, r, self.record_size);
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
fn select_active_meta(bytes: &[u8]) -> Result<Meta> {
    if bytes.len() < 2 * spec::PAGE_SIZE {
        return Err(Error::FileTooShort {
            need: (2 * spec::PAGE_SIZE) as u64,
            have: bytes.len() as u64,
        });
    }
    let a = classify(&bytes[..spec::PAGE_SIZE], 0)?;
    let b = classify(&bytes[spec::PAGE_SIZE..2 * spec::PAGE_SIZE], 1)?;
    match (a, b) {
        (None, None) => Err(Error::Structural("no valid meta page")),
        (Some(m), None) | (None, Some(m)) => Ok(m),
        (Some(ma), Some(mb)) => {
            let sa = &bytes[spec::META_STATIC_START..spec::META_STATIC_END];
            let sb = &bytes
                [spec::PAGE_SIZE + spec::META_STATIC_START..spec::PAGE_SIZE + spec::META_STATIC_END];
            if sa != sb {
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
fn classify(page: &[u8], expected_pgno: u32) -> Result<Option<Meta>> {
    // Class 1: torn / not a meta — discarded, never rejects the file by itself.
    if !crc32c::verify_page(page) {
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
    if m.version_minor == 0 && m.meta_size != spec::META_SIZE {
        return Err(Error::BadMetaSize(m.meta_size));
    }
    let expect_kw = IpVersion::from_flag_bit(m.flags).key_width();
    if m.key_width != expect_kw {
        return Err(Error::Structural("key_width disagrees with flags"));
    }
    if m.record_size != spec::record_size(m.key_width, m.scope_width) {
        return Err(Error::Structural("record_size mismatch"));
    }
    Ok(Some(m))
}

/// Binary search a leaf for the record covering `ip`: the record with greatest
/// `from <= ip`, a hit iff `ip <= to`. Returns the borrowed `scope`.
#[inline]
fn leaf_lookup<'a, K: IpKey>(leaf: &LeafView<'a, K>, ip: K) -> Option<&'a [u8]> {
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
        Some(rec.scope())
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
        scope_width: u8,
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
            scope_width,
            record_size: spec::record_size(version.key_width(), scope_width),
            created_unixtime: 0,
            root_pgno: root,
            tree_height: height,
            total_pages,
            record_count,
            txn_id: txn,
            updated_unixtime: 0,
        }
    }

    fn put_leaf<K: IpKey>(file: &mut [u8], pgno: u32, scope_width: u8, records: &[(K, K, &[u8])]) {
        let rs = spec::record_size(K::WIDTH as u8, scope_width) as usize;
        let base = pgno as usize * PAGE_SIZE;
        let page = &mut file[base..base + PAGE_SIZE];
        PageHeader::write(page, spec::PAGE_TYPE_LEAF, records.len() as u16, pgno);
        for (i, (f, t, s)) in records.iter().enumerate() {
            let off = PAGE_HEADER_SIZE + i * rs;
            record::write::<K>(&mut page[off..off + rs], *f, *t, s);
        }
        finalize_checksum(page);
    }

    /// 3 pages: meta-A (active, txn 2), meta-B (txn 1), one root leaf at pgno 2.
    fn build_single_leaf<K: IpKey>(
        version: IpVersion,
        scope_width: u8,
        records: &[(K, K, &[u8])],
    ) -> Vec<u8> {
        let mut file = vec![0u8; 3 * PAGE_SIZE];
        put_leaf::<K>(&mut file, 2, scope_width, records);
        let rc = records.len() as u64;
        meta(0, version, scope_width, 2, 1, 3, rc, 2).encode_into(&mut file[..PAGE_SIZE]);
        meta(1, version, scope_width, 2, 1, 3, rc, 1)
            .encode_into(&mut file[PAGE_SIZE..2 * PAGE_SIZE]);
        file
    }

    fn build_empty(version: IpVersion, scope_width: u8) -> Vec<u8> {
        let mut file = vec![0u8; 2 * PAGE_SIZE];
        meta(0, version, scope_width, 0, 0, 2, 0, 2).encode_into(&mut file[..PAGE_SIZE]);
        meta(1, version, scope_width, 0, 0, 2, 0, 1).encode_into(&mut file[PAGE_SIZE..]);
        file
    }

    /// 5 pages: metas, a root branch at pgno 2 (one separator), leaves at pgno 3/4.
    fn build_two_level<K: IpKey>(
        version: IpVersion,
        scope_width: u8,
        sep: K,
        left: &[(K, K, &[u8])],
        right: &[(K, K, &[u8])],
    ) -> Vec<u8> {
        let mut file = vec![0u8; 5 * PAGE_SIZE];
        put_leaf::<K>(&mut file, 3, scope_width, left);
        put_leaf::<K>(&mut file, 4, scope_width, right);
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
        meta(0, version, scope_width, 2, 2, 5, rc, 2).encode_into(&mut file[..PAGE_SIZE]);
        meta(1, version, scope_width, 2, 2, 5, rc, 1)
            .encode_into(&mut file[PAGE_SIZE..2 * PAGE_SIZE]);
        file
    }

    fn v4(n: u32) -> Ipv4Key {
        Ipv4Key(n)
    }

    // --- tests ---

    #[test]
    fn single_leaf_lookup_and_scan() {
        let recs: &[(Ipv4Key, Ipv4Key, &[u8])] =
            &[(v4(10), v4(20), &[1]), (v4(30), v4(40), &[2])];
        let file = build_single_leaf::<Ipv4Key>(IpVersion::V4, 1, recs);
        let r = Reader::open(&file).unwrap();
        assert_eq!(r.version(), IpVersion::V4);
        assert_eq!(r.record_count(), 2);
        assert!(!r.is_empty());

        assert_eq!(r.lookup_v4(v4(15)).unwrap(), Some(&[1u8][..]));
        assert_eq!(r.lookup_v4(v4(10)).unwrap(), Some(&[1u8][..])); // boundary
        assert_eq!(r.lookup_v4(v4(20)).unwrap(), Some(&[1u8][..])); // boundary
        assert_eq!(r.lookup_v4(v4(25)).unwrap(), None); // gap
        assert_eq!(r.lookup_v4(v4(30)).unwrap(), Some(&[2u8][..]));
        assert_eq!(r.lookup_v4(v4(40)).unwrap(), Some(&[2u8][..]));
        assert_eq!(r.lookup_v4(v4(9)).unwrap(), None); // before all
        assert_eq!(r.lookup_v4(v4(41)).unwrap(), None); // after all

        let mut seen = Vec::new();
        r.scan_v4(|f, t, s| seen.push((f.0, t.0, s.to_vec()))).unwrap();
        assert_eq!(seen, vec![(10, 20, vec![1]), (30, 40, vec![2])]);
    }

    #[test]
    fn empty_tree() {
        let file = build_empty(IpVersion::V4, 1);
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
        let left: &[(Ipv4Key, Ipv4Key, &[u8])] =
            &[(v4(10), v4(20), &[1]), (v4(50), v4(60), &[2])];
        let right: &[(Ipv4Key, Ipv4Key, &[u8])] =
            &[(v4(100), v4(110), &[3]), (v4(200), v4(210), &[4])];
        let file = build_two_level::<Ipv4Key>(IpVersion::V4, 1, v4(100), left, right);
        let r = Reader::open(&file).unwrap();
        assert_eq!(r.record_count(), 4);
        assert_eq!(r.lookup_v4(v4(15)).unwrap(), Some(&[1u8][..]));
        assert_eq!(r.lookup_v4(v4(55)).unwrap(), Some(&[2u8][..]));
        assert_eq!(r.lookup_v4(v4(105)).unwrap(), Some(&[3u8][..]));
        assert_eq!(r.lookup_v4(v4(205)).unwrap(), Some(&[4u8][..]));
        assert_eq!(r.lookup_v4(v4(70)).unwrap(), None); // gap in left leaf range
        assert_eq!(r.lookup_v4(v4(150)).unwrap(), None); // gap in right leaf range
        let mut seen = Vec::new();
        r.scan_v4(|f, _, _| seen.push(f.0)).unwrap();
        assert_eq!(seen, vec![10, 50, 100, 200]);
    }

    #[test]
    fn torn_inactive_meta_recovers() {
        let recs: &[(Ipv4Key, Ipv4Key, &[u8])] = &[(v4(10), v4(20), &[1])];
        let mut file = build_single_leaf::<Ipv4Key>(IpVersion::V4, 1, recs);
        // Corrupt the inactive meta (pgno 1) — CRC fails ⇒ class 1, discarded.
        file[PAGE_SIZE + 200] ^= 0xFF;
        let r = Reader::open(&file).unwrap();
        assert_eq!(r.lookup_v4(v4(15)).unwrap(), Some(&[1u8][..]));
    }

    #[test]
    fn both_metas_corrupt_rejects() {
        let recs: &[(Ipv4Key, Ipv4Key, &[u8])] = &[(v4(10), v4(20), &[1])];
        let mut file = build_single_leaf::<Ipv4Key>(IpVersion::V4, 1, recs);
        file[200] ^= 0xFF; // meta-A data
        file[PAGE_SIZE + 200] ^= 0xFF; // meta-B data
        assert!(matches!(Reader::open(&file), Err(Error::Structural(_))));
    }

    #[test]
    fn incompatible_major_fails_closed() {
        let recs: &[(Ipv4Key, Ipv4Key, &[u8])] = &[(v4(10), v4(20), &[1])];
        let mut file = build_single_leaf::<Ipv4Key>(IpVersion::V4, 1, recs);
        // Set version_major = 5 on the active meta and re-checksum ⇒ class 2 reject.
        let page = &mut file[..PAGE_SIZE];
        page[spec::META_VERSION_MAJOR..spec::META_VERSION_MAJOR + 2].copy_from_slice(&5u16.to_le_bytes());
        finalize_checksum(page);
        assert!(matches!(Reader::open(&file), Err(Error::UnsupportedMajor(5))));
    }

    #[test]
    fn malformed_unsorted_leaf_rejects() {
        // Records written out of order ⇒ the validate walk rejects.
        let recs: &[(Ipv4Key, Ipv4Key, &[u8])] =
            &[(v4(30), v4(40), &[2]), (v4(10), v4(20), &[1])];
        let file = build_single_leaf::<Ipv4Key>(IpVersion::V4, 1, recs);
        assert!(matches!(Reader::open(&file), Err(Error::Invariant(_))));
    }

    #[test]
    fn record_count_mismatch_rejects() {
        let recs: &[(Ipv4Key, Ipv4Key, &[u8])] = &[(v4(10), v4(20), &[1])];
        let mut file = build_single_leaf::<Ipv4Key>(IpVersion::V4, 1, recs);
        // Claim 5 records; the walk finds 1 ⇒ reject.
        let page = &mut file[..PAGE_SIZE];
        page[spec::META_RECORD_COUNT..spec::META_RECORD_COUNT + 8].copy_from_slice(&5u64.to_le_bytes());
        finalize_checksum(page);
        assert!(matches!(Reader::open(&file), Err(Error::Invariant(_))));
    }

    #[test]
    fn lookup_family_mismatch_errors() {
        let recs: &[(Ipv4Key, Ipv4Key, &[u8])] = &[(v4(10), v4(20), &[1])];
        let file = build_single_leaf::<Ipv4Key>(IpVersion::V4, 1, recs);
        let r = Reader::open(&file).unwrap();
        assert!(matches!(
            r.lookup_v6(crate::key::Ipv6Key { hi: 0, lo: 5 }),
            Err(Error::InvalidInput(_))
        ));
    }

    #[test]
    fn truncated_file_rejects() {
        let recs: &[(Ipv4Key, Ipv4Key, &[u8])] = &[(v4(10), v4(20), &[1])];
        let file = build_single_leaf::<Ipv4Key>(IpVersion::V4, 1, recs);
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
}
