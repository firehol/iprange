//! Named membership queries and ordered provider joins.

use std::ffi::c_void;
use std::mem::size_of;

use iprange_livedb::c_abi_support::MembershipScope;
use iprange_livedb::{
    FeedName, MatchingFeedSink, MembershipAggregateSink, MembershipAggregationMode,
    MembershipJoinSink,
};

use crate::abi::{ByteSlice, Cancellation, FeedInfo, FeedSinkFn, Ip, STATUS_OK};
use crate::abi_sdk::{
    DirectJoinBudget, DirectJoinCell, DirectJoinReport, DirectJoinSinkFn, FeedCardinality,
    FeedCardinalitySinkFn, FeedNameSinkFn, FeedNameValue, FeedOverlap, FeedOverlapSinkFn,
    FeedPairInput, MatchingFeedsReport, MembershipAggregationReport, MembershipCrossCell,
    MembershipCrossSinkFn, MembershipJoinReport, MembershipQueryBudget, UncoveredFeed,
    UncoveredFeedSinkFn,
};
use crate::callback;
use crate::error::{
    call, call_with_output, input_slice, require_struct_identity, required_input, required_output,
    BoundaryError, CallError, ErrorHandle,
};
use crate::handle::{MembershipScopeHandle, ReaderHandle};
use crate::ip::{self, Key};
use crate::membership::decode_name;
use crate::reader::encode_feed;
use crate::{registry, sink};

const OUTPUT_BATCH: usize = 32;

pub(crate) fn emit_names(
    names: impl IntoIterator<Item = FeedName>,
    callback: FeedNameSinkFn,
    context: *mut c_void,
    cancellation: &iprange_livedb::CancellationToken,
) -> Result<(), CallError> {
    let mut batch = [FeedNameValue::default(); OUTPUT_BATCH];
    let mut used = 0usize;
    for name in names {
        batch[used] = encode_name(name);
        used += 1;
        if used == batch.len() {
            if cancellation.is_cancelled() {
                return Err(iprange_livedb::Error::Cancelled.into());
            }
            emit(callback, context, &batch, "feed name sink")?;
            used = 0;
        }
    }
    if used != 0 {
        if cancellation.is_cancelled() {
            return Err(iprange_livedb::Error::Cancelled.into());
        }
        emit(callback, context, &batch[..used], "feed name sink")?;
    }
    Ok(())
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reader_all_feeds_scope(
    reader: *const ReaderHandle,
    budget: *const MembershipQueryBudget,
    cancellation: Cancellation,
    output: *mut *mut MembershipScopeHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, output, "scope output is null", || {
        // SAFETY: pointers are validated before the scope retains the reader.
        let reader =
            unsafe { crate::handle::required_handle_input(reader, "reader handle is null")? };
        let output = unsafe { required_output(output, "scope output is null")? };
        *output = std::ptr::null_mut();
        let budget =
            unsafe { decode_query_budget(required_input(budget, "query budget is null")?)? };
        let cancellation = callback::token(cancellation)?;
        let scope = MembershipScope::all(reader.get()?.clone(), budget, &cancellation)?;
        *output = Box::into_raw(Box::new(MembershipScopeHandle::new(scope)));
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reader_named_feeds_scope(
    reader: *const ReaderHandle,
    names: *const ByteSlice,
    name_count: u64,
    budget: *const MembershipQueryBudget,
    cancellation: Cancellation,
    output: *mut *mut MembershipScopeHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, output, "scope output is null", || {
        // SAFETY: opaque, array, and output pointers are validated before use.
        let reader =
            unsafe { crate::handle::required_handle_input(reader, "reader handle is null")? };
        let output = unsafe { required_output(output, "scope output is null")? };
        *output = std::ptr::null_mut();
        let inputs = unsafe { input_slice(names, name_count)? };
        validate_names(inputs)?;
        let budget =
            unsafe { decode_query_budget(required_input(budget, "query budget is null")?)? };
        let cancellation = callback::token(cancellation)?;
        let decoded = inputs.iter().map(|input| {
            decode_core_name(*input)
                .map_err(|_| iprange_livedb::Error::InvalidArgument("feed name is invalid"))
        });
        let scope = MembershipScope::named_from(
            reader.get()?.clone(),
            inputs.len(),
            decoded,
            budget,
            &cancellation,
        )?;
        *output = Box::into_raw(Box::new(MembershipScopeHandle::new(scope)));
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_membership_scope_feeds(
    scope: *const MembershipScopeHandle,
    callback: FeedSinkFn,
    context: *mut c_void,
    cancellation: Cancellation,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the opaque handle is validated before its callback gate is entered.
        let scope =
            unsafe { crate::handle::required_handle_input(scope, "membership scope is null")? };
        let cancellation = callback::token(cancellation)?;
        scope.with(|scope| {
            let mut batch = [FeedInfo::default(); OUTPUT_BATCH];
            let mut used = 0usize;
            for feed in scope.feeds() {
                batch[used] = encode_feed(feed);
                used += 1;
                if used == batch.len() {
                    if cancellation.is_cancelled() {
                        return Err(iprange_livedb::Error::Cancelled.into());
                    }
                    emit(callback, context, &batch, "membership scope feed sink")?;
                    used = 0;
                }
            }
            if used != 0 {
                if cancellation.is_cancelled() {
                    return Err(iprange_livedb::Error::Cancelled.into());
                }
                emit(
                    callback,
                    context,
                    &batch[..used],
                    "membership scope feed sink",
                )?;
            }
            Ok(())
        })
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_reader_matching_feeds(
    reader: *const ReaderHandle,
    address: Ip,
    callback: FeedNameSinkFn,
    context: *mut c_void,
    cancellation: Cancellation,
    output: *mut MatchingFeedsReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, output, "matching report is null", || {
        // SAFETY: input and output pointers are validated before the query.
        let reader =
            unsafe { crate::handle::required_handle_input(reader, "reader handle is null")? };
        let output = unsafe { required_output(output, "matching report is null")? };
        *output = MatchingFeedsReport::default();
        let cancellation = callback::token(cancellation)?;
        let mut adapter = NameSink::new(callback, context);
        let result = match ip::decode(address)? {
            Key::V4(address) => {
                reader
                    .get()?
                    .matching_feeds_v4(address, &mut adapter, &cancellation)
            }
            Key::V6(address) => {
                reader
                    .get()?
                    .matching_feeds_v6(address, &mut adapter, &cancellation)
            }
        };
        let report = finish_callback(result, adapter.failure.take())?;
        finish_callback(adapter.flush(), adapter.failure.take())?;
        *output = MatchingFeedsReport {
            abi_version: 1,
            struct_size: size_of::<MatchingFeedsReport>() as u32,
            matching_feed_count: report.matching_feed_count,
        };
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_membership_scope_aggregate(
    scope: *const MembershipScopeHandle,
    mode: u32,
    target: ByteSlice,
    pairs: *const FeedPairInput,
    pair_count: u64,
    cardinality_callback: FeedCardinalitySinkFn,
    overlap_callback: FeedOverlapSinkFn,
    context: *mut c_void,
    cancellation: Cancellation,
    output: *mut MembershipAggregationReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, output, "aggregation report is null", || {
        // SAFETY: handle, slices, and output are validated before the scan.
        let scope =
            unsafe { crate::handle::required_handle_input(scope, "membership scope is null")? };
        let output = unsafe { required_output(output, "aggregation report is null")? };
        *output = MembershipAggregationReport::default();
        let pairs = unsafe { input_slice(pairs, pair_count)? };
        let cancellation = callback::token(cancellation)?;
        let mut adapter = AggregateSink {
            cardinality_callback,
            overlap_callback,
            context,
            failure: None,
        };
        let result = scope.with(|scope| {
            let request = unsafe { decode_aggregation(scope, mode, target, pairs)? };
            let reserved = request.reserved_heap_bytes()?;
            Ok(scope.aggregate_reserved(request.mode(), reserved, &mut adapter, &cancellation)?)
        });
        let report = finish_callback(result, adapter.failure)?;
        *output = encode_aggregation_report(report);
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_membership_scope_join_direct(
    scope: *const MembershipScopeHandle,
    direct_reader: *const ReaderHandle,
    budget: *const DirectJoinBudget,
    callback: DirectJoinSinkFn,
    context: *mut c_void,
    cancellation: Cancellation,
    output: *mut DirectJoinReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, output, "direct join report is null", || {
        // SAFETY: handles, budget, and output are validated before the join.
        let scope =
            unsafe { crate::handle::required_handle_input(scope, "membership scope is null")? };
        let direct = unsafe {
            crate::handle::required_handle_input(direct_reader, "direct reader is null")?
        };
        let budget =
            unsafe { decode_direct_budget(required_input(budget, "direct join budget is null")?)? };
        let output = unsafe { required_output(output, "direct join report is null")? };
        *output = DirectJoinReport::default();
        let cancellation = callback::token(cancellation)?;
        let mut adapter = DirectSink {
            callback,
            context,
            failure: None,
        };
        let result = scope.with(|scope| {
            Ok(scope.join_direct(direct.get()?, budget, &mut adapter, &cancellation)?)
        });
        let report = finish_callback(result, adapter.failure)?;
        *output = encode_direct_report(report);
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_membership_scope_join_membership(
    left: *const MembershipScopeHandle,
    right: *const MembershipScopeHandle,
    cross_callback: MembershipCrossSinkFn,
    uncovered_callback: UncoveredFeedSinkFn,
    context: *mut c_void,
    cancellation: Cancellation,
    output: *mut MembershipJoinReport,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(
        error_output,
        output,
        "membership join report is null",
        || {
            // SAFETY: handles and output are validated before either callback gate is entered.
            let left = unsafe {
                crate::handle::required_handle_input(left, "left membership scope is null")?
            };
            let right = unsafe {
                crate::handle::required_handle_input(right, "right membership scope is null")?
            };
            let output = unsafe { required_output(output, "membership join report is null")? };
            *output = MembershipJoinReport::default();
            let cancellation = callback::token(cancellation)?;
            let mut adapter = CrossSink {
                cross_callback,
                uncovered_callback,
                context,
                failure: None,
            };
            let result = if std::ptr::eq(left, right) {
                left.with(|scope| Ok(scope.join_membership(scope, &mut adapter, &cancellation)?))
            } else {
                left.with(|left| {
                    right.with(|right| {
                        Ok(left.join_membership(right, &mut adapter, &cancellation)?)
                    })
                })
            };
            let report = finish_callback(result, adapter.failure)?;
            *output = encode_membership_report(report);
            Ok::<_, CallError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_membership_scope_close(
    scope: *mut MembershipScopeHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the handle is validated before its lifetime slot is cleared.
        let scope =
            unsafe { crate::handle::required_handle_input(scope, "membership scope is null")? };
        scope.close()?;
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_membership_scope_destroy(
    scope: *mut MembershipScopeHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    if scope.is_null() {
        return STATUS_OK;
    }
    call(error_output, || {
        // SAFETY: the handle is validated before ownership is consumed.
        let current =
            unsafe { crate::handle::required_handle_input(scope, "membership scope is null")? };
        if !current.is_closed()? {
            return Err(BoundaryError::handle_busy(
                "membership scope must be closed before destroy",
            )
            .into());
        }
        // SAFETY: the allocation was created by this ABI and is consumed once.
        unsafe { drop(Box::from_raw(scope)) };
        Ok::<_, CallError>(())
    })
}

struct NameSink {
    callback: FeedNameSinkFn,
    context: *mut c_void,
    records: [FeedNameValue; OUTPUT_BATCH],
    used: usize,
    failure: Option<CallError>,
}

impl NameSink {
    fn new(callback: FeedNameSinkFn, context: *mut c_void) -> Self {
        Self {
            callback,
            context,
            records: [FeedNameValue::default(); OUTPUT_BATCH],
            used: 0,
            failure: None,
        }
    }

    fn flush(&mut self) -> iprange_livedb::Result<()> {
        if self.used == 0 {
            return Ok(());
        }
        let result = emit_core(
            self.callback,
            self.context,
            &self.records[..self.used],
            "matching feed sink",
            &mut self.failure,
        );
        if result.is_ok() {
            self.used = 0;
        }
        result
    }
}

impl MatchingFeedSink for NameSink {
    fn matching_feed(&mut self, feed: FeedName) -> iprange_livedb::Result<()> {
        self.records[self.used] = encode_name(feed);
        self.used += 1;
        if self.used == self.records.len() {
            self.flush()?;
        }
        Ok(())
    }
}

struct AggregateSink {
    cardinality_callback: FeedCardinalitySinkFn,
    overlap_callback: FeedOverlapSinkFn,
    context: *mut c_void,
    failure: Option<CallError>,
}

impl MembershipAggregateSink for AggregateSink {
    fn feed_cardinalities(
        &mut self,
        batch: &[iprange_livedb::FeedCardinality],
    ) -> iprange_livedb::Result<()> {
        let mut output = [FeedCardinality::default(); OUTPUT_BATCH];
        for (destination, source) in output.iter_mut().zip(batch) {
            *destination = FeedCardinality {
                feed: encode_name(source.feed),
                addresses: crate::report::cardinality(source.addresses),
            };
        }
        emit_core(
            self.cardinality_callback,
            self.context,
            &output[..batch.len()],
            "feed cardinality sink",
            &mut self.failure,
        )
    }

    fn feed_overlaps(
        &mut self,
        batch: &[iprange_livedb::FeedOverlap],
    ) -> iprange_livedb::Result<()> {
        let mut output = [FeedOverlap::default(); OUTPUT_BATCH];
        for (destination, source) in output.iter_mut().zip(batch) {
            *destination = FeedOverlap {
                left: encode_name(source.left),
                right: encode_name(source.right),
                addresses: crate::report::cardinality(source.addresses),
            };
        }
        emit_core(
            self.overlap_callback,
            self.context,
            &output[..batch.len()],
            "feed overlap sink",
            &mut self.failure,
        )
    }
}

struct DirectSink {
    callback: DirectJoinSinkFn,
    context: *mut c_void,
    failure: Option<CallError>,
}

impl iprange_livedb::DirectJoinSink for DirectSink {
    fn direct_join_cells(
        &mut self,
        batch: &[iprange_livedb::DirectJoinCell],
    ) -> iprange_livedb::Result<()> {
        let mut output = [DirectJoinCell::default(); OUTPUT_BATCH];
        for (destination, source) in output.iter_mut().zip(batch) {
            *destination = DirectJoinCell {
                feed: encode_name(source.feed),
                direct_present: u8::from(source.direct_value.is_some()),
                reserved: [0; 3],
                direct_value: source.direct_value.unwrap_or(0),
                addresses: crate::report::cardinality(source.addresses),
            };
        }
        emit_core(
            self.callback,
            self.context,
            &output[..batch.len()],
            "direct join sink",
            &mut self.failure,
        )
    }
}

struct CrossSink {
    cross_callback: MembershipCrossSinkFn,
    uncovered_callback: UncoveredFeedSinkFn,
    context: *mut c_void,
    failure: Option<CallError>,
}

impl MembershipJoinSink for CrossSink {
    fn membership_cross_cells(
        &mut self,
        batch: &[iprange_livedb::MembershipCrossCell],
    ) -> iprange_livedb::Result<()> {
        let mut output = [MembershipCrossCell::default(); OUTPUT_BATCH];
        for (destination, source) in output.iter_mut().zip(batch) {
            *destination = MembershipCrossCell {
                left: encode_name(source.left),
                right: encode_name(source.right),
                addresses: crate::report::cardinality(source.addresses),
            };
        }
        emit_core(
            self.cross_callback,
            self.context,
            &output[..batch.len()],
            "membership cross sink",
            &mut self.failure,
        )
    }

    fn uncovered_feeds(
        &mut self,
        batch: &[iprange_livedb::UncoveredFeed],
    ) -> iprange_livedb::Result<()> {
        let mut output = [UncoveredFeed::default(); OUTPUT_BATCH];
        for (destination, source) in output.iter_mut().zip(batch) {
            *destination = UncoveredFeed {
                side: match source.side {
                    iprange_livedb::UncoveredSide::Left => registry::UNCOVERED_SIDE_LEFT,
                    iprange_livedb::UncoveredSide::Right => registry::UNCOVERED_SIDE_RIGHT,
                },
                reserved: 0,
                feed: encode_name(source.feed),
                addresses: crate::report::cardinality(source.addresses),
            };
        }
        emit_core(
            self.uncovered_callback,
            self.context,
            &output[..batch.len()],
            "uncovered feed sink",
            &mut self.failure,
        )
    }
}

fn decode_query_budget(
    value: &MembershipQueryBudget,
) -> Result<iprange_livedb::MembershipQueryBudget, BoundaryError> {
    require_struct_identity(
        value.abi_version,
        value.struct_size,
        size_of::<MembershipQueryBudget>(),
    )?;
    Ok(iprange_livedb::MembershipQueryBudget {
        max_heap_bytes: value.max_heap_bytes,
    })
}

fn decode_direct_budget(
    value: &DirectJoinBudget,
) -> Result<iprange_livedb::DirectJoinBudget, BoundaryError> {
    require_struct_identity(
        value.abi_version,
        value.struct_size,
        size_of::<DirectJoinBudget>(),
    )?;
    Ok(iprange_livedb::DirectJoinBudget {
        max_result_cells: value.max_result_cells,
    })
}

#[allow(clippy::large_enum_variant)] // FeedName remains inline in this allocation-free adapter.
enum AggregationRequest {
    Cardinalities,
    AllPairs,
    Target(FeedName),
    Selected(Vec<iprange_livedb::FeedPair>),
}

impl AggregationRequest {
    fn mode(&self) -> MembershipAggregationMode<'_> {
        match self {
            Self::Cardinalities => MembershipAggregationMode::Cardinalities,
            Self::AllPairs => MembershipAggregationMode::AllPairs,
            Self::Target(target) => MembershipAggregationMode::TargetAgainstScope(*target),
            Self::Selected(pairs) => MembershipAggregationMode::SelectedPairs(pairs),
        }
    }

    fn reserved_heap_bytes(&self) -> Result<u64, BoundaryError> {
        let Self::Selected(pairs) = self else {
            return Ok(0);
        };
        pairs
            .capacity()
            .checked_mul(size_of::<iprange_livedb::FeedPair>())
            .and_then(|bytes| u64::try_from(bytes).ok())
            .ok_or_else(|| BoundaryError::invalid_length("selected feed pairs are too large"))
    }
}

unsafe fn decode_aggregation(
    scope: &MembershipScope,
    mode: u32,
    target: ByteSlice,
    pairs: &[FeedPairInput],
) -> Result<AggregationRequest, CallError> {
    match mode {
        registry::MEMBERSHIP_AGGREGATION_CARDINALITIES => {
            require_empty_selection(target, pairs)?;
            Ok(AggregationRequest::Cardinalities)
        }
        registry::MEMBERSHIP_AGGREGATION_ALL_PAIRS => {
            require_empty_selection(target, pairs)?;
            Ok(AggregationRequest::AllPairs)
        }
        registry::MEMBERSHIP_AGGREGATION_TARGET_AGAINST_SCOPE => {
            if !pairs.is_empty() {
                return Err(BoundaryError::invalid_argument(
                    "target aggregation does not accept selected pairs",
                )
                .into());
            }
            let target = unsafe { decode_name(target.pointer, target.length)? };
            Ok(AggregationRequest::Target(target))
        }
        registry::MEMBERSHIP_AGGREGATION_SELECTED_PAIRS => {
            if target.length != 0 || !target.pointer.is_null() {
                return Err(BoundaryError::invalid_argument(
                    "selected-pair aggregation does not accept a target",
                )
                .into());
            }
            if pairs.is_empty() {
                return Err(BoundaryError::invalid_length("selected feed pairs are empty").into());
            }
            let minimum_bytes = pairs
                .len()
                .checked_mul(size_of::<iprange_livedb::FeedPair>())
                .and_then(|bytes| u64::try_from(bytes).ok())
                .ok_or_else(|| {
                    BoundaryError::invalid_length("selected feed pairs are too large")
                })?;
            scope.require_operation_reservation(minimum_bytes)?;
            let mut decoded = Vec::new();
            decoded
                .try_reserve_exact(pairs.len())
                .map_err(|_| iprange_livedb::Error::BudgetExceeded("C selected feed pairs"))?;
            for pair in pairs {
                decoded.push(iprange_livedb::FeedPair {
                    left: unsafe { decode_name(pair.left.pointer, pair.left.length)? },
                    right: unsafe { decode_name(pair.right.pointer, pair.right.length)? },
                });
            }
            Ok(AggregationRequest::Selected(decoded))
        }
        _ => Err(BoundaryError::invalid_enum("unknown membership aggregation mode").into()),
    }
}

fn require_empty_selection(target: ByteSlice, pairs: &[FeedPairInput]) -> Result<(), CallError> {
    if target.length != 0 || !target.pointer.is_null() || !pairs.is_empty() {
        return Err(BoundaryError::invalid_argument(
            "aggregation mode received unused selection inputs",
        )
        .into());
    }
    Ok(())
}

fn validate_names(names: &[ByteSlice]) -> Result<(), BoundaryError> {
    for name in names {
        // SAFETY: validation reads the caller-declared byte extent synchronously.
        unsafe { decode_name(name.pointer, name.length)? };
    }
    Ok(())
}

fn decode_core_name(input: ByteSlice) -> Result<FeedName, BoundaryError> {
    // SAFETY: the input slice remains valid for the enclosing C call.
    unsafe { decode_name(input.pointer, input.length) }
}

pub(crate) fn encode_name(name: FeedName) -> FeedNameValue {
    let bytes = name.as_str().as_bytes();
    let mut output = FeedNameValue {
        length: bytes.len() as u32,
        ..FeedNameValue::default()
    };
    output.bytes[..bytes.len()].copy_from_slice(bytes);
    output
}

fn encode_aggregation_report(
    report: iprange_livedb::MembershipAggregationReport,
) -> MembershipAggregationReport {
    MembershipAggregationReport {
        abi_version: 1,
        struct_size: size_of::<MembershipAggregationReport>() as u32,
        scanned_range_count: report.scanned_range_count,
        scanned_addresses: crate::report::cardinality(report.scanned_addresses),
        feed_result_count: report.feed_result_count,
        pair_result_count: report.pair_result_count,
    }
}

fn encode_direct_report(report: iprange_livedb::DirectJoinReport) -> DirectJoinReport {
    DirectJoinReport {
        abi_version: 1,
        struct_size: size_of::<DirectJoinReport>() as u32,
        membership_range_count: report.membership_range_count,
        direct_ranges_visited: report.direct_ranges_visited,
        joined_segment_count: report.joined_segment_count,
        selected_addresses: crate::report::cardinality(report.selected_addresses),
        mapped_addresses: crate::report::cardinality(report.mapped_addresses),
        unmapped_addresses: crate::report::cardinality(report.unmapped_addresses),
        result_cell_count: report.result_cell_count,
    }
}

fn encode_membership_report(report: iprange_livedb::MembershipJoinReport) -> MembershipJoinReport {
    MembershipJoinReport {
        abi_version: 1,
        struct_size: size_of::<MembershipJoinReport>() as u32,
        left_range_count: report.left_range_count,
        right_range_count: report.right_range_count,
        joined_segment_count: report.joined_segment_count,
        left_addresses: crate::report::cardinality(report.left_addresses),
        right_addresses: crate::report::cardinality(report.right_addresses),
        overlap_addresses: crate::report::cardinality(report.overlap_addresses),
        left_uncovered_addresses: crate::report::cardinality(report.left_uncovered_addresses),
        right_uncovered_addresses: crate::report::cardinality(report.right_uncovered_addresses),
        cross_result_count: report.cross_result_count,
        uncovered_result_count: report.uncovered_result_count,
    }
}

fn emit<T>(
    callback: Option<
        unsafe extern "C" fn(*mut c_void, *const T, u64, *mut crate::CallbackFailure) -> u32,
    >,
    context: *mut c_void,
    records: &[T],
    label: &'static str,
) -> Result<(), CallError> {
    match sink::records(callback, context, records, label)? {
        sink::Control::Continue => Ok(()),
        sink::Control::Stop => Err(iprange_livedb::Error::StoppedBySink.into()),
    }
}

fn emit_core<T>(
    callback: Option<
        unsafe extern "C" fn(*mut c_void, *const T, u64, *mut crate::CallbackFailure) -> u32,
    >,
    context: *mut c_void,
    records: &[T],
    label: &'static str,
    failure: &mut Option<CallError>,
) -> iprange_livedb::Result<()> {
    match emit(callback, context, records, label) {
        Ok(()) => Ok(()),
        Err(CallError::Core(error)) => Err(error),
        Err(error) => {
            *failure = Some(error);
            Err(iprange_livedb::Error::SinkFailed(Box::new(
                iprange_livedb::Error::InvalidArgument("C query sink failed"),
            )))
        }
    }
}

fn finish_callback<T>(
    result: Result<T, impl Into<CallError>>,
    callback: Option<CallError>,
) -> Result<T, CallError> {
    match (result, callback) {
        (Ok(value), None) => Ok(value),
        (_, Some(error)) => Err(error),
        (Err(error), None) => Err(error.into()),
    }
}
