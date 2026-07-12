//! Basic integration tests for the v4.3 streaming mmap COW engine.

use iprange_livedb::{Ipv4Key, Writer, Reader};

#[test]
fn create_empty_commit() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    w.commit(0, u64::MAX).unwrap();
    let image = w.into_image().unwrap();
    let r = Reader::open(&image).unwrap();
    assert_eq!(r.record_count(), 0);
}

#[test]
fn set_single_record() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    w.set(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
    w.commit(0, u64::MAX).unwrap();
    let image = w.into_image().unwrap();
    let r = Reader::open(&image).unwrap();
    assert_eq!(r.record_count(), 1);
    assert_eq!(r.lookup(Ipv4Key(15)).unwrap(), Some(1));
}

#[test]
fn set_multiple_sorted() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    w.set(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
    w.set(Ipv4Key(30), Ipv4Key(40), 2).unwrap();
    w.set(Ipv4Key(50), Ipv4Key(60), 3).unwrap();
    w.commit(0, u64::MAX).unwrap();
    let image = w.into_image().unwrap();
    let r = Reader::open(&image).unwrap();
    assert_eq!(r.record_count(), 3);
    assert_eq!(r.lookup(Ipv4Key(15)).unwrap(), Some(1));
    assert_eq!(r.lookup(Ipv4Key(35)).unwrap(), Some(2));
    assert_eq!(r.lookup(Ipv4Key(55)).unwrap(), Some(3));
}

#[test]
fn append_sorted_disjoint() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..1000u32 {
        w.append(Ipv4Key(i * 10), Ipv4Key(i * 10 + 5), i).unwrap();
    }
    w.commit(0, u64::MAX).unwrap();
    let image = w.into_image().unwrap();
    let r = Reader::open(&image).unwrap();
    assert_eq!(r.record_count(), 1000);
    assert_eq!(r.lookup(Ipv4Key(500)).unwrap(), Some(50));
}

#[test]
fn delete_overlap() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    w.set(Ipv4Key(10), Ipv4Key(100), 1).unwrap();
    w.delete(Ipv4Key(30), Ipv4Key(50)).unwrap();
    w.commit(0, u64::MAX).unwrap();
    let image = w.into_image().unwrap();
    let r = Reader::open(&image).unwrap();
    assert_eq!(r.record_count(), 2);
    assert_eq!(r.lookup(Ipv4Key(20)).unwrap(), Some(1));
    assert_eq!(r.lookup(Ipv4Key(40)).unwrap(), None);
    assert_eq!(r.lookup(Ipv4Key(60)).unwrap(), Some(1));
}

#[test]
fn set_overwrites() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    w.set(Ipv4Key(10), Ipv4Key(100), 1).unwrap();
    w.set(Ipv4Key(10), Ipv4Key(100), 2).unwrap();
    w.commit(0, u64::MAX).unwrap();
    let image = w.into_image().unwrap();
    let r = Reader::open(&image).unwrap();
    assert_eq!(r.record_count(), 1);
    assert_eq!(r.lookup(Ipv4Key(50)).unwrap(), Some(2));
}

#[test]
fn scan_all_records() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    w.set(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
    w.set(Ipv4Key(30), Ipv4Key(40), 2).unwrap();

    let mut records = vec![];
    w.scan(|from, to, scope| {
        records.push((from, to, scope));
    }).unwrap();

    assert_eq!(records.len(), 2);
    assert_eq!(records[0], (Ipv4Key(10), Ipv4Key(20), 1));
    assert_eq!(records[1], (Ipv4Key(30), Ipv4Key(40), 2));
}

#[test]
fn leaf_split_many_inserts() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..1000u32 {
        w.set(Ipv4Key(i * 2), Ipv4Key(i * 2 + 1), i).unwrap();
    }
    w.commit(0, u64::MAX).unwrap();
    let image = w.into_image().unwrap();
    let r = Reader::open(&image).unwrap();
    assert_eq!(r.record_count(), 1000);
    assert_eq!(r.lookup(Ipv4Key(0)).unwrap(), Some(0));
    assert_eq!(r.lookup(Ipv4Key(100)).unwrap(), Some(50));
    assert_eq!(r.lookup(Ipv4Key(1998)).unwrap(), Some(999));
    assert_eq!(r.lookup(Ipv4Key(2000)).unwrap(), None);
}

#[test]
fn writer_reader_committed_state() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    w.set(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
    w.commit(0, u64::MAX).unwrap();
    w.set(Ipv4Key(30), Ipv4Key(40), 2).unwrap(); // pending

    let r = w.reader().unwrap();
    assert_eq!(r.record_count(), 1);
    assert_eq!(r.lookup(Ipv4Key(15)).unwrap(), Some(1));
    assert_eq!(r.lookup(Ipv4Key(35)).unwrap(), None); // not committed
}

#[test]
fn large_append_10k() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..10_000u32 {
        w.append(Ipv4Key(i), Ipv4Key(i), i).unwrap();
    }
    w.commit(0, u64::MAX).unwrap();
    let image = w.into_image().unwrap();
    let r = Reader::open(&image).unwrap();
    assert_eq!(r.record_count(), 10_000);
    assert_eq!(r.lookup(Ipv4Key(5000)).unwrap(), Some(5000));
}
