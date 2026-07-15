use iprange_livedb::page_store::VecPageStore;
use iprange_livedb::scope_table::MAX_BITMAP_WIDTH;
use iprange_livedb::spec;
use iprange_livedb::wire::{finalize_checksum, Meta, PageHeader};
use iprange_livedb::{Ipv4Key, Ipv6Key, Writer};

fn active_meta(image: &[u8]) -> Meta {
    let first = Meta::decode(&image[..spec::PAGE_SIZE]);
    let second = Meta::decode(&image[spec::PAGE_SIZE..2 * spec::PAGE_SIZE]);
    if first.txn_id >= second.txn_id {
        first
    } else {
        second
    }
}

fn u32_at(bytes: &[u8], offset: usize) -> u32 {
    u32::from_le_bytes(bytes[offset..offset + 4].try_into().unwrap())
}

fn put_u32(bytes: &mut [u8], offset: usize, value: u32) {
    bytes[offset..offset + 4].copy_from_slice(&value.to_le_bytes());
}

fn indirect_free_list_fixture() -> (Vec<u8>, Meta, u32, u32, u32) {
    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_INDIRECT, 0).unwrap();
    let id = writer
        .scope_intern(&vec![1u8; MAX_BITMAP_WIDTH + 1])
        .unwrap();
    for i in 1..20u8 {
        writer.scope_intern(&[i, 1]).unwrap();
    }
    for i in 0..800u32 {
        writer.set(Ipv4Key(i * 2), Ipv4Key(i * 2), id).unwrap();
    }
    writer.commit(1, u64::MAX).unwrap();
    for i in 100..300u32 {
        writer.delete(Ipv4Key(i * 2), Ipv4Key(i * 2)).unwrap();
    }
    writer.commit(2, u64::MAX).unwrap();
    let image = writer.into_image().unwrap();
    let meta = active_meta(&image);
    assert_ne!(meta.free_list_head, 0);
    assert_ne!(meta.root_pgno, 0);
    assert_ne!(meta.scope_table_root, 0);

    let data_base = meta.root_pgno as usize * spec::PAGE_SIZE;
    let data_root = &image[data_base..data_base + spec::PAGE_SIZE];
    assert_eq!(
        PageHeader::decode(data_root).page_type,
        spec::PAGE_TYPE_BRANCH
    );
    let data_child = u32_at(data_root, spec::PAGE_HEADER_SIZE);

    let scope_base = meta.scope_table_root as usize * spec::PAGE_SIZE;
    let scope_root = &image[scope_base..scope_base + spec::PAGE_SIZE];
    assert_eq!(
        PageHeader::decode(scope_root).page_type,
        spec::PAGE_TYPE_SCOPE_BRANCH
    );
    let scope_child = u32_at(scope_root, spec::PAGE_HEADER_SIZE);
    let child_base = scope_child as usize * spec::PAGE_SIZE;
    let scope_leaf = &image[child_base..child_base + spec::PAGE_SIZE];
    assert_eq!(
        PageHeader::decode(scope_leaf).page_type,
        spec::PAGE_TYPE_SCOPE_LEAF
    );
    let overflow = u32_at(scope_leaf, spec::PAGE_HEADER_SIZE + 10);
    assert_ne!(overflow, 0);
    (image, meta, data_child, scope_child, overflow)
}

#[test]
fn free_list_rejects_every_reachable_page_class() {
    let (base, meta, data_child, scope_child, overflow) = indirect_free_list_fixture();
    let cases = [
        ("data-root", meta.root_pgno),
        ("data-child", data_child),
        ("scope-root", meta.scope_table_root),
        ("scope-child", scope_child),
        ("scope-overflow", overflow),
        ("free-list-chain", meta.free_list_head),
    ];
    let mut accepted = Vec::new();
    for (name, pgno) in cases {
        let mut image = base.clone();
        let base = meta.free_list_head as usize * spec::PAGE_SIZE;
        let free_page = &mut image[base..base + spec::PAGE_SIZE];
        assert!(u32_at(free_page, spec::TXN_FREE_COUNT) > 0);
        put_u32(free_page, spec::TXN_FREE_ARRAY, pgno);
        finalize_checksum(free_page);
        if Writer::<Ipv4Key>::open(Box::new(VecPageStore::new(image))).is_ok() {
            accepted.push((name, pgno));
        }
    }
    assert!(
        accepted.is_empty(),
        "writable open accepted reachable pages as free: {accepted:?}"
    );
}

#[test]
fn free_list_rejects_reachable_ipv6_non_first_child() {
    let mut writer = Writer::<Ipv6Key>::create(spec::SCOPE_MODE_SCALAR, 0).unwrap();
    for i in 0..800u64 {
        let ip = Ipv6Key { hi: 1, lo: i * 2 };
        writer.set(ip, ip, 1).unwrap();
    }
    writer.commit(1, u64::MAX).unwrap();
    for i in 100..300u64 {
        let ip = Ipv6Key { hi: 1, lo: i * 2 };
        writer.delete(ip, ip).unwrap();
    }
    writer.commit(2, u64::MAX).unwrap();
    let mut image = writer.into_image().unwrap();
    let meta = active_meta(&image);
    assert_ne!(meta.free_list_head, 0);
    assert_ne!(meta.root_pgno, 0);

    let data_base = meta.root_pgno as usize * spec::PAGE_SIZE;
    let data_root = &image[data_base..data_base + spec::PAGE_SIZE];
    let header = PageHeader::decode(data_root);
    assert_eq!(header.page_type, spec::PAGE_TYPE_BRANCH);
    assert!(header.entry_count >= 1);
    let child_one_offset = spec::PAGE_HEADER_SIZE + 4 + 16;
    let reachable_child = u32_at(data_root, child_one_offset);

    let free_base = meta.free_list_head as usize * spec::PAGE_SIZE;
    let free_page = &mut image[free_base..free_base + spec::PAGE_SIZE];
    assert!(u32_at(free_page, spec::TXN_FREE_COUNT) > 0);
    put_u32(free_page, spec::TXN_FREE_ARRAY, reachable_child);
    finalize_checksum(free_page);

    assert!(
        Writer::<Ipv6Key>::open(Box::new(VecPageStore::new(image))).is_err(),
        "writable open accepted reachable IPv6 non-first child page {reachable_child} as authoritative free state"
    );
}
