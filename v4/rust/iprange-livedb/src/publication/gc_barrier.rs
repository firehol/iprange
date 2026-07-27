//! Ordinary-operation precedence for Windows GC authority.

use super::namespace::{Directory, Identity, Name};
use super::problem::Problem;
use super::{ArtifactKind, DirectoryRole};

pub(crate) fn require_source_available(
    directory: &Directory,
    attempt_id: [u8; 16],
    ordinal: u32,
    kind: ArtifactKind,
    role: DirectoryRole,
    name: &Name,
    identity: Identity,
) -> Result<(), Problem> {
    #[cfg(windows)]
    {
        super::gc::require_source_available(
            directory, attempt_id, ordinal, kind, role, name, identity,
        )
    }
    #[cfg(not(windows))]
    {
        let _ = (directory, attempt_id, ordinal, kind, role, name, identity);
        Ok(())
    }
}
