use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, AddressFamily, CommitDurability, CreationState, Error, ImmutableReader, Ipv4Key,
    Ipv6Key, LiveReader, LiveWriter, TransactionBudget, ValueKind, ValueTag,
};

struct TestPair {
    main: PathBuf,
}

impl TestPair {
    fn new(label: &str) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        Self {
            main: std::env::temp_dir().join(format!(
                "iprange-v4-live-{label}-{}-{unique}",
                std::process::id()
            )),
        }
    }

    fn sidecar(&self) -> PathBuf {
        let mut name = self.main.file_name().unwrap().to_os_string();
        name.push(".readers");
        self.main.with_file_name(name)
    }
}

impl Drop for TestPair {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.main);
        let _ = fs::remove_file(self.sidecar());
    }
}

fn budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 0,
        max_private_pages: 10_000,
        max_file_growth_pages: 10_000,
        max_open_files: 2,
    }
}

#[test]
fn live_generations_are_atomic_and_old_readers_stay_pinned() {
    let files = TestPair::new("generations");
    let created = create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"asn").unwrap(),
        2,
    )
    .unwrap();
    assert_eq!(created.state, CreationState::Created);
    assert!(!created.residue_possible);
    assert!(ImmutableReader::open(&files.main).is_err());

    let old = LiveReader::open(&files.main).unwrap();
    assert_eq!(old.info().unwrap().transaction_id, 1);

    let mut writer = LiveWriter::open(&files.main, budget()).unwrap();
    assert!(matches!(
        LiveWriter::open(&files.main, budget()),
        Err(Error::WriterBusy)
    ));
    writer
        .assign_direct_v4(Ipv4Key(10), Ipv4Key(30), 1)
        .unwrap();
    writer
        .assign_direct_v4(Ipv4Key(20), Ipv4Key(25), 2)
        .unwrap();
    writer
        .assign_direct_v4(Ipv4Key(22), Ipv4Key(23), 3)
        .unwrap();
    let committed = writer.commit().unwrap();
    assert_eq!(committed.durability, CommitDurability::Committed);
    assert_eq!(committed.transaction_id, 2);
    assert!(committed.cause.is_none());

    assert_eq!(old.lookup_direct_v4(Ipv4Key(22)).unwrap(), None);
    let current = LiveReader::open(&files.main).unwrap();
    assert_eq!(current.info().unwrap().transaction_id, 2);
    assert_eq!(current.lookup_direct_v4(Ipv4Key(19)).unwrap(), Some(1));
    assert_eq!(current.lookup_direct_v4(Ipv4Key(21)).unwrap(), Some(2));
    assert_eq!(current.lookup_direct_v4(Ipv4Key(22)).unwrap(), Some(3));
    assert!(matches!(
        LiveReader::open(&files.main),
        Err(Error::ReaderCapacityExhausted)
    ));

    old.close().unwrap();
    let reused = LiveReader::open(&files.main).unwrap();
    reused.close().unwrap();
    current.close().unwrap();
    writer.close().unwrap();
}

#[test]
fn abort_and_noop_never_publish_a_generation() {
    let files = TestPair::new("abort");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"").unwrap(),
        1,
    )
    .unwrap();
    let mut writer = LiveWriter::open(&files.main, budget()).unwrap();

    assert!(!writer.clear_direct_v4(Ipv4Key(1), Ipv4Key(2)).unwrap());
    assert!(matches!(writer.commit(), Err(Error::NoPendingTransaction)));
    writer.assign_direct_v4(Ipv4Key(1), Ipv4Key(2), 7).unwrap();
    assert!(writer.abort().unwrap());
    writer.close().unwrap();

    let reader = LiveReader::open(&files.main).unwrap();
    assert_eq!(reader.info().unwrap().transaction_id, 1);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(1)).unwrap(), None);
    reader.close().unwrap();
}

#[test]
fn full_ipv6_space_round_trips_without_endpoint_overflow() {
    let files = TestPair::new("ipv6");
    create_live(
        &files.main,
        AddressFamily::Ipv6,
        ValueKind::Direct,
        ValueTag::new(b"geo").unwrap(),
        1,
    )
    .unwrap();
    let mut writer = LiveWriter::open(&files.main, budget()).unwrap();
    writer
        .assign_direct_v6(Ipv6Key::from_u128(0), Ipv6Key::from_u128(u128::MAX), 9)
        .unwrap();
    assert_eq!(
        writer.commit().unwrap().durability,
        CommitDurability::Committed
    );
    writer.close().unwrap();

    let reader = LiveReader::open(&files.main).unwrap();
    assert_eq!(
        reader
            .lookup_direct_v6(Ipv6Key::from_u128(u128::MAX))
            .unwrap(),
        Some(9)
    );
    reader.close().unwrap();
}
