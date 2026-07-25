//! Platform creator-only creation and access-policy proof.

#[cfg(unix)]
#[path = "security/posix.rs"]
mod platform;
#[cfg(windows)]
#[path = "security/windows.rs"]
mod platform;

#[cfg(windows)]
pub(crate) use platform::create_private;
#[cfg(unix)]
pub(crate) use platform::CREATOR_MODE;
pub(crate) use platform::{creator_only_commitment, secure_creator_only, Profile};
