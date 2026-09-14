//! Shared streaming legacy-compatible text input and atomic bounded
//! text/result output (SOW-0028).
//!
//! Persistence handlers never use this module to read or write v4
//! database bytes; it serves the released legacy surface and the
//! caller-selected text/JSONL/CSV/netset/ipset/ranges outputs.

// Legacy-compatible streaming input adapters shared by CLI surfaces.
pub mod input;

// Atomic, durable, budget-bounded export writers shared by export handlers.
pub(crate) mod export_writer;

// Never-blocking opens of caller-supplied input paths, shared by the
// input, CSV, and metadata arms.
pub(crate) mod caller_open;
