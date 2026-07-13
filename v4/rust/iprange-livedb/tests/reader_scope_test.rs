use iprange_livedb::{Ipv4Key, Reader, Writer};

#[test]
fn reader_scope_resolve() {
    let mut w = Writer::<Ipv4Key>::create(2, 0).unwrap(); // mode 2 = indirect
    let id1 = w.scope_intern(&[0b00000001]).unwrap();
    let id2 = w.scope_intern(&[0b00000011]).unwrap();
    w.set(Ipv4Key(10), Ipv4Key(20), id1).unwrap();
    w.set(Ipv4Key(30), Ipv4Key(40), id2).unwrap();
    w.commit(0, u64::MAX).unwrap();
    let img = w.into_image().unwrap();

    // Use Reader (not Writer) to resolve bitmaps
    let r = Reader::open(&img).unwrap();
    assert_eq!(r.scope_resolve(id1), Some(vec![0b00000001]));
    assert_eq!(r.scope_resolve(id2), Some(vec![0b00000011]));
    assert_eq!(r.scope_resolve(999), None); // non-existent
}

#[test]
fn reader_scope_list() {
    let mut w = Writer::<Ipv4Key>::create(2, 0).unwrap();
    w.scope_intern(&[0xAB]).unwrap();
    w.scope_intern(&[0xCD, 0xEF]).unwrap();
    w.set(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
    w.commit(0, u64::MAX).unwrap();
    let img = w.into_image().unwrap();

    let r = Reader::open(&img).unwrap();
    let list = r.scope_list();
    assert_eq!(list.len(), 2);
    // Entries are (scope_id, bitmap)
    assert!(list
        .iter()
        .any(|(id, bm)| *id == 1 && bm.as_slice() == [0xAB]));
    assert!(list
        .iter()
        .any(|(id, bm)| *id == 2 && bm.as_slice() == [0xCD, 0xEF]));
}

#[test]
fn reader_scope_resolve_mode0_returns_none() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap(); // mode 0 = scalar
    w.set(Ipv4Key(10), Ipv4Key(20), 42).unwrap();
    w.commit(0, u64::MAX).unwrap();
    let img = w.into_image().unwrap();

    let r = Reader::open(&img).unwrap();
    assert_eq!(r.scope_resolve(42), None); // mode 0 has no scope table
}
