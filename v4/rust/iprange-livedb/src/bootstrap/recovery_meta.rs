use crate::contract::MetaV4;
use crate::mapping::ByteSource;

use super::{
    identity_readable, validate_commit_identity, validate_declared_page_count, validate_direct,
    validate_membership, validate_metadata, validate_range_count, validate_retirement_count,
    validate_roots, validate_structured, MetaProblem,
};

#[derive(Clone, Copy)]
pub(crate) struct RecoveryMetaState {
    pub(crate) order: Result<MetaV4, MetaProblem>,
    pub(crate) recovery: Result<MetaV4, MetaProblem>,
}

pub(crate) fn classify_recovery_meta<S: ByteSource>(page: S) -> RecoveryMetaState {
    let order = identity_readable(page).and_then(|identity| {
        validate_commit_identity(&identity.meta)?;
        Ok(identity.meta)
    });
    let recovery = order.and_then(recovery_valid);
    RecoveryMetaState { order, recovery }
}

fn recovery_valid(meta: MetaV4) -> Result<MetaV4, MetaProblem> {
    validate_declared_page_count(&meta)?;
    validate_roots(&meta)?;
    validate_range_count(&meta)?;
    validate_retirement_count(&meta)?;
    validate_metadata(&meta)?;
    match meta.value_kind {
        crate::contract::ValueKind::Direct => validate_direct(&meta)?,
        crate::contract::ValueKind::Membership => validate_membership(&meta)?,
        crate::contract::ValueKind::Structured => validate_structured(&meta)?,
    }
    Ok(meta)
}
