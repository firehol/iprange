//! Reusable global-name algebra over retained membership scopes.

use std::ffi::c_void;
use std::mem::size_of;
use std::sync::Arc;

use iprange_livedb::c_abi_support::{MembershipAlgebra, MembershipScope};
use iprange_livedb::{AlgebraOutputMode, AlgebraSetOperation, FeedName, FeedSelection};

use crate::abi::{ByteSlice, Cancellation, Path, STATUS_OK};
use crate::abi_sdk::{
    AlgebraComparisonReport, AlgebraCountReport, AlgebraOutputBudget, AlgebraOutputModeInput,
    AlgebraSetOperationInput, AlgebraSetReport, FeedNameSinkFn, FeedSelectionInput,
    MembershipAlgebraBudget, OptionalByteSlice,
};
use crate::error::{
    call, call_with_output, call_with_outputs, input_slice, output_slot, require_struct_identity,
    required_input, required_output, BoundaryError, CallError, ErrorHandle,
};
use crate::handle::{MembershipAlgebraHandle, MembershipScopeHandle};
use crate::report::ReportHandle;
use crate::{callback, path, registry};

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_membership_algebra_create(
    scopes: *const *const MembershipScopeHandle,
    scope_count: u64,
    budget: *const MembershipAlgebraBudget,
    cancellation: Cancellation,
    output: *mut *mut MembershipAlgebraHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, output, "algebra output is null", || {
        // SAFETY: the pointer array, budget, and output are validated before cloning scopes.
        let output = unsafe { required_output(output, "algebra output is null")? };
        *output = std::ptr::null_mut();
        let scope_inputs = unsafe { input_slice(scopes, scope_count)? };
        let budget =
            unsafe { decode_budget(required_input(budget, "membership algebra budget is null")?)? };
        if scope_inputs.len() > budget.max_sources as usize {
            return Err(iprange_livedb::Error::BudgetExceeded("membership algebra sources").into());
        }
        let retained_bytes = scope_inputs
            .len()
            .checked_mul(size_of::<Arc<MembershipScope>>())
            .and_then(|bytes| u64::try_from(bytes).ok())
            .ok_or_else(|| {
                BoundaryError::invalid_length("membership algebra sources are too large")
            })?;
        if retained_bytes > budget.max_heap_bytes {
            return Err(
                iprange_livedb::Error::BudgetExceeded("membership algebra source heap").into(),
            );
        }
        let mut retained = Vec::new();
        retained
            .try_reserve_exact(scope_inputs.len())
            .map_err(|_| iprange_livedb::Error::BudgetExceeded("membership algebra source heap"))?;
        for &scope in scope_inputs {
            // SAFETY: each opaque pointer is validated before its retained Arc is cloned.
            let scope =
                unsafe { crate::handle::required_handle_input(scope, "membership scope is null")? };
            retained.push(scope.clone_scope()?);
        }
        let cancellation = callback::token(cancellation)?;
        let algebra = MembershipAlgebra::new(retained, budget, &cancellation)?;
        *output = Box::into_raw(Box::new(MembershipAlgebraHandle::new(algebra)));
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_membership_algebra_feeds(
    algebra: *const MembershipAlgebraHandle,
    callback: FeedNameSinkFn,
    context: *mut c_void,
    cancellation: Cancellation,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the opaque handle is validated before its gate is entered.
        let algebra =
            unsafe { crate::handle::required_handle_input(algebra, "membership algebra is null")? };
        let cancellation = callback::token(cancellation)?;
        algebra.with(|algebra| {
            crate::query::emit_names(algebra.feeds(), callback, context, &cancellation)
        })
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_membership_algebra_count(
    algebra: *const MembershipAlgebraHandle,
    selection: FeedSelectionInput,
    cancellation: Cancellation,
    output: *mut AlgebraCountReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, output, "algebra count report is null", || {
        // SAFETY: the handle and output are validated before the bounded operation.
        let algebra =
            unsafe { crate::handle::required_handle_input(algebra, "membership algebra is null")? };
        let output = unsafe { required_output(output, "algebra count report is null")? };
        *output = AlgebraCountReport::default();
        let selection = unsafe { ValidatedSelection::new(selection)? };
        let reserved = selection.heap_bytes()?;
        let cancellation = callback::token(cancellation)?;
        let report = algebra.with(|algebra| {
            algebra.require_operation_reservation(reserved)?;
            let decoded = selection.decode()?;
            let reserved = decoded.heap_bytes()?;
            Ok(algebra.count(decoded.view(), reserved, &cancellation)?)
        })?;
        *output = encode_count(report);
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_membership_algebra_compare(
    algebra: *const MembershipAlgebraHandle,
    left: FeedSelectionInput,
    right: FeedSelectionInput,
    cancellation: Cancellation,
    output: *mut AlgebraComparisonReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(
        error_output,
        output,
        "algebra comparison report is null",
        || {
            // SAFETY: the handle and output are validated before the bounded operation.
            let algebra = unsafe {
                crate::handle::required_handle_input(algebra, "membership algebra is null")?
            };
            let output = unsafe { required_output(output, "algebra comparison report is null")? };
            *output = AlgebraComparisonReport::default();
            let left = unsafe { ValidatedSelection::new(left)? };
            let right = unsafe { ValidatedSelection::new(right)? };
            let reserved = checked_add(left.heap_bytes()?, right.heap_bytes()?)?;
            let cancellation = callback::token(cancellation)?;
            let report = algebra.with(|algebra| {
                algebra.require_operation_reservation(reserved)?;
                let left = left.decode()?;
                let right = right.decode()?;
                let reserved = checked_add(left.heap_bytes()?, right.heap_bytes()?)?;
                Ok(algebra.compare(left.view(), right.view(), reserved, &cancellation)?)
            })?;
            *output = encode_comparison(report);
            Ok::<_, CallError>(())
        },
    )
}

#[no_mangle]
#[allow(clippy::too_many_arguments)]
pub unsafe extern "C" fn iprange_v4_abi1_membership_algebra_publish_set(
    algebra: *const MembershipAlgebraHandle,
    destination: Path,
    value_tag: ByteSlice,
    operation: AlgebraSetOperationInput,
    mode: AlgebraOutputModeInput,
    metadata_json: OptionalByteSlice,
    destination_policy: u32,
    budget: *const AlgebraOutputBudget,
    cancellation: Cancellation,
    semantic_output: *mut AlgebraSetReport,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_outputs(
        error_output,
        &[
            output_slot(semantic_output, "algebra set report is null"),
            output_slot(report_output, "publication report output is null"),
        ],
        || {
            // SAFETY: all pointers and tagged inputs are validated before publication starts.
            let algebra = unsafe {
                crate::handle::required_handle_input(algebra, "membership algebra is null")?
            };
            let semantic =
                unsafe { required_output(semantic_output, "algebra set report is null")? };
            *semantic = AlgebraSetReport::default();
            let report =
                unsafe { required_output(report_output, "publication report output is null")? };
            *report = std::ptr::null_mut();
            let operation = unsafe { ValidatedOperation::new(operation)? };
            let reserved = operation.heap_bytes()?;
            let mode = unsafe { decode_output_mode(mode)? };
            let destination = unsafe { path::decode(destination)? };
            let value_tag = unsafe { crate::lifecycle_ops::decode_value_tag(value_tag)? };
            let metadata =
                unsafe { crate::immutable_feed_ops::decode_optional_bytes(metadata_json)? };
            let policy = crate::publication_ops::decode_policy(destination_policy)?;
            let budget = unsafe {
                decode_output_budget(required_input(budget, "algebra output budget is null")?)?
            };
            let cancellation = callback::token(cancellation)?;
            let outcome = algebra.with(|algebra| {
                algebra.require_operation_reservation(reserved)?;
                let operation = operation.decode()?;
                let reserved = operation.heap_bytes()?;
                Ok(algebra.publish_set(
                    destination,
                    value_tag,
                    operation.view(),
                    mode,
                    metadata,
                    policy,
                    budget,
                    reserved,
                    &cancellation,
                ))
            })?;
            finish_publication(outcome, semantic, report)
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_membership_algebra_close(
    algebra: *mut MembershipAlgebraHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the handle is validated before its retained scopes are released.
        let algebra =
            unsafe { crate::handle::required_handle_input(algebra, "membership algebra is null")? };
        algebra.close()?;
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_membership_algebra_destroy(
    algebra: *mut MembershipAlgebraHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    if algebra.is_null() {
        return STATUS_OK;
    }
    call(error_output, || {
        // SAFETY: the handle is validated before ownership is consumed.
        let current =
            unsafe { crate::handle::required_handle_input(algebra, "membership algebra is null")? };
        if !current.is_closed()? {
            return Err(BoundaryError::handle_busy(
                "membership algebra must be closed before destroy",
            )
            .into());
        }
        // SAFETY: this allocation was created by this ABI and is consumed once.
        unsafe { drop(Box::from_raw(algebra)) };
        Ok::<_, CallError>(())
    })
}

enum ValidatedSelection<'a> {
    All,
    Named(&'a [ByteSlice]),
}

impl<'a> ValidatedSelection<'a> {
    unsafe fn new(input: FeedSelectionInput) -> Result<Self, BoundaryError> {
        if input.reserved != 0 {
            return Err(BoundaryError::reserved(
                "feed selection reserved field is nonzero",
            ));
        }
        match input.kind {
            registry::FEED_SELECTION_ALL => {
                if !input.names.is_null() || input.name_count != 0 {
                    return Err(BoundaryError::invalid_argument(
                        "all-feed selection has a name array",
                    ));
                }
                Ok(Self::All)
            }
            registry::FEED_SELECTION_NAMED => {
                // SAFETY: the caller supplies the complete descriptor array.
                let names = unsafe { input_slice(input.names, input.name_count)? };
                if names.is_empty() {
                    return Err(BoundaryError::invalid_length(
                        "named feed selection is empty",
                    ));
                }
                for name in names {
                    // SAFETY: validation reads each caller-declared byte extent synchronously.
                    unsafe { crate::membership::decode_name(name.pointer, name.length)? };
                }
                Ok(Self::Named(names))
            }
            _ => Err(BoundaryError::invalid_enum(
                "unknown algebra feed selection kind",
            )),
        }
    }

    fn heap_bytes(&self) -> Result<u64, BoundaryError> {
        let count = match self {
            Self::All => 0,
            Self::Named(names) => names.len(),
        };
        count
            .checked_mul(size_of::<FeedName>())
            .and_then(|bytes| u64::try_from(bytes).ok())
            .ok_or_else(|| BoundaryError::invalid_length("feed selection is too large"))
    }

    fn decode(&self) -> Result<DecodedSelection, CallError> {
        match self {
            Self::All => Ok(DecodedSelection::All),
            Self::Named(inputs) => {
                let mut names = Vec::new();
                names.try_reserve_exact(inputs.len()).map_err(|_| {
                    iprange_livedb::Error::BudgetExceeded("C algebra feed selection")
                })?;
                for input in *inputs {
                    // SAFETY: ValidatedSelection established every descriptor extent.
                    names.push(unsafe {
                        crate::membership::decode_name(input.pointer, input.length)?
                    });
                }
                Ok(DecodedSelection::Named(names))
            }
        }
    }
}

enum DecodedSelection {
    All,
    Named(Vec<FeedName>),
}

impl DecodedSelection {
    fn heap_bytes(&self) -> Result<u64, BoundaryError> {
        let capacity = match self {
            Self::All => 0,
            Self::Named(names) => names.capacity(),
        };
        capacity
            .checked_mul(size_of::<FeedName>())
            .and_then(|bytes| u64::try_from(bytes).ok())
            .ok_or_else(|| BoundaryError::invalid_length("feed selection is too large"))
    }

    fn view(&self) -> FeedSelection<'_> {
        match self {
            Self::All => FeedSelection::All,
            Self::Named(names) => FeedSelection::Named(names),
        }
    }
}

enum ValidatedOperation<'a> {
    Union(ValidatedSelection<'a>),
    Intersection(ValidatedSelection<'a>),
    Exclusion {
        included: ValidatedSelection<'a>,
        excluded: ValidatedSelection<'a>,
    },
}

impl<'a> ValidatedOperation<'a> {
    unsafe fn new(input: AlgebraSetOperationInput) -> Result<Self, BoundaryError> {
        if input.reserved != 0 {
            return Err(BoundaryError::reserved(
                "algebra operation reserved field is nonzero",
            ));
        }
        match input.kind {
            registry::ALGEBRA_SET_UNION => {
                require_unused(input.excluded)?;
                // SAFETY: the selection descriptors are validated synchronously.
                Ok(Self::Union(unsafe {
                    ValidatedSelection::new(input.included)?
                }))
            }
            registry::ALGEBRA_SET_INTERSECTION => {
                require_unused(input.excluded)?;
                // SAFETY: the selection descriptors are validated synchronously.
                Ok(Self::Intersection(unsafe {
                    ValidatedSelection::new(input.included)?
                }))
            }
            registry::ALGEBRA_SET_EXCLUSION => {
                // SAFETY: both selection descriptor arrays are validated synchronously.
                Ok(Self::Exclusion {
                    included: unsafe { ValidatedSelection::new(input.included)? },
                    excluded: unsafe { ValidatedSelection::new(input.excluded)? },
                })
            }
            _ => Err(BoundaryError::invalid_enum(
                "unknown algebra set operation kind",
            )),
        }
    }

    fn heap_bytes(&self) -> Result<u64, BoundaryError> {
        match self {
            Self::Union(selection) | Self::Intersection(selection) => selection.heap_bytes(),
            Self::Exclusion { included, excluded } => {
                checked_add(included.heap_bytes()?, excluded.heap_bytes()?)
            }
        }
    }

    fn decode(&self) -> Result<DecodedOperation, CallError> {
        Ok(match self {
            Self::Union(selection) => DecodedOperation::Union(selection.decode()?),
            Self::Intersection(selection) => DecodedOperation::Intersection(selection.decode()?),
            Self::Exclusion { included, excluded } => DecodedOperation::Exclusion {
                included: included.decode()?,
                excluded: excluded.decode()?,
            },
        })
    }
}

enum DecodedOperation {
    Union(DecodedSelection),
    Intersection(DecodedSelection),
    Exclusion {
        included: DecodedSelection,
        excluded: DecodedSelection,
    },
}

impl DecodedOperation {
    fn heap_bytes(&self) -> Result<u64, BoundaryError> {
        match self {
            Self::Union(selection) | Self::Intersection(selection) => selection.heap_bytes(),
            Self::Exclusion { included, excluded } => {
                checked_add(included.heap_bytes()?, excluded.heap_bytes()?)
            }
        }
    }

    fn view(&self) -> AlgebraSetOperation<'_> {
        match self {
            Self::Union(selection) => AlgebraSetOperation::Union(selection.view()),
            Self::Intersection(selection) => AlgebraSetOperation::Intersection(selection.view()),
            Self::Exclusion { included, excluded } => AlgebraSetOperation::Exclusion {
                included: included.view(),
                excluded: excluded.view(),
            },
        }
    }
}

fn require_unused(input: FeedSelectionInput) -> Result<(), BoundaryError> {
    if input.kind != 0 || input.reserved != 0 || !input.names.is_null() || input.name_count != 0 {
        return Err(BoundaryError::invalid_argument(
            "unused algebra feed selection is not empty",
        ));
    }
    Ok(())
}

unsafe fn decode_output_mode(
    input: AlgebraOutputModeInput,
) -> Result<AlgebraOutputMode, BoundaryError> {
    if input.reserved != 0 {
        return Err(BoundaryError::reserved(
            "algebra output mode reserved field is nonzero",
        ));
    }
    match input.kind {
        registry::ALGEBRA_OUTPUT_PRESERVE_FEEDS => {
            if !input.flat_name.pointer.is_null() || input.flat_name.length != 0 {
                return Err(BoundaryError::invalid_argument(
                    "preserved algebra output has a flat feed name",
                ));
            }
            Ok(AlgebraOutputMode::PreserveFeeds)
        }
        registry::ALGEBRA_OUTPUT_FLAT => {
            // SAFETY: the caller-declared name is decoded synchronously.
            let name = unsafe {
                crate::membership::decode_name(input.flat_name.pointer, input.flat_name.length)?
            };
            Ok(AlgebraOutputMode::Flat(name))
        }
        _ => Err(BoundaryError::invalid_enum("unknown algebra output mode")),
    }
}

unsafe fn decode_budget(
    input: &MembershipAlgebraBudget,
) -> Result<iprange_livedb::MembershipAlgebraBudget, BoundaryError> {
    require_struct_identity(
        input.abi_version,
        input.struct_size,
        size_of::<MembershipAlgebraBudget>(),
    )?;
    if input.reserved != 0 {
        return Err(BoundaryError::reserved(
            "membership algebra budget reserved field is nonzero",
        ));
    }
    Ok(iprange_livedb::MembershipAlgebraBudget {
        max_heap_bytes: input.max_heap_bytes,
        max_sources: input.max_sources,
    })
}

unsafe fn decode_output_budget(
    input: &AlgebraOutputBudget,
) -> Result<iprange_livedb::AlgebraOutputBudget, BoundaryError> {
    require_struct_identity(
        input.abi_version,
        input.struct_size,
        size_of::<AlgebraOutputBudget>(),
    )?;
    if input.reserved != 0 {
        return Err(BoundaryError::reserved(
            "algebra output budget reserved field is nonzero",
        ));
    }
    Ok(iprange_livedb::AlgebraOutputBudget {
        max_output_pages: input.max_output_pages,
        max_open_files: input.max_open_files,
    })
}

fn checked_add(left: u64, right: u64) -> Result<u64, BoundaryError> {
    left.checked_add(right)
        .ok_or_else(|| BoundaryError::invalid_length("algebra feed selection is too large"))
}

fn encode_count(report: iprange_livedb::AlgebraCountReport) -> AlgebraCountReport {
    AlgebraCountReport {
        abi_version: 1,
        struct_size: size_of::<AlgebraCountReport>() as u32,
        source_count: report.source_count,
        source_range_count: report.source_range_count,
        joined_segment_count: report.joined_segment_count,
        addresses: crate::report::cardinality(report.addresses),
    }
}

fn encode_comparison(report: iprange_livedb::AlgebraComparisonReport) -> AlgebraComparisonReport {
    AlgebraComparisonReport {
        abi_version: 1,
        struct_size: size_of::<AlgebraComparisonReport>() as u32,
        source_count: report.source_count,
        source_range_count: report.source_range_count,
        joined_segment_count: report.joined_segment_count,
        left_addresses: crate::report::cardinality(report.left_addresses),
        right_addresses: crate::report::cardinality(report.right_addresses),
        overlap_addresses: crate::report::cardinality(report.overlap_addresses),
        left_only_addresses: crate::report::cardinality(report.left_only_addresses),
        right_only_addresses: crate::report::cardinality(report.right_only_addresses),
        union_addresses: crate::report::cardinality(report.union_addresses),
        equal: u32::from(report.equal),
        reserved: 0,
    }
}

fn finish_publication(
    outcome: iprange_livedb::AlgebraSetOutcome,
    semantic: &mut AlgebraSetReport,
    report_output: &mut *mut ReportHandle,
) -> Result<(), CallError> {
    match outcome {
        Ok(result) => {
            *semantic = AlgebraSetReport {
                abi_version: 1,
                struct_size: size_of::<AlgebraSetReport>() as u32,
                source_count: result.report.source_count,
                source_range_count: result.report.source_range_count,
                joined_segment_count: result.report.joined_segment_count,
                output_feed_count: result.report.output_feed_count,
                output_range_count: result.report.output_range_count,
                output_addresses: crate::report::cardinality(result.report.output_addresses),
            };
            *report_output = Box::into_raw(Box::new(ReportHandle::publication(result.publication)));
            Ok(())
        }
        Err(mut failure) => {
            let mut report = ReportHandle::publication_preparation(
                &failure,
                registry::RESIDUE_OPERATION_ALGEBRA_PREPARATION_FAILURE,
            );
            report.set_cleanup_guard(failure.source_cleanup.take());
            *report_output = Box::into_raw(Box::new(report));
            let cleanup = failure.cleanup.iter().map(crate::facts::cleanup).collect();
            Err(ErrorHandle::publication_failure(failure.cause, cleanup, None).into())
        }
    }
}
