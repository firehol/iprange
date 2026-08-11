//! Public live-pair creation adapter.

use std::path::Path;

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, StructureKind, ValueKind, ValueTag};
use crate::error::Result;

pub use crate::live_lifecycle::creation::{CreateResult, CreationState};

/// Create an empty transaction-1 live database and reader table.
pub fn create_live(
    path: impl AsRef<Path>,
    address_family: AddressFamily,
    value_kind: ValueKind,
    structure_kind: StructureKind,
    value_tag: ValueTag,
    reader_capacity: u32,
    cancellation: &CancellationToken,
) -> Result<CreateResult> {
    crate::live_lifecycle::creation::create_live(
        path.as_ref(),
        address_family,
        value_kind,
        structure_kind,
        value_tag,
        reader_capacity,
        cancellation,
    )
}
