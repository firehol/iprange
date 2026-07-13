//! Integration tests for the streaming migrate API.

use iprange_livedb::{
    ext_sort, migrate, migrate_retention, DesiredRecord, ExtSortConfig, Ipv4Key,
    MigrateOptions, SortedStream, Writer,
};

fn rec(from: u32, to: u32, scope: u32) -> DesiredRecord<Ipv4Key> {
    DesiredRecord { from: Ipv4Key(from), to: Ipv4Key(to), scope_id: scope }
}

#[test]
fn migrate_empty_to_full() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    w.commit(0, u64::MAX).unwrap();

    let desired = SortedStream::from_unsorted(vec![rec(10, 20, 1), rec(30, 40, 2)]);
    let counters = migrate(&mut w, &mut desired.clone_stream(), &MigrateOptions::default()).unwrap();
    w.commit(0, u64::MAX).unwrap();

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
    w.commit(0, u64::MAX).unwrap();

    let desired = SortedStream::from_unsorted(vec![]);
    let counters = migrate(&mut w, &mut desired.clone_stream(), &MigrateOptions::default()).unwrap();
    w.commit(0, u64::MAX).unwrap();

    assert_eq!(counters.added, 0);
    assert_eq!(counters.removed, 2);
}

#[test]
fn migrate_identical() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    w.set(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
    w.commit(0, u64::MAX).unwrap();

    let desired = SortedStream::from_unsorted(vec![rec(10, 20, 1)]);
    let counters = migrate(&mut w, &mut desired.clone_stream(), &MigrateOptions::default()).unwrap();
    w.commit(0, u64::MAX).unwrap();

    assert_eq!(counters.unchanged, 1);
    assert_eq!(counters.added, 0);
    assert_eq!(counters.removed, 0);
}

#[test]
fn migrate_change_scope() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    w.set(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
    w.commit(0, u64::MAX).unwrap();

    let desired = SortedStream::from_unsorted(vec![rec(10, 20, 2)]);
    let counters = migrate(&mut w, &mut desired.clone_stream(), &MigrateOptions::default()).unwrap();
    w.commit(0, u64::MAX).unwrap();

    assert_eq!(counters.changed, 1);
}

// ── migrate_retention: Mode 0 timestamp preservation ──────────────────────

fn scope_of(image: &[u8], ip: u32) -> u32 {
    let r = iprange_livedb::Reader::open(image).unwrap();
    r.lookup(Ipv4Key(ip)).unwrap().unwrap()
}

#[test]
fn retention_keeps_older_when_desired_is_newer() {
    // Stored timestamp (100) is OLDER than desired (200). Retention must keep
    // min(100, 200) = 100 → no rewrite.
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    w.set(Ipv4Key(10), Ipv4Key(20), 100).unwrap();
    w.commit(0, u64::MAX).unwrap();

    let desired = SortedStream::from_unsorted(vec![rec(10, 20, 200)]);
    let counters = migrate_retention(&mut w, &mut desired.clone_stream()).unwrap();
    w.commit(0, u64::MAX).unwrap();
    let image = w.into_image().unwrap();

    assert_eq!(counters.changed, 1, "scope decision still counted as changed");
    assert_eq!(scope_of(&image, 15), 100, "older timestamp must survive");
}

#[test]
fn retention_overwrites_when_desired_is_older() {
    // Stored timestamp (200) is NEWER than desired (100). Retention must keep
    // min(200, 100) = 100 → rewrite to the older timestamp.
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    w.set(Ipv4Key(10), Ipv4Key(20), 200).unwrap();
    w.commit(0, u64::MAX).unwrap();

    let desired = SortedStream::from_unsorted(vec![rec(10, 20, 100)]);
    let counters = migrate_retention(&mut w, &mut desired.clone_stream()).unwrap();
    w.commit(0, u64::MAX).unwrap();
    let image = w.into_image().unwrap();

    assert_eq!(counters.changed, 1);
    assert_eq!(scope_of(&image, 15), 100, "older desired timestamp must win");
}

#[test]
fn retention_partial_overlap_preserves_old_on_overlap() {
    // Old [10-30] ts=100, desired [15-25] ts=300.
    // - old-only [10-14] and [26-30] removed (not in desired — desired is target)
    // - overlap [15-25] keeps min(100,300)=100
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    w.set(Ipv4Key(10), Ipv4Key(30), 100).unwrap();
    w.commit(0, u64::MAX).unwrap();

    let desired = SortedStream::from_unsorted(vec![rec(15, 25, 300)]);
    migrate_retention(&mut w, &mut desired.clone_stream()).unwrap();
    w.commit(0, u64::MAX).unwrap();
    let image = w.into_image().unwrap();
    let r = iprange_livedb::Reader::open(&image).unwrap();

    // Overlap region keeps the older stored timestamp.
    assert_eq!(r.lookup(Ipv4Key(20)).unwrap(), Some(100));
    // Old-only regions are removed (desired is the target state).
    assert_eq!(r.lookup(Ipv4Key(12)).unwrap(), None);
    assert_eq!(r.lookup(Ipv4Key(28)).unwrap(), None);
}

#[test]
fn combine_none_is_legacy_overwrite() {
    // Sanity: with combine unset, desired wins (legacy behavior).
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    w.set(Ipv4Key(10), Ipv4Key(20), 100).unwrap();
    w.commit(0, u64::MAX).unwrap();

    let desired = SortedStream::from_unsorted(vec![rec(10, 20, 200)]);
    migrate(&mut w, &mut desired.clone_stream(), &MigrateOptions::default()).unwrap();
    w.commit(0, u64::MAX).unwrap();
    let image = w.into_image().unwrap();

    assert_eq!(scope_of(&image, 15), 200, "default migrate overwrites");
}

#[test]
fn extsort_and_migrate() {
    // Unsorted input → extsort → migrate to empty DB
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    w.commit(0, u64::MAX).unwrap();

    let unsorted = vec![
        rec(30, 40, 2),
        rec(10, 20, 1),
        rec(50, 60, 3),
    ];
    let mut stream = ext_sort(unsorted, &ExtSortConfig::default()).unwrap();
    let counters = migrate(&mut w, &mut stream, &MigrateOptions::default()).unwrap();
    w.commit(0, u64::MAX).unwrap();

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
