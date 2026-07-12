use iprange_livedb::{Ipv4Key, Writer, Reader, DesiredRecord, MigrateOptions, SortedStream};
use iprange_livedb::page_store::{PageStore, VecPageStore};
use std::collections::BTreeMap;

fn make_db(records: &[(u32, u32, u32)]) -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for &(f, t, s) in records {
        w.set(Ipv4Key(f), Ipv4Key(t), s).unwrap();
    }
    w.commit(0, u64::MAX).unwrap();
    w.into_image().unwrap()
}

fn migrate_and_get_map(img: Vec<u8>, desired: Vec<DesiredRecord<Ipv4Key>>) -> BTreeMap<u32, u32> {
    let store: Box<dyn PageStore> = Box::new(VecPageStore::new(img));
    let mut w = Writer::<Ipv4Key>::open(store).unwrap();
    let mut stream = SortedStream::from_unsorted(desired);
    iprange_livedb::migrate(&mut w, &mut stream, &MigrateOptions::default()).unwrap();
    w.commit(1, u64::MAX).unwrap();
    let img2 = w.into_image().unwrap();
    let r = Reader::open(&img2).unwrap();
    let mut map = BTreeMap::new();
    r.scan_v4(|f, t, s| {
        for ip in f.0..=t.0 { map.insert(ip, s); }
    }).unwrap();
    map
}

fn oracle_map(desired: &[DesiredRecord<Ipv4Key>]) -> BTreeMap<u32, u32> {
    // Input-order last-wins: later records in the input override earlier
    // ones for overlapping ranges. This matches the F8 fix in normalize_chunk.
    let mut map = BTreeMap::new();
    for d in desired {
        for ip in d.from.0..=d.to.0 { map.insert(ip, d.scope_id); }
    }
    map
}

fn dr(f: u32, t: u32, s: u32) -> DesiredRecord<Ipv4Key> {
    DesiredRecord { from: Ipv4Key(f), to: Ipv4Key(t), scope_id: s }
}

#[test]
fn partial_overlap_old_extends() {
    let img = make_db(&[(10, 20, 1)]);
    let desired = vec![dr(10, 15, 1)];
    assert_eq!(migrate_and_get_map(img, desired.clone()), oracle_map(&desired));
}

#[test]
fn partial_overlap_desired_extends() {
    let img = make_db(&[(10, 15, 1)]);
    let desired = vec![dr(10, 20, 1)];
    assert_eq!(migrate_and_get_map(img, desired.clone()), oracle_map(&desired));
}

#[test]
fn overlapping_different_scope() {
    let img = make_db(&[(10, 20, 1)]);
    let desired = vec![dr(15, 25, 2)];
    assert_eq!(migrate_and_get_map(img, desired.clone()), oracle_map(&desired));
}

#[test]
fn one_to_many() {
    let img = make_db(&[(10, 30, 1)]);
    let desired = vec![dr(10, 15, 1), dr(20, 30, 1)];
    assert_eq!(migrate_and_get_map(img, desired.clone()), oracle_map(&desired));
}

#[test]
fn many_to_one() {
    let img = make_db(&[(10, 15, 1), (20, 30, 1)]);
    let desired = vec![dr(10, 30, 1)];
    assert_eq!(migrate_and_get_map(img, desired.clone()), oracle_map(&desired));
}

#[test]
fn disjoint_replacement() {
    let img = make_db(&[(10, 20, 1)]);
    let desired = vec![dr(30, 40, 1)];
    assert_eq!(migrate_and_get_map(img, desired.clone()), oracle_map(&desired));
}

#[test]
fn full_replacement_different_scopes() {
    let img = make_db(&[(10, 20, 1), (30, 40, 2), (50, 60, 3)]);
    let desired = vec![dr(15, 25, 9), dr(35, 45, 8)];
    assert_eq!(migrate_and_get_map(img, desired.clone()), oracle_map(&desired));
}

#[test]
fn empty_old() {
    let img = make_db(&[]);
    let desired = vec![dr(10, 20, 1), dr(30, 40, 2)];
    assert_eq!(migrate_and_get_map(img, desired.clone()), oracle_map(&desired));
}

#[test]
fn empty_desired() {
    let img = make_db(&[(10, 20, 1), (30, 40, 2)]);
    let desired: Vec<DesiredRecord<Ipv4Key>> = vec![];
    assert_eq!(migrate_and_get_map(img, desired.clone()), oracle_map(&desired));
}

#[test]
fn boundary_ips() {
    let img = make_db(&[(0, 100, 1)]);
    let desired = vec![dr(0, 50, 2)];
    assert_eq!(migrate_and_get_map(img, desired.clone()), oracle_map(&desired));
}

#[test]
fn random_oracle() {
    let mut rng_state: u64 = 42;
    let mut next_rand = || {
        rng_state = rng_state.wrapping_mul(6364136223846793005).wrapping_add(1442695040888963407);
        (rng_state >> 32) as u32
    };

    for trial in 0..50 {
        let mut old = vec![];
        let mut desired = vec![];
        let n_old = (next_rand() % 8) as usize + 1;
        let n_des = (next_rand() % 8) as usize + 1;
        for _ in 0..n_old {
            let f = next_rand() % 200;
            let t = f + next_rand() % 30;
            old.push((f, t, next_rand() % 3));
        }
        for _ in 0..n_des {
            let f = next_rand() % 200;
            let t = f + next_rand() % 30;
            desired.push(dr(f, t, next_rand() % 3));
        }

        let img = make_db(&old);
        let result_map = migrate_and_get_map(img, desired.clone());
        let expected_map = oracle_map(&desired);

        assert_eq!(result_map, expected_map,
            "trial {} mismatch:\n  old: {:?}\n  desired: {:?}\n  result: {:?}\n  expected: {:?}",
            trial, old, desired, result_map, expected_map);
    }
}
