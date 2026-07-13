//! Audit round 3: hardening tests.
//!
//! I3 — Validate() must verify scope-table per-page CRC.
//! I4 — Writer::open must reject corrupt scope-table and free-list pages.

use iprange_livedb::crc32c::verify_page;
use iprange_livedb::page_store::VecPageStore;
use iprange_livedb::spec;
use iprange_livedb::{Ipv4Key, Reader, Writer};

const PAGE_SIZE: usize = spec::PAGE_SIZE;

fn build_mode2_image() -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_INDIRECT, 0).unwrap();
    for i in 0..10u32 {
        let mut bm = vec![0u8; 32];
        bm[(i / 8) as usize] |= 1u8 << (i % 8);
        w.scope_intern(&bm).unwrap();
    }
    w.set(Ipv4Key(1), Ipv4Key(10), 1).unwrap();
    w.commit(1, u64::MAX).unwrap();
    w.into_image().unwrap()
}

fn build_scalar_with_freelist_image() -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_SCALAR, 0).unwrap();
    for i in 0..100u32 {
        w.set(Ipv4Key(i), Ipv4Key(i), 7).unwrap();
    }
    w.commit(1, u64::MAX).unwrap();
    // Delete ~half to create free-list entries, then commit.
    for i in 0..50u32 {
        w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap();
    }
    w.commit(2, u64::MAX).unwrap();
    w.into_image().unwrap()
}

/// Find the first page of the given type at or after page 2, and flip a body
/// byte to break its CRC. Returns true if a page was corrupted.
fn corrupt_first_page_of_type(img: &mut [u8], page_type: u8, body_offset: usize) -> bool {
    let npages = img.len() / PAGE_SIZE;
    for p in 2..npages {
        let off = p * PAGE_SIZE;
        if img[off] == page_type {
            img[off + body_offset] ^= 0xFF;
            return true;
        }
    }
    false
}

// ── I3: Validate() must verify scope-table per-page CRC ──────────────────────

#[test]
fn i3_validate_rejects_corrupt_scope_table() {
    let img = build_mode2_image();

    // Sanity: the clean image validates.
    {
        let r = Reader::open(&img[..]).unwrap();
        r.validate().expect("clean file must validate");
    }

    // Corrupt a scope-leaf body byte (past the header).
    let mut bad = img.clone();
    let corrupted = corrupt_first_page_of_type(
        &mut bad,
        spec::PAGE_TYPE_SCOPE_LEAF,
        spec::PAGE_HEADER_SIZE + 5,
    );
    assert!(corrupted, "test setup: no scope-leaf page found");

    // Validate MUST reject the corrupt scope page.
    let r = Reader::open(&bad[..]).unwrap();
    let result = r.validate();
    assert!(
        result.is_err(),
        "Validate accepted a corrupt scope-table page (CRC not checked)"
    );
}

// ── I4: Writer::open must reject corrupt scope-table and free-list pages ─────

#[test]
fn i4_open_writer_rejects_corrupt_scope_table() {
    let img = build_mode2_image();
    let mut bad = img.clone();
    let corrupted = corrupt_first_page_of_type(
        &mut bad,
        spec::PAGE_TYPE_SCOPE_LEAF,
        spec::PAGE_HEADER_SIZE + 5,
    );
    assert!(corrupted, "no scope-leaf page to corrupt");

    let store = VecPageStore::new(bad);
    let result = Writer::<Ipv4Key>::open(Box::new(store));
    assert!(
        result.is_err(),
        "Writer::open accepted a file with a corrupt scope-table page"
    );
}

#[test]
fn i4_open_writer_rejects_corrupt_free_list_chain() {
    let img = build_scalar_with_freelist_image();
    let mut bad = img.clone();
    // Corrupt a byte in the TXN_FREE freed-page array region.
    let corrupted = corrupt_first_page_of_type(
        &mut bad,
        spec::PAGE_TYPE_TXN_FREE,
        spec::TXN_FREE_ARRAY + 4,
    );
    assert!(corrupted, "no free-list chain page to corrupt");

    let store = VecPageStore::new(bad);
    let result = Writer::<Ipv4Key>::open(Box::new(store));
    assert!(
        result.is_err(),
        "Writer::open accepted a file with a corrupt free-list chain page"
    );
}

// Verify the corrupted page actually fails CRC (sanity for the test setup).
#[test]
fn i4_corruption_actually_breaks_crc() {
    let img = build_scalar_with_freelist_image();
    let mut bad = img.clone();
    let npages = bad.len() / PAGE_SIZE;
    for p in 2..npages {
        let off = p * PAGE_SIZE;
        if bad[off] == spec::PAGE_TYPE_TXN_FREE {
            // Before corruption: CRC valid.
            assert!(verify_page(&bad[off..off + PAGE_SIZE]), "pre-corrupt CRC");
            bad[off + spec::TXN_FREE_ARRAY + 4] ^= 0xFF;
            // After corruption: CRC invalid.
            assert!(!verify_page(&bad[off..off + PAGE_SIZE]), "post-corrupt CRC");
            return;
        }
    }
    panic!("no TXN_FREE page found");
}

// ── I1: FileWriter::commit must hold the reader-table lock ───────────────────
//
// Deterministically verify that commit acquires LOCK_SH on the reader
// companion file for the duration of the commit. While an external LOCK_EX is
// held, commit MUST block; releasing MUST let it proceed. Without the I1 fix,
// commit would complete immediately (no lock), allowing a reader to register
// between the oldest-txn query and the meta flip.
#[cfg(feature = "os")]
#[test]
fn i1_commit_acquires_reader_table_lock() {
    use iprange_livedb::os::FileWriter;
    use std::os::unix::io::AsRawFd;

    let path = std::env::temp_dir().join(format!(
        "iprange_i1_lock_{}.iprdb", std::process::id(),
    ));
    let _ = std::fs::remove_file(&path);
    let _ = std::fs::remove_file(path.with_extension("iprdb.readers"));

    // txn 1: create with data.
    {
        let mut w = FileWriter::<Ipv4Key>::create(&path, 0, 0).unwrap();
        w.set(Ipv4Key(1), Ipv4Key(10), 7).unwrap();
        w.commit(1).unwrap();
        w.close();
    }

    let mut w = FileWriter::<Ipv4Key>::open(&path).unwrap();

    // Take LOCK_EX on the companion file.
    let readers_path = path.with_extension("iprdb.readers");
    let lock_file = std::fs::OpenOptions::new()
        .read(true).write(true).open(&readers_path).unwrap();
    unsafe {
        assert_eq!(libc::flock(lock_file.as_raw_fd(), libc::LOCK_EX), 0,
            "test setup: failed to take LOCK_EX");
    }

    // Commit must BLOCK while the lock is held. Move the writer into a thread.
    let done = std::sync::Arc::new(std::sync::Mutex::new(false));
    let done2 = done.clone();
    let handle = std::thread::spawn(move || {
        let result = w.commit(2);
        *done2.lock().unwrap() = true;
        result
    });

    // Wait 500ms — the commit should still be blocked.
    std::thread::sleep(std::time::Duration::from_millis(500));
    let blocked = !*done.lock().unwrap();
    assert!(blocked, "commit did not block while reader table was locked — lock not acquired");

    // Release the LOCK_EX; commit should now complete.
    unsafe { libc::flock(lock_file.as_raw_fd(), libc::LOCK_UN); }
    drop(lock_file);

    let result = handle.join().expect("commit thread panicked");
    result.expect("commit failed after lock release");

    let _ = std::fs::remove_file(&path);
    let _ = std::fs::remove_file(path.with_extension("iprdb.readers"));
}
