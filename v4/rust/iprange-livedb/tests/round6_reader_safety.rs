use iprange_livedb::spec;
use iprange_livedb::wire::{finalize_checksum, Meta, PageHeader};
use iprange_livedb::{Ipv4Key, Ipv6Key, Reader, Writer};

fn cyclic_reader_image() -> Vec<u8> {
    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_SCALAR, 0).unwrap();
    for i in 0..800u32 {
        let ip = Ipv4Key(i * 2);
        writer.append(ip, ip, 1).unwrap();
    }
    writer.commit(1, u64::MAX).unwrap();
    let mut image = writer.into_image().unwrap();
    let first = Meta::decode(&image[..spec::PAGE_SIZE]);
    let second = Meta::decode(&image[spec::PAGE_SIZE..2 * spec::PAGE_SIZE]);
    let active = if first.txn_id >= second.txn_id {
        first
    } else {
        second
    };
    let root_base = active.root_pgno as usize * spec::PAGE_SIZE;
    let root = &mut image[root_base..root_base + spec::PAGE_SIZE];
    let header = PageHeader::decode(root);
    assert_eq!(header.page_type, spec::PAGE_TYPE_BRANCH);
    assert!(active.tree_height >= 2);
    root[spec::PAGE_HEADER_SIZE..spec::PAGE_HEADER_SIZE + 4]
        .copy_from_slice(&active.root_pgno.to_le_bytes());
    finalize_checksum(root);
    image
}

#[test]
fn reader_lookup_rejects_cyclic_branch_instead_of_returning_fabricated_data() {
    let image = cyclic_reader_image();
    let reader = Reader::open(&image).unwrap();
    assert_eq!(
        reader.lookup_v4(Ipv4Key(100)).unwrap(),
        None,
        "lookup returned fabricated data from a cyclic branch interpreted as a leaf"
    );
}

#[test]
fn reader_scan_rejects_cyclic_branch() {
    let image = cyclic_reader_image();
    let reader = Reader::open(&image).unwrap();
    assert!(
        reader.scan_v4(|_, _, _| {}).is_err(),
        "scan accepted a cyclic branch and interpreted branch bytes as records"
    );
}

#[test]
fn reader_rejects_cross_family_lookup_and_scan() {
    let mut v4 = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_SCALAR, 0).unwrap();
    v4.append(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
    v4.commit(1, u64::MAX).unwrap();
    let v4_image = v4.into_image().unwrap();
    let v4_reader = Reader::open(&v4_image).unwrap();
    assert!(v4_reader.lookup_v6(Ipv6Key { hi: 0, lo: 10 }).is_err());
    let mut v4_called = false;
    assert!(v4_reader
        .scan_v6(|_, _, _| {
            v4_called = true;
        })
        .is_err());
    assert!(!v4_called, "cross-family scan invoked the callback");

    let mut v6 = Writer::<Ipv6Key>::create(spec::SCOPE_MODE_SCALAR, 0).unwrap();
    v6.append(Ipv6Key { hi: 0, lo: 10 }, Ipv6Key { hi: 0, lo: 20 }, 1)
        .unwrap();
    v6.commit(1, u64::MAX).unwrap();
    let v6_image = v6.into_image().unwrap();
    let v6_reader = Reader::open(&v6_image).unwrap();
    assert!(v6_reader.lookup_v4(Ipv4Key(10)).is_err());
    let mut v6_called = false;
    assert!(v6_reader
        .scan_v4(|_, _, _| {
            v6_called = true;
        })
        .is_err());
    assert!(!v6_called, "cross-family scan invoked the callback");
}

#[test]
fn reader_scope_apis_do_not_panic_on_malformed_entry_count() {
    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_INDIRECT, 0).unwrap();
    let id = writer.scope_intern(&[1]).unwrap();
    writer.commit(1, u64::MAX).unwrap();
    let mut image = writer.into_image().unwrap();
    let first = Meta::decode(&image[..spec::PAGE_SIZE]);
    let second = Meta::decode(&image[spec::PAGE_SIZE..2 * spec::PAGE_SIZE]);
    let active = if first.txn_id >= second.txn_id {
        first
    } else {
        second
    };
    let scope_base = active.scope_table_root as usize * spec::PAGE_SIZE;
    let scope = &mut image[scope_base..scope_base + spec::PAGE_SIZE];
    assert_eq!(
        PageHeader::decode(scope).page_type,
        spec::PAGE_TYPE_SCOPE_LEAF
    );
    scope[spec::PH_ENTRY_COUNT..spec::PH_ENTRY_COUNT + 2].copy_from_slice(&u16::MAX.to_le_bytes());
    finalize_checksum(scope);
    let reader = Reader::open(&image).unwrap();

    let resolved = std::panic::catch_unwind(|| reader.scope_resolve(id));
    let listed = std::panic::catch_unwind(|| reader.scope_list());
    assert!(
        matches!(resolved, Ok(None)),
        "scope_resolve panicked or returned data from a malformed scope leaf: {resolved:?}"
    );
    assert!(
        matches!(listed, Ok(ref entries) if entries.is_empty()),
        "scope_list panicked or returned data from a malformed scope leaf"
    );
}
