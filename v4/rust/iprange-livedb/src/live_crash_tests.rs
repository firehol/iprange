use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};

use crate::{
    create_live, initialize_live, reset_live_coordination, resolve_interrupted_live_transition,
    AddressFamily, CancellationToken, CreationState, ImmutableReader, Ipv4Key, LiveReader,
    LiveResidueStatus, LiveTransitionResolutionMode, LiveWriter, TransactionBudget, ValueKind,
    ValueTag,
};

const CHILD_TEST: &str = "live_crash_tests::crash_child";
const CHILD_ACTION: &str = "IPRANGE_V4_TEST_ACTION";
const CHILD_PATH: &str = "IPRANGE_V4_TEST_PATH";
static PAIR_SEQUENCE: AtomicU64 = AtomicU64::new(0);

struct Pair(PathBuf);

impl Pair {
    fn new(label: &str) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let sequence = PAIR_SEQUENCE.fetch_add(1, Ordering::Relaxed);
        Self(std::env::temp_dir().join(format!(
            "iprange-v4-crash-{label}-{}-{unique}-{sequence}",
            std::process::id()
        )))
    }

    fn sidecar(&self) -> PathBuf {
        let mut name = self.0.file_name().unwrap().to_os_string();
        name.push(".readers");
        self.0.with_file_name(name)
    }

    fn reset_temp(&self) -> PathBuf {
        let mut name = self.0.file_name().unwrap().to_os_string();
        name.push(".readers.reset");
        self.0.with_file_name(name)
    }
}

impl Drop for Pair {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.0);
        let _ = fs::remove_file(self.sidecar());
        let _ = fs::remove_file(self.reset_temp());
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
        &crate::CancellationToken::new(),
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
        assert!(LiveReader::open(&files.0, &crate::CancellationToken::new()).is_err());
    }

    let files = Pair::new("create.after_ready_write");
    run_child(&files.0, "create", Some("create.after_ready_write"));
    assert!(ImmutableReader::open(&files.0).is_err());
    if let Ok(mut reader) = LiveReader::open(&files.0, &crate::CancellationToken::new()) {
        assert_eq!(reader.info().unwrap().transaction_id, 1);
        reader.close().unwrap();
    }

    for point in ["create.after_ready_sync", "create.after_ready_parent_sync"] {
        let files = Pair::new(point);
        run_child(&files.0, "create", Some(point));
        let mut reader = LiveReader::open(&files.0, &crate::CancellationToken::new()).unwrap();
        assert_eq!(reader.info().unwrap().transaction_id, 1);
        reader.close().unwrap();
    }
}

#[test]
fn creation_crashes_are_recoverable_without_the_lost_result() {
    for point in [
        "create.after_sidecar_sync",
        "create.after_sidecar_parent_sync",
    ] {
        let files = Pair::new(point);
        run_child(&files.0, "create", Some(point));
        let recovered = resolve_interrupted_live_transition(
            &files.0,
            LiveTransitionResolutionMode::Rollback,
            &CancellationToken::new(),
        )
        .unwrap();
        assert_eq!(recovered.status, LiveResidueStatus::Removed);
        assert!(!files.0.exists());
        assert!(!files.sidecar().exists());
    }

    for point in ["create.after_main_sync", "create.after_main_parent_sync"] {
        let files = Pair::new(point);
        run_child(&files.0, "create", Some(point));
        let recovered = resolve_interrupted_live_transition(
            &files.0,
            LiveTransitionResolutionMode::Complete,
            &CancellationToken::new(),
        )
        .unwrap();
        assert_eq!(recovered.status, LiveResidueStatus::Completed);
        let mut reader = LiveReader::open(&files.0, &CancellationToken::new()).unwrap();
        reader.close().unwrap();
    }
}

#[test]
fn initialization_crashes_are_recoverable_without_the_lost_result() {
    for point in [
        "live_initialize.after_creating_sync",
        "live_initialize.after_creating_parent_sync",
        "live_initialize.after_ready_sync",
        "live_initialize.after_ready_parent_sync",
    ] {
        let files = Pair::new(point);
        create(&files.0, 1);
        fs::remove_file(files.sidecar()).unwrap();
        run_child(&files.0, "initialize", Some(point));

        let recovered = resolve_interrupted_live_transition(
            &files.0,
            LiveTransitionResolutionMode::Complete,
            &CancellationToken::new(),
        )
        .unwrap();
        assert!(matches!(
            recovered.status,
            LiveResidueStatus::Completed | LiveResidueStatus::Ready
        ));
        let mut reader = LiveReader::open(&files.0, &CancellationToken::new()).unwrap();
        reader.close().unwrap();
    }
}

#[test]
fn reset_crashes_leave_a_retryable_or_ready_database() {
    for point in [
        "live_reset.after_creating_sync",
        "live_reset.after_ready_sync",
        "live_reset.after_private_parent_sync",
        "live_reset.before_replace",
    ] {
        let files = Pair::new(point);
        create(&files.0, 1);
        fs::write(files.sidecar(), b"corrupt").unwrap();
        run_child(&files.0, "reset", Some(point));

        let recovered = resolve_interrupted_live_transition(
            &files.0,
            LiveTransitionResolutionMode::Rollback,
            &CancellationToken::new(),
        )
        .unwrap();
        assert_eq!(recovered.status, LiveResidueStatus::Removed);
        assert!(!files.reset_temp().exists());
        reset_live_coordination(&files.0, 2, &CancellationToken::new()).unwrap();
        let mut reader = LiveReader::open(&files.0, &CancellationToken::new()).unwrap();
        reader.close().unwrap();
    }

    for point in [
        "live_reset.after_replace",
        "live_reset.after_directory_sync",
    ] {
        let files = Pair::new(point);
        create(&files.0, 1);
        fs::write(files.sidecar(), b"corrupt").unwrap();
        run_child(&files.0, "reset", Some(point));

        let recovered = resolve_interrupted_live_transition(
            &files.0,
            LiveTransitionResolutionMode::Complete,
            &CancellationToken::new(),
        )
        .unwrap();
        assert!(matches!(
            recovered.status,
            LiveResidueStatus::Completed | LiveResidueStatus::Ready
        ));
        let mut reader = LiveReader::open(&files.0, &CancellationToken::new()).unwrap();
        reader.close().unwrap();
    }
}

#[test]
fn commit_crashes_select_only_a_complete_generation() {
    for point in ["commit.before_private_sync", "commit.after_private_sync"] {
        let files = Pair::new(point);
        create(&files.0, 1);
        run_child(&files.0, "commit", Some(point));

        let mut reader = LiveReader::open(&files.0, &crate::CancellationToken::new()).unwrap();
        assert_eq!(reader.info().unwrap().transaction_id, 1);
        assert_eq!(reader.lookup_direct_v4(Ipv4Key(10)).unwrap(), None);
        reader.close().unwrap();

        let mut writer =
            LiveWriter::open(&files.0, budget(), &crate::CancellationToken::new()).unwrap();
        writer.close().unwrap();
    }

    for point in ["commit.after_meta_write", "commit.after_meta_sync"] {
        let files = Pair::new(point);
        create(&files.0, 1);
        run_child(&files.0, "commit", Some(point));

        let mut reader = LiveReader::open(&files.0, &crate::CancellationToken::new()).unwrap();
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

        let mut reader = LiveReader::open(&files.0, &crate::CancellationToken::new()).unwrap();
        assert_eq!(reader.metadata_json().unwrap().as_deref(), expected);
        reader.close().unwrap();
    }
}

#[test]
fn process_death_releases_reader_and_writer_locks() {
    let files = Pair::new("locks");
    create(&files.0, 1);

    run_child(&files.0, "reader", None);
    let mut reader = LiveReader::open(&files.0, &crate::CancellationToken::new()).unwrap();
    reader.close().unwrap();

    run_child(&files.0, "writer", None);
    let mut writer =
        LiveWriter::open(&files.0, budget(), &crate::CancellationToken::new()).unwrap();
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
        let mut writer =
            LiveWriter::open(&files.0, budget(), &crate::CancellationToken::new()).unwrap();
        let cancellation = CancellationToken::new();
        let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
        transaction.assign_v4(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
        transaction.commit().unwrap();
        let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
        transaction.assign_v4(Ipv4Key(12), Ipv4Key(18), 2).unwrap();
        transaction.commit().unwrap();
        writer.close().unwrap();

        run_child(&files.0, "reclaim", Some(point));
        let mut reader = LiveReader::open(&files.0, &crate::CancellationToken::new()).unwrap();
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
                &crate::CancellationToken::new(),
            );
        }
        "commit" => {
            let mut writer =
                LiveWriter::open(&path, budget(), &crate::CancellationToken::new()).unwrap();
            let cancellation = CancellationToken::new();
            let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
            transaction
                .assign_v4(Ipv4Key(10), Ipv4Key(20), 123)
                .unwrap();
            let _ = transaction.commit();
        }
        "metadata" => {
            let mut writer =
                LiveWriter::open(&path, budget(), &crate::CancellationToken::new()).unwrap();
            let cancellation = CancellationToken::new();
            writer
                .set_metadata_json(b"metadata crash value", &cancellation)
                .unwrap();
            let _ = writer.commit(&cancellation);
        }
        "reader" => {
            let _reader = LiveReader::open(&path, &crate::CancellationToken::new()).unwrap();
            unsafe { libc::_exit(86) }
        }
        "writer" => {
            let _writer =
                LiveWriter::open(&path, budget(), &crate::CancellationToken::new()).unwrap();
            unsafe { libc::_exit(86) }
        }
        "reclaim" => {
            let mut writer =
                LiveWriter::open(&path, budget(), &crate::CancellationToken::new()).unwrap();
            let _ = writer.reclaim(10, 10_000, &CancellationToken::new());
        }
        "initialize" => {
            let _ = initialize_live(&path, 1, &CancellationToken::new());
        }
        "reset" => {
            let _ = reset_live_coordination(&path, 2, &CancellationToken::new());
        }
        _ => panic!("unknown child action"),
    }
    panic!("configured crash point was not reached");
}
