use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use super::*;
use crate::publication::{CleanupState, CoordinationCleanup};
use crate::validation::ValidationProgress;
use crate::{create_live, AddressFamily, ErrorCode, ValueKind, ValueTag};

struct TestFile {
    main: PathBuf,
    saved_sidecar: PathBuf,
}

impl TestFile {
    fn new() -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let main = std::env::temp_dir().join(format!(
            "iprange-v4-validation-cleanup-{}-{unique}",
            std::process::id()
        ));
        let saved_sidecar = main.with_extension("readers.saved");
        Self {
            main,
            saved_sidecar,
        }
    }
}

impl Drop for TestFile {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.main);
        let _ = fs::remove_file(&self.saved_sidecar);
        if let Ok(sidecar) = crate::path::canonical_sidecar(&self.main) {
            let _ = fs::remove_file(sidecar);
        }
    }
}

#[test]
#[cfg(any(target_os = "linux", target_vendor = "apple", windows))]
fn failed_live_release_returns_one_retryable_cleanup_guard() {
    let file = TestFile::new();
    let cancellation = CancellationToken::new();
    create_live(
        &file.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::RETENTION,
        1,
        &cancellation,
    )
    .unwrap();
    let LiveOpened::Selected(source) = LiveSource::open(&file.main, &cancellation).unwrap() else {
        panic!("new live database must select a generation");
    };
    let sidecar = crate::path::canonical_sidecar(&file.main).unwrap();
    fs::rename(&sidecar, &file.saved_sidecar).unwrap();

    let end = source.finish(Ok(()));
    let mut failure = crate::validation::failure_with_guard(
        end.cause.expect("release failure must report its cause"),
        ValidationProgress::new(),
        end.guard,
    );
    assert_eq!(
        failure.coordination_cleanup,
        CoordinationCleanup::CleanupGuard
    );
    assert_eq!(failure.cleanup_state(), CleanupState::ResiduePossible);
    assert!(failure.cleanup.is_empty());

    let guard = failure
        .source_cleanup
        .as_mut()
        .expect("failed release must retain cleanup authority");
    assert_eq!(guard.last_problem().code, ErrorCode::NameNotFound);
    fs::rename(&file.saved_sidecar, sidecar).unwrap();
    assert!(guard.retry_cleanup().unwrap());
    assert!(!guard.cleanup_pending());
    assert!(!guard.retry_cleanup().unwrap());
}
