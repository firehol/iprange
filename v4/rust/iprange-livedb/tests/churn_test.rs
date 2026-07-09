use iprange_livedb::{Ipv4Key, Writer, Reader};
use iprange_livedb::page_store::{PageStore, VecPageStore};

#[test]
fn churn_no_readers_stable() {
    // With safe_reclaim_txn_id = 0 (no active readers), the file should
    // stabilize quickly: old COW victims are reclaimed.
    let mut img = {
        let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
        for i in 0..1000u32 {
            w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap();
        }
        w.commit(0).unwrap();
        w.into_image().unwrap()
    };
    let initial_pages = img.len() / 4096;
    eprintln!("Initial: {} pages", initial_pages);

    for cycle in 0..10 {
        let store: Box<dyn PageStore> = Box::new(VecPageStore::new(img.clone()));
        let mut w = Writer::<Ipv4Key>::open(store).unwrap();
        // No active readers → safe to reclaim everything
        w.set_safe_reclaim_txn_id(0);
        for i in 0..1000u32 {
            w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap();
        }
        for i in 0..1000u32 {
            w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap();
        }
        w.commit(cycle as u64 + 1).unwrap();
        w.set_safe_reclaim_txn_id(0); // for the next load_free_list
        img = w.into_image().unwrap();
        if cycle < 5 || cycle == 9 {
            eprintln!("Cycle {}: {} pages", cycle, img.len() / 4096);
        }
    }
    
    let final_pages = img.len() / 4096;
    eprintln!("Final: {} pages (was {} initial)", final_pages, initial_pages);
    
    // The file should NOT grow unboundedly. With reclamation it should
    // stabilize around 2-3x the initial size (COW overhead per cycle).
    assert!(final_pages < initial_pages * 15, 
        "file grew to {} pages ({}x initial) — reclamation not working", 
        final_pages, final_pages / initial_pages);
    
    let r = Reader::open(&img).unwrap();
    assert_eq!(r.record_count(), 1000);
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

#[test]
fn data_integrity_after_churn() {
    let mut img = {
        let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
        for i in 0..5_000u32 {
            w.set(Ipv4Key(i * 2), Ipv4Key(i * 2 + 1), i).unwrap();
        }
        w.commit(0).unwrap();
        w.into_image().unwrap()
    };

    // Churn: delete half, re-add
    for cycle in 0..5 {
        let store: Box<dyn PageStore> = Box::new(VecPageStore::new(img.clone()));
        let mut w = Writer::<Ipv4Key>::open(store).unwrap();
        w.set_safe_reclaim_txn_id(0);
        for i in 0..5_000u32 {
            w.delete(Ipv4Key(i * 2), Ipv4Key(i * 2 + 1)).unwrap();
        }
        for i in 0..5_000u32 {
            w.set(Ipv4Key(i * 2), Ipv4Key(i * 2 + 1), i).unwrap();
        }
        w.commit(cycle as u64 + 1).unwrap();
        img = w.into_image().unwrap();
    }
    
    let r = Reader::open(&img).unwrap();
    assert_eq!(r.record_count(), 5_000);
    for i in 0..5_000u32 {
        assert_eq!(r.lookup(Ipv4Key(i * 2)).unwrap(), Some(i), "mismatch at {}", i);
    }
}
