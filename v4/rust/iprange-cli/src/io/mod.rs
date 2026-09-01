//! Shared streaming legacy-compatible text input and atomic bounded
//! text/result output (SOW-0028).
//!
//! Persistence handlers never use this module to read or write v4
//! database bytes; it serves the released legacy surface and the
//! caller-selected text/JSONL/CSV/netset/ipset/ranges outputs.

