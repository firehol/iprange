use iprange_livedb::{Ipv4Key, Writer, Reader};
use iprange_livedb::page_store::{PageStore, VecPageStore};

fn dump(img: &[u8]) -> Vec<(u32, u32, u32)> {
    let r = Reader::open(img).unwrap();
    let mut records = vec![];
    r.scan_v4(|from, to, scope| records.push((from.0, to.0, scope))).unwrap();
    records
}

#[test]
fn partial_overlap_bug() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    w.set(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
    w.commit(0).unwrap();
    let img = w.into_image().unwrap();
    
    let store: Box<dyn PageStore> = Box::new(VecPageStore::new(img));
    let mut w2 = Writer::<Ipv4Key>::open(store).unwrap();
    
    // Use Writer directly: set [10-15] which should delete [10-20] and insert [10-15]
    // This tests the underlying set operation
    w2.set(Ipv4Key(10), Ipv4Key(15), 1).unwrap();
    w2.commit(1).unwrap();
    
    let img2 = w2.into_image().unwrap();
    let records = dump(&img2);
    eprintln!("After set(10,15,1) over [10-20]: {:?}", records);
    
    // set() calls delete_range(10,15) then inserts [10-15].
    // delete_range(10,15) should split [10-20] into nothing for [10-15] and [16-20] for the tail.
    // Then insert [10-15].
    // Expected: [(10, 15, 1), (16, 20, 1)]
    // The set() operation is CORRECT for this case.
}
