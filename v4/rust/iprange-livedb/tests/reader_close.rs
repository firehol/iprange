use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    c_abi_support::Reader as AbiReader, create_live, reset_live_coordination, AddressFamily,
    CancellationToken, CloseOutcome, Error, LiveReader, LiveResetPolicy, LiveTransitionStatus,
    ValueKind, ValueTag,
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
        let main = std::env::temp_dir().join(format!(
            "iprange-v4-reader-close-{label}-{}-{unique}",
            std::process::id()
        ));
        create_live(
            &main,
            AddressFamily::Ipv4,
            ValueKind::Direct,
            ValueTag::new(b"asn").unwrap(),
            1,
            &CancellationToken::new(),
        )
        .unwrap();
        Self { main }
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
        let _ = fs::remove_file(self.sidecar().with_extension("readers.saved"));
    }
}

#[test]
fn successful_close_is_idempotent_and_releases_all_locks() {
    let files = TestPair::new("idempotent");
    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();

    let closed = reader.close().unwrap();
    assert_eq!(closed.outcome, CloseOutcome::Closed);
    assert!(closed.cause.is_none());
    assert!(matches!(reader.info(), Err(Error::WrongState(_))));

    let reset = reset_live_coordination(
        &files.main,
        2,
        if cfg!(windows) {
            LiveResetPolicy::DiscardPrevious
        } else {
            LiveResetPolicy::RollbackSafe
        },
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(reset.status, LiveTransitionStatus::Initialized);
    assert_eq!(reader.close().unwrap().outcome, CloseOutcome::Closed);
}

#[test]
fn failed_close_keeps_exact_retry_authority() {
    let files = TestPair::new("retry");
    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    let saved = files.sidecar().with_extension("readers.saved");
    fs::rename(files.sidecar(), &saved).unwrap();

    let failed = reader.close().unwrap();
    assert_eq!(failed.outcome, CloseOutcome::CloseIncomplete);
    assert!(failed.cause.is_some());
    assert!(matches!(reader.info(), Err(Error::WrongState(_))));

    fs::rename(&saved, files.sidecar()).unwrap();
    assert_eq!(reader.close().unwrap().outcome, CloseOutcome::Closed);
    let mut replacement = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    replacement.close().unwrap();
}

#[test]
fn c_bridge_keeps_the_reader_until_close_really_finishes() {
    let files = TestPair::new("c-bridge-retry");
    let mut reader = AbiReader::open_live(&files.main, &CancellationToken::new()).unwrap();
    let saved = files.sidecar().with_extension("readers.saved");
    fs::rename(files.sidecar(), &saved).unwrap();

    assert!(reader.close().is_err());
    assert!(matches!(reader.info(), Err(Error::WrongState(_))));

    fs::rename(&saved, files.sidecar()).unwrap();
    reader.close().unwrap();
    assert!(matches!(reader.info(), Err(Error::WrongState(_))));
}
