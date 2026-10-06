//! Platform creator-only creation and access-policy proof.

const COMMITMENT_DOMAIN: &[u8; 8] = b"IPR4PSEC";

/// Process-wide creator-only switch. Off unless `IPRANGE_CREATOR_ONLY`
/// is exactly `1`. A database that already records the choice still
/// follows that record. This is not a per-file default.
pub fn creator_only_requested() -> bool {
    std::env::var_os("IPRANGE_CREATOR_ONLY").as_deref() == Some(std::ffi::OsStr::new("1"))
}

/// Turns the process switch on for one test and restores the previous
/// value when dropped. Rust tests in one binary share the process, so a
/// bare set leaks into the next test.
#[cfg(test)]
pub(crate) struct CreatorOnlyGuard {
    previous: Option<std::ffi::OsString>,
}

#[cfg(test)]
impl CreatorOnlyGuard {
    pub(crate) fn on() -> Self {
        let previous = std::env::var_os("IPRANGE_CREATOR_ONLY");
        std::env::set_var("IPRANGE_CREATOR_ONLY", "1");
        Self { previous }
    }
}

#[cfg(test)]
impl Drop for CreatorOnlyGuard {
    fn drop(&mut self) {
        match self.previous.take() {
            Some(value) => std::env::set_var("IPRANGE_CREATOR_ONLY", value),
            None => std::env::remove_var("IPRANGE_CREATOR_ONLY"),
        }
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
#[cfg(unix)]
pub(crate) use platform::CREATOR_MODE;
pub(crate) use platform::{creator_only_commitment, secure_creator_only, Profile};
