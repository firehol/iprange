use iprange_livedb::{Ipv4Key, Writer, Reader};

#[test]
fn stress_10k() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..10_000u32 {
        w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap();
    }
    w.commit(0, u64::MAX).unwrap();
    let img = w.into_image().unwrap();
    let r = Reader::open(&img).unwrap();
    assert_eq!(r.record_count(), 10_000);
}

#[test]
fn stress_200k_branch_split() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..200_000u32 {
        w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap();
    }
    w.commit(0, u64::MAX).unwrap();
    let img = w.into_image().unwrap();
    let r = Reader::open(&img).unwrap();
    assert_eq!(r.record_count(), 200_000);
    // Verify samples
    assert_eq!(r.lookup(Ipv4Key(0)).unwrap(), Some(0));
    assert_eq!(r.lookup(Ipv4Key(100_000)).unwrap(), Some(100_000));
    assert_eq!(r.lookup(Ipv4Key(199_999)).unwrap(), Some(199_999));
    assert_eq!(r.lookup(Ipv4Key(200_000)).unwrap(), None); // past end
}

#[test]
fn stress_500k_deep_tree() {
    // 500k records → multi-level branch splits
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..500_000u32 {
        w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap();
    }
    w.commit(0, u64::MAX).unwrap();
    let img = w.into_image().unwrap();
    let r = Reader::open(&img).unwrap();
    assert_eq!(r.record_count(), 500_000);
    // Verify every 1000th record
    for i in (0..500_000).step_by(1000) {
        assert_eq!(r.lookup(Ipv4Key(i)).unwrap(), Some(i), "mismatch at {}", i);
    }
}
