//! Atomic installation of one prepared live sidecar.

use std::path::Path;

use crate::error::{Error, Result};
use crate::live_sidecar::{self, Identity};

/// Install `private` at `canonical` without losing an unexpected inode.
pub(super) fn install(
    private: &Path,
    canonical: &Path,
    private_identity: Identity,
    previous: Option<Identity>,
) -> Result<()> {
    match previous {
        Some(previous) => exchange(private, canonical, private_identity, previous),
        None => rename_no_replace(private, canonical),
    }
}

#[cfg(target_os = "linux")]
fn rename_no_replace(private: &Path, canonical: &Path) -> Result<()> {
    renameat2(private, canonical, libc::RENAME_NOREPLACE)
}

#[cfg(target_os = "linux")]
fn exchange(
    private: &Path,
    canonical: &Path,
    private_identity: Identity,
    previous: Identity,
) -> Result<()> {
    renameat2(private, canonical, libc::RENAME_EXCHANGE)?;
    if live_sidecar::verify_path(canonical, private_identity).is_ok()
        && live_sidecar::verify_path(private, previous).is_ok()
    {
        return Ok(());
    }

    let cause = Error::CleanupConflict("canonical coordination changed during reset");
    match renameat2(private, canonical, libc::RENAME_EXCHANGE) {
        Ok(()) => Err(cause),
        Err(cleanup) => Err(Error::CleanupIncomplete {
            cause: Box::new(cause),
            cleanup: Box::new(cleanup),
        }),
    }
}

#[cfg(target_os = "linux")]
fn renameat2(from: &Path, to: &Path, flags: libc::c_uint) -> Result<()> {
    use std::ffi::CString;
    use std::os::unix::ffi::OsStrExt;

    let from = CString::new(from.as_os_str().as_bytes())
        .map_err(|_| Error::InvalidArgument("live sidecar path contains NUL"))?;
    let to = CString::new(to.as_os_str().as_bytes())
        .map_err(|_| Error::InvalidArgument("live sidecar path contains NUL"))?;
    let result = unsafe {
        libc::renameat2(
            libc::AT_FDCWD,
            from.as_ptr(),
            libc::AT_FDCWD,
            to.as_ptr(),
            flags,
        )
    };
    if result == 0 {
        Ok(())
    } else {
        Err(std::io::Error::last_os_error().into())
    }
}

#[cfg(not(target_os = "linux"))]
fn rename_no_replace(_private: &Path, _canonical: &Path) -> Result<()> {
    Err(Error::Unsupported(
        "atomic no-replace sidecar installation is not implemented on this platform",
    ))
}

#[cfg(not(target_os = "linux"))]
fn exchange(
    _private: &Path,
    _canonical: &Path,
    _private_identity: Identity,
    _previous: Identity,
) -> Result<()> {
    Err(Error::Unsupported(
        "atomic sidecar exchange is not implemented on this platform",
    ))
}

#[cfg(all(test, target_os = "linux"))]
mod tests {
    use std::fs;
    use std::path::PathBuf;
    use std::time::{SystemTime, UNIX_EPOCH};

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
        let private = live_sidecar::create_private(&paths.private).unwrap();
        let original = live_sidecar::create_private(&paths.canonical).unwrap();
        let expected = live_sidecar::identity(&original).unwrap();
        fs::remove_file(&paths.canonical).unwrap();
        let foreign = live_sidecar::create_private(&paths.canonical).unwrap();
        let foreign_identity = live_sidecar::identity(&foreign).unwrap();
        assert_ne!(expected, foreign_identity);

        let result = install(
            &paths.private,
            &paths.canonical,
            live_sidecar::identity(&private).unwrap(),
            Some(expected),
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
