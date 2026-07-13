//! Hostile-input robustness (§9 / §10): `Reader::open` must return only `Ok` or a typed
//! `Err` — **never** panic, loop, or read out of bounds — on truncations, bit-flips, and
//! arbitrary buffers. (`Writer::open_image` runs the same validation, so this also
//! guards the writer's open path.)

use iprange_livedb::crc32c;
use iprange_livedb::spec::{self, PAGE_SIZE};
use iprange_livedb::{Error, Ipv4Key, Reader, Writer};

/// A multi-level valid file with some freed (unreachable) pages, to exercise both the
/// reachable-page reject path and the unreachable-page ignore path.
fn valid_file() -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..2000u32 {
        w.set(Ipv4Key(i * 7), Ipv4Key(i * 7 + 2), i & 0xff).unwrap();
    }
    for i in (0..2000u32).step_by(5) {
        w.delete(Ipv4Key(i * 7), Ipv4Key(i * 7 + 2)).unwrap(); // frees pages
    }
    w.commit(0, u64::MAX).unwrap();
    w.into_image().unwrap()
}

fn lcg(state: &mut u64) -> u64 {
    *state = state
        .wrapping_mul(6364136223846793005)
        .wrapping_add(1442695040888963407);
    *state >> 33
}

// --- exact-message rejection assertions (SOW-0010 de-vacuization) -------------------------
//
// Every targeted corruption test asserts the EXACT validator error *message*, not just the
// broad class. The class alone is vacuous when a neighbouring check returns the same class —
// e.g. `ip_cross_leaf_overlap_rejected` shares `Invariant` with "record outside node bound",
// so removing the cross-leaf check still rejects (via the node-bound check) and a class-only
// assertion passes anyway. Pinning the `&'static str` ties each test to its named check:
// delete the check and the file either opens OK or rejects with a *different* message → fail.

/// Open `file`, assert it is rejected, and return the typed error (panics if it opened OK).
/// All targeted single-field corruption tests funnel through this so an accidental "opens OK"
/// is a loud failure, not a silently-passing class match.
fn reject(file: &[u8]) -> Error {
    match Reader::open(file).and_then(|r| r.validate()) {
        Ok(_) => panic!("expected Reader::open + validate to reject, but the file opened OK"),
        Err(e) => e,
    }
}

/// Assert `Reader::open(file)` rejects with `$variant($msg)` exactly (typed class AND the
/// `&'static str` the named check carries). `$variant` is any single-`&str` `Error` arm
/// (`Error::Structural`, `Error::Invariant`, `Error::NonZeroReserved`, `Error::Incompatible`,
/// `Error::InvalidInput`). On a wrong class/message the actual error is printed.
macro_rules! assert_reject {
    ($file:expr, $variant:path, $msg:expr) => {
        match reject($file) {
            $variant(m) => assert_eq!(m, $msg, "wrong message for {}", stringify!($variant)),
            e => panic!("expected {}({:?}), got {:?}", stringify!($variant), $msg, e),
        }
    };
}

#[test]
fn arbitrary_buffers_never_panic() {
    let mut s = 0x1234_5678_90ab_cdefu64;
    for &size in &[
        0usize, 1, 16, 100, 4095, 4096, 4097, 8191, 8192, 8193, 12288, 20000,
    ] {
        for _ in 0..40 {
            let mut buf = vec![0u8; size];
            for b in buf.iter_mut() {
                *b = lcg(&mut s) as u8;
            }
            let _ = Reader::open(&buf);
        }
    }
}

#[test]
fn tree_region_flip_never_silently_accepted() {
    // A bit flip in the data region (pages ≥ 2) is either detected (a reachable page's
    // checksum fails ⇒ reject) or ignored (an unreachable/free page ⇒ same data). It is
    // never accepted as a *different* reachable tree. (Meta-region flips are not tested
    // here: tearing the active meta legitimately recovers the previous committed state,
    // §6.3 — covered by the writer's crash-recovery tests.)
    let f = valid_file();
    let r0 = Reader::open(&f).unwrap();
    let mut base = Vec::new();
    r0.scan_v4(|a, b, sc| base.push((a.0, b.0, sc))).unwrap();

    let two = 2 * PAGE_SIZE;
    let mut s = 0xdead_beef_cafe_babeu64;
    for _ in 0..4000 {
        let pos = two + lcg(&mut s) as usize % (f.len() - two);
        let bit = (lcg(&mut s) & 7) as u8;
        let mut g = f.clone();
        g[pos] ^= 1 << bit;
        if let Ok(r) = Reader::open(&g) {
            if r.validate().is_ok() {
                let mut got = Vec::new();
                r.scan_v4(|a, b, sc| got.push((a.0, b.0, sc))).unwrap();
                assert_eq!(got, base, "accepted a corrupted reachable tree (pos {pos})");
            }
        }
    }
}

//
// The bit-flip / truncation fuzz above mutates bytes WITHOUT recomputing the page CRC, so
// corrupt pages are rejected at the CRC gate and the *structural* validator is never
// exercised on checksum-VALID hostile input — the actual threat model, and why F1/F2 slipped
// through review. These tests re-stamp the mutated page's CRC32C, reaching the structural
// validator with a checksum-valid-but-hostile file.

/// Recompute and store page `p`'s CRC32C (D9), so a structurally-mutated page passes the
/// checksum gate and reaches the structural validator (the F5 threat model).
fn restamp(file: &mut [u8], p: usize) {
    let base = p * PAGE_SIZE;
    let page = &mut file[base..base + PAGE_SIZE];
    let sum = crc32c::page_checksum(page);
    page[spec::PH_CHECKSUM..spec::PH_CHECKSUM + 8].copy_from_slice(&sum.to_le_bytes());
}

#[inline]
fn page_type(file: &[u8], p: usize) -> u8 {
    file[p * PAGE_SIZE + spec::PH_PAGE_TYPE]
}

#[inline]
fn entry_count(file: &[u8], p: usize) -> u16 {
    let o = p * PAGE_SIZE + spec::PH_ENTRY_COUNT;
    u16::from_le_bytes([file[o], file[o + 1]])
}

// =====================================================================================
// Targeted checksum-VALID structural rejection tests (SOW-0010). Each builds a VALID file
// with the writer, asserts it opens, mutates EXACTLY ONE field to violate EXACTLY ONE
// invariant, re-stamps the touched page (so the CRC gate passes and the structural
// validator is reached — except size-level corruptions), and asserts a typed `Err`. The
// "opens OK first" assert is the built-in non-vacuity guard: only the one corrupt field
// differs from a valid file.
// --- small byte helpers (LE reads / page locators / meta editors) ---

fn read_u32_at(file: &[u8], off: usize) -> u32 {
    u32::from_le_bytes([file[off], file[off + 1], file[off + 2], file[off + 3]])
}

fn read_u64_at(file: &[u8], off: usize) -> u64 {
    let mut b = [0u8; 8];
    b.copy_from_slice(&file[off..off + 8]);
    u64::from_le_bytes(b)
}

/// The active meta page (0 or 1): the checksum-valid candidate with the higher `txn_id`
/// (tie → pgno 0), mirroring `select_active_meta`.
fn active_meta_page(file: &[u8]) -> usize {
    if read_u64_at(file, PAGE_SIZE + spec::META_TXN_ID) > read_u64_at(file, spec::META_TXN_ID) {
        1
    } else {
        0
    }
}

/// `total_pages` from the active meta.
fn total_pages_of(file: &[u8]) -> u64 {
    let a = active_meta_page(file);
    read_u64_at(file, a * PAGE_SIZE + spec::META_TOTAL_PAGES)
}

/// Set a `u8`/`u16`/`u32`/`u64` field at byte offset `off` in BOTH meta pages and re-stamp
/// both CRCs (mirrors the Go meta-forging `for p in 0..2` pattern: whichever meta is active,
/// it now carries the forged field, and both stay checksum-valid).
fn set_meta_u8_both(file: &mut [u8], off: usize, v: u8) {
    file[off] = v;
    file[PAGE_SIZE + off] = v;
    restamp(file, 0);
    restamp(file, 1);
}
fn set_meta_u16_both(file: &mut [u8], off: usize, v: u16) {
    for p in 0..2 {
        let o = p * PAGE_SIZE + off;
        file[o..o + 2].copy_from_slice(&v.to_le_bytes());
    }
    restamp(file, 0);
    restamp(file, 1);
}
fn set_meta_u32_both(file: &mut [u8], off: usize, v: u32) {
    for p in 0..2 {
        let o = p * PAGE_SIZE + off;
        file[o..o + 4].copy_from_slice(&v.to_le_bytes());
    }
    restamp(file, 0);
    restamp(file, 1);
}
fn set_meta_u64_both(file: &mut [u8], off: usize, v: u64) {
    for p in 0..2 {
        let o = p * PAGE_SIZE + off;
        file[o..o + 8].copy_from_slice(&v.to_le_bytes());
    }
    restamp(file, 0);
    restamp(file, 1);
}

// --- IP-tree (§5) byte offsets (key_width 4: child[0] | sep,child pairs of 8 bytes) ---

/// Byte offset of branch page `p`'s child pgno `j` (IPv4 branch layout).
fn ip_child_off(p: usize, j: usize) -> usize {
    if j == 0 {
        p * PAGE_SIZE + spec::PAGE_HEADER_SIZE
    } else {
        p * PAGE_SIZE + spec::PAGE_HEADER_SIZE + 4 + (j - 1) * 8 + 4
    }
}
/// Byte offset of branch page `p`'s separator key `i` (IPv4 branch layout).
fn ip_sep_off(p: usize, i: usize) -> usize {
    p * PAGE_SIZE + spec::PAGE_HEADER_SIZE + 4 + i * 8
}

/// A clean, fully-reachable multi-level IPv4 tree (no deletes ⇒ no free pages): 2000 disjoint
/// records ⇒ ~5 leaves under one branch root (tree_height 2). Used by the TIER 1b IP-tree
/// rejection tests (locating a reachable branch/leaf by descending from the active root).
fn valid_ip_tree() -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..2000u32 {
        w.set(Ipv4Key(i * 7), Ipv4Key(i * 7 + 2), i & 0xff).unwrap();
    }
    w.commit(0, u64::MAX).unwrap();
    w.into_image().unwrap()
}

/// `(root_pgno, tree_height, total_pages)` of the active meta.
fn ip_geom(file: &[u8]) -> (usize, u32, u64) {
    let a = active_meta_page(file);
    let root = read_u32_at(file, a * PAGE_SIZE + spec::META_ROOT_PGNO) as usize;
    let height = read_u32_at(file, a * PAGE_SIZE + spec::META_TREE_HEIGHT);
    let total = read_u64_at(file, a * PAGE_SIZE + spec::META_TOTAL_PAGES);
    (root, height, total)
}

/// The IPv4 leaf record size for `scope_width == 1` (2·4 + 1).
const IP_RS: usize = 12; // v4.3: 2*4+4

// ---------------- TIER 1b: targeted IP-tree (§5/§9) rejection tests ----------------

#[test]
fn ip_branch_duplicate_child_rejected() {
    let mut file = valid_ip_tree();
    assert!(Reader::open(&file).is_ok());
    let (root, height, _t) = ip_geom(&file);
    assert_eq!(height, 2, "valid_ip_tree must be a 2-level tree");
    assert_eq!(page_type(&file, root), spec::PAGE_TYPE_BRANCH);
    assert!(entry_count(&file, root) >= 1);
    // child[1] := child[0] ⇒ the same subtree is reached twice (duplicate child pgno).
    let c0 = read_u32_at(&file, ip_child_off(root, 0));
    let o1 = ip_child_off(root, 1);
    file[o1..o1 + 4].copy_from_slice(&c0.to_le_bytes());
    restamp(&mut file, root);
    assert_reject!(&file, Error::Structural, "duplicate child pgno");
}

#[test]
fn ip_branch_separator_misroute_rejected() {
    let mut file = valid_ip_tree();
    assert!(Reader::open(&file).is_ok());
    let (root, _h, _t) = ip_geom(&file);
    // Shrink separator 0 to 1: child[0] (the leftmost leaf, whose first record is [0,2]) now
    // exceeds its separator-derived bound [MIN, 0], while sep0 = 1 stays > MIN and < sep1
    // (separators remain strictly increasing) — only the bound check can reject this.
    let o = ip_sep_off(root, 0);
    file[o..o + 4].copy_from_slice(&1u32.to_le_bytes());
    restamp(&mut file, root);
    // child[0]'s first record [0,2] now exceeds its separator-derived bound [MIN, 0].
    assert_reject!(&file, Error::Invariant, "record outside node bound");
}

#[test]
fn ip_leaf_record_to_lt_from_rejected() {
    let mut file = valid_ip_tree();
    assert!(Reader::open(&file).is_ok());
    let (root, _h, _t) = ip_geom(&file);
    let leaf = read_u32_at(&file, ip_child_off(root, 0)) as usize;
    assert_eq!(page_type(&file, leaf), spec::PAGE_TYPE_LEAF);
    // Record 1 (from >= 1): set its `to` below `from`.
    let rec1 = leaf * PAGE_SIZE + spec::PAGE_HEADER_SIZE + IP_RS;
    let from = read_u32_at(&file, rec1);
    assert!(from >= 1);
    file[rec1 + 4..rec1 + 8].copy_from_slice(&(from - 1).to_le_bytes());
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::Invariant, "record to < from");
}

#[test]
fn ip_cross_leaf_overlap_rejected() {
    let mut file = valid_ip_tree();
    assert!(Reader::open(&file).is_ok());
    let (root, _h, _t) = ip_geom(&file);
    // The second leaf's first `from` := 0 (<= the first leaf's last `to`) ⇒ cross-leaf overlap.
    let leaf2 = read_u32_at(&file, ip_child_off(root, 1)) as usize;
    assert_eq!(page_type(&file, leaf2), spec::PAGE_TYPE_LEAF);
    let rec0 = leaf2 * PAGE_SIZE + spec::PAGE_HEADER_SIZE;
    file[rec0..rec0 + 4].copy_from_slice(&0u32.to_le_bytes());
    restamp(&mut file, leaf2);
    // Pinning the message is what makes this non-vacuous: "record outside node bound" (a
    // neighbouring Invariant check) also rejects this file, so a class-only assertion would
    // pass even with the cross-leaf check deleted (see the SOW-0010 non-vacuity proof).
    assert_reject!(&file, Error::Invariant, "cross-leaf overlap");
}

#[test]
fn ip_branch_separators_not_increasing_rejected() {
    let mut file = valid_ip_tree();
    assert!(Reader::open(&file).is_ok());
    let (root, _h, _t) = ip_geom(&file);
    assert!(entry_count(&file, root) >= 2);
    // sep[1] := sep[0] ⇒ separators no longer strictly increasing.
    let s0 = read_u32_at(&file, ip_sep_off(root, 0));
    let o1 = ip_sep_off(root, 1);
    file[o1..o1 + 4].copy_from_slice(&s0.to_le_bytes());
    restamp(&mut file, root);
    assert_reject!(
        &file,
        Error::Invariant,
        "separators not strictly increasing"
    );
}

#[test]
fn ip_child_pgno_out_of_range_rejected() {
    let mut file = valid_ip_tree();
    assert!(Reader::open(&file).is_ok());
    let (root, _h, total) = ip_geom(&file);
    // child[0] := total_pages (>= total_pages ⇒ out of range).
    let o0 = ip_child_off(root, 0);
    file[o0..o0 + 4].copy_from_slice(&(total as u32).to_le_bytes());
    restamp(&mut file, root);
    assert_reject!(&file, Error::Structural, "child pgno out of range");
}

#[test]
fn ip_wrong_page_type_at_depth_rejected() {
    let mut file = valid_ip_tree();
    assert!(Reader::open(&file).is_ok());
    let (root, _h, _t) = ip_geom(&file);
    // A reachable leaf claims to be a branch ⇒ wrong page_type at tree_height.
    let leaf = read_u32_at(&file, ip_child_off(root, 0)) as usize;
    file[leaf * PAGE_SIZE + spec::PH_PAGE_TYPE] = spec::PAGE_TYPE_BRANCH;
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::Structural, "expected leaf at tree_height");
}

#[test]
fn ip_leaf_tail_nonzero_rejected() {
    let mut file = valid_ip_tree();
    assert!(Reader::open(&file).is_ok());
    let (root, _h, _t) = ip_geom(&file);
    let leaf = read_u32_at(&file, ip_child_off(root, 0)) as usize;
    // A nonzero byte in the leaf tail (after the records).
    file[leaf * PAGE_SIZE + PAGE_SIZE - 1] = 1;
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::NonZeroReserved, "leaf tail");
}

#[test]
fn ip_branch_tail_nonzero_rejected() {
    let mut file = valid_ip_tree();
    assert!(Reader::open(&file).is_ok());
    let (root, _h, _t) = ip_geom(&file);
    // A nonzero byte in the branch tail (after the separators/children).
    file[root * PAGE_SIZE + PAGE_SIZE - 1] = 1;
    restamp(&mut file, root);
    assert_reject!(&file, Error::NonZeroReserved, "branch tail");
}

#[test]
fn ip_page_header_reserved_nonzero_rejected() {
    let mut file = valid_ip_tree();
    assert!(Reader::open(&file).is_ok());
    let (root, _h, _t) = ip_geom(&file);
    file[root * PAGE_SIZE + spec::PH_RESERVED] = 1;
    restamp(&mut file, root);
    assert_reject!(&file, Error::NonZeroReserved, "page header reserved");
}

#[test]
fn ip_self_pgno_mismatch_rejected() {
    let mut file = valid_ip_tree();
    assert!(Reader::open(&file).is_ok());
    let (root, _h, _t) = ip_geom(&file);
    // The root's stored pgno no longer matches the pgno the walk descended to.
    let off = root * PAGE_SIZE + spec::PH_PGNO;
    file[off..off + 4].copy_from_slice(&((root as u32) + 1).to_le_bytes());
    restamp(&mut file, root);
    assert_reject!(&file, Error::Structural, "page self-pgno mismatch");
}

#[test]
fn ip_leaf_entry_count_out_of_range_rejected() {
    let mut file = valid_ip_tree();
    assert!(Reader::open(&file).is_ok());
    let (root, _h, _t) = ip_geom(&file);
    let leaf = read_u32_at(&file, ip_child_off(root, 0)) as usize;
    // entry_count > leaf_max (453 for record_size 12).
    let off = leaf * PAGE_SIZE + spec::PH_ENTRY_COUNT;
    file[off..off + 2].copy_from_slice(&1000u16.to_le_bytes());
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::Invariant, "leaf entry_count out of range");
}

#[test]
fn ip_branch_child_cycle_rejected() {
    let mut file = valid_ip_tree();
    assert!(Reader::open(&file).is_ok());
    let (root, _h, _t) = ip_geom(&file);
    // Point child[0] at the root itself: the walk re-enters the root at depth 2 (== tree_height)
    // where a leaf is required but a branch is found ⇒ rejected. (In a height-2 tree the cycle
    // is caught by the page-type-at-depth invariant rather than the depth counter; either way
    // the file is rejected, never a panic/loop/wrong answer.)
    let o0 = ip_child_off(root, 0);
    file[o0..o0 + 4].copy_from_slice(&(root as u32).to_le_bytes());
    restamp(&mut file, root);
    // Deliberately class-agnostic (not message-pinned): this is a loop/DoS-safety test, not a
    // single-named-check test. The depth-counter cycle defense ("path deeper than tree_height")
    // is genuinely unreachable here by a single-field mutation — "expected leaf at tree_height"
    // fires first in a height-2 tree — so any typed rejection (never a panic/loop) is the win.
    assert!(Reader::open(&file).and_then(|r| r.validate()).is_err());
}

// ---------------- TIER 2b: meta/bootstrap + geometry rejection tests ----------------
//
// Bootstrap class-2 checks fire inside `classify`, which `select_active_meta` runs on BOTH
// candidates with `?`, so forging the field on both metas (and re-stamping both) is rejected
// regardless of which is active. Geometry checks read the active meta in `open`. The scope-
// table / kv_root / file-size checks fire later in the §9 walk.

#[test]
fn meta_page_size_not_4096_rejected() {
    let mut file = valid_file();
    assert!(Reader::open(&file).is_ok());
    set_meta_u32_both(&mut file, spec::META_PAGE_SIZE, 8192);
    assert_reject!(&file, Error::Incompatible, "page_size");
}

#[test]
fn meta_checksum_algo_unknown_rejected() {
    let mut file = valid_file();
    assert!(Reader::open(&file).is_ok());
    set_meta_u8_both(&mut file, spec::META_CHECKSUM_ALGO, 7);
    assert_reject!(&file, Error::Incompatible, "checksum_algo");
}

#[test]
fn meta_unknown_flags_bit_rejected() {
    let mut file = valid_file();
    assert!(Reader::open(&file).is_ok());
    // Set a reserved high flags bit (the IPv4 file's flags are 0).
    set_meta_u8_both(&mut file, spec::META_FLAGS, 0x80);
    assert_reject!(&file, Error::Incompatible, "unknown flags bit");
}

#[test]
fn meta_key_width_vs_flags_rejected() {
    let mut file = valid_file();
    assert!(Reader::open(&file).is_ok());
    // key_width 16 while flags bit0 says IPv4 (expects 4).
    set_meta_u8_both(&mut file, spec::META_KEY_WIDTH, 16);
    assert_reject!(&file, Error::Structural, "key_width disagrees with flags");
}

#[test]
fn meta_record_size_mismatch_rejected() {
    let mut file = valid_file();
    assert!(Reader::open(&file).is_ok());
    // record_size != 2·key_width + 4 (12).
    set_meta_u32_both(&mut file, spec::META_RECORD_SIZE, 99);
    assert_reject!(&file, Error::Structural, "record_size mismatch");
}

#[test]
fn metas_disagree_static_identity_rejected() {
    let mut file = valid_file();
    assert!(Reader::open(&file).is_ok());
    // Edit a STATIC-IDENTITY field (created_unixtime, [42,50) ⊂ [16,50)) on the INACTIVE meta
    // only and re-stamp it: both metas stay checksum-valid but now disagree on static identity.
    let inactive = 1 - active_meta_page(&file);
    let off = inactive * PAGE_SIZE + spec::META_CREATED_UNIXTIME;
    file[off..off + 8].copy_from_slice(&0xDEAD_BEEFu64.to_le_bytes());
    restamp(&mut file, inactive);
    assert_reject!(
        &file,
        Error::Structural,
        "metas disagree on static identity"
    );
}

#[test]
fn meta_minor0_metasize_not_90_rejected() {
    let mut file = valid_file(); // v4.0 (minor 0)
    assert!(Reader::open(&file).is_ok());
    set_meta_u16_both(&mut file, spec::META_META_SIZE, 92);
    // Pin the value carried by BadMetaSize (the only non-string reject variant here): four
    // checks return BadMetaSize, so the value ties this to the minor-0 pin's input.
    match reject(&file) {
        Error::BadMetaSize(s) => assert_eq!(s, 92),
        e => panic!("expected BadMetaSize(92), got {e:?}"),
    }
}

#[test]
fn total_pages_out_of_range_rejected() {
    let mut file = valid_file();
    assert!(Reader::open(&file).is_ok());
    set_meta_u64_both(&mut file, spec::META_TOTAL_PAGES, 1);
    assert_reject!(&file, Error::Structural, "total_pages out of range");
}

#[test]
fn tree_height_gt_32_rejected() {
    let mut file = valid_file();
    assert!(Reader::open(&file).is_ok());
    set_meta_u32_both(&mut file, spec::META_TREE_HEIGHT, 33);
    assert_reject!(&file, Error::Structural, "tree_height > 32");
}

#[test]
fn root_pgno_out_of_range_rejected() {
    let mut file = valid_file();
    assert!(Reader::open(&file).is_ok());
    let total = total_pages_of(&file);
    set_meta_u32_both(&mut file, spec::META_ROOT_PGNO, total as u32);
    assert_reject!(&file, Error::Structural, "root_pgno out of range");
}

#[test]
fn file_size_not_page_multiple_rejected() {
    let mut file = valid_file();
    assert!(Reader::open(&file).is_ok());
    // Append one byte: the real size is no longer a page multiple (size-level; no restamp).
    file.push(0);
    // FileSizeMismatch carries struct fields, not a string, and is the sole producer of this
    // variant (reader.rs `have % page_size != 0`), so the variant alone is already non-vacuous.
    assert!(matches!(reject(&file), Error::FileSizeMismatch { .. }));
}

// =====================================================================================
// SOW-0010 FIX ROUND — gap-closing targeted CRC-valid rejection tests. The no-wrong-answer
// fuzz excludes data-authority bytes (KV entry headers, slot directories, scope record
// fields, overflow descriptors) because re-stamping them is a legitimately different file,
// not a wrong answer — so those structural-validity fields had no targeted coverage. Each
// test below builds a valid file, asserts it opens, mutates EXACTLY ONE such field, restamps,
// and asserts the EXACT message of the one check that must reject it.
// =====================================================================================

/// A clean, fully-reachable **3-level** IPv4 tree. `scope_width 255` ⇒ `record_size 263` ⇒
/// `leaf_max 15` (the IPv4 minimum), so a modest record count yields > `branch_max + 1` (510)
/// leaves and a `root branch → intermediate branches → leaves` shape — the only shape with a
/// **nested** branch whose inherited `hi` bound is below the family max (needed for the
/// `separator > hi` test, unreachable at the root where `hi == MAX`).
fn valid_ip_tree_h3() -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..200_000u32 {
        w.set(Ipv4Key(i * 4), Ipv4Key(i * 4 + 1), 0x5a5a5a5au32)
            .unwrap();
    }
    w.commit(0, u64::MAX).unwrap();
    let img = w.into_image().unwrap();
    assert_eq!(
        ip_geom(&img).1,
        3,
        "valid_ip_tree_h3 must be a 3-level tree"
    );
    img
}

// ---------------- TIER 7: meta / IP geometry ----------------

#[test]
fn empty_tree_record_count_nonzero() {
    let mut file = {
        let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
        w.commit(0, u64::MAX).unwrap();
        w.into_image().unwrap()
    };
    assert!(Reader::open(&file).is_ok(), "empty file must open");
    set_meta_u64_both(&mut file, spec::META_RECORD_COUNT, 5);
    assert_reject!(
        &file,
        Error::Invariant,
        "record_count nonzero for empty tree"
    );
}

#[test]
fn tree_height_root_pgno_inconsistent() {
    let mut file = valid_file(); // non-empty: tree_height != 0, root_pgno != 0
    assert!(Reader::open(&file).is_ok());
    // tree_height := 0 while root_pgno stays != 0 ⇒ the two disagree.
    set_meta_u32_both(&mut file, spec::META_TREE_HEIGHT, 0);
    assert_reject!(
        &file,
        Error::Structural,
        "tree_height/root_pgno inconsistent"
    );
}

#[test]
fn meta_size_outside_range_low() {
    let mut file = valid_file();
    assert!(Reader::open(&file).is_ok());
    // version_minor := 2 bypasses the minor-0/1 exact-size pins, isolating the generic
    // [90, page_size] bound check (otherwise the minor-0 pin would also return BadMetaSize,
    // making the test vacuous to that bound's removal).
    set_meta_u16_both(&mut file, spec::META_VERSION_MINOR, 2);
    set_meta_u16_both(&mut file, spec::META_META_SIZE, 89); // < 90
    match reject(&file) {
        Error::BadMetaSize(s) => assert_eq!(s, 89),
        e => panic!("expected BadMetaSize(89), got {e:?}"),
    }
}

#[test]
fn meta_size_outside_range_high() {
    let mut file = valid_file();
    assert!(Reader::open(&file).is_ok());
    set_meta_u16_both(&mut file, spec::META_VERSION_MINOR, 2);
    set_meta_u16_both(&mut file, spec::META_META_SIZE, 5000); // > page_size (4096)
    match reject(&file) {
        Error::BadMetaSize(s) => assert_eq!(s, 5000),
        e => panic!("expected BadMetaSize(5000), got {e:?}"),
    }
}

// ---------------- TIER 8: exact IP separator bounds ----------------

#[test]
fn ip_separator_le_lo() {
    let mut file = valid_ip_tree();
    assert!(Reader::open(&file).is_ok());
    let (root, _h, _t) = ip_geom(&file);
    // The root's lo bound is MIN (0); set sep[0] = 0 (<= lo). Distinct from the misroute test
    // (sep[0] = 1), which trips the child's node-bound check instead.
    let o = ip_sep_off(root, 0);
    file[o..o + 4].copy_from_slice(&0u32.to_le_bytes());
    restamp(&mut file, root);
    assert_reject!(&file, Error::Invariant, "separator <= lo");
}

#[test]
fn ip_separator_gt_hi() {
    let mut file = valid_ip_tree_h3();
    assert!(Reader::open(&file).is_ok());
    let (root, height, _t) = ip_geom(&file);
    assert!(height >= 3);
    // root.child(0) is a depth-2 branch whose inherited hi bound = root.sep[0] - 1 (< MAX).
    let nested = read_u32_at(&file, ip_child_off(root, 0)) as usize;
    assert_eq!(page_type(&file, nested), spec::PAGE_TYPE_BRANCH);
    let root_sep0 = read_u32_at(&file, ip_sep_off(root, 0));
    // Set the nested branch's sep[0] = root.sep[0] (> its hi = root.sep[0] - 1).
    let o = ip_sep_off(nested, 0);
    file[o..o + 4].copy_from_slice(&root_sep0.to_le_bytes());
    restamp(&mut file, nested);
    assert_reject!(&file, Error::Invariant, "separator > hi");
}

// =====================================================================================
// SOW-0010 SYSTEMATIC CLOSURE — dedicated exact-message tests for every remaining
// reachable validator rejection the prior rounds had only fuzz coverage for. Each builds a
// valid file, asserts it opens, mutates EXACTLY ONE field to violate EXACTLY ONE check, and
// asserts that check's EXACT typed message. CRC-restamped tests reach the structural
// validator; the ChecksumFailed tests deliberately do NOT restamp (they reject at the CRC
// gate, proving the reachable-page CRC path). See the SOW coverage matrix for the full
// rejection→test mapping and the documented unreachable-by-single-mutation rejections.
// =====================================================================================
// ---------------- TIER 9: IP-tree (§5/§9) remaining rejections ----------------

#[test]
fn ip_reachable_page_checksum_failed() {
    // Flip a byte in a REACHABLE leaf WITHOUT re-stamping ⇒ the §9 walk verifies the page CRC
    // and rejects. (No restamp: this is the CRC gate, not the structural validator.)
    let mut file = valid_ip_tree();
    assert!(Reader::open(&file).is_ok());
    let (root, _h, _t) = ip_geom(&file);
    let leaf = read_u32_at(&file, ip_child_off(root, 0)) as usize;
    file[leaf * PAGE_SIZE + spec::PAGE_HEADER_SIZE] ^= 0xFF; // a record byte
    assert_reject!(&file, Error::ChecksumFailed, "reachable page");
}

#[test]
fn ip_expected_branch_above_tree_height() {
    // A page at depth < tree_height claims to be a leaf ⇒ "expected branch above tree_height"
    // (the mirror of `ip_wrong_page_type_at_depth_rejected`, which sets a leaf to a branch).
    let mut file = valid_ip_tree();
    assert!(Reader::open(&file).is_ok());
    let (root, height, _t) = ip_geom(&file);
    assert_eq!(height, 2);
    file[root * PAGE_SIZE + spec::PH_PAGE_TYPE] = spec::PAGE_TYPE_LEAF;
    restamp(&mut file, root);
    assert_reject!(
        &file,
        Error::Structural,
        "expected branch above tree_height"
    );
}

#[test]
fn ip_branch_separator_count_out_of_range() {
    // entry_count > branch_max (509). The no-wrong-answer fuzz flips only the low byte of
    // entry_count, so it can never reach > 509; this sets it directly.
    let mut file = valid_ip_tree();
    assert!(Reader::open(&file).is_ok());
    let (root, _h, _t) = ip_geom(&file);
    assert_eq!(page_type(&file, root), spec::PAGE_TYPE_BRANCH);
    let off = root * PAGE_SIZE + spec::PH_ENTRY_COUNT;
    file[off..off + 2].copy_from_slice(&600u16.to_le_bytes());
    restamp(&mut file, root);
    assert_reject!(
        &file,
        Error::Invariant,
        "branch separator count out of range"
    );
}
