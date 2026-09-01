//! `iprange.v1.system.describe`

use serde_json::{json, Value};

use super::super::dispatch::HandlerError;
use super::super::session::SessionState;

/// system.describe takes no parameters.
pub fn validate_describe_params(params: &Value) -> Result<(), String> {
    if params.as_object().map(|o| o.is_empty()) != Some(true) {
        return Err("describe takes no parameters".into());
    }
    Ok(())
}

/// Capability discovery.
pub fn describe(_state: &mut SessionState, _params: Value) -> Result<Value, HandlerError> {
    let worker = iprange_livedb::validation::worker_availability();
    Ok(json!({
        "method": "iprange.v1.system.describe",
        "product": "iprange",
        "product_version": env!("CARGO_PKG_VERSION"),
        "implementation": "rust",
        "jsonrpc_version": "2.0",
        "api_version": "1",
        "format": "iprange-v4-phase1-unsigned",
        "platform": platform_name(),
        "families": ["ipv4", "ipv6"],
        "methods": super::super::dispatch::advertised(),
        "export_formats": ["netset", "ipset", "ranges", "csv", "jsonl", "legacy_binary"],
        "limits": {
            "input_frame_bytes": "1048576",
            "output_frame_bytes": "1048576",
            "response_object_bytes": "65000",
            "batch_requests": 16,
            "queued_requests": 16,
            "reader_handles": 64,
            "cursor_handles": 64,
            "lookup_addresses": 4096,
            "cursor_records": 4096
        },
        "fault_worker": {
            "available": worker.available,
            "protocol": worker.protocol,
        },
        "platform_result_fields": []
    }))
}

fn platform_name() -> &'static str {
    if cfg!(target_os = "linux") {
        "linux"
    } else if cfg!(target_os = "macos") {
        "macos"
    } else if cfg!(target_os = "windows") {
        "windows"
    } else if cfg!(target_os = "freebsd") {
        "freebsd"
    } else {
        "other"
    }
}
