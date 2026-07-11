use iprange_livedb::{Ipv4Key, Writer, Reader};
use iprange_livedb::page_store::{PageStore, VecPageStore};

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

    for cycle in 0..5 {
        let store: Box<dyn PageStore> = Box::new(VecPageStore::new(img.clone()));
        let mut w = Writer::<Ipv4Key>::open(store).unwrap();
        for i in 0..5_000u32 { w.delete(Ipv4Key(i * 2), Ipv4Key(i * 2 + 1)).unwrap(); }
        for i in 0..5_000u32 { w.set(Ipv4Key(i * 2), Ipv4Key(i * 2 + 1), i).unwrap(); }
        w.commit(cycle as u64 + 1).unwrap();
        img = w.into_image().unwrap();
    }
    
    let r = Reader::open(&img).unwrap();
    assert_eq!(r.record_count(), 5_000);
    for i in 0..5_000u32 {
        assert_eq!(r.lookup(Ipv4Key(i * 2)).unwrap(), Some(i), "mismatch at {}", i);
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
