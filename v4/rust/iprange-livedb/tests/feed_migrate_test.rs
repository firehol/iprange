use iprange_livedb::feed_migrate::migrate_feed;
use iprange_livedb::page_store::{PageStore, VecPageStore};
use iprange_livedb::{DesiredRecord, Ipv4Key, MigrateOptions, Reader, SortedStream, Writer};
use std::collections::BTreeMap;

fn dr(f: u32, t: u32) -> DesiredRecord<Ipv4Key> {
    DesiredRecord {
        from: Ipv4Key(f),
        to: Ipv4Key(t),
        scope_id: 0,
    }
}

fn per_ip_map(img: &[u8]) -> BTreeMap<u32, u32> {
    let r = Reader::open(img).unwrap();
    let mut map = BTreeMap::new();
    r.scan_v4(|f, t, s| {
        for ip in f.0..=t.0 {
            map.insert(ip, s);
        }
    })
    .unwrap();
    map
}

fn has_feed_bit(scope_id: u32, bit: u32) -> bool {
    (scope_id & (1 << bit)) != 0
}

#[allow(dead_code)]
fn make_and_open(records: &[(u32, u32, u32)]) -> Writer<Ipv4Key> {
    let mut w = Writer::<Ipv4Key>::create(1, 0).unwrap();
    for &(f, t, s) in records {
        w.set(Ipv4Key(f), Ipv4Key(t), s).unwrap();
    }
    w.commit(0, u64::MAX).unwrap();
    let img = w.into_image().unwrap();
    let store: Box<dyn PageStore> = Box::new(VecPageStore::new(img));
    Writer::<Ipv4Key>::open(store).unwrap()
}

#[test]
fn feed_migrate_add_feed() {
    let mut w = Writer::<Ipv4Key>::create(1, 0).unwrap();
    let desired = SortedStream::from_unsorted(vec![dr(10, 20)]);
    let _counters =
        migrate_feed(&mut w, 0, &mut desired.clone(), &MigrateOptions::default()).unwrap();
    w.commit(0, u64::MAX).unwrap();
    let img = w.into_image().unwrap();
    let map = per_ip_map(&img);
    for ip in 10..=20 {
        assert!(has_feed_bit(map[&ip], 0));
    }
    assert!(!map.contains_key(&5));
}

#[test]
fn feed_migrate_preserve_other_feeds() {
    // Feed 0: [10-30]
    let img = {
        let mut w = Writer::<Ipv4Key>::create(1, 0).unwrap();
        let d0 = SortedStream::from_unsorted(vec![dr(10, 30)]);
        migrate_feed(&mut w, 0, &mut d0.clone(), &MigrateOptions::default()).unwrap();
        w.commit(0, u64::MAX).unwrap();
        w.into_image().unwrap()
    };

    // Add feed 1: [15-25]
    let store: Box<dyn PageStore> = Box::new(VecPageStore::new(img));
    let mut w2 = Writer::<Ipv4Key>::open(store).unwrap();
    let d1 = SortedStream::from_unsorted(vec![dr(15, 25)]);
    migrate_feed(&mut w2, 1, &mut d1.clone(), &MigrateOptions::default()).unwrap();
    w2.commit(1, u64::MAX).unwrap();
    let img2 = w2.into_image().unwrap();
    let map = per_ip_map(&img2);

    for ip in 10..=14 {
        assert!(has_feed_bit(map[&ip], 0));
        assert!(!has_feed_bit(map[&ip], 1));
    }
    for ip in 15..=25 {
        assert!(has_feed_bit(map[&ip], 0));
        assert!(has_feed_bit(map[&ip], 1));
    }
    for ip in 26..=30 {
        assert!(has_feed_bit(map[&ip], 0));
        assert!(!has_feed_bit(map[&ip], 1));
    }
}

#[test]
fn feed_migrate_remove_feed() {
    // Setup: feed 0 [10-30], feed 1 [15-25]
    let img = {
        let mut w = Writer::<Ipv4Key>::create(1, 0).unwrap();
        let d0 = SortedStream::from_unsorted(vec![dr(10, 30)]);
        migrate_feed(&mut w, 0, &mut d0.clone(), &MigrateOptions::default()).unwrap();
        w.commit(0, u64::MAX).unwrap();
        let img1 = w.into_image().unwrap();
        let store: Box<dyn PageStore> = Box::new(VecPageStore::new(img1));
        let mut w2 = Writer::<Ipv4Key>::open(store).unwrap();
        let d1 = SortedStream::from_unsorted(vec![dr(15, 25)]);
        migrate_feed(&mut w2, 1, &mut d1.clone(), &MigrateOptions::default()).unwrap();
        w2.commit(1, u64::MAX).unwrap();
        w2.into_image().unwrap()
    };

    // Update feed 1 to [20-22] — should remove [15-19] and [23-25] from feed 1
    let store: Box<dyn PageStore> = Box::new(VecPageStore::new(img));
    let mut w3 = Writer::<Ipv4Key>::open(store).unwrap();
    let d1_new = SortedStream::from_unsorted(vec![dr(20, 22)]);
    migrate_feed(&mut w3, 1, &mut d1_new.clone(), &MigrateOptions::default()).unwrap();
    w3.commit(2, u64::MAX).unwrap();
    let img3 = w3.into_image().unwrap();
    let map = per_ip_map(&img3);

    for ip in 15..=19 {
        assert!(has_feed_bit(map[&ip], 0));
        assert!(!has_feed_bit(map[&ip], 1));
    }
    for ip in 20..=22 {
        assert!(has_feed_bit(map[&ip], 0));
        assert!(has_feed_bit(map[&ip], 1));
    }
    for ip in 23..=25 {
        assert!(has_feed_bit(map[&ip], 0));
        assert!(!has_feed_bit(map[&ip], 1));
    }
}
