use iprange_livedb::overlap::{all_to_all_overlap, foreign_vs_all_slice, FeedOverlap};
use iprange_livedb::page_store::VecPageStore;
use iprange_livedb::scope_table::{MAX_BITMAP_WIDTH, SCOPE_BITMAP_OVERFLOW, SCOPE_ENTRY_SIZE};
use iprange_livedb::spec;
use iprange_livedb::wire::{finalize_checksum, Meta, PageHeader};
use iprange_livedb::{Ipv4Key, Ipv6Key, Reader, Writer};
use std::collections::BTreeMap;

const OVERFLOW_PAYLOAD_OFF: usize = spec::PAGE_HEADER_SIZE + 4;
const OVERFLOW_PAYLOAD: usize = spec::PAGE_SIZE - OVERFLOW_PAYLOAD_OFF;

fn bitmap(size: usize) -> Vec<u8> {
    let mut bitmap: Vec<u8> = (0..size).map(|i| ((i * 131 + 17) % 251) as u8).collect();
    if let Some(last) = bitmap.last_mut() {
        *last |= 1;
    }
    bitmap
}

fn active_meta(image: &[u8]) -> Meta {
    let first = Meta::decode(&image[..spec::PAGE_SIZE]);
    let second = Meta::decode(&image[spec::PAGE_SIZE..2 * spec::PAGE_SIZE]);
    if first.txn_id >= second.txn_id {
        first
    } else {
        second
    }
}

fn u16_at(bytes: &[u8], offset: usize) -> u16 {
    u16::from_le_bytes(bytes[offset..offset + 2].try_into().unwrap())
}

fn u32_at(bytes: &[u8], offset: usize) -> u32 {
    u32::from_le_bytes(bytes[offset..offset + 4].try_into().unwrap())
}

fn put_u16(bytes: &mut [u8], offset: usize, value: u16) {
    bytes[offset..offset + 2].copy_from_slice(&value.to_le_bytes());
}

fn put_u32(bytes: &mut [u8], offset: usize, value: u32) {
    bytes[offset..offset + 4].copy_from_slice(&value.to_le_bytes());
}

fn committed_overflow_fixture(bitmap: &[u8]) -> (Vec<u8>, Meta, u32, Vec<u32>) {
    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_INDIRECT, 0).unwrap();
    let scope_id = writer.scope_intern(bitmap).unwrap();
    writer.set(Ipv4Key(10), Ipv4Key(20), scope_id).unwrap();
    writer.commit(1, u64::MAX).unwrap();
    let image = writer.into_image().unwrap();
    let meta = active_meta(&image);
    let leaf_base = meta.scope_table_root as usize * spec::PAGE_SIZE;
    let leaf = &image[leaf_base..leaf_base + spec::PAGE_SIZE];
    assert_eq!(
        PageHeader::decode(leaf).page_type,
        spec::PAGE_TYPE_SCOPE_LEAF
    );
    assert_eq!(
        u16_at(leaf, spec::PAGE_HEADER_SIZE + 4),
        SCOPE_BITMAP_OVERFLOW
    );
    let first = u32_at(leaf, spec::PAGE_HEADER_SIZE + 10);
    assert_ne!(first, 0);
    let mut pages = Vec::new();
    let mut pgno = first;
    while pgno != 0 {
        assert!(!pages.contains(&pgno), "fixture overflow chain cycles");
        pages.push(pgno);
        let base = pgno as usize * spec::PAGE_SIZE;
        pgno = u32_at(&image[base..base + spec::PAGE_SIZE], spec::PAGE_HEADER_SIZE);
    }
    (image, meta, scope_id, pages)
}

fn overflow_rejection(image: &[u8]) -> (bool, bool) {
    let reader_rejected = Reader::open(image)
        .and_then(|reader| reader.validate())
        .is_err();
    let writer_rejected =
        Writer::<Ipv4Key>::open(Box::new(VecPageStore::new(image.to_vec()))).is_err();
    (reader_rejected, writer_rejected)
}

fn require_invalid_overflow(image: Vec<u8>) {
    let (reader_rejected, writer_rejected) = overflow_rejection(&image);
    assert!(
        reader_rejected && writer_rejected,
        "malformed overflow chain accepted: reader_rejected={reader_rejected} writer_rejected={writer_rejected}"
    );
}

#[test]
fn scope_bitmap_round_trips_every_storage_boundary() {
    for size in [
        MAX_BITMAP_WIDTH,
        MAX_BITMAP_WIDTH + 1,
        OVERFLOW_PAYLOAD,
        OVERFLOW_PAYLOAD + 1,
        32 * OVERFLOW_PAYLOAD + 1,
    ] {
        let want = bitmap(size);
        let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_INDIRECT, 0).unwrap();
        let id = writer.scope_intern(&want).unwrap();
        writer.set(Ipv4Key(1), Ipv4Key(1), id).unwrap();
        writer.commit(1, u64::MAX).unwrap();
        let got = writer.scope_resolve(id).unwrap();
        assert!(
            got == want,
            "writer resolved {} bytes, want {size}",
            got.len()
        );
        let image = writer.into_image().unwrap();
        let reader = Reader::open(&image).unwrap();
        reader.validate().unwrap();
        let got = reader.scope_resolve(id).unwrap();
        assert!(
            got == want,
            "reader resolved {} bytes, want {size}",
            got.len()
        );
    }
}

#[test]
fn committed_overflow_scope_participates_in_all_overlap_apis() {
    let mut bits = vec![0u8; MAX_BITMAP_WIDTH + 1];
    bits[0] = 1;
    bits[MAX_BITMAP_WIDTH] = 1;
    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_INDIRECT, 0).unwrap();
    let id = writer.scope_intern(&bits).unwrap();
    writer.set(Ipv4Key(10), Ipv4Key(19), id).unwrap();
    writer.commit(1, u64::MAX).unwrap();

    let mut pairs = Vec::new();
    all_to_all_overlap(&writer, &mut |overlap| pairs.push(overlap)).unwrap();
    assert_eq!(
        pairs,
        vec![FeedOverlap {
            feed_a: 0,
            feed_b: (MAX_BITMAP_WIDTH * 8) as u32,
            ip_count: 10,
        }]
    );

    let mut counts = BTreeMap::new();
    foreign_vs_all_slice(
        &writer,
        &[(Ipv4Key(12), Ipv4Key(15))],
        &mut |feed, _, count| *counts.entry(feed).or_insert(0u64) += count,
    )
    .unwrap();
    assert_eq!(counts.get(&0), Some(&4));
    assert_eq!(counts.get(&((MAX_BITMAP_WIDTH * 8) as u32)), Some(&4));
    assert_eq!(counts.len(), 2);
}

#[test]
fn all_to_all_rejects_accumulated_pair_count_overflow() {
    let mut writer = Writer::<Ipv6Key>::create(spec::SCOPE_MODE_BITMAP, 0).unwrap();
    let half = 1u64 << 63;
    writer
        .set(
            Ipv6Key { hi: 0, lo: 0 },
            Ipv6Key {
                hi: 0,
                lo: half - 1,
            },
            0b11,
        )
        .unwrap();
    writer
        .set(
            Ipv6Key { hi: 1, lo: 0 },
            Ipv6Key {
                hi: 1,
                lo: half - 1,
            },
            0b11,
        )
        .unwrap();
    assert!(
        all_to_all_overlap(&writer, &mut |_| {}).is_err(),
        "two individually representable spans whose sum is 2^64 did not report overflow"
    );
}

#[test]
fn scope_overflow_validation_rejects_malformed_chains() {
    let (one_page, one_meta, _, one_pages) =
        committed_overflow_fixture(&bitmap(MAX_BITMAP_WIDTH + 1));
    let (two_page, two_meta, _, two_pages) =
        committed_overflow_fixture(&bitmap(OVERFLOW_PAYLOAD + 1));
    assert_eq!(one_pages.len(), 1);
    assert_eq!(two_pages.len(), 2);

    let cases: Vec<(&str, Vec<u8>)> = vec![
        ("noncanonical-overflow-length", {
            let mut image = one_page.clone();
            let base = one_meta.scope_table_root as usize * spec::PAGE_SIZE;
            let leaf = &mut image[base..base + spec::PAGE_SIZE];
            put_u32(leaf, spec::PAGE_HEADER_SIZE + 6, MAX_BITMAP_WIDTH as u32);
            finalize_checksum(leaf);
            image
        }),
        ("absurd-persisted-length", {
            let mut image = one_page.clone();
            let base = one_meta.scope_table_root as usize * spec::PAGE_SIZE;
            let leaf = &mut image[base..base + spec::PAGE_SIZE];
            put_u32(leaf, spec::PAGE_HEADER_SIZE + 6, u32::MAX);
            finalize_checksum(leaf);
            image
        }),
        ("zero-first-page", {
            let mut image = one_page.clone();
            let base = one_meta.scope_table_root as usize * spec::PAGE_SIZE;
            let leaf = &mut image[base..base + spec::PAGE_SIZE];
            put_u32(leaf, spec::PAGE_HEADER_SIZE + 10, 0);
            finalize_checksum(leaf);
            image
        }),
        ("bad-overflow-crc", {
            let mut image = one_page.clone();
            let base = one_pages[0] as usize * spec::PAGE_SIZE;
            image[base + OVERFLOW_PAYLOAD_OFF] ^= 0x80;
            image
        }),
        ("nonzero-unused-payload-tail", {
            let mut image = one_page.clone();
            let base = one_pages[0] as usize * spec::PAGE_SIZE;
            let page = &mut image[base..base + spec::PAGE_SIZE];
            page[OVERFLOW_PAYLOAD_OFF + MAX_BITMAP_WIDTH + 1] = 0xa5;
            finalize_checksum(page);
            image
        }),
        ("wrong-page-type", {
            let mut image = one_page.clone();
            let base = one_pages[0] as usize * spec::PAGE_SIZE;
            let page = &mut image[base..base + spec::PAGE_SIZE];
            page[spec::PH_PAGE_TYPE] = spec::PAGE_TYPE_LEAF;
            finalize_checksum(page);
            image
        }),
        ("reserved-header-byte", {
            let mut image = one_page.clone();
            let base = one_pages[0] as usize * spec::PAGE_SIZE;
            let page = &mut image[base..base + spec::PAGE_SIZE];
            page[spec::PH_RESERVED] = 1;
            finalize_checksum(page);
            image
        }),
        ("self-page-number-mismatch", {
            let mut image = one_page.clone();
            let base = one_pages[0] as usize * spec::PAGE_SIZE;
            let page = &mut image[base..base + spec::PAGE_SIZE];
            put_u32(page, spec::PH_PGNO, one_pages[0] + 1);
            finalize_checksum(page);
            image
        }),
        ("nonzero-entry-count", {
            let mut image = one_page.clone();
            let base = one_pages[0] as usize * spec::PAGE_SIZE;
            let page = &mut image[base..base + spec::PAGE_SIZE];
            put_u16(page, spec::PH_ENTRY_COUNT, 1);
            finalize_checksum(page);
            image
        }),
        ("next-page-out-of-bounds", {
            let mut image = one_page.clone();
            let total_pages = (image.len() / spec::PAGE_SIZE) as u32;
            let base = one_pages[0] as usize * spec::PAGE_SIZE;
            let page = &mut image[base..base + spec::PAGE_SIZE];
            put_u32(page, spec::PAGE_HEADER_SIZE, total_pages);
            finalize_checksum(page);
            image
        }),
        ("cycle-before-required-length", {
            let mut image = two_page.clone();
            let base = two_pages[0] as usize * spec::PAGE_SIZE;
            let page = &mut image[base..base + spec::PAGE_SIZE];
            put_u32(page, spec::PAGE_HEADER_SIZE, two_pages[0]);
            finalize_checksum(page);
            image
        }),
        ("early-chain-termination", {
            let mut image = two_page.clone();
            let base = two_pages[0] as usize * spec::PAGE_SIZE;
            let page = &mut image[base..base + spec::PAGE_SIZE];
            put_u32(page, spec::PAGE_HEADER_SIZE, 0);
            finalize_checksum(page);
            image
        }),
        ("extra-page-after-declared-length", {
            let mut image = two_page.clone();
            let base = two_meta.scope_table_root as usize * spec::PAGE_SIZE;
            let leaf = &mut image[base..base + spec::PAGE_SIZE];
            put_u32(
                leaf,
                spec::PAGE_HEADER_SIZE + 6,
                (MAX_BITMAP_WIDTH + 1) as u32,
            );
            finalize_checksum(leaf);
            image
        }),
    ];

    let mut accepted = Vec::new();
    for (name, image) in cases {
        let (reader_rejected, writer_rejected) = overflow_rejection(&image);
        if !reader_rejected || !writer_rejected {
            accepted.push((name, reader_rejected, writer_rejected));
        }
    }
    assert!(
        accepted.is_empty(),
        "malformed chains accepted: {accepted:?}"
    );
}

#[test]
fn scope_overflow_page_cannot_be_owned_by_two_scopes() {
    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_INDIRECT, 0).unwrap();
    let first_id = writer.scope_intern(&bitmap(MAX_BITMAP_WIDTH + 1)).unwrap();
    let mut second = bitmap(MAX_BITMAP_WIDTH + 2);
    second[0] ^= 0x40;
    let second_id = writer.scope_intern(&second).unwrap();
    writer.set(Ipv4Key(1), Ipv4Key(1), first_id).unwrap();
    writer.set(Ipv4Key(3), Ipv4Key(3), second_id).unwrap();
    writer.commit(1, u64::MAX).unwrap();
    let mut image = writer.into_image().unwrap();
    let meta = active_meta(&image);
    let base = meta.scope_table_root as usize * spec::PAGE_SIZE;
    let leaf = &mut image[base..base + spec::PAGE_SIZE];
    let first_overflow = u32_at(leaf, spec::PAGE_HEADER_SIZE + 10);
    assert_ne!(first_overflow, 0);
    assert_ne!(
        u32_at(leaf, spec::PAGE_HEADER_SIZE + SCOPE_ENTRY_SIZE + 10),
        0
    );
    put_u32(
        leaf,
        spec::PAGE_HEADER_SIZE + SCOPE_ENTRY_SIZE + 10,
        first_overflow,
    );
    finalize_checksum(leaf);
    require_invalid_overflow(image);
}

#[test]
fn scope_overflow_page_cannot_alias_live_data() {
    let (mut image, meta, _, _) = committed_overflow_fixture(&bitmap(MAX_BITMAP_WIDTH + 1));
    let base = meta.scope_table_root as usize * spec::PAGE_SIZE;
    let leaf = &mut image[base..base + spec::PAGE_SIZE];
    put_u32(leaf, spec::PAGE_HEADER_SIZE + 10, meta.root_pgno);
    finalize_checksum(leaf);
    require_invalid_overflow(image);
}

#[test]
fn scope_branch_separator_must_equal_right_child_minimum() {
    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_INDIRECT, 0).unwrap();
    for i in 0..20u8 {
        writer.scope_intern(&[i, 1]).unwrap();
    }
    writer.commit(1, u64::MAX).unwrap();
    let mut image = writer.into_image().unwrap();
    let meta = active_meta(&image);
    let base = meta.scope_table_root as usize * spec::PAGE_SIZE;
    let root = &mut image[base..base + spec::PAGE_SIZE];
    let header = PageHeader::decode(root);
    assert_eq!(header.page_type, spec::PAGE_TYPE_SCOPE_BRANCH);
    assert_eq!(header.entry_count, 1);
    let sep_offset = spec::PAGE_HEADER_SIZE + 4;
    let separator = u32_at(root, sep_offset);
    put_u32(root, sep_offset, separator + 1);
    finalize_checksum(root);

    let reader_rejected = Reader::open(&image)
        .and_then(|reader| reader.validate())
        .is_err();
    let writer_rejected =
        Writer::<Ipv4Key>::open(Box::new(VecPageStore::new(image.clone()))).is_err();
    assert!(
        reader_rejected && writer_rejected,
        "scope separator mismatch accepted: reader_rejected={reader_rejected} writer_rejected={writer_rejected}"
    );
}

#[test]
fn overflow_scope_survives_reopen_and_scope_table_rebuild() {
    let want = bitmap(OVERFLOW_PAYLOAD + 1);
    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_INDIRECT, 0).unwrap();
    let id = writer.scope_intern(&want).unwrap();
    writer.set(Ipv4Key(1), Ipv4Key(1), id).unwrap();
    writer.commit(1, u64::MAX).unwrap();
    let mut image = writer.into_image().unwrap();

    for generation in 2..=5u64 {
        let mut writer =
            Writer::<Ipv4Key>::open(Box::new(VecPageStore::new(image.clone()))).unwrap();
        writer.scope_intern(&[generation as u8, 1]).unwrap();
        writer.commit(generation, u64::MAX).unwrap();
        assert_eq!(
            writer.scope_resolve(id).as_deref(),
            Some(want.as_slice()),
            "writer mismatch at generation {generation}"
        );
        image = writer.into_image().unwrap();
        let reader = Reader::open(&image).unwrap();
        reader.validate().unwrap();
        assert_eq!(
            reader.scope_resolve(id).as_deref(),
            Some(want.as_slice()),
            "reader mismatch at generation {generation}"
        );
    }
}

#[test]
fn overflow_scope_rebuild_reclaims_every_long_chain_page() {
    let (image, _, _, old_pages) = committed_overflow_fixture(&bitmap(34 * OVERFLOW_PAYLOAD + 1));
    assert!(old_pages.len() > spec::TREE_HEIGHT_MAX as usize + 1);

    let mut writer = Writer::<Ipv4Key>::open(Box::new(VecPageStore::new(image))).unwrap();
    writer.scope_intern(&[0x55, 0x01]).unwrap();
    writer.commit(2, u64::MAX).unwrap();
    let after = writer.into_image().unwrap();
    let meta = active_meta(&after);
    assert_ne!(meta.free_list_head, 0);
    let store = VecPageStore::new(after);
    let entries = iprange_livedb::free_list::read_chain(&store, meta.free_list_head).unwrap();
    let freed: std::collections::BTreeSet<u32> = entries
        .into_iter()
        .filter(|entry| entry.freed_txn_id != u64::MAX)
        .map(|entry| entry.pgno)
        .collect();
    let missing: Vec<u32> = old_pages
        .iter()
        .copied()
        .filter(|pgno| !freed.contains(pgno))
        .collect();
    assert!(
        missing.is_empty(),
        "scope rebuild orphaned overflow pages {missing:?} from a {}-page chain",
        old_pages.len()
    );
}
