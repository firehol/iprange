//! Shared private immutable construction; workspace is unmapped before truncation.
use super::unordered::{workspace::Workspace, BuildFailure, Built, OutputStats, PreparedBudget};
use super::{Builder, OutputBudget, OutputSpec};
use crate::{contract::PAGE_SIZE, heap::Heap, mapping::Mapping, Error, Result};
use std::fs::File;

pub(crate) fn build(
    file: File,
    spec: OutputSpec,
    budget: PreparedBudget,
    needs_workspace: bool,
    operation: impl FnOnce(&mut Builder, Option<&mut Workspace>, u64) -> Result<OutputStats>,
) -> std::result::Result<Built, BuildFailure> {
    let output_budget = OutputBudget {
        max_output_pages: budget.max_output_pages,
    };
    let mut heap = Heap::new(budget.max_heap_bytes);
    let mut builder =
        Builder::new_owned_with_extent(file, spec, output_budget, budget.total_pages, &mut heap)
            .map_err(|failure| BuildFailure {
                file: failure.file,
                cause: failure.cause,
            })?;
    let mut workspace = if needs_workspace {
        let workspace_file = match builder.clone_file() {
            Ok(file) => file,
            Err(cause) => {
                return Err(BuildFailure {
                    file: builder.into_file(),
                    cause,
                });
            }
        };
        let Some(extent) = budget.total_pages.checked_mul(PAGE_SIZE as u64) else {
            return Err(BuildFailure {
                file: builder.into_file(),
                cause: Error::ArithmeticOverflow("immutable construction extent"),
            });
        };
        let mapping = match Mapping::read_write(workspace_file, extent) {
            Ok(mapping) => mapping,
            Err(cause) => {
                return Err(BuildFailure {
                    file: builder.into_file(),
                    cause,
                });
            }
        };
        let workspace = match Workspace::new(
            mapping,
            budget.max_output_pages,
            budget.total_pages,
            spec.transaction_id,
        ) {
            Ok(workspace) => workspace,
            Err(cause) => {
                return Err(BuildFailure {
                    file: builder.into_file(),
                    cause,
                });
            }
        };
        Some(workspace)
    } else {
        None
    };
    let result = operation(&mut builder, workspace.as_mut(), heap.remaining());
    drop(workspace);
    let report = match result {
        Ok(report) => report,
        Err(cause) => {
            return Err(BuildFailure {
                file: builder.into_file(),
                cause,
            });
        }
    };
    let finished = builder.finish_owned().map_err(|failure| BuildFailure {
        file: failure.builder.into_file(),
        cause: failure.cause,
    })?;
    Ok(Built {
        finished,
        input_record_count: report.input_record_count,
        normalized_interval_count: report.normalized_interval_count,
        addresses: report.addresses,
    })
}
