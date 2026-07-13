//! Issue 3: a scope leaf with a checksum-valid but structurally impossible
//! entry_count (e.g. 0xFFFF) must be REJECTED at writer open, not cause a
//! slice-bounds panic when the leaf is later read.
//!
//! The writer's open-time scope validator previously checked only per-page CRC.
//! A corrupt entry_count that still verifies against a recomputed CRC passed
//! the guard; then `read_scope_node`/`find_scope` sliced past the page and
//! panicked. Fix: validate structural integrity (entry_count bounds, page type,
//! child page range) alongside the CRC check.

use iprange_livedb::crc32c;
use iprange_livedb::key::Ipv4Key;
use iprange_livedb::page_store::{PageStore, VecPageStore};
use iprange_livedb::spec;
use iprange_livedb::wire::PageHeader;
use iprange_livedb::Writer;

fn finalize_checksum(page: &mut [u8]) {
    let sum = crc32c::page_checksum(page);
    // Checksum field is a u64 at PH_CHECKSUM (low 4 bytes = CRC, high 4 = 0).
    let bytes = sum.to_le_bytes();
    page[spec::PH_CHECKSUM..spec::PH_CHECKSUM + 8].copy_from_slice(&bytes);
}

#[test]
fn writer_open_rejects_checksum_valid_corrupt_scope_leaf() {
    // Build a mode-2 DB with one interned scope, committed.
    let img = {
        let mut w = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_INDIRECT, 0).unwrap();
        let id = w.scope_intern(&[0b00000001]).unwrap();
        w.set(Ipv4Key(10), Ipv4Key(20), id).unwrap();
        w.commit(0, u64::MAX).unwrap();
        w.into_image().unwrap()
    };

    let mut img = img;

    // Find the scope leaf page (page_type == SCOPE_LEAF). For a 1-scope DB there
    // is exactly one; it sits at page >= 2 (pages 0/1 are meta).
    let mut target_page = None;
    let n_pages = img.len() / spec::PAGE_SIZE;
    for pgno in 2..n_pages {
        let off = pgno * spec::PAGE_SIZE;
        let h = PageHeader::decode(&img[off..off + spec::PAGE_SIZE]);
        if h.page_type == spec::PAGE_TYPE_SCOPE_LEAF {
            target_page = Some(pgno);
            break;
        }
    }
    let pgno = target_page.expect("test DB must contain a scope leaf");
    let off = pgno * spec::PAGE_SIZE;

    // Corrupt entry_count to 0xFFFF (structurally impossible: far beyond the
    // page capacity), then recompute the CRC so the page still "verifies".
    img[off + spec::PH_ENTRY_COUNT] = 0xFF;
    img[off + spec::PH_ENTRY_COUNT + 1] = 0xFF;
    finalize_checksum(&mut img[off..off + spec::PAGE_SIZE]);

    // Sanity: the corrupted page still passes CRC.
    assert!(
        crc32c::verify_page(&img[off..off + spec::PAGE_SIZE]),
        "test setup: corrupted leaf must still pass CRC"
    );

    // Writer::open MUST reject this (structural error), NOT panic.
    let store: Box<dyn PageStore> = Box::new(VecPageStore::new(img));
    let result = Writer::<Ipv4Key>::open(store);
    match result {
        Err(_) => { /* expected: structural corruption rejected */ }
        Ok(w) => panic!(
            "Writer::open accepted a checksum-valid but structurally corrupt scope leaf (entry_count=0xFFFF); it should have been rejected. free_page_count={}",
            w.free_page_count()
        ),
    }
}
