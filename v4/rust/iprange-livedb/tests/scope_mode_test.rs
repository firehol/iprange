use iprange_livedb::{Ipv4Key, Writer, Reader};
use iprange_livedb::page_store::{PageStore, VecPageStore};

#[test]
fn mode2_intern_resolve() {
    let mut w = Writer::<Ipv4Key>::create(2, 0).unwrap(); // scope_mode = 2 (indirect)
    
    // Intern some bitmaps
    let id1 = w.scope_intern(&[0b00000001]).unwrap(); // feed 0
    let id2 = w.scope_intern(&[0b00000011]).unwrap(); // feeds 0,1
    let id1b = w.scope_intern(&[0b00000001]).unwrap(); // same as id1 → reuse
    
    assert_eq!(id1, 1);
    assert_eq!(id2, 2);
    assert_eq!(id1b, 1);
    
    // Set IP ranges with these scope_ids
    w.set(Ipv4Key(10), Ipv4Key(20), id1).unwrap();
    w.set(Ipv4Key(30), Ipv4Key(40), id2).unwrap();
    
    w.commit(0).unwrap();
    
    // Resolve and verify
    assert_eq!(w.scope_resolve(id1), Some(&[0b00000001][..]));
    assert_eq!(w.scope_resolve(id2), Some(&[0b00000011][..]));
}

#[test]
fn mode2_persist_across_commit() {
    let img = {
        let mut w = Writer::<Ipv4Key>::create(2, 0).unwrap();
        let id1 = w.scope_intern(&[0b00000101]).unwrap();
        w.set(Ipv4Key(10), Ipv4Key(20), id1).unwrap();
        w.commit(0).unwrap();
        w.into_image().unwrap()
    };
    
    // Reopen and verify scope table is loaded
    let store: Box<dyn PageStore> = Box::new(VecPageStore::new(img));
    let mut w2 = Writer::<Ipv4Key>::open(store).unwrap();
    
    // scope_resolve should work from the loaded registry
    let bitmap = w2.scope_resolve(1);
    assert_eq!(bitmap, Some(&[0b00000101][..]));
}

#[test]
fn mode2_bitmap_ops() {
    let mut w = Writer::<Ipv4Key>::create(2, 0).unwrap();
    
    // Start with an empty bitmap
    let empty = w.scope_intern(&[]).unwrap();
    
    // Add feed 0
    let with_0 = w.scope_bitmap_set_feed(empty, 0).unwrap();
    assert_ne!(with_0, empty);
    let bm = w.scope_resolve(with_0).unwrap();
    assert!(bm[0] & 1 != 0); // bit 0 set
    
    // Add feed 5
    let with_05 = w.scope_bitmap_set_feed(with_0, 5).unwrap();
    let bm = w.scope_resolve(with_05).unwrap();
    assert!(bm[0] & 1 != 0);  // bit 0
    assert!(bm[0] & 32 != 0); // bit 5
    
    // Remove feed 0
    let with_5 = w.scope_bitmap_clear_feed(with_05, 0).unwrap();
    let bm = w.scope_resolve(with_5).unwrap();
    assert!(bm[0] & 1 == 0);  // bit 0 cleared
    assert!(bm[0] & 32 != 0); // bit 5 still set
    
    // Remove feed 5 → empty
    let result = w.scope_bitmap_clear_feed(with_5, 5).unwrap();
    assert_eq!(result, 0); // empty bitmap → 0
}

#[test]
fn mode2_many_scopes() {
    let mut w = Writer::<Ipv4Key>::create(2, 0).unwrap();
    
    // Create 100 distinct bitmaps
    for i in 0..100u32 {
        let mut bitmap = vec![0u8; (i / 8 + 1) as usize];
        bitmap[i as usize / 8] = 1 << (i % 8);
        let id = w.scope_intern(&bitmap).unwrap();
        w.set(Ipv4Key(i * 10), Ipv4Key(i * 10 + 9), id).unwrap();
    }
    
    w.commit(0).unwrap();
    
    let img = w.into_image().unwrap();
    let r = Reader::open(&img).unwrap();
    assert_eq!(r.record_count(), 100);
    
    // Verify scope table is persisted
    let store: Box<dyn PageStore> = Box::new(VecPageStore::new(img));
    let w2 = Writer::<Ipv4Key>::open(store).unwrap();
    // Verify a few scope resolutions
    for i in 0..100u32 {
        let id = i + 1;
        let bitmap = w2.scope_resolve(id);
        assert!(bitmap.is_some(), "scope {} not found", id);
        let bm = bitmap.unwrap();
        assert!(bm[i as usize / 8] & (1 << (i % 8)) != 0, "scope {} bitmap wrong", id);
    }
}
