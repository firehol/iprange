//! Validation policy adapter over the canonical membership record codec.

use crate::mapping::{ByteRange, ByteSource};
use crate::membership_dictionary::codec;

pub(super) use codec::{HashKey, Record, Storage};

pub(super) fn decode_record<P: ByteSource>(cell: P) -> Option<Record> {
    codec::decode(cell).ok()
}

pub(super) fn inline_bytes<P: ByteSource>(cell: P, record: Record) -> Option<ByteRange<P>> {
    codec::inline_bytes(cell, record).ok()
}

pub(super) fn decode_hash<P: ByteSource>(cell: P) -> Option<HashKey> {
    codec::decode_hash(cell).ok()
}
