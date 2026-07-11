use iprange_livedb::{Ipv4Key, Writer, Reader};
use iprange_livedb::page_store::{PageStore, VecPageStore};

#[test]
fn churn_stable_size() {
    // With the bitset-based COW approach, the file should stabilize:
    // pages freed by COW are derived as free at open time, then reused.
    let mut img = {
        let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
        for i in 0..1000u32 { w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap(); }
        w.commit(0).unwrap();
        w.into_image().unwrap()
    };
    let initial_pages = img.len() / 4096;

    for cycle in 0..20 {
        let store: Box<dyn PageStore> = Box::new(VecPageStore::new(img.clone()));
        let mut w = Writer::<Ipv4Key>::open(store).unwrap();
        for i in 0..1000u32 { w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap(); }
        for i in 0..1000u32 { w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap(); }
        w.commit(cycle as u64 + 1).unwrap();
        img = w.into_image().unwrap();
    }

    let final_pages = img.len() / 4096;
    eprintln!("Churn: {} → {} pages over 20 cycles", initial_pages, final_pages);

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
fn append_only_compact() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..10_000u32 { w.append(Ipv4Key(i), Ipv4Key(i), i).unwrap(); }
    w.commit(0).unwrap();
    let img = w.into_image().unwrap();
    let pages = img.len() / 4096;
    assert!(pages < 80, "append-only should be compact: {} pages", pages);
    let r = Reader::open(&img).unwrap();
    assert_eq!(r.record_count(), 10_000);
}
