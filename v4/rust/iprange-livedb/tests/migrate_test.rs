//! Integration tests for the streaming migrate API.

use iprange_livedb::{
    ext_sort, migrate, Change, DesiredRecord, ExtSortConfig, Ipv4Key,
    MigrateOptions, SortedStream, Writer,
};

fn rec(from: u32, to: u32, scope: u32) -> DesiredRecord<Ipv4Key> {
    DesiredRecord { from: Ipv4Key(from), to: Ipv4Key(to), scope_id: scope }
}

#[test]
fn migrate_empty_to_full() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    w.commit(0).unwrap();

    let desired = SortedStream::from_unsorted(vec![rec(10, 20, 1), rec(30, 40, 2)]);
    let counters = migrate(&mut w, &mut desired.clone_stream(), &MigrateOptions::default()).unwrap();
    w.commit(0).unwrap();

    assert_eq!(counters.added, 2);
    assert_eq!(counters.removed, 0);
    assert_eq!(counters.changed, 0);
    assert_eq!(counters.unchanged, 0);
}

#[test]
fn migrate_full_to_empty() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    w.set(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
    w.set(Ipv4Key(30), Ipv4Key(40), 2).unwrap();
    w.commit(0).unwrap();

    let desired = SortedStream::from_unsorted(vec![]);
    let counters = migrate(&mut w, &mut desired.clone_stream(), &MigrateOptions::default()).unwrap();
    w.commit(0).unwrap();

    assert_eq!(counters.added, 0);
    assert_eq!(counters.removed, 2);
}

#[test]
fn migrate_identical() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    w.set(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
    w.commit(0).unwrap();

    let desired = SortedStream::from_unsorted(vec![rec(10, 20, 1)]);
    let counters = migrate(&mut w, &mut desired.clone_stream(), &MigrateOptions::default()).unwrap();
    w.commit(0).unwrap();

    assert_eq!(counters.unchanged, 1);
    assert_eq!(counters.added, 0);
    assert_eq!(counters.removed, 0);
}

#[test]
fn migrate_change_scope() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    w.set(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
    w.commit(0).unwrap();

    let desired = SortedStream::from_unsorted(vec![rec(10, 20, 2)]);
    let counters = migrate(&mut w, &mut desired.clone_stream(), &MigrateOptions::default()).unwrap();
    w.commit(0).unwrap();

    assert_eq!(counters.changed, 1);
}

#[test]
fn extsort_and_migrate() {
    // Unsorted input → extsort → migrate to empty DB
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    w.commit(0).unwrap();

    let unsorted = vec![
        rec(30, 40, 2),
        rec(10, 20, 1),
        rec(50, 60, 3),
    ];
    let mut stream = ext_sort(unsorted, &ExtSortConfig::default()).unwrap();
    let counters = migrate(&mut w, &mut stream, &MigrateOptions::default()).unwrap();
    w.commit(0).unwrap();

    assert_eq!(counters.added, 3);

    // Verify the DB
    let image = w.into_image().unwrap();
    let r = iprange_livedb::Reader::open(&image).unwrap();
    assert_eq!(r.record_count(), 3);
    assert_eq!(r.lookup(Ipv4Key(15)).unwrap(), Some(1));
    assert_eq!(r.lookup(Ipv4Key(35)).unwrap(), Some(2));
    assert_eq!(r.lookup(Ipv4Key(55)).unwrap(), Some(3));
}

// Helper: clone a SortedStream (for test convenience)
trait CloneStream<K: iprange_livedb::IpKey> {
    fn clone_stream(&self) -> SortedStream<K>;
}

impl CloneStream<Ipv4Key> for SortedStream<Ipv4Key> {
    fn clone_stream(&self) -> SortedStream<Ipv4Key> {
        SortedStream {
            records: self.records.clone(),
            pos: 0,
        }
    }
}
