use iprange_livedb::{Ipv4Key, Writer, Reader};
use iprange_livedb::page_store::{PageStore, VecPageStore};

#[test]
fn churn_growth_baseline() {
    // Baseline measurement: file grows with churn until reader coordination
    // (Phase 3 reader registration) is wired into reclamation. This is
    // expected — committed pages can't be safely reused while a reader
    // might be using an older snapshot. A "compact" operation reclaims space.
    let mut img = {
        let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
        for i in 0..1000u32 {
            w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap();
        }
        w.commit(0).unwrap();
        w.into_image().unwrap()
    };
    
    for cycle in 0..5 {
        let store: Box<dyn PageStore> = Box::new(VecPageStore::new(img.clone()));
        let mut w = Writer::<Ipv4Key>::open(store).unwrap();
        for i in 0..1000u32 {
            w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap();
        }
        for i in 0..1000u32 {
            w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap();
        }
        w.commit(cycle as u64 + 1).unwrap();
        img = w.into_image().unwrap();
    }
    
    // Data integrity check
    let r = Reader::open(&img).unwrap();
    assert_eq!(r.record_count(), 1000);
    for i in 0..1000u32 {
        assert_eq!(r.lookup(Ipv4Key(i)).unwrap(), Some(i));
    }
}

#[test]
fn no_churn_stable() {
    // Without churn (append-only), the file size should be proportional to data.
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..10_000u32 {
        w.append(Ipv4Key(i), Ipv4Key(i), i).unwrap();
    }
    w.commit(0).unwrap();
    let img = w.into_image().unwrap();
    
    // 10k records × 12 bytes = 120KB data + overhead ≈ 60 pages
    let pages = img.len() / 4096;
    assert!(pages < 80, "append-only should be compact: {} pages", pages);
    
    let r = Reader::open(&img).unwrap();
    assert_eq!(r.record_count(), 10_000);
}
