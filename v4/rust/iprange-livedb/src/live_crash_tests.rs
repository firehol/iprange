use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::time::{SystemTime, UNIX_EPOCH};

use crate::{
    create_live, AddressFamily, CreationState, ImmutableReader, Ipv4Key, LiveReader, LiveWriter,
    TransactionBudget, ValueKind, ValueTag,
};

const CHILD_TEST: &str = "live_crash_tests::crash_child";
const CHILD_ACTION: &str = "IPRANGE_V4_TEST_ACTION";
const CHILD_PATH: &str = "IPRANGE_V4_TEST_PATH";

struct Pair(PathBuf);

impl Pair {
    fn new(label: &str) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        Self(std::env::temp_dir().join(format!(
            "iprange-v4-crash-{label}-{}-{unique}",
            std::process::id()
        )))
    }

    fn sidecar(&self) -> PathBuf {
        let mut name = self.0.file_name().unwrap().to_os_string();
        name.push(".readers");
        self.0.with_file_name(name)
    }
}

impl Drop for Pair {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.0);
        let _ = fs::remove_file(self.sidecar());
    }
}

fn budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 2 * 1024 * 1024,
        max_private_pages: 10_000,
        max_file_growth_pages: 10_000,
        max_open_files: 2,
    }
}

fn create(path: &Path, capacity: u32) {
    let result = create_live(
        path,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::RETENTION,
        capacity,
    )
    .unwrap();
    assert_eq!(result.state, CreationState::Created);
}

fn run_child(path: &Path, action: &str, crash_at: Option<&str>) {
    let mut command = Command::new(std::env::current_exe().unwrap());
    command
        .arg("--ignored")
        .arg("--exact")
        .arg(CHILD_TEST)
        .env(CHILD_ACTION, action)
        .env(CHILD_PATH, path);
    if let Some(point) = crash_at {
        command.env("IPRANGE_V4_TEST_CRASH_AT", point);
    }
    let status = command.status().unwrap();
    assert_eq!(status.code(), Some(86));
}

#[test]
fn creation_crashes_never_expose_a_partial_database() {
    for point in [
        "create.after_sidecar_sync",
        "create.after_sidecar_parent_sync",
        "create.after_main_sync",
        "create.after_main_parent_sync",
    ] {
        let files = Pair::new(point);
        run_child(&files.0, "create", Some(point));
        assert!(ImmutableReader::open(&files.0).is_err());
        assert!(LiveReader::open(&files.0).is_err());
    }

    let files = Pair::new("create.after_ready_write");
    run_child(&files.0, "create", Some("create.after_ready_write"));
    assert!(ImmutableReader::open(&files.0).is_err());
    if let Ok(reader) = LiveReader::open(&files.0) {
        assert_eq!(reader.info().unwrap().transaction_id, 1);
        reader.close().unwrap();
    }

    for point in ["create.after_ready_sync", "create.after_ready_parent_sync"] {
        let files = Pair::new(point);
        run_child(&files.0, "create", Some(point));
        let reader = LiveReader::open(&files.0).unwrap();
        assert_eq!(reader.info().unwrap().transaction_id, 1);
        reader.close().unwrap();
    }
}

#[test]
fn commit_crashes_select_only_a_complete_generation() {
    for point in ["commit.before_private_sync", "commit.after_private_sync"] {
        let files = Pair::new(point);
        create(&files.0, 1);
        run_child(&files.0, "commit", Some(point));

        let reader = LiveReader::open(&files.0).unwrap();
        assert_eq!(reader.info().unwrap().transaction_id, 1);
        assert_eq!(reader.lookup_direct_v4(Ipv4Key(10)).unwrap(), None);
        reader.close().unwrap();

        let writer = LiveWriter::open(&files.0, budget()).unwrap();
        writer.close().unwrap();
    }

    for point in ["commit.after_meta_write", "commit.after_meta_sync"] {
        let files = Pair::new(point);
        create(&files.0, 1);
        run_child(&files.0, "commit", Some(point));

        let reader = LiveReader::open(&files.0).unwrap();
        assert_eq!(reader.info().unwrap().transaction_id, 2);
        assert_eq!(reader.lookup_direct_v4(Ipv4Key(10)).unwrap(), Some(123));
        reader.close().unwrap();
    }
}

#[test]
fn metadata_crashes_select_absence_or_the_complete_value() {
    let payload = b"metadata crash value";
    for (point, expected) in [
        ("commit.before_private_sync", None),
        ("commit.after_private_sync", None),
        ("commit.after_meta_write", Some(&payload[..])),
        ("commit.after_meta_sync", Some(&payload[..])),
    ] {
        let files = Pair::new(point);
        create(&files.0, 1);
        run_child(&files.0, "metadata", Some(point));

        let reader = LiveReader::open(&files.0).unwrap();
        assert_eq!(reader.metadata_json().unwrap().as_deref(), expected);
        reader.close().unwrap();
    }
}

#[test]
fn process_death_releases_reader_and_writer_locks() {
    let files = Pair::new("locks");
    create(&files.0, 1);

    run_child(&files.0, "reader", None);
    let reader = LiveReader::open(&files.0).unwrap();
    reader.close().unwrap();

    run_child(&files.0, "writer", None);
    let writer = LiveWriter::open(&files.0, budget()).unwrap();
    writer.close().unwrap();
}

#[test]
fn reclamation_crashes_preserve_a_complete_readable_generation() {
    for (point, expected_txn) in [
        ("commit.before_private_sync", 3),
        ("commit.after_private_sync", 3),
        ("commit.after_meta_write", 4),
        ("commit.after_meta_sync", 4),
    ] {
        let files = Pair::new(point);
        create(&files.0, 1);
        let mut writer = LiveWriter::open(&files.0, budget()).unwrap();
        writer
            .assign_direct_v4(Ipv4Key(10), Ipv4Key(20), 1)
            .unwrap();
        writer.commit().unwrap();
        writer
            .assign_direct_v4(Ipv4Key(12), Ipv4Key(18), 2)
            .unwrap();
        writer.commit().unwrap();
        writer.close().unwrap();

        run_child(&files.0, "reclaim", Some(point));
        let reader = LiveReader::open(&files.0).unwrap();
        assert_eq!(reader.info().unwrap().transaction_id, expected_txn);
        assert_eq!(reader.lookup_direct_v4(Ipv4Key(11)).unwrap(), Some(1));
        assert_eq!(reader.lookup_direct_v4(Ipv4Key(15)).unwrap(), Some(2));
        reader.close().unwrap();
    }
}

#[test]
#[ignore = "subprocess entry point"]
fn crash_child() {
    let path = PathBuf::from(std::env::var_os(CHILD_PATH).unwrap());
    match std::env::var(CHILD_ACTION).unwrap().as_str() {
        "create" => {
            let _ = create_live(
                &path,
                AddressFamily::Ipv4,
                ValueKind::Direct,
                ValueTag::RETENTION,
                1,
            );
        }
        "commit" => {
            let mut writer = LiveWriter::open(&path, budget()).unwrap();
            writer
                .assign_direct_v4(Ipv4Key(10), Ipv4Key(20), 123)
                .unwrap();
            let _ = writer.commit();
        }
        "metadata" => {
            let mut writer = LiveWriter::open(&path, budget()).unwrap();
            writer.set_metadata_json(b"metadata crash value").unwrap();
            let _ = writer.commit();
        }
        "reader" => {
            let _reader = LiveReader::open(&path).unwrap();
            unsafe { libc::_exit(86) }
        }
        "writer" => {
            let _writer = LiveWriter::open(&path, budget()).unwrap();
            unsafe { libc::_exit(86) }
        }
        "reclaim" => {
            let mut writer = LiveWriter::open(&path, budget()).unwrap();
            let _ = writer.reclaim(10, 10_000);
        }
        _ => panic!("unknown child action"),
    }
    panic!("configured crash point was not reached");
}
