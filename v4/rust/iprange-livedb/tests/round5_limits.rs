use iprange_livedb::page_store::VecPageStore;
use iprange_livedb::scope_table::{ScopeEntry, ScopeRegistry};
use iprange_livedb::spec;
use iprange_livedb::wire::{finalize_checksum, Meta, PageHeader};
use iprange_livedb::{Ipv4Key, Reader, Writer};
use std::panic::{catch_unwind, AssertUnwindSafe};

fn active_meta(image: &[u8]) -> Meta {
    let first = Meta::decode(&image[..spec::PAGE_SIZE]);
    let second = Meta::decode(&image[spec::PAGE_SIZE..2 * spec::PAGE_SIZE]);
    if first.txn_id >= second.txn_id {
        first
    } else {
        second
    }
}

fn active_meta_page(image: &mut [u8]) -> &mut [u8] {
    let first = Meta::decode(&image[..spec::PAGE_SIZE]);
    let second = Meta::decode(&image[spec::PAGE_SIZE..2 * spec::PAGE_SIZE]);
    if first.txn_id >= second.txn_id {
        &mut image[..spec::PAGE_SIZE]
    } else {
        &mut image[spec::PAGE_SIZE..2 * spec::PAGE_SIZE]
    }
}

fn put_u32(bytes: &mut [u8], offset: usize, value: u32) {
    bytes[offset..offset + 4].copy_from_slice(&value.to_le_bytes());
}

fn put_u64(bytes: &mut [u8], offset: usize, value: u64) {
    bytes[offset..offset + 8].copy_from_slice(&value.to_le_bytes());
}

fn max_scope_id_image() -> Vec<u8> {
    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_INDIRECT, 0).unwrap();
    let id = writer.scope_intern(&[1]).unwrap();
    writer.set(Ipv4Key(10), Ipv4Key(20), id).unwrap();
    writer.commit(1, u64::MAX).unwrap();
    let mut image = writer.into_image().unwrap();
    let meta = active_meta(&image);
    let scope_base = meta.scope_table_root as usize * spec::PAGE_SIZE;
    let scope = &mut image[scope_base..scope_base + spec::PAGE_SIZE];
    assert_eq!(
        PageHeader::decode(scope).page_type,
        spec::PAGE_TYPE_SCOPE_LEAF
    );
    put_u32(scope, spec::PAGE_HEADER_SIZE, u32::MAX);
    finalize_checksum(scope);
    let data_base = meta.root_pgno as usize * spec::PAGE_SIZE;
    let data = &mut image[data_base..data_base + spec::PAGE_SIZE];
    assert_eq!(PageHeader::decode(data).page_type, spec::PAGE_TYPE_LEAF);
    put_u32(data, spec::PAGE_HEADER_SIZE + 8, u32::MAX);
    finalize_checksum(data);
    image
}

#[test]
fn scope_id_exhaustion_returns_error_without_losing_readable_scopes() {
    let image = max_scope_id_image();
    let reader = Reader::open(&image).unwrap();
    reader.validate().unwrap();
    assert_eq!(reader.scope_resolve(u32::MAX).as_deref(), Some(&[1][..]));

    let opened = catch_unwind(AssertUnwindSafe(|| {
        Writer::<Ipv4Key>::open(Box::new(VecPageStore::new(image)))
    }));
    assert!(
        opened.is_ok(),
        "writable open panicked at scope_id exhaustion"
    );
    let mut writer = opened
        .unwrap()
        .expect("max-scope image should remain writable");
    assert_eq!(writer.scope_intern(&[1]).unwrap(), u32::MAX);
    assert!(
        writer.scope_intern(&[2]).is_err(),
        "scope exhaustion minted a reserved/wrapped scope_id"
    );
}

#[test]
fn transaction_id_exhaustion_returns_error_instead_of_wrapping() {
    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_SCALAR, 0).unwrap();
    writer.set(Ipv4Key(1), Ipv4Key(1), 1).unwrap();
    writer.commit(1, u64::MAX).unwrap();
    let mut image = writer.into_image().unwrap();
    let active = active_meta_page(&mut image);
    put_u64(active, spec::META_TXN_ID, u64::MAX);
    finalize_checksum(active);

    let mut writer = Writer::<Ipv4Key>::open(Box::new(VecPageStore::new(image))).unwrap();
    writer.set(Ipv4Key(2), Ipv4Key(2), 2).unwrap();
    let committed = catch_unwind(AssertUnwindSafe(|| writer.commit(2, u64::MAX)));
    assert!(committed.is_ok(), "Commit panicked at txn_id exhaustion");
    assert!(
        committed.unwrap().is_err(),
        "Commit wrapped txn_id after the maximum generation"
    );
}

#[test]
fn scope_registry_can_mint_maximum_id_then_reports_exhaustion() {
    let mut registry = ScopeRegistry::from_entries(vec![ScopeEntry {
        scope_id: u32::MAX - 1,
        bitmap: vec![1],
    }]);
    let minted = catch_unwind(AssertUnwindSafe(|| registry.intern(&[2], &[])));
    assert!(minted.is_ok(), "minting the maximum scope_id panicked");
    let (id, created) = minted.unwrap().expect("maximum scope_id should be legal");
    assert_eq!(id, u32::MAX);
    assert!(created);
    assert!(
        registry.intern(&[3], &[]).is_err(),
        "registry minted a wrapped/reserved scope_id after the maximum"
    );
}

#[test]
fn scope_registry_constructs_at_maximum_id_without_panic() {
    let constructed = catch_unwind(AssertUnwindSafe(|| {
        ScopeRegistry::from_entries(vec![ScopeEntry {
            scope_id: u32::MAX,
            bitmap: vec![1],
        }])
    }));
    assert!(
        constructed.is_ok(),
        "ScopeRegistry::from_entries panicked at maximum scope_id"
    );
    let mut registry = constructed.unwrap();
    let (id, created) = registry.intern(&[1], &[]).unwrap();
    assert_eq!(id, u32::MAX);
    assert!(!created);
    assert!(
        registry.intern(&[2], &[]).is_err(),
        "registry minted a wrapped/reserved scope_id after construction at maximum"
    );
}
