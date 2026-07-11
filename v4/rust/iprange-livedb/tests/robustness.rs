//! Hostile-input robustness (§9 / §10): `Reader::open` must return only `Ok` or a typed
//! `Err` — **never** panic, loop, or read out of bounds — on truncations, bit-flips, and
//! arbitrary buffers. (`Writer::open_image` runs the same validation, so this also
//! guards the writer's open path.)

use iprange_livedb::crc32c;
use iprange_livedb::spec::{self, PAGE_SIZE};
use iprange_livedb::{Error, Ipv4Key, MetaEntry, Reader, Writer};

/// A multi-level valid file with some freed (unreachable) pages, to exercise both the
/// reachable-page reject path and the unreachable-page ignore path.
fn valid_file() -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..2000u32 {
        w.set(Ipv4Key(i * 7), Ipv4Key(i * 7 + 2), i & 0xff)
            .unwrap();
    }
    for i in (0..2000u32).step_by(5) {
        w.delete(Ipv4Key(i * 7), Ipv4Key(i * 7 + 2)).unwrap(); // frees pages
    }
    w.commit(0).unwrap();
    w.into_image().unwrap()
}

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
/// A valid v4.1 file carrying the full metadata surface: an IP tree, a multi-level scope
/// table, per-scope KV (inline + a multi-page overflow chain), and FILE (scope 0) KV. Used
/// to fuzz the v4.1 validation paths (scope-table walk, KV slot directories, overflow
/// read-by-count) against truncations / bit-flips / arbitrary buffers.
fn valid_file_with_kv() -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..400u32 {
        w.set(Ipv4Key(i * 11), Ipv4Key(i * 11 + 3), i & 0xff)
            .unwrap();
    }
    // Many scopes -> a multi-level scope table; each with KV.
    for s in 0..40u32 {
        let id = w.scope_define(format!("scope-{s}").as_bytes()).unwrap();
        w.meta_set(id, b"license", 0, b"MIT").unwrap();
        w.meta_set(id, format!("note-{s}").as_bytes(), 0, b"text value")
            .unwrap();
    }
    // A large overflow-spanning value and a binary value on scope 1.
    let big: Vec<u8> = (0..9000u32).map(|i| (i * 7) as u8).collect();
    w.meta_set(1, b"blob", 9, &big).unwrap();
    // FILE (scope 0) dataset metadata.
    w.meta_set(0, b"dataset", 0, b"firehol").unwrap();
    w.commit(0).unwrap();
    w.into_image().unwrap()
}
*/

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

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn truncations_never_panic() {
    for f in [valid_file(), valid_file_with_kv()] {
        let two = 2 * PAGE_SIZE;
        for len in 0..f.len() {
            // every byte through the meta region (where bootstrap is most fragile), strided
            // beyond — opening any prefix must never panic.
            if len < two || len % 37 == 0 {
                let _ = Reader::open(&f[..len]);
            }
        }
        let _ = Reader::open(&f);
    }
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn single_bit_flips_never_panic() {
    let mut s = 0x9e37_79b9_7f4a_7c15u64;
    for f in [valid_file(), valid_file_with_kv()] {
        for _ in 0..5000 {
            let pos = lcg(&mut s) as usize % f.len();
            let bit = (lcg(&mut s) & 7) as u8;
            let mut g = f.clone();
            g[pos] ^= 1 << bit;
            let _ = Reader::open(&g);
            let _ = std::panic::catch_unwind(|| Writer::<Ipv4Key>::open_image(g));
        }
    }
}
*/

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
    r0.scan_v4(|a, b, sc| base.push((a.0, b.0, sc)))
        .unwrap();

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
                r.scan_v4(|a, b, sc| got.push((a.0, b.0, sc)))
                    .unwrap();
                assert_eq!(got, base, "accepted a corrupted reachable tree (pos {pos})");
            }
        }
    }
}

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_region_flip_never_silently_accepted() {
    // A bit flip anywhere in a v4.1 file (scope table, KV leaves/branches, overflow pages)
    // is either detected (a reachable page's checksum/structure fails ⇒ reject) or ignored
    // (an unreachable/free page). It is never accepted as a *different* valid metadata view.
    // Read the canonical metadata of the pristine file, then assert every accepted reopen
    // returns byte-identical IP records + per-scope KV.
    let f = valid_file_with_kv();
    let mut ip0 = Vec::new();
    {
        let r0 = Reader::open(&f).unwrap();
        r0.scan_v4(|a, b, sc| ip0.push((a.0, b.0, sc)))
            .unwrap();
    }
    // Re-derive the metadata via the writer (it exposes the KV/registry API).
    let (scopes0, kv0) = {
        let w0 = Writer::<Ipv4Key>::open_image(f.clone()).unwrap();
        let scopes0 = w0.scope_list();
        let mut kv0 = Vec::new();
        for (id, _) in w0
            .scope_list()
            .iter()
            .chain(core::iter::once(&(0u32, vec![])))
        {
            kv0.push((*id, w0.meta_list(*id).unwrap()));
        }
        (scopes0, kv0)
    };

    let two = 2 * PAGE_SIZE;
    let mut s = 0x0bad_f00d_1234_5678u64;
    for _ in 0..6000 {
        let pos = two + lcg(&mut s) as usize % (f.len() - two);
        let bit = (lcg(&mut s) & 7) as u8;
        let mut g = f.clone();
        g[pos] ^= 1 << bit;
        // Reader: any accepted reopen must yield the identical IP tree.
        if let Ok(r) = Reader::open(&g) {
            if r.validate().is_ok() {
                let mut ip = Vec::new();
                r.scan_v4(|a, b, sc| ip.push((a.0, b.0, sc)))
                    .unwrap();
                assert_eq!(
                    ip, ip0,
                    "accepted a corrupted reachable IP tree (pos {pos})"
                );
            }
        }
        // Writer (runs the same validation, then exposes KV): any accepted reopen must
        // yield identical scopes + KV.
        if let Ok(w) = Writer::<Ipv4Key>::open_image(g) {
            if w.validate().is_ok() {
                assert_eq!(w.scope_list(), scopes0, "scope list drifted (pos {pos})");
                for (id, want) in &kv0 {
                    assert_eq!(
                        &w.meta_list(*id).unwrap(),
                        want,
                        "kv drifted id {id} (pos {pos})"
                    );
                }
            }
        }
    }
}
*/
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

/// Byte offset of KV leaf/branch slot `i` (the entry's start within page `p`). The KV leaf
/// directory begins right after the header; the KV branch directory begins after the header
/// **and** the fixed leftmost-child `u32`.
fn kv_slot_off(file: &[u8], p: usize, i: usize, branch: bool) -> usize {
    let dir = spec::PAGE_HEADER_SIZE + if branch { 4 } else { 0 };
    let so = p * PAGE_SIZE + dir + i * spec::KV_SLOT_SIZE;
    let entry = u16::from_le_bytes([file[so], file[so + 1]]) as usize;
    p * PAGE_SIZE + entry
}

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn structural_mutation_fuzz_recrc_never_panics() {
    // Mutate REDUNDANT structural bytes of a REACHABLE page (any page type, IP tree INCLUDED),
    // RE-STAMP the page CRC (so it clears the checksum gate and reaches the structural
    // validator — the F5 threat model), then open. Two guarantees:
    //   1. open is total — `Reader::open`/`Writer::open_image` return only `Ok` or a typed
    //      `Err`, never a panic / OOB / loop;
    //   2. no silent wrong answer (§9) — against a baseline view captured once, every accepted
    //      reopen returns a byte-identical view (reject OR identical, never a different valid
    //      answer).
    // Only REDUNDANT structural fields are perturbed (see `structural_offsets`): data-authority
    // bytes (records / keys / values / scope names) are excluded — re-stamping those is a
    // legitimately different file, not a wrong answer (those bytes are covered for panic-safety
    // by the CRC-gated flip fuzzes above). KV leaf/branch entry_count IS now included: the
    // SOW-0010 canonical-packing check makes a shrunk count reject (the dropped slot/entry bytes
    // land in the no-longer-zero free gap), so perturbing it must reject-or-be-identical too.
    // `valid_ip_tree` supplies IP branch + leaf pages;
    // `valid_file_with_kv` supplies the scope table, KV leaves, and overflow chains.
    fuzz_structural_mutations(valid_ip_tree());
    fuzz_structural_mutations(valid_file_with_kv());
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
/// The full observable view of an opened image: in-order IP records, the scope list, and every
/// target's ordered KV (FILE first, then each scope in `scope_list` order). The "answer" a
/// reader returns; the no-wrong-answer fuzz asserts an accepted reopen returns this exact view.
#[allow(clippy::type_complexity)]
fn capture_view(
    r: &Reader,
) -> (
    Vec<(u32, u32, Vec<u8>)>,
    Vec<(u32, Vec<u8>)>,
    Vec<Vec<MetaEntry>>,
) {
    let mut ip = Vec::new();
    r.scan_v4(|a, b, sc| ip.push((a.0, b.0, sc)))
        .unwrap();
    let scopes = r.scope_list();
    let mut metas = Vec::new();
    metas.push(r.meta_list(spec::FILE_SCOPE_ID).unwrap_or_default());
    for (id, _) in 0x5a5a5a5au32s {
        metas.push(r.meta_list(*id).unwrap_or_default());
    }
    (ip, scopes, metas)
}
*/

/// Every page the validators actually walk (IP tree, scope table, per-scope KV trees, overflow
/// chains): exactly the pages whose CRC `Reader::open` verifies, so a page is reachable iff
/// breaking its CRC (without re-stamping) turns an `Ok` open into an `Err`. Fuzzing only these
/// keeps the cost bounded — a freshly built file has thousands of free pages (whose mutation
/// the bit-flip fuzz already covers) that retain stale page-type bytes.
fn reachable_pages(base: &mut [u8]) -> Vec<usize> {
    let total = base.len() / PAGE_SIZE;
    let mut out = Vec::new();
    for p in 2..total {
        let off = p * PAGE_SIZE + spec::PH_CHECKSUM;
        let orig = base[off];
        base[off] ^= 0xFF; // break this page's stored CRC (no re-stamp)
        if Reader::open(base).and_then(|r| r.validate()).is_err() {
            out.push(p); // the reader verified (and rejected) this page ⇒ reachable
        }
        base[off] = orig; // restore
    }
    out
}

/// Byte offsets (within a page) of REDUNDANT structural fields for page type `pt` — fields the
/// validator cross-checks, so a re-CRC'd mutation there must be REJECTED or be a no-op, never a
/// different valid view. Data-authority bytes are deliberately excluded (record / key / value /
/// name bytes). entry_count is redundant for EVERY tree page: IP/scope leaf+branch enforce a
/// tail-zero region, and (since SOW-0010) KV leaf/branch enforce canonical packing — so a wrong
/// count is always rejected, and entry_count is included for all four. Mirrors `structuralOffsets`.
fn structural_offsets(pt: u8) -> Vec<usize> {
    // KV branch slot directory starts after the header + the fixed leftmost-child u32.
    const KV_BRANCH_DIR_START: usize = spec::PAGE_HEADER_SIZE + 4;
    let mut h = vec![spec::PH_PAGE_TYPE, spec::PH_RESERVED, spec::PH_PGNO];
    match pt {
        spec::PAGE_TYPE_LEAF | spec::PAGE_TYPE_SCOPE_LEAF => h.push(spec::PH_ENTRY_COUNT),
        spec::PAGE_TYPE_BRANCH | spec::PAGE_TYPE_SCOPE_BRANCH => {
            // IPv4-layout branch: count (tail-checked) + child[0] / sep[0] / child[1].
            h.extend([
                spec::PH_ENTRY_COUNT,
                spec::PAGE_HEADER_SIZE,     // child[0]
                spec::PAGE_HEADER_SIZE + 4, // sep[0]
                spec::PAGE_HEADER_SIZE + 8, // child[1]
            ]);
        }
        spec::PAGE_TYPE_KV_BRANCH => {
            // entry_count (now redundant: the canonical-packing check rejects a shrunk count via
            // the non-zero free gap) + leftmost child + the first two slot-directory bytes.
            h.extend([
                spec::PH_ENTRY_COUNT,
                spec::PAGE_HEADER_SIZE, // leftmost child
                KV_BRANCH_DIR_START,
                KV_BRANCH_DIR_START + 2,
            ]);
        }
        spec::PAGE_TYPE_KV_LEAF => {
            // entry_count (now redundant via the canonical-packing free-gap/tiling check) + the
            // slot directory (front) — repointing a slot is rejected (key order/bounds).
            h.extend([
                spec::PH_ENTRY_COUNT,
                spec::PAGE_HEADER_SIZE,
                spec::PAGE_HEADER_SIZE + 2,
            ]);
        }
        spec::PAGE_TYPE_OVERFLOW => {
            // count is unused on overflow pages; next_pgno is cross-checked by read-by-count.
            h.extend([
                spec::PH_ENTRY_COUNT,
                spec::OVERFLOW_NEXT_PGNO,
                spec::OVERFLOW_NEXT_PGNO + 1,
            ]);
        }
        _ => {}
    }
    h
}

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
fn fuzz_structural_mutations(mut base: Vec<u8>) {
    let want = capture_view(&Reader::open(&base).unwrap()); // pristine baseline
    let reachable = reachable_pages(&mut base);
    let mut s = 0xa5a5_f00d_1234_5678u64;
    for p in reachable {
        let pt = page_type(&base, p);
        let offsets = structural_offsets(pt);
        for _ in 0..60 {
            let mut g = base.clone();
            let bp = p * PAGE_SIZE;
            // Apply 1-3 redundant-structural byte mutations.
            for _ in 0..1 + (lcg(&mut s) % 3) as usize {
                let off = offsets[lcg(&mut s) as usize % offsets.len()];
                g[bp + off] ^= (1 + lcg(&mut s) % 255) as u8;
            }
            restamp(&mut g, p); // this page now passes the CRC gate
            if let Ok(r) = Reader::open(&g) {
                if r.validate().is_ok() {
                    // Accepted ⇒ the view MUST equal the baseline (redundant mutation is a no-op or
                    // rejected; never a different valid answer, §9).
                    assert_eq!(
                        capture_view(&r),
                        want,
                        "page {p} (type {pt}): accepted a corrupted image with a different view"
                    );
                }
            }
            // Writer trusts the file (no validation); a corrupt image may panic internally.
            let _ = std::panic::catch_unwind(|| Writer::<Ipv4Key>::open_image(g));
        }
    }
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
/// A v4.1 file with TWO overflow-backed KV entries on one scope, so the shared-chain PoC can
/// repoint one entry's chain head at the other's. Small keys + large values ⇒ both overflow
/// descriptors land in a single KV leaf.
fn file_two_overflow_entries() -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    let id = w.scope_define(b"s").unwrap();
    let a: Vec<u8> = (0..6000u32).map(|i| (i * 3) as u8).collect();
    let b: Vec<u8> = (0..6000u32).map(|i| (i * 5 + 1) as u8).collect();
    w.meta_set(id, b"a", 9, &a).unwrap();
    w.meta_set(id, b"b", 9, &b).unwrap();
    w.commit(0).unwrap();
    w.into_image().unwrap()
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn shared_overflow_chain_rejected() {
    // The glm PoC: two KV leaf entries pointing their overflow chains at the SAME pages. A
    // checksum-valid file that, before F2, accepted it and returned a WRONG ANSWER. After F2
    // the file-wide page-disjointness walk rejects it.
    let mut file = file_two_overflow_entries();
    let total = file.len() / PAGE_SIZE;
    // Find the KV leaf (page_type 7) holding the two overflow entries.
    let leaf = (2..total)
        .find(|&p| page_type(&file, p) == spec::PAGE_TYPE_KV_LEAF && entry_count(&file, p) == 2)
        .expect("a 2-entry KV leaf");

    // Each entry: key_len(u16) | key | type(u32) | value_kind(u8) | first_pgno(u32) |
    // total_len(u64). Read entry 0's (first_pgno, total_len), then overwrite entry 1's with
    // them so both reference entry 0's chain.
    let read_overflow_desc = |file: &[u8], i: usize| -> (usize, u32, u64) {
        let mut off = kv_slot_off(file, leaf, i, false);
        let key_len = u16::from_le_bytes([file[off], file[off + 1]]) as usize;
        off += 2 + key_len + 4; // skip key_len, key, type
        assert_eq!(file[off], spec::KV_VALUE_OVERFLOW, "entry {i} is overflow");
        off += 1; // skip value_kind
        let first = u32::from_le_bytes([file[off], file[off + 1], file[off + 2], file[off + 3]]);
        let mut tl = [0u8; 8];
        tl.copy_from_slice(&file[off + 4..off + 12]);
        (off, first, u64::from_le_bytes(tl))
    };

    let (_o0, first0, total0) = read_overflow_desc(&file, 0);
    let (o1, first1, _total1) = read_overflow_desc(&file, 1);
    assert_ne!(first0, first1, "the two entries start at distinct chains");
    // Point entry 1 at entry 0's chain (head + length) — now two entries alias one chain.
    file[o1..o1 + 4].copy_from_slice(&first0.to_le_bytes());
    file[o1 + 4..o1 + 12].copy_from_slice(&total0.to_le_bytes());
    restamp(&mut file, leaf);

    // Must be rejected — never an Ok that returns a wrong answer. Entry 0's chain is walked
    // first (marks its pages in the shared `visited` bitset); entry 1 then re-enters the same
    // first page → the read-by-count revisit guard fires.
    assert_reject!(
        &file,
        Error::Structural,
        "kv overflow chain revisits a page"
    );
    assert!(Reader::open(&file).and_then(|r| r.validate()).is_err());
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn duplicate_child_in_scope_branch_rejected() {
    // A scope-table branch (IPv4-branch layout) with two child pgnos pointing at the same
    // page. The file-wide disjointness walk reaches that page twice ⇒ reject (F2).
    // Many scopes force a multi-level scope table (a branch root).
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for s in 0..40u32 {
        w.scope_define(format!("scope-{s}").as_bytes()).unwrap();
    }
    w.commit(0).unwrap();
    let mut file = w.into_image().unwrap();
    let total = file.len() / PAGE_SIZE;

    let branch = (2..total)
        .find(|&p| {
            page_type(&file, p) == spec::PAGE_TYPE_SCOPE_BRANCH && entry_count(&file, p) >= 1
        })
        .expect("a scope branch with >= 1 separator");
    // Layout: child[0] at +16; child[1] at +16+4+SCOPE_KEY_WIDTH. Set child[1] = child[0].
    let bp = branch * PAGE_SIZE;
    let c0_off = bp + spec::PAGE_HEADER_SIZE;
    let c1_off = c0_off + 4 + spec::SCOPE_KEY_WIDTH;
    let c0 = [
        file[c0_off],
        file[c0_off + 1],
        file[c0_off + 2],
        file[c0_off + 3],
    ];
    file[c1_off..c1_off + 4].copy_from_slice(&c0);
    restamp(&mut file, branch);

    // The file-wide disjointness bitset reaches child[1] (== child[0]) a second time.
    assert_reject!(
        &file,
        Error::Structural,
        "scope page reached twice (aliased)"
    );
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn duplicate_child_in_kv_branch_rejected() {
    // A KV branch with two children at the same pgno. The file-wide disjointness walk reaches
    // that subtree twice ⇒ reject (F2). Many small KV entries force a multi-level KV tree.
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    let id = w.scope_define(b"s").unwrap();
    // Long keys keep the leaf fanout low so the tree branches with a modest entry count.
    for i in 0..400u32 {
        let key = format!("key-{i:08}-{}", "x".repeat(200));
        w.meta_set(id, key.as_bytes(), 0, b"v").unwrap();
    }
    w.commit(0).unwrap();
    let mut file = w.into_image().unwrap();
    let total = file.len() / PAGE_SIZE;

    let branch = (2..total)
        .find(|&p| page_type(&file, p) == spec::PAGE_TYPE_KV_BRANCH && entry_count(&file, p) >= 1)
        .expect("a KV branch with >= 1 separator");
    // child[0] is the fixed leftmost u32 at +16; child[1] is the trailing u32 of separator 0's
    // heap entry. Alias child[1] := child[0] (leftmost) — the SAME direction as the scope-branch
    // sibling. The walk validates child[0] correctly first (its keys fit [b"", sep[0])), marking
    // its subtree in the shared bitset; then child[1] (now the same page) re-enters it → the
    // disjointness check fires. (The reverse direction — leftmost := sep0's child — would instead
    // trip the separator-bound misroute check at child[0], which `kv_branch_separator_misroute`
    // already covers; this test must reach F2.)
    let bp = branch * PAGE_SIZE;
    let leftmost_off = bp + spec::PAGE_HEADER_SIZE;
    let leftmost = [
        file[leftmost_off],
        file[leftmost_off + 1],
        file[leftmost_off + 2],
        file[leftmost_off + 3],
    ];
    let mut sep_off = kv_slot_off(&file, branch, 0, true);
    let sep_len = u16::from_le_bytes([file[sep_off], file[sep_off + 1]]) as usize;
    sep_off += 2 + sep_len; // skip sep_len + sep_key → child pgno (u32)
    file[sep_off..sep_off + 4].copy_from_slice(&leftmost);
    restamp(&mut file, branch);

    // The file-wide disjointness bitset reaches the aliased child a second time.
    assert_reject!(&file, Error::Structural, "kv page reached twice (aliased)");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn scope_branch_separator_misroute_rejected() {
    // codex Finding 3: a scope-table branch whose separator no longer matches the child
    // boundaries. Separators stay strictly increasing and every CRC is valid, but a lookup
    // would misroute. The validator must confine each child's ids to its separator-derived
    // bound and reject (parity with the IP-tree validator).
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for s in 0..40u32 {
        w.scope_define(format!("scope-{s}").as_bytes()).unwrap();
    }
    w.commit(0).unwrap();
    let mut file = w.into_image().unwrap();
    let total = file.len() / PAGE_SIZE;

    let branch = (2..total)
        .find(|&p| {
            page_type(&file, p) == spec::PAGE_TYPE_SCOPE_BRANCH && entry_count(&file, p) >= 1
        })
        .expect("a scope branch with >= 1 separator");
    // Shrink separator 0 to id 1: child[0] (the leftmost leaf, ids >= 1) now exceeds its
    // bound [lo, sep0-1] = [0, 0], yet sep0 = 1 is still > lo and < sep1, so the only failing
    // check is the new separator-derived bound check (the old validator accepted this).
    let sep0_off = branch * PAGE_SIZE + spec::PAGE_HEADER_SIZE + 4; // after child[0] (u32)
    file[sep0_off..sep0_off + 4].copy_from_slice(&1u32.to_le_bytes());
    restamp(&mut file, branch);

    // child[0] (ids >= 1) now exceeds its separator-derived bound [0, 0].
    assert_reject!(&file, Error::Invariant, "scope id outside node bound");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_branch_separator_misroute_rejected() {
    // codex Finding 3 (KV side): a KV branch whose separator key is shifted below its child's
    // real key range. Separators stay strictly increasing and CRCs are valid, but child[0]'s
    // keys would now fall at/above the (shrunken) separator → misroute. Must be rejected.
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    let id = w.scope_define(b"s").unwrap();
    for i in 0..400u32 {
        let key = format!("key-{i:08}-{}", "x".repeat(200));
        w.meta_set(id, key.as_bytes(), 0, b"v").unwrap();
    }
    w.commit(0).unwrap();
    let mut file = w.into_image().unwrap();
    let total = file.len() / PAGE_SIZE;

    let branch = (2..total)
        .find(|&p| page_type(&file, p) == spec::PAGE_TYPE_KV_BRANCH && entry_count(&file, p) >= 1)
        .expect("a KV branch with >= 1 separator");
    // Decrement the first byte of separator 0's key ("key-…" → "jey-…"): still a valid,
    // strictly-smaller key (so separators stay increasing), but now below every key in
    // child[0], so child[0]'s keys fall at/above the node's upper bound → reject.
    let sep_off = kv_slot_off(&file, branch, 0, true);
    let key_off = sep_off + 2; // skip sep_len (u16)
    file[key_off] -= 1;
    restamp(&mut file, branch);

    // child[0]'s keys now fall at/above the (shrunken) separator that bounds its interval.
    assert_reject!(&file, Error::Invariant, "kv key at/above node bound");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn overflow_total_len_u64_max_rejected() {
    // Patch a KV overflow entry's value_total_len to u64::MAX: a naive div_ceil
    // `(total+payload-1)/payload` wraps to a tiny page count, slips past the chain-length cap,
    // and returns a truncated value on a checksum-valid file. The overflow-safe div_ceil must
    // reject it. Mirrors the Go robustness test (cross-language coverage parity).
    let mut file = {
        let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
        let id = w.scope_define(b"s").unwrap();
        let big: Vec<u8> = (0..6000u32).map(|i| (i * 3) as u8).collect();
        w.meta_set(id, b"k", 9, &big).unwrap();
        w.commit(0).unwrap();
        w.into_image().unwrap()
    };
    assert!(Reader::open(&file).is_ok(), "valid overflow file rejected");
    let total = file.len() / PAGE_SIZE;
    let leaf = (2..total)
        .find(|&p| page_type(&file, p) == spec::PAGE_TYPE_KV_LEAF)
        .expect("a KV leaf");
    // Entry 0 layout: key_len(2) · key · type(4) · value_kind(1) · first_pgno(4) · total_len(8).
    let e = kv_slot_off(&file, leaf, 0, false);
    let key_len = u16::from_le_bytes([file[e], file[e + 1]]) as usize;
    let kind_off = e + 2 + key_len + 4;
    assert_eq!(
        file[kind_off],
        spec::KV_VALUE_OVERFLOW,
        "entry 0 must be an overflow entry"
    );
    let total_len_off = kind_off + 1 + 4; // skip value_kind(1) + first_pgno(4)
    file[total_len_off..total_len_off + 8].copy_from_slice(&u64::MAX.to_le_bytes());
    restamp(&mut file, leaf);
    // div_ceil(u64::MAX, payload) is a page budget far larger than the file → reject.
    assert_reject!(
        &file,
        Error::Structural,
        "kv overflow chain longer than file"
    );
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn shared_kv_root_across_scopes_rejected() {
    // Two scope records sharing one kv_root ⇒ the file-wide page-disjointness walk reaches the
    // same KV subtree twice ⇒ structural error (F2). Mirrors the Go robustness test.
    let mut file = {
        let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
        let a = w.scope_define(b"a").unwrap();
        let b = w.scope_define(b"b").unwrap();
        w.meta_set(a, b"ka", 0, b"va").unwrap();
        w.meta_set(b, b"kb", 0, b"vb").unwrap();
        w.commit(0).unwrap();
        w.into_image().unwrap()
    };
    assert!(
        Reader::open(&file).is_ok(),
        "valid two-scope-KV file rejected"
    );
    let total = file.len() / PAGE_SIZE;
    let leaf = (2..total)
        .find(|&p| page_type(&file, p) == spec::PAGE_TYPE_SCOPE_LEAF)
        .expect("a scope leaf");
    // Records are sorted by id; scope a (id 1) is record 0, scope b (id 2) is record 1.
    let rec = |i: usize| leaf * PAGE_SIZE + spec::PAGE_HEADER_SIZE + i * spec::SCOPE_RECORD_SIZE;
    let id0 = u32::from_le_bytes([
        file[rec(0)],
        file[rec(0) + 1],
        file[rec(0) + 2],
        file[rec(0) + 3],
    ]);
    let id1 = u32::from_le_bytes([
        file[rec(1)],
        file[rec(1) + 1],
        file[rec(1) + 2],
        file[rec(1) + 3],
    ]);
    assert!(id0 < id1, "scope records sorted by id (got {id0}, {id1})");
    // Copy record 0's kv_root into record 1 — both records now point at the same KV tree.
    let r0 = rec(0) + spec::SCOPE_REC_KV_ROOT;
    let r1 = rec(1) + spec::SCOPE_REC_KV_ROOT;
    let root0 = [file[r0], file[r0 + 1], file[r0 + 2], file[r0 + 3]];
    file[r1..r1 + 4].copy_from_slice(&root0);
    restamp(&mut file, leaf);
    // The second scope's kv_root re-enters the first scope's already-walked KV tree.
    assert_reject!(&file, Error::Structural, "kv page reached twice (aliased)");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn f1_lone_last_child_kv_round_trips() {
    // F1: build a KV tree whose branch level would (pre-fix) leave a final node with a single
    // child. ~1 KiB keys ⇒ low branch fanout, so a careful entry count forces the remainder-1
    // case. The writer must rebalance the last two nodes; the file must open + read back.
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    let id = w.scope_define(b"s").unwrap();
    // 1024-byte keys: branch fanout ≈ 4; sweep enough counts to hit the remainder-1 boundary
    // at several levels.
    let mut expect: Vec<(Vec<u8>, u32, Vec<u8>)> = Vec::new();
    for i in 0..600u32 {
        // Distinct, sorted, ~1 KiB UTF-8 keys.
        let key = format!("{i:06}-{}", "k".repeat(spec::KV_KEY_MAX - 7));
        let kb = key.into_bytes();
        w.meta_set(id, &kb, 0, b"x").unwrap();
        expect.push((kb, 0u32, b"x".to_vec()));
    }
    expect.sort_by(|a, b| a.0.cmp(&b.0));
    w.commit(0).unwrap();
    let img = w.into_image().unwrap();

    // Opens (full structural validation, incl. F2 disjointness + branch count >= 1) and the
    // KV list round-trips exactly.
    let r = Reader::open(&img).expect("F1 large-key KV file must open");
    assert_eq!(r.meta_list(id).unwrap(), expect);
    let w2 = Writer::<Ipv4Key>::open_image(img).unwrap();
    assert_eq!(w2.meta_list(id).unwrap(), expect);
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn f1_lone_last_child_scopes_round_trips() {
    // F1 for the scope table: enough scopes that a branch level hits remainder 1 (so the
    // final branch node would be a lone child pre-fix). scope_leaf_max * K + 1 across a sweep.
    let leaf_max = spec::scope_leaf_max();
    // Children per scope branch node.
    let fanout = spec::scope_branch_max() + 1;
    // `fanout * leaf_max + 1` scopes ⇒ `fanout + 1` leaves ⇒ the branch level has the exact
    // remainder-1 children count (`fanout + 1`) that pre-fix left a lone last child. The
    // smaller counts exercise the two-leaf and three-leaf branch builds.
    let remainder_one = fanout * leaf_max + 1;
    for &n in &[leaf_max + 1, leaf_max * 2 + 1, remainder_one] {
        let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
        let mut ids = Vec::new();
        for s in 0..n as u32 {
            ids.push(w.scope_define(format!("scope-{s}").as_bytes()).unwrap());
        }
        w.commit(0).unwrap();
        let img = w.into_image().unwrap();

        let r = Reader::open(&img).unwrap_or_else(|e| panic!("F1 scopes n={n} must open: {e}"));
        let list = r.scope_list();
        assert_eq!(list.len(), n, "scope count n={n}");
        // Ascending by id, names intact.
        for (k, (id, name)) in list.iter().enumerate() {
            assert_eq!(*id, ids[k]);
            assert_eq!(name, format!("scope-{k}").as_bytes());
        }
    }
}
*/

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

/// The first page (pgno >= 2) of the given `page_type`.
fn find_page(file: &[u8], pt: u8) -> usize {
    let total = file.len() / PAGE_SIZE;
    (2..total)
        .find(|&p| page_type(file, p) == pt)
        .expect("a page of the requested type")
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

/// Set a `u16`/`u32` field at `off` in the ACTIVE meta only and re-stamp it. Used for v4.1-only
/// meta fields whose offset lies in the v4.0 meta's reserved tail (e.g. `scope_table_root` at
/// 90): the inactive meta of a just-upgraded file is still v4.0, so editing it there would
/// trip the meta-tail-zero check instead of the intended invariant.
fn set_active_meta_u16(file: &mut [u8], off: usize, v: u16) {
    let a = active_meta_page(file);
    let o = a * PAGE_SIZE + off;
    file[o..o + 2].copy_from_slice(&v.to_le_bytes());
    restamp(file, a);
}
fn set_active_meta_u32(file: &mut [u8], off: usize, v: u32) {
    let a = active_meta_page(file);
    let o = a * PAGE_SIZE + off;
    file[o..o + 4].copy_from_slice(&v.to_le_bytes());
    restamp(file, a);
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
        w.set(Ipv4Key(i * 7), Ipv4Key(i * 7 + 2), i & 0xff)
            .unwrap();
    }
    w.commit(0).unwrap();
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

// ---------------- TIER 2a: hostile non-UTF-8 / NUL on the read/validate path ----------------

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
fn file_named_scope() -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    w.scope_define(b"scope-x").unwrap();
    w.commit(0).unwrap();
    w.into_image().unwrap()
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
fn file_inline_text_kv() -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    let id = w.scope_define(b"s").unwrap();
    w.meta_set(id, b"k", spec::KV_TYPE_TEXT, b"hello").unwrap();
    w.commit(0).unwrap();
    w.into_image().unwrap()
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
fn file_overflow_text_kv() -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    let id = w.scope_define(b"s").unwrap();
    // ASCII text larger than the inline cap ⇒ a real (multi-page) overflow chain.
    let big = vec![b'a'; spec::KV_INLINE_MAX + spec::OVERFLOW_PAYLOAD + 50];
    w.meta_set(id, b"k", spec::KV_TYPE_TEXT, &big).unwrap();
    w.commit(0).unwrap();
    w.into_image().unwrap()
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn scope_name_non_utf8_rejected() {
    let mut file = file_named_scope();
    assert!(Reader::open(&file).is_ok());
    let leaf = find_page(&file, spec::PAGE_TYPE_SCOPE_LEAF);
    let base = leaf * PAGE_SIZE + spec::PAGE_HEADER_SIZE; // record 0
    let name_len = u16::from_le_bytes([
        file[base + spec::SCOPE_REC_NAME_LEN],
        file[base + spec::SCOPE_REC_NAME_LEN + 1],
    ]) as usize;
    assert!(name_len >= 1);
    // 0xFF is never a valid UTF-8 byte ⇒ the name within name_len is no longer valid UTF-8.
    file[base + spec::SCOPE_REC_NAME] = 0xFF;
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::Invariant, "scope name not valid UTF-8");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_inline_text_value_non_utf8_rejected() {
    let mut file = file_inline_text_kv();
    assert!(Reader::open(&file).is_ok());
    let leaf = find_page(&file, spec::PAGE_TYPE_KV_LEAF);
    let e = kv_slot_off(&file, leaf, 0, false);
    let key_len = u16::from_le_bytes([file[e], file[e + 1]]) as usize;
    let kind_off = e + 2 + key_len + 4; // key_len(2) · key · type(4)
    assert_eq!(file[kind_off], spec::KV_VALUE_INLINE);
    let value_off = kind_off + 1 + 4; // value_kind(1) · value_len(4)
    file[value_off] = 0xFF;
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::Invariant, "kv text value not valid UTF-8");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_inline_text_value_nul_rejected() {
    let mut file = file_inline_text_kv();
    assert!(Reader::open(&file).is_ok());
    let leaf = find_page(&file, spec::PAGE_TYPE_KV_LEAF);
    let e = kv_slot_off(&file, leaf, 0, false);
    let key_len = u16::from_le_bytes([file[e], file[e + 1]]) as usize;
    let kind_off = e + 2 + key_len + 4;
    assert_eq!(file[kind_off], spec::KV_VALUE_INLINE);
    let value_off = kind_off + 1 + 4;
    file[value_off] = 0x00; // an embedded NUL in a type-0 text value
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::Invariant, "kv text value contains NUL");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_overflow_text_value_non_utf8_rejected() {
    let mut file = file_overflow_text_kv();
    assert!(Reader::open(&file).is_ok());
    // The first overflow page; corrupt a standalone payload byte (surrounded by ASCII 'a').
    let ovf = find_page(&file, spec::PAGE_TYPE_OVERFLOW);
    let payload = ovf * PAGE_SIZE + spec::PAGE_HEADER_SIZE + 4; // header(16) · next_pgno(4)
    file[payload + 10] = 0xFF;
    restamp(&mut file, ovf);
    assert_reject!(&file, Error::Invariant, "kv text value not valid UTF-8");
}
*/

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

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn scope_table_root_out_of_range_rejected() {
    let mut file = valid_file_with_kv(); // v4.1
    assert!(Reader::open(&file).is_ok());
    let total = total_pages_of(&file);
    set_active_meta_u32(&mut file, spec::META_SCOPE_TABLE_ROOT, total as u32);
    assert_reject!(&file, Error::Structural, "scope_table_root out of range");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_root_out_of_range_rejected() {
    let mut file = file_inline_text_kv();
    assert!(Reader::open(&file).is_ok());
    let total = total_pages_of(&file);
    let leaf = find_page(&file, spec::PAGE_TYPE_SCOPE_LEAF);
    let count = entry_count(&file, leaf) as usize;
    // Pick a scope record with KV (kv_root != 0) and point it out of range.
    let base = (0..count)
        .map(|i| leaf * PAGE_SIZE + spec::PAGE_HEADER_SIZE + i * spec::SCOPE_RECORD_SIZE)
        .find(|&b| read_u32_at(&file, b + spec::SCOPE_REC_KV_ROOT) != 0)
        .expect("a scope record with kv_root != 0");
    let off = base + spec::SCOPE_REC_KV_ROOT;
    file[off..off + 4].copy_from_slice(&(total as u32).to_le_bytes());
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::Structural, "kv_root out of range");
}
*/

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

// ---------------- TIER 3: parity ----------------

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn minor1_meta_size_pinned() {
    // F7 (v4.1 side): at version_minor == 1 the reader requires meta_size exactly 94. Any
    // other in-range size with a valid CRC is rejected as BadMetaSize (mirrors Go's
    // TestMinor1MetaSizePinned and the minor-0 rule).
    let file = valid_file_with_kv(); // a v4.1 file (any metadata ⇒ minor 1 / meta_size 94)
    assert!(Reader::open(&file).is_ok());
    for bad in [90u16, 92, 95, 100] {
        let mut g = file.clone();
        // Edit the ACTIVE (v4.1) meta only — the inactive meta is still v4.0, where a meta_size
        // edit would exercise the minor-0 pin instead of the minor-1 pin under test.
        set_active_meta_u16(&mut g, spec::META_META_SIZE, bad);
        // Pin the value: BadMetaSize must carry exactly the forged size (ties the rejection to
        // the minor-1 pin's input, not some other BadMetaSize producer).
        match reject(&g) {
            Error::BadMetaSize(s) => assert_eq!(s, bad, "minor1 meta_size={bad}"),
            e => panic!("minor1 meta_size={bad}: expected BadMetaSize, got {e:?}"),
        }
    }
}
*/

// =====================================================================================
// SOW-0010 FIX ROUND — gap-closing targeted CRC-valid rejection tests. The no-wrong-answer
// fuzz excludes data-authority bytes (KV entry headers, slot directories, scope record
// fields, overflow descriptors) because re-stamping them is a legitimately different file,
// not a wrong answer — so those structural-validity fields had no targeted coverage. Each
// test below builds a valid file, asserts it opens, mutates EXACTLY ONE such field, restamps,
// and asserts the EXACT message of the one check that must reject it.
// =====================================================================================

// --- builders for the v4.1 KV / overflow / deep-IP shapes the new tests need ---

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
/// A v4.1 file whose single scope has enough long-keyed entries to force a multi-level KV
/// tree (≥ 1 `PAGE_TYPE_KV_BRANCH`). Mirrors the inline builders in the F2 KV-branch tests.
fn file_kv_branch() -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    let id = w.scope_define(b"s").unwrap();
    for i in 0..400u32 {
        let key = format!("key-{i:08}-{}", "x".repeat(200));
        w.meta_set(id, key.as_bytes(), 0, b"v").unwrap();
    }
    w.commit(0).unwrap();
    w.into_image().unwrap()
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
/// A v4.1 file with one scope holding a single **binary** (`type 9`) overflow-backed value,
/// so the overflow checks are isolated from the `type == 0` text path. ~6000 bytes ⇒ a real
/// 2-page chain with a non-zero last-page tail.
fn file_binary_overflow() -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    let id = w.scope_define(b"s").unwrap();
    let big: Vec<u8> = (0..6000u32).map(|i| (i * 3) as u8).collect();
    w.meta_set(id, b"k", 9, &big).unwrap();
    w.commit(0).unwrap();
    w.into_image().unwrap()
}
*/

/// A clean, fully-reachable **3-level** IPv4 tree. `scope_width 255` ⇒ `record_size 263` ⇒
/// `leaf_max 15` (the IPv4 minimum), so a modest record count yields > `branch_max + 1` (510)
/// leaves and a `root branch → intermediate branches → leaves` shape — the only shape with a
/// **nested** branch whose inherited `hi` bound is below the family max (needed for the
/// `separator > hi` test, unreachable at the root where `hi == MAX`).
fn valid_ip_tree_h3() -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..200_000u32 {
        w.set(Ipv4Key(i * 4), Ipv4Key(i * 4 + 1), 0x5a5a5a5au32).unwrap();
    }
    w.commit(0).unwrap();
    let img = w.into_image().unwrap();
    assert_eq!(
        ip_geom(&img).1,
        3,
        "valid_ip_tree_h3 must be a 3-level tree"
    );
    img
}

/// The terminal overflow page of a (single) chain — the one whose `next_pgno == 0`.
fn last_overflow_page(file: &[u8]) -> usize {
    let total = file.len() / PAGE_SIZE;
    (2..total)
        .find(|&p| {
            page_type(file, p) == spec::PAGE_TYPE_OVERFLOW
                && read_u32_at(file, p * PAGE_SIZE + spec::OVERFLOW_NEXT_PGNO) == 0
        })
        .expect("a terminal overflow page (next_pgno == 0)")
}

/// `(entry_offset, key_len)` of KV leaf entry `i`: the entry start (from its slot) and the
/// decoded `key_len`. Entry layout: `key_len(2) · key · type(4) · value_kind(1) · …`.
fn kv_leaf_entry(file: &[u8], leaf: usize, i: usize) -> (usize, usize) {
    let e = kv_slot_off(file, leaf, i, false);
    let key_len = u16::from_le_bytes([file[e], file[e + 1]]) as usize;
    (e, key_len)
}

// ---------------- TIER 4a: KV slot directory / entry_count ----------------

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_leaf_empty() {
    let mut file = file_inline_text_kv();
    assert!(Reader::open(&file).is_ok());
    let leaf = find_page(&file, spec::PAGE_TYPE_KV_LEAF);
    file[leaf * PAGE_SIZE + spec::PH_ENTRY_COUNT..leaf * PAGE_SIZE + spec::PH_ENTRY_COUNT + 2]
        .copy_from_slice(&0u16.to_le_bytes());
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::Invariant, "kv leaf empty");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_leaf_slot_directory_overflow() {
    let mut file = file_inline_text_kv();
    assert!(Reader::open(&file).is_ok());
    let leaf = find_page(&file, spec::PAGE_TYPE_KV_LEAF);
    // entry_count so large the slot directory (16 + count·2) exceeds the page.
    file[leaf * PAGE_SIZE + spec::PH_ENTRY_COUNT..leaf * PAGE_SIZE + spec::PH_ENTRY_COUNT + 2]
        .copy_from_slice(&3000u16.to_le_bytes());
    restamp(&mut file, leaf);
    assert_reject!(
        &file,
        Error::Structural,
        "kv leaf slot directory overflows page"
    );
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_branch_empty() {
    let mut file = file_kv_branch();
    assert!(Reader::open(&file).is_ok());
    let branch = find_page(&file, spec::PAGE_TYPE_KV_BRANCH);
    file[branch * PAGE_SIZE + spec::PH_ENTRY_COUNT..branch * PAGE_SIZE + spec::PH_ENTRY_COUNT + 2]
        .copy_from_slice(&0u16.to_le_bytes());
    restamp(&mut file, branch);
    assert_reject!(&file, Error::Invariant, "kv branch has no separators");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_branch_slot_directory_overflow() {
    let mut file = file_kv_branch();
    assert!(Reader::open(&file).is_ok());
    let branch = find_page(&file, spec::PAGE_TYPE_KV_BRANCH);
    // entry_count so large the branch slot directory (20 + count·2) exceeds the page.
    file[branch * PAGE_SIZE + spec::PH_ENTRY_COUNT..branch * PAGE_SIZE + spec::PH_ENTRY_COUNT + 2]
        .copy_from_slice(&3000u16.to_le_bytes());
    restamp(&mut file, branch);
    assert_reject!(
        &file,
        Error::Structural,
        "kv branch slot directory overflows page"
    );
}
*/

// ---------------- TIER 4b: KV entry-header heap fields (the fuzz never touches these) ----

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_leaf_key_len_out_of_range() {
    let mut file = file_inline_text_kv();
    assert!(Reader::open(&file).is_ok());
    let leaf = find_page(&file, spec::PAGE_TYPE_KV_LEAF);
    let (e, _) = kv_leaf_entry(&file, leaf, 0);
    // key_len = 0 is below KV_KEY_MIN (1).
    file[e..e + 2].copy_from_slice(&0u16.to_le_bytes());
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::Invariant, "kv key_len out of range");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_leaf_unknown_value_kind() {
    let mut file = file_inline_text_kv();
    assert!(Reader::open(&file).is_ok());
    let leaf = find_page(&file, spec::PAGE_TYPE_KV_LEAF);
    let (e, key_len) = kv_leaf_entry(&file, leaf, 0);
    let kind_off = e + 2 + key_len + 4; // key_len(2) · key · type(4)
    assert_eq!(file[kind_off], spec::KV_VALUE_INLINE);
    file[kind_off] = 2; // neither INLINE (0) nor OVERFLOW (1)
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::Structural, "kv leaf unknown value_kind");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_branch_sep_len_out_of_range() {
    let mut file = file_kv_branch();
    assert!(Reader::open(&file).is_ok());
    let branch = (2..file.len() / PAGE_SIZE)
        .find(|&p| page_type(&file, p) == spec::PAGE_TYPE_KV_BRANCH && entry_count(&file, p) >= 1)
        .expect("a KV branch with >= 1 separator");
    let sep_off = kv_slot_off(&file, branch, 0, true);
    // sep_len > KV_KEY_MAX (1024).
    file[sep_off..sep_off + 2].copy_from_slice(&2000u16.to_le_bytes());
    restamp(&mut file, branch);
    assert_reject!(&file, Error::Invariant, "kv sep_len out of range");
}
*/

// ---------------- TIER 4c: hostile KV key / separator text (check_key → InvalidInput) ----

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_leaf_key_nul() {
    let mut file = file_inline_text_kv();
    assert!(Reader::open(&file).is_ok());
    let leaf = find_page(&file, spec::PAGE_TYPE_KV_LEAF);
    let (e, key_len) = kv_leaf_entry(&file, leaf, 0);
    assert!(key_len >= 1);
    file[e + 2] = 0x00; // first key byte → embedded NUL
    restamp(&mut file, leaf);
    // The on-disk key is re-validated by `check_key`, which is caller-facing → InvalidInput.
    assert_reject!(&file, Error::InvalidInput, "kv key contains NUL");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_leaf_key_non_utf8() {
    let mut file = file_inline_text_kv();
    assert!(Reader::open(&file).is_ok());
    let leaf = find_page(&file, spec::PAGE_TYPE_KV_LEAF);
    let (e, key_len) = kv_leaf_entry(&file, leaf, 0);
    assert!(key_len >= 1);
    file[e + 2] = 0xFF; // 0xFF is never valid UTF-8
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::InvalidInput, "kv key not valid UTF-8");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_branch_separator_key_nul() {
    let mut file = file_kv_branch();
    assert!(Reader::open(&file).is_ok());
    let branch = (2..file.len() / PAGE_SIZE)
        .find(|&p| page_type(&file, p) == spec::PAGE_TYPE_KV_BRANCH && entry_count(&file, p) >= 1)
        .expect("a KV branch with >= 1 separator");
    let sep_off = kv_slot_off(&file, branch, 0, true);
    file[sep_off + 2] = 0x00; // first separator-key byte → NUL (skip sep_len u16)
    restamp(&mut file, branch);
    assert_reject!(&file, Error::InvalidInput, "kv key contains NUL");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_branch_separator_key_non_utf8() {
    let mut file = file_kv_branch();
    assert!(Reader::open(&file).is_ok());
    let branch = (2..file.len() / PAGE_SIZE)
        .find(|&p| page_type(&file, p) == spec::PAGE_TYPE_KV_BRANCH && entry_count(&file, p) >= 1)
        .expect("a KV branch with >= 1 separator");
    let sep_off = kv_slot_off(&file, branch, 0, true);
    file[sep_off + 2] = 0xFF;
    restamp(&mut file, branch);
    assert_reject!(&file, Error::InvalidInput, "kv key not valid UTF-8");
}
*/

// ---------------- TIER 5: overflow descriptor / payload tail ----------------

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_overflow_total_len_zero() {
    let mut file = file_binary_overflow();
    assert!(Reader::open(&file).is_ok());
    let leaf = find_page(&file, spec::PAGE_TYPE_KV_LEAF);
    let (e, key_len) = kv_leaf_entry(&file, leaf, 0);
    let kind_off = e + 2 + key_len + 4;
    assert_eq!(file[kind_off], spec::KV_VALUE_OVERFLOW);
    let total_len_off = kind_off + 1 + 4; // value_kind(1) · first_pgno(4)
    file[total_len_off..total_len_off + 8].copy_from_slice(&0u64.to_le_bytes());
    restamp(&mut file, leaf);
    assert_reject!(
        &file,
        Error::Structural,
        "kv overflow chain for empty value"
    );
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_overflow_first_pgno_out_of_range() {
    let mut file = file_binary_overflow();
    assert!(Reader::open(&file).is_ok());
    let total = total_pages_of(&file);
    let leaf = find_page(&file, spec::PAGE_TYPE_KV_LEAF);
    let (e, key_len) = kv_leaf_entry(&file, leaf, 0);
    let kind_off = e + 2 + key_len + 4;
    assert_eq!(file[kind_off], spec::KV_VALUE_OVERFLOW);
    let first_pgno_off = kind_off + 1; // value_kind(1)
    file[first_pgno_off..first_pgno_off + 4].copy_from_slice(&(total as u32).to_le_bytes());
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::Structural, "kv overflow pgno out of range");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_overflow_text_value_nul() {
    let mut file = file_overflow_text_kv();
    assert!(Reader::open(&file).is_ok());
    let ovf = find_page(&file, spec::PAGE_TYPE_OVERFLOW);
    let payload = ovf * PAGE_SIZE + spec::PAGE_HEADER_SIZE + 4; // header(16) · next_pgno(4)
    file[payload + 10] = 0x00; // an embedded NUL in an overflow text payload
    restamp(&mut file, ovf);
    assert_reject!(&file, Error::Invariant, "kv text value contains NUL");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_overflow_last_page_tail_nonzero() {
    // Binary value (type 9) ⇒ no text path; isolate the read-by-count last-page tail check.
    let mut file = file_binary_overflow();
    assert!(Reader::open(&file).is_ok());
    let last = last_overflow_page(&file);
    file[last * PAGE_SIZE + PAGE_SIZE - 1] = 1; // a byte in the unused payload tail
    restamp(&mut file, last);
    assert_reject!(&file, Error::NonZeroReserved, "kv overflow last-page tail");
}
*/

// ---------------- TIER 5b: KV canonical packing (SOW-0010) ----------------
//
// A slot-directory page (KV leaf / branch) has a slot directory at the front and a
// variable-length entry heap at the back. Before SOW-0010 the validator parsed exactly
// `entry_count` entries and returned Ok with NO canonical-packing check, so a CRC-valid file
// could shrink `entry_count` (stale slot/heap bytes ignored) or shrink an inline value / key /
// separator length (leftover heap bytes ignored) and be accepted as a DIFFERENT view. The
// canonical-packing check closes both: the free gap [slot_dir_end, heap_start) MUST be zero,
// and the entries MUST tile [heap_start, PAGE_SIZE) exactly. (Non-vacuity of each half is
// proven in `kv_leaf_entry_count_shrink_*` below; see the SOW for the revert proofs.)

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
/// A v4.1 file whose single scope holds exactly two small inline KV entries in one KV leaf
/// (entry_count == 2), so an `entry_count -= 1` drops the bottom-most heap entry — caught only
/// by the free-gap half of the canonical check (the surviving entry still tiles to PAGE_SIZE).
fn file_two_inline_kv() -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    let id = w.scope_define(b"s").unwrap();
    w.meta_set(id, b"a", spec::KV_TYPE_TEXT, b"va").unwrap();
    w.meta_set(id, b"b", spec::KV_TYPE_TEXT, b"vb").unwrap();
    w.commit(0).unwrap();
    w.into_image().unwrap()
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_leaf_entry_count_shrink_rejected() {
    // entry_count 2 -> 1: the validator now reads one fewer slot, so the dropped slot (at the
    // new slot_dir_end) and its entry's heap bytes fall into the free gap [slot_dir_end,
    // heap_start), which is no longer zero. The surviving entry still tiles to PAGE_SIZE, so
    // ONLY the free-gap half rejects (the SOW revert proof: remove it and this file opens with
    // a wrong 1-entry view).
    let mut file = file_two_inline_kv();
    let leaf = (2..file.len() / PAGE_SIZE)
        .find(|&p| page_type(&file, p) == spec::PAGE_TYPE_KV_LEAF && entry_count(&file, p) == 2)
        .expect("a 2-entry KV leaf");
    assert!(Reader::open(&file).is_ok());
    let ec = leaf * PAGE_SIZE + spec::PH_ENTRY_COUNT;
    file[ec..ec + 2].copy_from_slice(&1u16.to_le_bytes());
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::NonZeroReserved, "kv leaf free gap not zero");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_branch_entry_count_shrink_rejected() {
    // Multi-level KV tree: shrink a branch's separator count by 1. The dropped slot + its
    // separator heap bytes fall into the new free gap → reject. (The dropped rightmost child
    // subtree is simply no longer walked; the free-gap check fires before the child recursion.)
    let mut file = file_kv_branch();
    let branch = (2..file.len() / PAGE_SIZE)
        .find(|&p| page_type(&file, p) == spec::PAGE_TYPE_KV_BRANCH && entry_count(&file, p) >= 2)
        .expect("a KV branch with >= 2 separators");
    assert!(Reader::open(&file).is_ok());
    let ec = branch * PAGE_SIZE + spec::PH_ENTRY_COUNT;
    let n = entry_count(&file, branch);
    file[ec..ec + 2].copy_from_slice(&(n - 1).to_le_bytes());
    restamp(&mut file, branch);
    assert_reject!(&file, Error::NonZeroReserved, "kv branch free gap not zero");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_separators_not_increasing() {
    // A KV branch with >= 2 fixed-length separators: overwrite separator 1's key with separator
    // 0's (equal length ⇒ packing intact) ⇒ sep[1] == sep[0], so the strictly-increasing
    // separator check fires (before any child recursion). Mirrors scope_separators_not_increasing
    // and the Go test; closes the last KV-branch validator check.
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    let id = w.scope_define(b"s").unwrap();
    for i in 0..400u32 {
        let key = format!("key-{i:08}-{}", "x".repeat(200)); // fixed-length keys ⇒ equal seps
        w.meta_set(id, key.as_bytes(), 0, b"v").unwrap();
    }
    w.commit(0).unwrap();
    let mut file = w.into_image().unwrap();
    let total = file.len() / PAGE_SIZE;
    let branch = (2..total)
        .find(|&p| page_type(&file, p) == spec::PAGE_TYPE_KV_BRANCH && entry_count(&file, p) >= 2)
        .expect("a KV branch with >= 2 separators");
    assert!(Reader::open(&file).is_ok());
    let off0 = kv_slot_off(&file, branch, 0, true);
    let off1 = kv_slot_off(&file, branch, 1, true);
    let len0 = u16::from_le_bytes([file[off0], file[off0 + 1]]) as usize;
    let len1 = u16::from_le_bytes([file[off1], file[off1 + 1]]) as usize;
    assert_eq!(len0, len1, "expected equal-length separators");
    let key0: Vec<u8> = file[off0 + 2..off0 + 2 + len0].to_vec();
    file[off1 + 2..off1 + 2 + len1].copy_from_slice(&key0);
    restamp(&mut file, branch);
    assert_reject!(&file, Error::Invariant, "kv separators not increasing");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_inline_value_len_shrink_rejected() {
    // Shrink an inline value's value_len by 1: the entry now parses 1 byte short, leaving an
    // uncovered heap byte → the tiling half of the canonical check rejects (free gap unchanged,
    // since no slot/start moved). The SOW revert proof: remove the tiling check and this file
    // opens returning the truncated value "hell" — a wrong answer.
    let mut file = file_inline_text_kv(); // single entry: key "k", type 0, value "hello"
    assert!(Reader::open(&file).is_ok());
    let leaf = find_page(&file, spec::PAGE_TYPE_KV_LEAF);
    let (e, key_len) = kv_leaf_entry(&file, leaf, 0);
    let kind_off = e + 2 + key_len + 4; // key_len(2) · key · type(4)
    assert_eq!(file[kind_off], spec::KV_VALUE_INLINE);
    let value_len_off = kind_off + 1; // value_kind(1)
    let vlen = read_u32_at(&file, value_len_off);
    assert!(
        vlen >= 2,
        "value must be long enough to shrink and stay valid UTF-8"
    );
    file[value_len_off..value_len_off + 4].copy_from_slice(&(vlen - 1).to_le_bytes());
    restamp(&mut file, leaf);
    assert_reject!(
        &file,
        Error::Structural,
        "kv leaf entries not canonically packed"
    );
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
/// A v4.1 file whose single scope holds one inline KV entry with an EMPTY value (value_len 0)
/// and type 0. The empty value makes a `key_len -= 1` shift benign: the re-read value_kind/
/// value_len land on the entry's trailing zero bytes, so the entry still parses (as a valid,
/// 1-byte-shorter entry) instead of erroring — isolating the tiling half via a key-length shrink.
fn file_empty_value_kv() -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    let id = w.scope_define(b"s").unwrap();
    w.meta_set(id, b"aaaa", spec::KV_TYPE_TEXT, b"").unwrap();
    w.commit(0).unwrap();
    w.into_image().unwrap()
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_leaf_key_len_shrink_leaves_gap_rejected() {
    // Distinct from `kv_leaf_key_len_out_of_range` (key_len 0): here key_len 4 -> 3 stays in
    // [1,1024]. The trailing fields shift left by 1, but because the value is empty (type/kind/
    // value_len bytes are all zero around the shift), the entry re-parses as a VALID shorter
    // entry — so no earlier check fires and the 1-byte shortfall trips the tiling check.
    let mut file = file_empty_value_kv();
    assert!(Reader::open(&file).is_ok());
    let leaf = find_page(&file, spec::PAGE_TYPE_KV_LEAF);
    let (e, key_len) = kv_leaf_entry(&file, leaf, 0);
    assert!(
        key_len >= 2,
        "need key_len >= 2 to shrink and stay in [1,1024]"
    );
    file[e..e + 2].copy_from_slice(&((key_len - 1) as u16).to_le_bytes());
    restamp(&mut file, leaf);
    assert_reject!(
        &file,
        Error::Structural,
        "kv leaf entries not canonically packed"
    );
}
*/

// ---------------- TIER 6: scope record fields ----------------

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn scope_name_len_gt_256() {
    let mut file = file_named_scope();
    assert!(Reader::open(&file).is_ok());
    let leaf = find_page(&file, spec::PAGE_TYPE_SCOPE_LEAF);
    let base = leaf * PAGE_SIZE + spec::PAGE_HEADER_SIZE; // record 0
    let off = base + spec::SCOPE_REC_NAME_LEN;
    file[off..off + 2].copy_from_slice(&257u16.to_le_bytes()); // > SCOPE_NAME_MAX (256)
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::Invariant, "scope name_len > 256");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn scope_name_padding_nonzero() {
    let mut file = file_named_scope();
    assert!(Reader::open(&file).is_ok());
    let leaf = find_page(&file, spec::PAGE_TYPE_SCOPE_LEAF);
    let base = leaf * PAGE_SIZE + spec::PAGE_HEADER_SIZE; // record 0 ("scope-x", name_len 7)
    let name_len = u16::from_le_bytes([
        file[base + spec::SCOPE_REC_NAME_LEN],
        file[base + spec::SCOPE_REC_NAME_LEN + 1],
    ]) as usize;
    // A non-zero byte in the fixed name slot beyond name_len (UTF-8 of the name stays valid).
    file[base + spec::SCOPE_REC_NAME + name_len] = 1;
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::NonZeroReserved, "scope name padding");
}
*/

// ---------------- TIER 7: meta / IP geometry ----------------

#[test]
fn empty_tree_record_count_nonzero() {
    let mut file = {
        let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
        w.commit(0).unwrap();
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

// --- shared locators for the v4.1 scope-table / KV trees ---

/// The active meta's `scope_table_root` pgno.
fn scope_table_root_of(file: &[u8]) -> usize {
    let a = active_meta_page(file);
    read_u32_at(file, a * PAGE_SIZE + spec::META_SCOPE_TABLE_ROOT) as usize
}

/// The leftmost-child / child[0] pgno of a branch page (scope branch and KV branch both store
/// it in the 4 bytes immediately after the header).
fn leftmost_child(file: &[u8], pgno: usize) -> usize {
    read_u32_at(file, pgno * PAGE_SIZE + spec::PAGE_HEADER_SIZE) as usize
}

/// `kv_root` of the single scope in a one-scope file (its scope-table root is a leaf).
fn first_scope_kv_root(file: &[u8]) -> usize {
    let sroot = scope_table_root_of(file);
    assert_eq!(
        page_type(file, sroot),
        spec::PAGE_TYPE_SCOPE_LEAF,
        "single-scope file: scope-table root is a leaf"
    );
    let rec0 = sroot * PAGE_SIZE + spec::PAGE_HEADER_SIZE;
    read_u32_at(file, rec0 + spec::SCOPE_REC_KV_ROOT) as usize
}

/// Byte offset of scope-branch separator `i` (IPv4-branch layout: child[0] then `(sep,child)`
/// pairs of `SCOPE_KEY_WIDTH + 4` bytes).
fn scope_sep_off(branch: usize, i: usize) -> usize {
    branch * PAGE_SIZE + spec::PAGE_HEADER_SIZE + 4 + i * (spec::SCOPE_KEY_WIDTH + 4)
}
/// Byte offset of scope-branch child `j` (0 = leftmost).
fn scope_child_off(branch: usize, j: usize) -> usize {
    if j == 0 {
        branch * PAGE_SIZE + spec::PAGE_HEADER_SIZE
    } else {
        branch * PAGE_SIZE
            + spec::PAGE_HEADER_SIZE
            + 4
            + (j - 1) * (spec::SCOPE_KEY_WIDTH + 4)
            + spec::SCOPE_KEY_WIDTH
    }
}

/// The chain-head overflow page of a single-entry overflow file (the entry's `first_pgno`).
fn overflow_head(file: &[u8]) -> usize {
    let leaf = find_page(file, spec::PAGE_TYPE_KV_LEAF);
    let (e, key_len) = kv_leaf_entry(file, leaf, 0);
    let kind_off = e + 2 + key_len + 4; // key_len(2) · key · type(4)
    assert_eq!(file[kind_off], spec::KV_VALUE_OVERFLOW);
    read_u32_at(file, kind_off + 1) as usize // value_kind(1) → first_pgno
}

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
/// A v4.1 file with `n` scopes (no KV) — the scope-table corruption fixtures.
fn file_scopes(n: u32) -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for s in 0..n {
        w.scope_define(format!("scope-{s}").as_bytes()).unwrap();
    }
    w.commit(0).unwrap();
    w.into_image().unwrap()
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
/// A v4.1 file whose scope table is a single leaf (5 scopes ≤ `scope_leaf_max` 14): lo/hi at
/// the root are the full `[0, MAX]` id space, isolating per-record id checks from bound checks.
fn file_multi_scope_leaf() -> Vec<u8> {
    let file = file_scopes(5);
    assert_eq!(
        page_type(&file, scope_table_root_of(&file)),
        spec::PAGE_TYPE_SCOPE_LEAF
    );
    file
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
/// A v4.1 file whose scope table is a single **root branch** with leaf children (≥ 15 scopes).
fn file_scope_branch() -> Vec<u8> {
    let file = file_scopes(40); // 40 / 14 = 3 leaves ⇒ a root branch with 2 separators
    let root = scope_table_root_of(&file);
    assert_eq!(page_type(&file, root), spec::PAGE_TYPE_SCOPE_BRANCH);
    assert!(entry_count(&file, root) >= 2, "need >= 2 separators");
    file
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
/// A clean **3-level** scope table. With `scope_leaf_max` 14 and a scope-branch fanout of 510,
/// more than 510 leaves (more than 7140 scopes) force two branch levels — the only shape with a
/// **nested** (non-root) scope branch (inherited `hi < u32::MAX`) needed for the
/// `scope separator > hi` and `scope leaves at differing depths` tests.
fn valid_scope_tree_h3() -> Vec<u8> {
    // child fanout = scope_branch_max + 1; > fanout leaves (> fanout·leaf_max scopes) ⇒ 3 levels.
    let fanout = spec::scope_branch_max() as u32 + 1;
    let file = file_scopes(fanout * spec::scope_leaf_max() as u32 + 1);
    let root = scope_table_root_of(&file);
    assert_eq!(page_type(&file, root), spec::PAGE_TYPE_SCOPE_BRANCH);
    assert_eq!(
        page_type(&file, leftmost_child(&file, root)),
        spec::PAGE_TYPE_SCOPE_BRANCH,
        "height >= 3: the root's leftmost child is itself a branch"
    );
    file
}
*/

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

// ---------------- TIER 10: scope-table (§C.2/§D) remaining rejections ----------------

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn scope_page_checksum_failed() {
    let mut file = file_named_scope();
    assert!(Reader::open(&file).is_ok());
    let leaf = scope_table_root_of(&file);
    file[leaf * PAGE_SIZE + spec::PAGE_HEADER_SIZE + 1] ^= 0xFF; // no restamp ⇒ CRC gate
    assert_reject!(&file, Error::ChecksumFailed, "scope page");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn scope_page_header_reserved_nonzero() {
    let mut file = file_named_scope();
    assert!(Reader::open(&file).is_ok());
    let leaf = scope_table_root_of(&file);
    file[leaf * PAGE_SIZE + spec::PH_RESERVED] = 1;
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::NonZeroReserved, "scope page header reserved");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn scope_page_self_pgno_mismatch() {
    let mut file = file_named_scope();
    assert!(Reader::open(&file).is_ok());
    let leaf = scope_table_root_of(&file);
    let off = leaf * PAGE_SIZE + spec::PH_PGNO;
    file[off..off + 4].copy_from_slice(&((leaf as u32) + 1).to_le_bytes());
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::Structural, "scope page self-pgno mismatch");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn scope_leaf_entry_count_out_of_range() {
    let mut file = file_named_scope();
    assert!(Reader::open(&file).is_ok());
    let leaf = scope_table_root_of(&file);
    let off = leaf * PAGE_SIZE + spec::PH_ENTRY_COUNT;
    file[off..off + 2].copy_from_slice(&0u16.to_le_bytes()); // count < 1
    restamp(&mut file, leaf);
    assert_reject!(
        &file,
        Error::Invariant,
        "scope leaf entry_count out of range"
    );
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn scope_leaf_tail_nonzero() {
    let mut file = file_named_scope();
    assert!(Reader::open(&file).is_ok());
    let leaf = scope_table_root_of(&file);
    file[leaf * PAGE_SIZE + PAGE_SIZE - 1] = 1; // a byte in the tail after the records
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::NonZeroReserved, "scope leaf tail");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn scope_ids_not_sorted_disjoint() {
    // A single scope leaf (root, bounds [0, MAX]): set record 1's id == record 0's id. The
    // bound check passes (id is in range); only the strictly-increasing check rejects. The
    // fuzz excludes these scope-record heap bytes, so this had no targeted coverage.
    let mut file = file_multi_scope_leaf();
    assert!(Reader::open(&file).is_ok());
    let leaf = scope_table_root_of(&file);
    let rec = |i: usize| leaf * PAGE_SIZE + spec::PAGE_HEADER_SIZE + i * spec::SCOPE_RECORD_SIZE;
    let id0 = read_u32_at(&file, rec(0) + spec::SCOPE_REC_ID);
    let o1 = rec(1) + spec::SCOPE_REC_ID;
    file[o1..o1 + 4].copy_from_slice(&id0.to_le_bytes());
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::Invariant, "scope ids not sorted/disjoint");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn scope_branch_separator_count_out_of_range() {
    let mut file = file_scope_branch();
    assert!(Reader::open(&file).is_ok());
    let branch = scope_table_root_of(&file);
    let off = branch * PAGE_SIZE + spec::PH_ENTRY_COUNT;
    file[off..off + 2].copy_from_slice(&600u16.to_le_bytes()); // > scope_branch_max (509)
    restamp(&mut file, branch);
    assert_reject!(
        &file,
        Error::Invariant,
        "scope branch separator count out of range"
    );
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn scope_branch_tail_nonzero() {
    let mut file = file_scope_branch();
    assert!(Reader::open(&file).is_ok());
    let branch = scope_table_root_of(&file);
    file[branch * PAGE_SIZE + PAGE_SIZE - 1] = 1;
    restamp(&mut file, branch);
    assert_reject!(&file, Error::NonZeroReserved, "scope branch tail");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn scope_separator_le_lo() {
    // The root scope branch has lo == 0; set separator 0 to 0 (<= lo).
    let mut file = file_scope_branch();
    assert!(Reader::open(&file).is_ok());
    let branch = scope_table_root_of(&file);
    let o = scope_sep_off(branch, 0);
    file[o..o + 4].copy_from_slice(&0u32.to_le_bytes());
    restamp(&mut file, branch);
    assert_reject!(&file, Error::Invariant, "scope separator <= lo");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn scope_separators_not_increasing() {
    let mut file = file_scope_branch();
    assert!(Reader::open(&file).is_ok());
    let branch = scope_table_root_of(&file);
    assert!(entry_count(&file, branch) >= 2);
    let s0 = read_u32_at(&file, scope_sep_off(branch, 0));
    let o1 = scope_sep_off(branch, 1);
    file[o1..o1 + 4].copy_from_slice(&s0.to_le_bytes()); // sep[1] := sep[0]
    restamp(&mut file, branch);
    assert_reject!(&file, Error::Invariant, "scope separators not increasing");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn scope_child_pgno_out_of_range() {
    let mut file = file_scope_branch();
    assert!(Reader::open(&file).is_ok());
    let branch = scope_table_root_of(&file);
    let total = total_pages_of(&file);
    let o = scope_child_off(branch, 0); // leftmost child := total_pages (out of range)
    file[o..o + 4].copy_from_slice(&(total as u32).to_le_bytes());
    restamp(&mut file, branch);
    assert_reject!(&file, Error::Structural, "scope child pgno out of range");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn scope_unexpected_page_type() {
    let mut file = file_named_scope();
    assert!(Reader::open(&file).is_ok());
    let leaf = scope_table_root_of(&file);
    file[leaf * PAGE_SIZE + spec::PH_PAGE_TYPE] = 99; // neither SCOPE_BRANCH (4) nor SCOPE_LEAF (5)
    restamp(&mut file, leaf);
    assert_reject!(
        &file,
        Error::Structural,
        "unexpected page_type in scope table"
    );
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn scope_separator_gt_hi() {
    // A nested (non-root) scope branch: shrink the root's separator 0 to 1, so the leftmost
    // child's inherited bound collapses to [0, 0] while that child's real separators (ids >= 1)
    // exceed it. Unreachable at the root (hi == u32::MAX); needs the 3-level tree.
    let mut file = valid_scope_tree_h3();
    assert!(Reader::open(&file).is_ok());
    let root = scope_table_root_of(&file);
    let nested = leftmost_child(&file, root);
    assert_eq!(page_type(&file, nested), spec::PAGE_TYPE_SCOPE_BRANCH);
    let o = scope_sep_off(root, 0);
    file[o..o + 4].copy_from_slice(&1u32.to_le_bytes()); // child[0] hi := sep0-1 == 0
    restamp(&mut file, root);
    assert_reject!(&file, Error::Invariant, "scope separator > hi");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn scope_leaves_at_differing_depths() {
    // Redirect the root's leftmost child from its depth-2 branch B0 to B0's leftmost depth-3
    // leaf L. L (smallest ids) validates at depth 2 ⇒ leaf_depth = 2; the next subtree's leaves
    // are still at depth 3 ⇒ reject. B0 is orphaned (not the alias case), so the disjointness
    // guard does not fire first.
    let mut file = valid_scope_tree_h3();
    assert!(Reader::open(&file).is_ok());
    let root = scope_table_root_of(&file);
    let b0 = leftmost_child(&file, root);
    assert_eq!(page_type(&file, b0), spec::PAGE_TYPE_SCOPE_BRANCH);
    let l = leftmost_child(&file, b0);
    assert_eq!(page_type(&file, l), spec::PAGE_TYPE_SCOPE_LEAF);
    let o = scope_child_off(root, 0);
    file[o..o + 4].copy_from_slice(&(l as u32).to_le_bytes());
    restamp(&mut file, root);
    assert_reject!(&file, Error::Invariant, "scope leaves at differing depths");
}
*/

// ---------------- TIER 11: KV (§C.4/§D) remaining rejections ----------------

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_leaf_entry_offset_out_of_bounds() {
    // Repoint leaf slot 0 to offset 0 (inside the slot directory) ⇒ "entry offset out of bounds".
    let mut file = file_inline_text_kv();
    assert!(Reader::open(&file).is_ok());
    let leaf = find_page(&file, spec::PAGE_TYPE_KV_LEAF);
    let so = leaf * PAGE_SIZE + spec::PAGE_HEADER_SIZE; // slot 0
    file[so..so + 2].copy_from_slice(&0u16.to_le_bytes());
    restamp(&mut file, leaf);
    assert_reject!(
        &file,
        Error::Structural,
        "kv leaf entry offset out of bounds"
    );
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_branch_entry_offset_out_of_bounds() {
    let mut file = file_kv_branch();
    assert!(Reader::open(&file).is_ok());
    let branch = (2..file.len() / PAGE_SIZE)
        .find(|&p| page_type(&file, p) == spec::PAGE_TYPE_KV_BRANCH && entry_count(&file, p) >= 1)
        .expect("a KV branch");
    // Branch slot directory begins after the header + the leftmost-child u32.
    let so = branch * PAGE_SIZE + spec::PAGE_HEADER_SIZE + 4; // slot 0
    file[so..so + 2].copy_from_slice(&0u16.to_le_bytes());
    restamp(&mut file, branch);
    assert_reject!(
        &file,
        Error::Structural,
        "kv branch entry offset out of bounds"
    );
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_inline_value_len_too_large() {
    // GROW an inline value_len far past the page ⇒ the bounds-safe cursor rejects, never an OOB
    // read or panic ("kv entry read past page").
    let mut file = file_inline_text_kv();
    assert!(Reader::open(&file).is_ok());
    let leaf = find_page(&file, spec::PAGE_TYPE_KV_LEAF);
    let (e, key_len) = kv_leaf_entry(&file, leaf, 0);
    let kind_off = e + 2 + key_len + 4; // key_len(2) · key · type(4)
    assert_eq!(file[kind_off], spec::KV_VALUE_INLINE);
    let value_len_off = kind_off + 1; // value_kind(1)
    file[value_len_off..value_len_off + 4].copy_from_slice(&5000u32.to_le_bytes());
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::Structural, "kv entry read past page");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_branch_sep_len_shrink() {
    // Shrink a root KV branch separator's sep_len by 1: the parsed separator is 1 byte short, so
    // the entries no longer tile the heap exactly ⇒ "not canonically packed" (the branch mirror
    // of `kv_inline_value_len_shrink_rejected`). The root branch has lo == b"" so the shrunken
    // (still valid, smaller) separator can't trip the sep<=lo check first.
    let mut file = file_kv_branch();
    assert!(Reader::open(&file).is_ok());
    let root = first_scope_kv_root(&file);
    assert_eq!(page_type(&file, root), spec::PAGE_TYPE_KV_BRANCH);
    let sep_off = kv_slot_off(&file, root, 0, true);
    let sep_len = u16::from_le_bytes([file[sep_off], file[sep_off + 1]]);
    assert!(sep_len >= 2);
    file[sep_off..sep_off + 2].copy_from_slice(&(sep_len - 1).to_le_bytes());
    restamp(&mut file, root);
    assert_reject!(
        &file,
        Error::Structural,
        "kv branch entries not canonically packed"
    );
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_page_checksum_failed() {
    let mut file = file_inline_text_kv();
    assert!(Reader::open(&file).is_ok());
    let leaf = first_scope_kv_root(&file);
    file[leaf * PAGE_SIZE + spec::PAGE_HEADER_SIZE] ^= 0xFF; // no restamp ⇒ CRC gate
    assert_reject!(&file, Error::ChecksumFailed, "kv page");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_page_header_reserved_nonzero() {
    let mut file = file_inline_text_kv();
    assert!(Reader::open(&file).is_ok());
    let leaf = first_scope_kv_root(&file);
    file[leaf * PAGE_SIZE + spec::PH_RESERVED] = 1;
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::NonZeroReserved, "kv page header reserved");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_page_self_pgno_mismatch() {
    let mut file = file_inline_text_kv();
    assert!(Reader::open(&file).is_ok());
    let leaf = first_scope_kv_root(&file);
    let off = leaf * PAGE_SIZE + spec::PH_PGNO;
    file[off..off + 4].copy_from_slice(&((leaf as u32) + 1).to_le_bytes());
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::Structural, "kv page self-pgno mismatch");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_overflow_page_checksum_failed() {
    let mut file = file_binary_overflow();
    assert!(Reader::open(&file).is_ok());
    let ovf = find_page(&file, spec::PAGE_TYPE_OVERFLOW);
    file[ovf * PAGE_SIZE + spec::PAGE_HEADER_SIZE + 4 + 5] ^= 0xFF; // no restamp ⇒ CRC gate
    assert_reject!(&file, Error::ChecksumFailed, "kv overflow page");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_overflow_wrong_page_type() {
    let mut file = file_binary_overflow();
    assert!(Reader::open(&file).is_ok());
    let ovf = find_page(&file, spec::PAGE_TYPE_OVERFLOW);
    file[ovf * PAGE_SIZE + spec::PH_PAGE_TYPE] = 99; // not PAGE_TYPE_OVERFLOW (8)
    restamp(&mut file, ovf);
    assert_reject!(&file, Error::Structural, "kv overflow wrong page_type");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_overflow_header_reserved_nonzero() {
    let mut file = file_binary_overflow();
    assert!(Reader::open(&file).is_ok());
    let ovf = find_page(&file, spec::PAGE_TYPE_OVERFLOW);
    file[ovf * PAGE_SIZE + spec::PH_RESERVED] = 1;
    restamp(&mut file, ovf);
    assert_reject!(&file, Error::NonZeroReserved, "kv overflow header reserved");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_overflow_self_pgno_mismatch() {
    let mut file = file_binary_overflow();
    assert!(Reader::open(&file).is_ok());
    let ovf = find_page(&file, spec::PAGE_TYPE_OVERFLOW);
    let off = ovf * PAGE_SIZE + spec::PH_PGNO;
    file[off..off + 4].copy_from_slice(&((ovf as u32) + 1).to_le_bytes());
    restamp(&mut file, ovf);
    assert_reject!(&file, Error::Structural, "kv overflow self-pgno mismatch");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_overflow_chain_longer_than_length() {
    // The terminal overflow page must have next_pgno == 0 (read-by-count). Set it non-zero ⇒
    // the chain claims to be longer than its computed length.
    let mut file = file_binary_overflow();
    assert!(Reader::open(&file).is_ok());
    let last = last_overflow_page(&file);
    let off = last * PAGE_SIZE + spec::OVERFLOW_NEXT_PGNO;
    file[off..off + 4].copy_from_slice(&1u32.to_le_bytes());
    restamp(&mut file, last);
    assert_reject!(
        &file,
        Error::Structural,
        "kv overflow chain longer than length"
    );
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_overflow_chain_shorter_than_length() {
    // A non-terminal overflow page must have next_pgno != 0. Set the chain HEAD's next to 0 ⇒
    // the chain ends before its computed length. (file_binary_overflow is a 2-page chain.)
    let mut file = file_binary_overflow();
    assert!(Reader::open(&file).is_ok());
    let head = overflow_head(&file);
    assert_ne!(
        read_u32_at(&file, head * PAGE_SIZE + spec::OVERFLOW_NEXT_PGNO),
        0,
        "head must be a non-terminal page"
    );
    let off = head * PAGE_SIZE + spec::OVERFLOW_NEXT_PGNO;
    file[off..off + 4].copy_from_slice(&0u32.to_le_bytes());
    restamp(&mut file, head);
    assert_reject!(
        &file,
        Error::Structural,
        "kv overflow chain shorter than length"
    );
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_keys_not_sorted_disjoint() {
    // Single KV leaf (root, bounds [b"", +inf)): set entry 1's key == entry 0's key. Below/above
    // bound never fires (lo == b"", hi == None); only the strictly-increasing check rejects.
    let mut file = file_two_inline_kv();
    assert!(Reader::open(&file).is_ok());
    let leaf = (2..file.len() / PAGE_SIZE)
        .find(|&p| page_type(&file, p) == spec::PAGE_TYPE_KV_LEAF && entry_count(&file, p) == 2)
        .expect("a 2-entry KV leaf");
    let (e0, kl0) = kv_leaf_entry(&file, leaf, 0);
    let (e1, kl1) = kv_leaf_entry(&file, leaf, 1);
    assert_eq!(kl0, 1);
    assert_eq!(kl1, 1);
    file[e1 + 2] = file[e0 + 2]; // entry 1's 1-byte key := entry 0's key
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::Invariant, "kv keys not sorted/disjoint");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_key_below_node_bound() {
    // Descend the root's child[1] to its leftmost leaf (inherited lo == root.sep[0], non-empty)
    // and decrement that leaf's first key's first byte ('k' → 'j') ⇒ the key falls below the
    // node's lower bound. (The mirror of `kv_branch_separator_misroute_rejected`'s at/above
    // case.) Checked before the global sorted check, so it pins this specific rejection.
    let mut file = file_kv_branch();
    assert!(Reader::open(&file).is_ok());
    let root = first_scope_kv_root(&file);
    assert_eq!(page_type(&file, root), spec::PAGE_TYPE_KV_BRANCH);
    // root.child(1) = separator 0's child pgno.
    let sep0 = kv_slot_off(&file, root, 0, true);
    let sep_len = u16::from_le_bytes([file[sep0], file[sep0 + 1]]) as usize;
    let mut node = read_u32_at(&file, sep0 + 2 + sep_len) as usize;
    while page_type(&file, node) == spec::PAGE_TYPE_KV_BRANCH {
        node = leftmost_child(&file, node);
    }
    assert_eq!(page_type(&file, node), spec::PAGE_TYPE_KV_LEAF);
    let (e, key_len) = kv_leaf_entry(&file, node, 0);
    assert!(key_len >= 1);
    file[e + 2] -= 1; // first key byte 'k' → 'j' ⇒ below the inherited lo (root.sep[0])
    restamp(&mut file, node);
    assert_reject!(&file, Error::Invariant, "kv key below node bound");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_child_pgno_out_of_range() {
    let mut file = file_kv_branch();
    assert!(Reader::open(&file).is_ok());
    let root = first_scope_kv_root(&file);
    assert_eq!(page_type(&file, root), spec::PAGE_TYPE_KV_BRANCH);
    let total = total_pages_of(&file);
    let o = root * PAGE_SIZE + spec::PAGE_HEADER_SIZE; // leftmost child := total_pages
    file[o..o + 4].copy_from_slice(&(total as u32).to_le_bytes());
    restamp(&mut file, root);
    assert_reject!(&file, Error::Structural, "kv child pgno out of range");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_unexpected_page_type() {
    let mut file = file_inline_text_kv();
    assert!(Reader::open(&file).is_ok());
    let leaf = first_scope_kv_root(&file);
    file[leaf * PAGE_SIZE + spec::PH_PAGE_TYPE] = 99; // neither KV_BRANCH (6) nor KV_LEAF (7)
    restamp(&mut file, leaf);
    assert_reject!(&file, Error::Structural, "kv unexpected page_type");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_separator_ge_hi() {
    // A nested KV branch: shrink the root's separator 0's first byte ('k' → 'j'), so the leftmost
    // child's inherited hi drops below its real separators ("key…" >= "jey…") ⇒ "separator >= hi".
    // Unreachable at the root (hi == None); needs the 3-level KV tree.
    let mut file = file_kv_branch();
    assert!(Reader::open(&file).is_ok());
    let root = first_scope_kv_root(&file);
    assert_eq!(page_type(&file, root), spec::PAGE_TYPE_KV_BRANCH);
    assert_eq!(
        page_type(&file, leftmost_child(&file, root)),
        spec::PAGE_TYPE_KV_BRANCH,
        "child[0] must be a nested branch"
    );
    let sep0 = kv_slot_off(&file, root, 0, true);
    file[sep0 + 2] -= 1; // first separator-key byte 'k' → 'j' (still < sep[1])
    restamp(&mut file, root);
    assert_reject!(&file, Error::Invariant, "kv separator >= hi");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_separator_le_lo() {
    // A nested KV branch: grow the root's LAST separator's first byte ('k' → 'l'), so the last
    // child's inherited lo rises above its real separators ("key…" <= "ley…") ⇒ "separator <= lo".
    let mut file = file_kv_branch();
    assert!(Reader::open(&file).is_ok());
    let root = first_scope_kv_root(&file);
    assert_eq!(page_type(&file, root), spec::PAGE_TYPE_KV_BRANCH);
    let count = entry_count(&file, root) as usize;
    assert!(count >= 1);
    let last = kv_slot_off(&file, root, count - 1, true);
    file[last + 2] += 1; // first separator-key byte 'k' → 'l' (> sep[count-2], no successor)
    restamp(&mut file, root);
    assert_reject!(&file, Error::Invariant, "kv separator <= lo");
}
*/

// TODO: re-enable when scope/KV metadata APIs are re-implemented (v4.3 Phase 4c)
/*
#[test]
fn kv_leaves_at_differing_depths() {
    // Redirect the root's leftmost child from its depth-2 branch B0 to B0's leftmost depth-3
    // leaf L. L (smallest keys) validates at depth 2 ⇒ leaf_depth = 2; the next subtree's leaves
    // are still at depth 3 ⇒ reject. B0 is orphaned, so the disjointness guard does not fire.
    let mut file = file_kv_branch();
    assert!(Reader::open(&file).is_ok());
    let root = first_scope_kv_root(&file);
    assert_eq!(page_type(&file, root), spec::PAGE_TYPE_KV_BRANCH);
    let b0 = leftmost_child(&file, root);
    assert_eq!(page_type(&file, b0), spec::PAGE_TYPE_KV_BRANCH);
    let l = leftmost_child(&file, b0);
    assert_eq!(page_type(&file, l), spec::PAGE_TYPE_KV_LEAF);
    let o = root * PAGE_SIZE + spec::PAGE_HEADER_SIZE; // root's leftmost child := L
    file[o..o + 4].copy_from_slice(&(l as u32).to_le_bytes());
    restamp(&mut file, root);
    assert_reject!(&file, Error::Invariant, "kv leaves at differing depths");
}
*/
