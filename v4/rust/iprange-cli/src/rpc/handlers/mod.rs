//! Method-family adapters over public `iprange-livedb` APIs.
//!
//! Each module owns one specification family from
//! iprange-jsonrpc-v1.md. Handlers convert params from the strict
//! wire schema, call only public SDK functions, and convert the
//! factual SDK results with the mechanical rules of the wire result
//! schemas (v4/cli/schema/results.py is the machine authority).

pub mod convert;
pub mod cursors;
pub mod output;
pub mod reader;
pub mod system;
