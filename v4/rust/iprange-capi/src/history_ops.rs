//! One-pass last-seen history projection.

use iprange_livedb::HistoryWindow;

use crate::abi::Cancellation;
use crate::abi_sdk::HistoryWindowInput;
use crate::callback;
use crate::error::{
    call_with_output, input_slice, required_output, BoundaryError, CallError, ErrorHandle,
};
use crate::handle::{ReaderHandle, WriterHandle};
use crate::membership::decode_name;
use crate::report::ReportHandle;

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_writer_project_history(
    writer: *const WriterHandle,
    last_seen_reader: *const ReaderHandle,
    windows: *const HistoryWindowInput,
    window_count: u64,
    cancellation: Cancellation,
    report_output: *mut *mut ReportHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(error_output, report_output, "report output is null", || {
        // SAFETY: handles, input array, and output are validated before mutation.
        let writer =
            unsafe { crate::handle::required_handle_input(writer, "writer handle is null")? };
        let source = unsafe {
            crate::handle::required_handle_input(last_seen_reader, "last-seen reader is null")?
        };
        let windows = unsafe { input_slice(windows, window_count)? };
        validate_windows(windows)?;
        let output = unsafe { required_output(report_output, "report output is null")? };
        *output = std::ptr::null_mut();
        let cancellation = callback::token(cancellation)?;
        let source = source.get()?.clone();
        let decoded = windows.iter().map(|window| {
            decode_window(*window)
                .map_err(|_| iprange_livedb::Error::InvalidArgument("history window is invalid"))
        });
        let report = writer.with_mut(|writer| {
            Ok(writer.project_history_from(source, windows.len(), decoded, &cancellation)?)
        })?;
        *output = Box::into_raw(Box::new(ReportHandle::history_projection(report)));
        Ok::<_, CallError>(())
    })
}

fn validate_windows(windows: &[HistoryWindowInput]) -> Result<(), BoundaryError> {
    if windows.is_empty() {
        return Err(BoundaryError::invalid_length("history windows are empty"));
    }
    for window in windows {
        if window.reserved != 0 {
            return Err(BoundaryError::reserved(
                "history window reserved field is nonzero",
            ));
        }
        // SAFETY: the caller-declared name is read synchronously.
        unsafe { input_slice(window.feed_name.pointer, window.feed_name.length)? };
        // Preserve the exact public feed-name validation classifier.
        unsafe { decode_name(window.feed_name.pointer, window.feed_name.length)? };
    }
    Ok(())
}

fn decode_window(input: HistoryWindowInput) -> Result<HistoryWindow, BoundaryError> {
    // SAFETY: validation established this input's complete readable extent.
    let feed_name = unsafe { decode_name(input.feed_name.pointer, input.feed_name.length)? };
    Ok(HistoryWindow {
        feed_name,
        cutoff: input.cutoff,
    })
}
