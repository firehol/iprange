//! Connection-owned reader and cursor state for the read-only methods.

use std::collections::HashMap;

use iprange_livedb::ImmutableReader;

/// Reader modes that can be attached to one connection-local handle. The
/// supported immutable mode owns a pinned sidecar-free generation.
pub enum ReaderValue {
    Immutable(ImmutableReader),
}

impl ReaderValue {
    pub fn immutable(&self) -> Option<&ImmutableReader> {
        match self {
            Self::Immutable(reader) => Some(reader),
        }
    }
}

/// One canonical address checkpoint used to re-open and seek a cursor.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CursorPoint {
    V4(u32),
    V6(u128),
}

/// Logical view represented by a cursor.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum CursorView {
    Direct,
    Structured,
    Feed { name: String },
}

/// Cursor progression state. Public Rust cursors borrow their reader, so
/// the connection retains the semantic checkpoint and each `next` operation
/// opens a fresh cursor and seeks to it. This keeps every response bounded.
#[derive(Clone, Debug)]
pub struct CursorValue {
    pub reader: String,
    pub view: CursorView,
    pub reverse: bool,
    pub point: Option<CursorPoint>,
    pub range_skip: u64,
    pub last_feed_index: Option<u32>,
    pub batch_size: usize,
    pub exhausted: bool,
}

/// Mutable per-connection resources. Closed cursor handles are retained as
/// tombstones so a subsequent use can be distinguished from an unknown handle;
/// they do not count against the active cursor limit.
#[derive(Default)]
pub struct ConnectionState {
    pub readers: HashMap<String, ReaderValue>,
    pub cursors: HashMap<String, CursorValue>,
    pub closed_cursors: HashMap<String, ()>,
}
