use iprange_livedb::{Ipv4Key, Writer, Reader};

fn dump(img: &[u8]) -> Vec<(u32, u32, u32)> {
    let r = Reader::open(img).unwrap();
    let mut out = vec![];
    r.scan_v4(|f, t, s| out.push((f.0, t.0, s))).unwrap();
    out
}

#[test]
fn feed_add_to_empty() {
    let mut w = Writer::<Ipv4Key>::create(1, 0).unwrap(); // mode 1 = bitmap
    w.feed_add_range(Ipv4Key(10), Ipv4Key(20), 0).unwrap(); // feed 0
    w.commit(0, u64::MAX).unwrap();
    let img = w.into_image().unwrap();
    let records = dump(&img);
    // scope_id should have bit 0 set = 0b1 = 1
    assert_eq!(records.len(), 1);
    assert_eq!(records[0].2 & 1, 1); // feed 0 is set
}

#[test]
fn feed_add_multiple_feeds() {
    let mut w = Writer::<Ipv4Key>::create(1, 0).unwrap();
    w.feed_add_range(Ipv4Key(10), Ipv4Key(20), 0).unwrap(); // feed 0
    w.feed_add_range(Ipv4Key(10), Ipv4Key(20), 1).unwrap(); // feed 1
    w.commit(0, u64::MAX).unwrap();
    let img = w.into_image().unwrap();
    let records = dump(&img);
    assert_eq!(records.len(), 1); // coalesced into one record
    assert_eq!(records[0].2, 0b11); // both feed 0 and 1 set
}

#[test]
fn feed_add_partial_overlap() {
    let mut w = Writer::<Ipv4Key>::create(1, 0).unwrap();
    w.feed_add_range(Ipv4Key(10), Ipv4Key(30), 0).unwrap(); // [10-30] feed 0
    w.feed_add_range(Ipv4Key(20), Ipv4Key(40), 1).unwrap(); // [20-40] feed 1
    w.commit(0, u64::MAX).unwrap();
    let img = w.into_image().unwrap();
    let records = dump(&img);
    eprintln!("records: {:?}", records);
    // Expected: [10-19] scope=0b1, [20-30] scope=0b11, [31-40] scope=0b10
    // But may not be coalesced by the writer.
    // Check per-IP:
    let r = Reader::open(&img).unwrap();
    assert_eq!(r.lookup(Ipv4Key(15)).unwrap(), Some(0b001)); // only feed 0
    assert_eq!(r.lookup(Ipv4Key(25)).unwrap(), Some(0b011)); // feeds 0+1
    assert_eq!(r.lookup(Ipv4Key(35)).unwrap(), Some(0b010)); // only feed 1
}

#[test]
fn feed_remove() {
    let mut w = Writer::<Ipv4Key>::create(1, 0).unwrap();
    w.feed_add_range(Ipv4Key(10), Ipv4Key(30), 0).unwrap();
    w.feed_add_range(Ipv4Key(10), Ipv4Key(30), 1).unwrap();
    w.feed_remove_range(Ipv4Key(10), Ipv4Key(30), 0).unwrap();
    w.commit(0, u64::MAX).unwrap();
    let img = w.into_image().unwrap();
    let r = Reader::open(&img).unwrap();
    assert_eq!(r.lookup(Ipv4Key(15)).unwrap(), Some(0b010)); // only feed 1 remains
}

#[test]
fn feed_remove_all_deletes_record() {
    let mut w = Writer::<Ipv4Key>::create(1, 0).unwrap();
    w.feed_add_range(Ipv4Key(10), Ipv4Key(20), 0).unwrap();
    w.feed_remove_range(Ipv4Key(10), Ipv4Key(20), 0).unwrap();
    w.commit(0, u64::MAX).unwrap();
    let img = w.into_image().unwrap();
    let r = Reader::open(&img).unwrap();
    assert_eq!(r.record_count(), 0);
}
