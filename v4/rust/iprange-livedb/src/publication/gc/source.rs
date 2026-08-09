//! Exact source-name and directory-role binding for Windows GC records.

use std::os::windows::ffi::OsStringExt;

use crate::{artifact_name, path};

use super::super::namespace::{private_name, Name, OUTPUT_PREFIX, RESERVATION_PREFIX};
use super::super::{ArtifactKind, DirectoryRole};

pub(super) const fn role_matches(kind: ArtifactKind, role: DirectoryRole) -> bool {
    match kind {
        ArtifactKind::PrivateOutput | ArtifactKind::PrivateReservation => {
            matches!(role, DirectoryRole::Destination)
        }
        ArtifactKind::OwnedCoordination => {
            matches!(role, DirectoryRole::Destination | DirectoryRole::MainFile)
        }
        ArtifactKind::AuthorizedScratch => matches!(role, DirectoryRole::ScratchDirectory),
        ArtifactKind::OwnedMain => matches!(role, DirectoryRole::MainFile),
        ArtifactKind::UnpublishedMainTail => false,
    }
}

pub(super) fn name_matches(
    kind: ArtifactKind,
    attempt_id: [u8; 16],
    ordinal: u32,
    source: &Name,
) -> bool {
    match kind {
        ArtifactKind::PrivateOutput => {
            private_name(OUTPUT_PREFIX, attempt_id).is_ok_and(|name| source.bytes() == name.bytes())
        }
        ArtifactKind::PrivateReservation => private_name(RESERVATION_PREFIX, attempt_id)
            .is_ok_and(|name| source.bytes() == name.bytes()),
        ArtifactKind::OwnedCoordination => coordination_source(source),
        ArtifactKind::AuthorizedScratch => {
            source.bytes() == scratch_name(attempt_id, ordinal).bytes()
        }
        ArtifactKind::OwnedMain => main_source(source),
        ArtifactKind::UnpublishedMainTail => false,
    }
}

fn scratch_name(attempt_id: [u8; 16], ordinal: u32) -> Name {
    let bytes = artifact_name::scratch_name(attempt_id, ordinal);
    Name::new(&bytes).expect("fixed GC-bound scratch name")
}

fn coordination_source(source: &Name) -> bool {
    const READERS: &[u8] = b".\0r\0e\0a\0d\0e\0r\0s\0";
    const RESET: &[u8] = b".\0r\0e\0a\0d\0e\0r\0s\0.\0r\0e\0s\0e\0t\0";
    let Some(main) = source
        .bytes()
        .strip_suffix(READERS)
        .or_else(|| source.bytes().strip_suffix(RESET))
    else {
        return false;
    };
    let units = main
        .chunks_exact(2)
        .map(|pair| u16::from_le_bytes([pair[0], pair[1]]))
        .collect::<Vec<_>>();
    path::validate_main_name(&std::ffi::OsString::from_wide(&units)).is_ok()
}

fn main_source(source: &Name) -> bool {
    let units = source
        .bytes()
        .chunks_exact(2)
        .map(|pair| u16::from_le_bytes([pair[0], pair[1]]))
        .collect::<Vec<_>>();
    path::validate_main_name(&std::ffi::OsString::from_wide(&units)).is_ok()
}
