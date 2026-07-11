use iprange_livedb::{Ipv4Key, Writer, Reader};

#[test]
fn feed_bit_436_no_panic() {
    let mut w = Writer::<Ipv4Key>::create(2, 0).unwrap(); // mode 2 = indirect
    // Feed bit 436 should NOT panic
    w.feed_add_range(Ipv4Key(10), Ipv4Key(20), 436).unwrap();
    w.commit(0).unwrap();
    let img = w.into_image().unwrap();
    let r = Reader::open(&img).unwrap();
    // Verify the record exists
    assert_eq!(r.record_count(), 1);
    let scope_id = r.lookup(Ipv4Key(15)).unwrap().unwrap();
    // Resolve the bitmap and check feed bit 436 is set
    let bitmap = r.scope_resolve(scope_id).expect("should resolve");
    let byte_idx = 436 / 8; // 54
    let bit_idx = 436 % 8;  // 4
    assert!(bitmap.len() > byte_idx, "bitmap too short: {} bytes", bitmap.len());
    assert!(bitmap[byte_idx as usize] & (1 << bit_idx) != 0, "feed bit 436 not set");
}

#[test]
fn feed_bit_32_bitmap_mode_errors() {
    let mut w = Writer::<Ipv4Key>::create(1, 0).unwrap(); // mode 1 = bitmap (32 bits max)
    // Feed bit 32 in bitmap mode should return an error, not panic
    let result = w.feed_add_range(Ipv4Key(10), Ipv4Key(20), 32);
    assert!(result.is_err());
}

#[test]
fn feed_bit_high_indirect_mode() {
    let mut w = Writer::<Ipv4Key>::create(2, 0).unwrap();
    w.feed_add_range(Ipv4Key(0), Ipv4Key(100), 100).unwrap();
    w.feed_add_range(Ipv4Key(0), Ipv4Key(100), 200).unwrap();
    w.commit(0).unwrap();
    let img = w.into_image().unwrap();
    let r = Reader::open(&img).unwrap();
    let scope_id = r.lookup(Ipv4Key(50)).unwrap().unwrap();
    let bitmap = r.scope_resolve(scope_id).unwrap();
    // Check bits 100 and 200
    assert!(bitmap[100 / 8] & (1 << (100 % 8)) != 0);
    assert!(bitmap[200 / 8] & (1 << (200 % 8)) != 0);
}
