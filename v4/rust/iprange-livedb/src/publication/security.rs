//! Platform creator-only creation and access-policy proof.

const COMMITMENT_DOMAIN: &[u8; 8] = b"IPR4PSEC";

/// Process-wide creator-only switch. Off unless `IPRANGE_CREATOR_ONLY`
/// is exactly `1`. A database that already records the choice still
/// follows that record. This is not a per-file default.
pub fn creator_only_requested() -> bool {
    std::env::var_os("IPRANGE_CREATOR_ONLY").as_deref() == Some(std::ffi::OsStr::new("1"))
}

/// Serializes every test that mutates or asserts the process switch.
/// Rust test binaries run their tests on parallel threads, so an
/// unserialized `set_var` could flip the switch under a concurrent test
/// that reads it through a product path. Available on every test
/// platform: the switch-asserting tests include portable ones (the
/// crafted-header classifier test), not only the unix mode tests.
#[cfg(test)]
pub(crate) static CREATOR_ONLY_ENV_LOCK: std::sync::Mutex<()> =
    std::sync::Mutex::new(());

/// Turns the process switch on for one test and restores the previous
/// value when dropped. Rust tests in one binary share the process, so a
/// bare set leaks into the next test. The guard also holds the env lock
/// for its lifetime, serializing it against every other switch-asserting
/// test in the binary.
#[cfg(all(test, unix))]
pub(crate) struct CreatorOnlyGuard {
    previous: Option<std::ffi::OsString>,
    lock: Option<std::sync::MutexGuard<'static, ()>>,
}

#[cfg(all(test, unix))]
impl CreatorOnlyGuard {
    pub(crate) fn on() -> Self {
        let lock = Some(
            CREATOR_ONLY_ENV_LOCK
                .lock()
                .unwrap_or_else(|poisoned| poisoned.into_inner()),
        );
        let previous = std::env::var_os("IPRANGE_CREATOR_ONLY");
        std::env::set_var("IPRANGE_CREATOR_ONLY", "1");
        Self { previous, lock }
    }
}

#[cfg(all(test, unix))]
impl Drop for CreatorOnlyGuard {
    fn drop(&mut self) {
        // Release the serialization lock only after the switch is
        // restored, so the next guard observes the restored value.
        let lock = self.lock.take();
        match self.previous.take() {
            Some(value) => std::env::set_var("IPRANGE_CREATOR_ONLY", value),
            None => std::env::remove_var("IPRANGE_CREATOR_ONLY"),
        }
        drop(lock);
    }
}

#[cfg(unix)]
#[path = "security/posix.rs"]
mod platform;
#[cfg(windows)]
#[path = "security/windows.rs"]
mod platform;

#[cfg(windows)]
pub(crate) use platform::{create_private, create_unprotected};
#[cfg(windows)]
#[doc(hidden)]
pub use platform::create_private_artifact;
/// The creator-only creation mode on POSIX platforms (Windows applies
/// the DACL instead). Platform-independent by value so every create
/// site names the same constant (the wrong-site-mutation confusion of
/// round 8 came from two hand-typed literals).
pub(crate) const CREATOR_MODE: u32 = 0o600;
pub(crate) use platform::{creator_only_commitment, secure_creator_only, Profile};
