use iprange_livedb::{Ipv4Key, Writer, Reader};
use iprange_livedb::page_store::{PageStore, VecPageStore};

#[test]
fn churn_stable_size() {
    // With safe_reclaim_txn_id = 0 (no active readers), the file must
    // stabilize: old COW victims are reclaimed. Growth is limited to
    // one-time COW overhead.
    let mut img = {
        let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
        for i in 0..1000u32 {
            w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap();
        }
        w.commit(0).unwrap();
        w.into_image().unwrap()
    };
    let initial_pages = img.len() / 4096;

    for cycle in 0..20 {
        let store: Box<dyn PageStore> = Box::new(VecPageStore::new(img.clone()));
        let mut w = Writer::<Ipv4Key>::open(store).unwrap();
        w.set_safe_reclaim_txn_id(0);
        for i in 0..1000u32 {
            w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap();
        }
        for i in 0..1000u32 {
            w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap();
        }
        w.commit(cycle as u64 + 1).unwrap();
        img = w.into_image().unwrap();
    }
    
    let final_pages = img.len() / 4096;
    eprintln!("Initial: {} pages, Final: {} pages", initial_pages, final_pages);
    
    // File must stabilize within 2x the initial size.
    assert!(final_pages <= initial_pages * 2,
        "file grew to {} pages ({}x initial) — reclamation broken",
        final_pages, final_pages / initial_pages);
    
    let r = Reader::open(&img).unwrap();
    assert_eq!(r.record_count(), 1000);
    for i in 0..1000u32 {
        assert_eq!(r.lookup(Ipv4Key(i)).unwrap(), Some(i), "mismatch at {}", i);
    }
}

#[test]
fn churn_large_dataset() {
    let mut img = {
        let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
        for i in 0..10_000u32 {
            w.set(Ipv4Key(i * 2), Ipv4Key(i * 2 + 1), i).unwrap();
        }
        w.commit(0).unwrap();
        w.into_image().unwrap()
    };
    let initial_pages = img.len() / 4096;

    for cycle in 0..10 {
        let store: Box<dyn PageStore> = Box::new(VecPageStore::new(img.clone()));
        let mut w = Writer::<Ipv4Key>::open(store).unwrap();
        w.set_safe_reclaim_txn_id(0);
        // Partial churn: delete and re-add only 20% of records
        for i in 0..10_000u32 {
            if i % 5 == 0 {
                w.delete(Ipv4Key(i * 2), Ipv4Key(i * 2 + 1)).unwrap();
            }
        }
        for i in 0..10_000u32 {
            if i % 5 == 0 {
                w.set(Ipv4Key(i * 2), Ipv4Key(i * 2 + 1), i).unwrap();
            }
        }
        w.commit(cycle as u64 + 1).unwrap();
        img = w.into_image().unwrap();
    }
    
    let final_pages = img.len() / 4096;
    eprintln!("10k records, 20% churn × 10 cycles: {} → {} pages", initial_pages, final_pages);
    assert!(final_pages <= initial_pages * 2,
        "file grew to {} pages — reclamation broken", final_pages);
    
    let r = Reader::open(&img).unwrap();
    assert_eq!(r.record_count(), 10_000);
}

#[test]
fn append_only_compact() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..10_000u32 {
        w.append(Ipv4Key(i), Ipv4Key(i), i).unwrap();
    }
    w.commit(0).unwrap();
    let img = w.into_image().unwrap();
    let pages = img.len() / 4096;
    assert!(pages < 80, "append-only should be compact: {} pages", pages);
    let r = Reader::open(&img).unwrap();
    assert_eq!(r.record_count(), 10_000);
}
