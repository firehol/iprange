//! Issue 2: file-backed reopen cycles must not grow without bound.
//!
//! Root cause: when Commit allocates a free-list chain page in the growth
//! region with no trailing free pages to reclaim (trailing == 0), the committed
//! total did not include the chain page. After close+reopen, committed_pages
//! was smaller than the chain page position, so `load_free_list` silently
//! dropped the free-list head and the freed pages were never reclaimed — the
//! file grew ~1 page per cycle.
//!
//! Fix: (1) Commit must include the highest chain page in committed_pages even
//! when trailing == 0. (2) `FileWriter::close` must truncate the file to exactly
//! `committed_pages * PAGE_SIZE` so no stale chain pages linger across reopen.

#![cfg(feature = "os")]

use iprange_livedb::key::Ipv4Key;
use iprange_livedb::os::FileWriter;

#[test]
fn reopen_cycle_does_not_grow_unbounded() {
    let path = std::env::temp_dir().join(format!(
        "iprange_issue2_reopen_{}.iprdb",
        std::process::id()
    ));
    let _ = std::fs::remove_file(&path);
    let _ = std::fs::remove_file(path.with_extension("iprdb.readers"));

    // Initial create: 1 record, commit, close.
    {
        let mut w = FileWriter::<Ipv4Key>::create(&path, 0, 0).unwrap();
        w.set(Ipv4Key(0), Ipv4Key(3), 1).unwrap();
        w.commit(1).unwrap();
        w.close();
    }

    // 201 reopen cycles: each sets [0,3] with a new scope, commits, closes.
    for i in 1u32..=201 {
        let mut w = FileWriter::<Ipv4Key>::open(&path)
            .unwrap_or_else(|e| panic!("cycle {} open: {}", i, e));
        w.set(Ipv4Key(0), Ipv4Key(3), i + 1).unwrap();
        w.commit(i as u64 + 1).unwrap();
        w.close();
    }

    // The file MUST stay bounded: a 1-record DB is a handful of live pages plus
    // a small free-list chain (compacted once it reaches 20 chain pages). 80
    // pages is a generous upper bound that still catches unbounded growth.
    let len = std::fs::metadata(&path).map(|m| m.len()).unwrap();
    let page_size = 4096usize;
    let pages = (len as usize) / page_size;
    assert!(
        pages <= 80,
        "file grew to {} pages after 201 reopen cycles (bound 80) — free-list chain was lost across reopen",
        pages
    );

    // Data correctness: the last scope written must be readable.
    {
        use iprange_livedb::os::MmapReader;
        let rdr = MmapReader::open(&path).unwrap();
        let r = rdr.reader().unwrap();
        assert_eq!(
            r.lookup(Ipv4Key(1)).unwrap(),
            Some(202),
            "lookup after reopen cycles"
        );
    }

    let _ = std::fs::remove_file(&path);
    let _ = std::fs::remove_file(path.with_extension("iprdb.readers"));
}
