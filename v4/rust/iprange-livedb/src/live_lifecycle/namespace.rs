//! Atomic installation of one prepared live sidecar.

use std::fs::File;
use std::path::Path;

#[cfg(not(any(target_os = "linux", target_vendor = "apple")))]
use crate::error::Error;
use crate::error::Result;
use crate::live_sidecar::{self, Identity};

use super::LiveResetPolicy;

/// Install `private` at `canonical` without losing an unexpected inode.
pub(super) fn install(
    private: &Path,
    private_file: &File,
    canonical: &Path,
    private_identity: Identity,
    previous: Option<Identity>,
    policy: LiveResetPolicy,
) -> Result<()> {
    match previous {
        Some(previous) if policy == LiveResetPolicy::RollbackSafe => {
            exchange(private, private_file, canonical, private_identity, previous)
        }
        Some(previous) => live_sidecar::install_replace_discarding(
            private,
            private_file,
            canonical,
            private_identity,
            previous,
        ),
        None => live_sidecar::install_noreplace(private, private_file, canonical, private_identity),
    }
}

#[cfg(any(target_os = "linux", target_vendor = "apple"))]
fn exchange(
    private: &Path,
    private_file: &File,
    canonical: &Path,
    private_identity: Identity,
    previous: Identity,
) -> Result<()> {
    live_sidecar::install_exchange(private, private_file, canonical, private_identity, previous)
}

#[cfg(not(any(target_os = "linux", target_vendor = "apple")))]
fn exchange(
    _private: &Path,
    _private_file: &File,
    _canonical: &Path,
    _private_identity: Identity,
    _previous: Identity,
) -> Result<()> {
    Err(Error::DurabilityUnsupported(
        "atomic sidecar exchange is not implemented on this platform",
    ))
}

#[cfg(all(test, target_os = "linux"))]
mod tests {
    use std::fs;
    use std::path::PathBuf;
    use std::time::{SystemTime, UNIX_EPOCH};

    use crate::error::Error;

    use super::*;

    struct Paths {
        private: PathBuf,
        canonical: PathBuf,
    }

    impl Paths {
        fn new() -> Self {
            let unique = SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos();
            let base = std::env::temp_dir().join(format!(
                "iprange-v4-namespace-{}-{unique}",
                std::process::id()
            ));
            Self {
                private: base.with_extension("private"),
                canonical: base.with_extension("canonical"),
            }
        }
    }

    impl Drop for Paths {
        fn drop(&mut self) {
            let _ = fs::remove_file(&self.private);
            let _ = fs::remove_file(&self.canonical);
        }
    }

    #[test]
    fn exchange_restores_a_foreign_canonical_inode() {
        let paths = Paths::new();
        let private = live_sidecar::create_private_for_test(&paths.private).unwrap();
        let original = live_sidecar::create_private_for_test(&paths.canonical).unwrap();
        let expected = live_sidecar::identity(&original).unwrap();
        fs::remove_file(&paths.canonical).unwrap();
        let foreign = live_sidecar::create_private_for_test(&paths.canonical).unwrap();
        let foreign_identity = live_sidecar::identity(&foreign).unwrap();
        assert_ne!(expected, foreign_identity);

        let result = install(
            &paths.private,
            &private,
            &paths.canonical,
            live_sidecar::identity(&private).unwrap(),
            Some(expected),
            LiveResetPolicy::RollbackSafe,
        );
        assert!(
            matches!(result, Err(Error::CleanupConflict(_))),
            "{result:?}"
        );
        live_sidecar::verify_path(&paths.canonical, foreign_identity).unwrap();
        live_sidecar::verify_path(&paths.private, live_sidecar::identity(&private).unwrap())
            .unwrap();
    }
}
