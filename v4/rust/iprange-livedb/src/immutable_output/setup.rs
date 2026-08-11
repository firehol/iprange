//! Identity, budget, and empty-metadata setup for immutable outputs.

use std::fs::File;

use crate::contract::{StructureKind, ValueKind, MAX_PAGE_COUNT};
use crate::error::{Error, Result};

use super::{OutputBudget, OutputSpec};

pub(super) fn require_new_output(
    file: &File,
    spec: OutputSpec,
    budget: OutputBudget,
) -> Result<()> {
    if file.metadata()?.len() != 0 {
        return Err(Error::InvalidArgument("immutable output file is not empty"));
    }
    require_identity(spec)?;
    require_limits(spec, budget)
}

fn require_identity(spec: OutputSpec) -> Result<()> {
    if spec.database_id == [0; 16] || spec.commit_nonce == [0; 16] || spec.transaction_id == 0 {
        return Err(Error::InvalidArgument(
            "immutable output identity is invalid",
        ));
    }
    let kinds_match = match spec.value_kind {
        ValueKind::Direct | ValueKind::Membership => spec.structure_kind == StructureKind::None,
        ValueKind::Structured => spec.structure_kind != StructureKind::None,
    };
    if !kinds_match {
        return Err(Error::WrongStructureKind(
            "immutable output value and structure kinds do not match",
        ));
    }
    Ok(())
}

fn require_limits(spec: OutputSpec, budget: OutputBudget) -> Result<()> {
    if budget.max_output_pages < 2 {
        return Err(Error::BudgetExceeded("immutable output pages"));
    }
    if spec.feed_index_limit > MAX_PAGE_COUNT
        || (spec.value_kind == ValueKind::Direct && spec.feed_index_limit != 0)
    {
        return Err(Error::InvalidArgument(
            "immutable output feed-index limit is invalid",
        ));
    }
    Ok(())
}
