use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, initialize_live, reset_live_coordination, resolve_live_transition, AddressFamily,
    CancellationToken, Error, ImmutableReader, LiveCoordinationLocation, LiveReader,
    LiveResetPolicy, LiveTransitionOperation, LiveTransitionResolutionMode, LiveTransitionStatus,
    LiveWriter, TransactionBudget, ValueKind, ValueTag,
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
                "iprange-v4-transition-{label}-{}-{unique}",
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

fn create(files: &TestPair, capacity: u32) {
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"asn").unwrap(),
        capacity,
        &CancellationToken::new(),
    )
    .unwrap();
}

fn budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 2 * 1024 * 1024,
        max_private_pages: 1_000,
        max_file_growth_pages: 1_000,
        max_open_files: 2,
    }
}

#[test]
fn immutable_main_is_initialized_explicitly() {
    let files = TestPair::new("initialize");
    create(&files, 1);
    fs::remove_file(files.sidecar()).unwrap();

    let immutable = ImmutableReader::open(&files.main).unwrap();
    assert_eq!(immutable.info().transaction_id, 1);
    drop(immutable);

    let result = initialize_live(&files.main, 3, &CancellationToken::new()).unwrap();
    assert_eq!(result.operation, LiveTransitionOperation::Initialize);
    assert_eq!(result.status, LiveTransitionStatus::Initialized);
    assert_eq!(
        result.new_sidecar_location,
        LiveCoordinationLocation::Canonical
    );
    assert_eq!(result.reader_capacity, 3);
    assert!(result.cause.is_none());
    assert_eq!(
        resolve_live_transition(
            &files.main,
            &result,
            LiveTransitionResolutionMode::Complete,
            &CancellationToken::new(),
        )
        .unwrap()
        .status,
        LiveTransitionStatus::Initialized
    );

    assert!(ImmutableReader::open(&files.main).is_err());
    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(reader.info().unwrap().transaction_id, 1);
    reader.close().unwrap();
}

#[test]
fn initialization_never_repairs_existing_coordination() {
    let files = TestPair::new("initialize-existing");
    create(&files, 1);

    assert!(matches!(
        initialize_live(&files.main, 2, &CancellationToken::new()),
        Err(Error::WrongMode(_))
    ));
    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    reader.close().unwrap();
}

#[test]
#[cfg(any(target_os = "linux", target_vendor = "apple"))]
fn reset_replaces_corrupt_coordination_without_changing_the_main() {
    let files = TestPair::new("reset");
    create(&files, 1);
    let before = fs::read(&files.main).unwrap();
    fs::write(files.sidecar(), b"corrupt").unwrap();

    let result = reset_live_coordination(
        &files.main,
        2,
        LiveResetPolicy::RollbackSafe,
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(result.operation, LiveTransitionOperation::Reset);
    assert_eq!(result.reset_policy, Some(LiveResetPolicy::RollbackSafe));
    assert_eq!(result.status, LiveTransitionStatus::Initialized);
    assert_eq!(
        result.new_sidecar_location,
        LiveCoordinationLocation::Canonical
    );
    assert!(result.previous_sidecar_identity.is_some());
    assert!(result.new_sidecar_identity.is_some());
    assert_ne!(
        result.previous_sidecar_identity,
        result.new_sidecar_identity
    );
    assert_eq!(fs::read(&files.main).unwrap(), before);
    assert_eq!(
        resolve_live_transition(
            &files.main,
            &result,
            LiveTransitionResolutionMode::Complete,
            &CancellationToken::new(),
        )
        .unwrap()
        .status,
        LiveTransitionStatus::Initialized
    );

    let mut first = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    let mut second = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert!(matches!(
        LiveReader::open(&files.main, &CancellationToken::new()),
        Err(Error::ReaderCapacityExhausted)
    ));
    first.close().unwrap();
    second.close().unwrap();
}

#[test]
#[cfg(windows)]
fn rollback_safe_reset_fails_before_changing_either_file() {
    let files = TestPair::new("reset-strict-unsupported");
    create(&files, 1);
    fs::write(files.sidecar(), b"corrupt").unwrap();
    let main = fs::read(&files.main).unwrap();
    let sidecar = fs::read(files.sidecar()).unwrap();

    let error = reset_live_coordination(
        &files.main,
        2,
        LiveResetPolicy::RollbackSafe,
        &CancellationToken::new(),
    )
    .unwrap_err();

    assert!(matches!(error, Error::DurabilityUnsupported(_)));
    assert_eq!(fs::read(&files.main).unwrap(), main);
    assert_eq!(fs::read(files.sidecar()).unwrap(), sidecar);
    let mut private = files.sidecar().as_os_str().to_os_string();
    private.push(".reset");
    assert!(!PathBuf::from(private).exists());
}

#[test]
fn discarding_reset_reports_policy_and_cannot_roll_back_after_installation() {
    let files = TestPair::new("reset-discard");
    create(&files, 1);
    let before = fs::read(&files.main).unwrap();
    fs::write(files.sidecar(), b"corrupt").unwrap();

    let result = reset_live_coordination(
        &files.main,
        2,
        LiveResetPolicy::DiscardPrevious,
        &CancellationToken::new(),
    )
    .unwrap();

    assert_eq!(result.status, LiveTransitionStatus::Initialized);
    assert_eq!(result.reset_policy, Some(LiveResetPolicy::DiscardPrevious));
    assert_eq!(fs::read(&files.main).unwrap(), before);
    let canonical = fs::read(files.sidecar()).unwrap();
    let error = resolve_live_transition(
        &files.main,
        &result,
        LiveTransitionResolutionMode::Rollback,
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert!(matches!(error, Error::Unresolvable(_)));
    assert_eq!(fs::read(files.sidecar()).unwrap(), canonical);

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    reader.close().unwrap();
}

#[test]
fn cancelled_transition_leaves_an_immutable_main_unchanged() {
    let files = TestPair::new("cancelled");
    create(&files, 1);
    fs::remove_file(files.sidecar()).unwrap();
    let before = fs::read(&files.main).unwrap();
    let cancelled = CancellationToken::new();
    cancelled.cancel();

    assert!(matches!(
        initialize_live(&files.main, 2, &cancelled),
        Err(Error::Cancelled)
    ));
    assert_eq!(fs::read(&files.main).unwrap(), before);
    assert!(!files.sidecar().exists());
    assert_eq!(
        ImmutableReader::open(&files.main)
            .unwrap()
            .info()
            .transaction_id,
        1
    );
}

#[test]
fn cancelled_creation_and_open_change_nothing() {
    let absent = TestPair::new("cancelled-create");
    let cancelled = CancellationToken::new();
    cancelled.cancel();
    assert!(matches!(
        create_live(
            &absent.main,
            AddressFamily::Ipv4,
            ValueKind::Direct,
            ValueTag::new(b"asn").unwrap(),
            2,
            &cancelled,
        ),
        Err(Error::Cancelled)
    ));
    assert!(!absent.main.exists());
    assert!(!absent.sidecar().exists());

    let files = TestPair::new("cancelled-open");
    create(&files, 2);
    assert!(matches!(
        LiveReader::open(&files.main, &cancelled),
        Err(Error::Cancelled)
    ));
    assert!(matches!(
        LiveWriter::open(&files.main, budget(), &cancelled),
        Err(Error::Cancelled)
    ));

    let active = CancellationToken::new();
    let mut reader = LiveReader::open(&files.main, &active).unwrap();
    reader.close().unwrap();
    let mut writer = LiveWriter::open(&files.main, budget(), &active).unwrap();
    writer.close().unwrap();
}
