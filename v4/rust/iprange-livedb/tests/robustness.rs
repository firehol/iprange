//! Hostile-input robustness (§9 / §10): `Reader::open` must return only `Ok` or a typed
//! `Err` — **never** panic, loop, or read out of bounds — on truncations, bit-flips, and
//! arbitrary buffers. (`Writer::open_image` runs the same validation, so this also
//! guards the writer's open path.)

use iprange_livedb::crc32c;
use iprange_livedb::spec::{self, PAGE_SIZE};
use iprange_livedb::{Ipv4Key, Reader, Writer};

/// A multi-level valid file with some freed (unreachable) pages, to exercise both the
/// reachable-page reject path and the unreachable-page ignore path.
fn valid_file() -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::create(1, 0);
    for i in 0..2000u32 {
        w.set(Ipv4Key(i * 7), Ipv4Key(i * 7 + 2), &[(i & 0xff) as u8])
            .unwrap();
    }
    for i in (0..2000u32).step_by(5) {
        w.delete(Ipv4Key(i * 7), Ipv4Key(i * 7 + 2)).unwrap(); // frees pages
    }
    w.commit(0).unwrap();
    w.into_image()
}

/// A valid v4.1 file carrying the full metadata surface: an IP tree, a multi-level scope
/// table, per-scope KV (inline + a multi-page overflow chain), and FILE (scope 0) KV. Used
/// to fuzz the v4.1 validation paths (scope-table walk, KV slot directories, overflow
/// read-by-count) against truncations / bit-flips / arbitrary buffers.
fn valid_file_with_kv() -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::create(1, 0);
    for i in 0..400u32 {
        w.set(Ipv4Key(i * 11), Ipv4Key(i * 11 + 3), &[(i & 0xff) as u8])
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
    w.into_image()
}

fn lcg(state: &mut u64) -> u64 {
    *state = state
        .wrapping_mul(6364136223846793005)
        .wrapping_add(1442695040888963407);
    *state >> 33
}

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
            let _ = Writer::<Ipv4Key>::open_image(g);
        }
    }
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
    r0.scan_v4(|a, b, sc| base.push((a.0, b.0, sc.to_vec())))
        .unwrap();

    let two = 2 * PAGE_SIZE;
    let mut s = 0xdead_beef_cafe_babeu64;
    for _ in 0..4000 {
        let pos = two + lcg(&mut s) as usize % (f.len() - two);
        let bit = (lcg(&mut s) & 7) as u8;
        let mut g = f.clone();
        g[pos] ^= 1 << bit;
        if let Ok(r) = Reader::open(&g) {
            let mut got = Vec::new();
            r.scan_v4(|a, b, sc| got.push((a.0, b.0, sc.to_vec())))
                .unwrap();
            assert_eq!(got, base, "accepted a corrupted reachable tree (pos {pos})");
        }
    }
}

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
        r0.scan_v4(|a, b, sc| ip0.push((a.0, b.0, sc.to_vec())))
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
            let mut ip = Vec::new();
            r.scan_v4(|a, b, sc| ip.push((a.0, b.0, sc.to_vec())))
                .unwrap();
            assert_eq!(
                ip, ip0,
                "accepted a corrupted reachable IP tree (pos {pos})"
            );
        }
        // Writer (runs the same validation, then exposes KV): any accepted reopen must
        // yield identical scopes + KV.
        if let Ok(w) = Writer::<Ipv4Key>::open_image(g) {
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

// --- F5: structural-mutation fuzz (CRC re-stamped) + deterministic invariant unit tests ---
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

#[test]
fn structural_mutation_fuzz_recrc_never_panics() {
    // Walk every metadata-bearing page, perturb its structural fields (entry_count, slot
    // offsets, overflow descriptors, branch children), re-stamp the CRC, and open. The open
    // MUST be total: only `Ok` or a typed `Err`, never a panic / OOB / loop / wrong answer.
    let base = valid_file_with_kv();
    let total = base.len() / PAGE_SIZE;
    let mut s = 0xfeed_face_dead_beefu64;

    for p in 2..total {
        let pt = page_type(&base, p);
        // Touch the scope/KV/overflow pages only; the IP tree is locked v4.0 (out of scope).
        if !matches!(
            pt,
            spec::PAGE_TYPE_SCOPE_BRANCH
                | spec::PAGE_TYPE_SCOPE_LEAF
                | spec::PAGE_TYPE_KV_BRANCH
                | spec::PAGE_TYPE_KV_LEAF
                | spec::PAGE_TYPE_OVERFLOW
        ) {
            continue;
        }
        // For each page try a spread of structural perturbations.
        for trial in 0..64u64 {
            let mut g = base.clone();
            let bp = p * PAGE_SIZE;
            match trial % 4 {
                0 => {
                    // Corrupt entry_count to an arbitrary u16.
                    let v = (lcg(&mut s) as u16) ^ (trial as u16).wrapping_mul(7);
                    g[bp + spec::PH_ENTRY_COUNT..bp + spec::PH_ENTRY_COUNT + 2]
                        .copy_from_slice(&v.to_le_bytes());
                }
                1 => {
                    // Corrupt the self-pgno field (offset 4..8).
                    let v = lcg(&mut s) as u32;
                    g[bp + 4..bp + 8].copy_from_slice(&v.to_le_bytes());
                }
                2 => {
                    // Corrupt a body word (slot offsets / overflow first_pgno / total_len /
                    // branch child pgnos all live in the body region).
                    let off = bp
                        + spec::PAGE_HEADER_SIZE
                        + (lcg(&mut s) as usize % (PAGE_SIZE - spec::PAGE_HEADER_SIZE - 4));
                    let v = lcg(&mut s) as u32;
                    g[off..off + 4].copy_from_slice(&v.to_le_bytes());
                }
                _ => {
                    // For overflow pages specifically, scramble next_pgno; otherwise a header
                    // byte. Either way exercises the read-by-count chain walk.
                    if pt == spec::PAGE_TYPE_OVERFLOW {
                        let off = bp + spec::OVERFLOW_NEXT_PGNO;
                        let v = lcg(&mut s) as u32;
                        g[off..off + 4].copy_from_slice(&v.to_le_bytes());
                    } else {
                        g[bp + (lcg(&mut s) as usize % spec::PAGE_HEADER_SIZE)] ^= 0x5a;
                    }
                }
            }
            restamp(&mut g, p);
            // Total functions: a typed Result either way, no panic.
            let _ = Reader::open(&g);
            let _ = Writer::<Ipv4Key>::open_image(g);
        }
    }
}

/// A v4.1 file with TWO overflow-backed KV entries on one scope, so the shared-chain PoC can
/// repoint one entry's chain head at the other's. Small keys + large values ⇒ both overflow
/// descriptors land in a single KV leaf.
fn file_two_overflow_entries() -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::create(1, 0);
    let id = w.scope_define(b"s").unwrap();
    let a: Vec<u8> = (0..6000u32).map(|i| (i * 3) as u8).collect();
    let b: Vec<u8> = (0..6000u32).map(|i| (i * 5 + 1) as u8).collect();
    w.meta_set(id, b"a", 9, &a).unwrap();
    w.meta_set(id, b"b", 9, &b).unwrap();
    w.commit(0).unwrap();
    w.into_image()
}

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

    // Must be rejected — never an Ok that returns a wrong answer.
    assert!(
        Reader::open(&file).is_err(),
        "shared overflow chain must be rejected"
    );
    assert!(Writer::<Ipv4Key>::open_image(file).is_err());
}

#[test]
fn duplicate_child_in_scope_branch_rejected() {
    // A scope-table branch (IPv4-branch layout) with two child pgnos pointing at the same
    // page. The file-wide disjointness walk reaches that page twice ⇒ reject (F2).
    // Many scopes force a multi-level scope table (a branch root).
    let mut w = Writer::<Ipv4Key>::create(1, 0);
    for s in 0..40u32 {
        w.scope_define(format!("scope-{s}").as_bytes()).unwrap();
    }
    w.commit(0).unwrap();
    let mut file = w.into_image();
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

    assert!(
        Reader::open(&file).is_err(),
        "duplicate child in scope branch must be rejected"
    );
}

#[test]
fn duplicate_child_in_kv_branch_rejected() {
    // A KV branch with two children at the same pgno. The file-wide disjointness walk reaches
    // that subtree twice ⇒ reject (F2). Many small KV entries force a multi-level KV tree.
    let mut w = Writer::<Ipv4Key>::create(1, 0);
    let id = w.scope_define(b"s").unwrap();
    // Long keys keep the leaf fanout low so the tree branches with a modest entry count.
    for i in 0..400u32 {
        let key = format!("key-{i:08}-{}", "x".repeat(200));
        w.meta_set(id, key.as_bytes(), 0, b"v").unwrap();
    }
    w.commit(0).unwrap();
    let mut file = w.into_image();
    let total = file.len() / PAGE_SIZE;

    let branch = (2..total)
        .find(|&p| page_type(&file, p) == spec::PAGE_TYPE_KV_BRANCH && entry_count(&file, p) >= 1)
        .expect("a KV branch with >= 1 separator");
    // child[0] is the fixed leftmost u32 at +16; child[1] is the trailing u32 of separator 0's
    // heap entry. Set the leftmost child = separator-0's child (alias).
    let bp = branch * PAGE_SIZE;
    let mut sep_off = kv_slot_off(&file, branch, 0, true);
    let sep_len = u16::from_le_bytes([file[sep_off], file[sep_off + 1]]) as usize;
    sep_off += 2 + sep_len; // skip sep_len + sep_key → child pgno (u32)
    let child1 = [
        file[sep_off],
        file[sep_off + 1],
        file[sep_off + 2],
        file[sep_off + 3],
    ];
    let leftmost_off = bp + spec::PAGE_HEADER_SIZE;
    file[leftmost_off..leftmost_off + 4].copy_from_slice(&child1);
    restamp(&mut file, branch);

    assert!(
        Reader::open(&file).is_err(),
        "duplicate child in KV branch must be rejected"
    );
}

#[test]
fn scope_branch_separator_misroute_rejected() {
    // codex Finding 3: a scope-table branch whose separator no longer matches the child
    // boundaries. Separators stay strictly increasing and every CRC is valid, but a lookup
    // would misroute. The validator must confine each child's ids to its separator-derived
    // bound and reject (parity with the IP-tree validator).
    let mut w = Writer::<Ipv4Key>::create(1, 0);
    for s in 0..40u32 {
        w.scope_define(format!("scope-{s}").as_bytes()).unwrap();
    }
    w.commit(0).unwrap();
    let mut file = w.into_image();
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

    assert!(
        Reader::open(&file).is_err(),
        "scope branch with a mismatched separator must be rejected"
    );
}

#[test]
fn kv_branch_separator_misroute_rejected() {
    // codex Finding 3 (KV side): a KV branch whose separator key is shifted below its child's
    // real key range. Separators stay strictly increasing and CRCs are valid, but child[0]'s
    // keys would now fall at/above the (shrunken) separator → misroute. Must be rejected.
    let mut w = Writer::<Ipv4Key>::create(1, 0);
    let id = w.scope_define(b"s").unwrap();
    for i in 0..400u32 {
        let key = format!("key-{i:08}-{}", "x".repeat(200));
        w.meta_set(id, key.as_bytes(), 0, b"v").unwrap();
    }
    w.commit(0).unwrap();
    let mut file = w.into_image();
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

    assert!(
        Reader::open(&file).is_err(),
        "KV branch with a mismatched separator must be rejected"
    );
}

#[test]
fn overflow_total_len_u64_max_rejected() {
    // Patch a KV overflow entry's value_total_len to u64::MAX: a naive div_ceil
    // `(total+payload-1)/payload` wraps to a tiny page count, slips past the chain-length cap,
    // and returns a truncated value on a checksum-valid file. The overflow-safe div_ceil must
    // reject it. Mirrors the Go robustness test (cross-language coverage parity).
    let mut file = {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        let id = w.scope_define(b"s").unwrap();
        let big: Vec<u8> = (0..6000u32).map(|i| (i * 3) as u8).collect();
        w.meta_set(id, b"k", 9, &big).unwrap();
        w.commit(0).unwrap();
        w.into_image()
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
    assert!(
        Reader::open(&file).is_err(),
        "overflow value_total_len = u64::MAX must be rejected"
    );
}

#[test]
fn shared_kv_root_across_scopes_rejected() {
    // Two scope records sharing one kv_root ⇒ the file-wide page-disjointness walk reaches the
    // same KV subtree twice ⇒ structural error (F2). Mirrors the Go robustness test.
    let mut file = {
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        let a = w.scope_define(b"a").unwrap();
        let b = w.scope_define(b"b").unwrap();
        w.meta_set(a, b"ka", 0, b"va").unwrap();
        w.meta_set(b, b"kb", 0, b"vb").unwrap();
        w.commit(0).unwrap();
        w.into_image()
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
    assert!(
        Reader::open(&file).is_err(),
        "shared kv_root across two scopes must be rejected (F2)"
    );
}

#[test]
fn f1_lone_last_child_kv_round_trips() {
    // F1: build a KV tree whose branch level would (pre-fix) leave a final node with a single
    // child. ~1 KiB keys ⇒ low branch fanout, so a careful entry count forces the remainder-1
    // case. The writer must rebalance the last two nodes; the file must open + read back.
    let mut w = Writer::<Ipv4Key>::create(1, 0);
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
    let img = w.into_image();

    // Opens (full structural validation, incl. F2 disjointness + branch count >= 1) and the
    // KV list round-trips exactly.
    let r = Reader::open(&img).expect("F1 large-key KV file must open");
    assert_eq!(r.meta_list(id).unwrap(), expect);
    let w2 = Writer::<Ipv4Key>::open_image(img).unwrap();
    assert_eq!(w2.meta_list(id).unwrap(), expect);
}

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
        let mut w = Writer::<Ipv4Key>::create(1, 0);
        let mut ids = Vec::new();
        for s in 0..n as u32 {
            ids.push(w.scope_define(format!("scope-{s}").as_bytes()).unwrap());
        }
        w.commit(0).unwrap();
        let img = w.into_image();

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
