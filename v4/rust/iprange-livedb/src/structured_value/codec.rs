//! Common structure dictionary envelopes and fixed-tree codecs.

use std::marker::PhantomData;

use crate::contract::{u16_le, u32_le, u64_le, StructureKind};
use crate::error::{Error, Result};
use crate::fixed_tree::Codec as TreeCodec;
use crate::format::page_type;
use crate::mapping::{ByteRange, ByteSource};

const LENGTH_OFFSET: usize = 0;
const RESERVED_OFFSET: usize = 2;
const ID_OFFSET: usize = 4;
pub(crate) const REFCOUNT_OFFSET: usize = 8;
const DIGEST_OFFSET: usize = 16;
pub(crate) const PAYLOAD_OFFSET: usize = 48;
const HASH_DIGEST_OFFSET: usize = 0;
const HASH_ID_OFFSET: usize = 32;

pub(crate) const MAX_PAYLOAD_SIZE: usize = 32;
const MAX_RECORD_SIZE: usize = PAYLOAD_OFFSET + MAX_PAYLOAD_SIZE;
pub(crate) const HASH_KEY_SIZE: usize = 36;
pub(crate) const HASH_BRANCH_SIZE: usize = HASH_KEY_SIZE + 4;
pub(crate) const HASH_BRANCH_TYPE: u8 = page_type::STRUCTURE_HASH_BRANCH;
pub(crate) const HASH_LEAF_TYPE: u8 = page_type::STRUCTURE_HASH_LEAF;

/// One exact payload from the compile-time structure registry.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct Payload {
    bytes: [u8; MAX_PAYLOAD_SIZE],
    len: u16,
}

impl Payload {
    pub(crate) fn new(bytes: &[u8]) -> Result<Self> {
        if bytes.len() > MAX_PAYLOAD_SIZE {
            return Err(Error::InvalidArgument(
                "structure payload exceeds the hardcoded registry limit",
            ));
        }
        let mut payload = Self {
            bytes: [0; MAX_PAYLOAD_SIZE],
            len: bytes.len() as u16,
        };
        payload.bytes[..bytes.len()].copy_from_slice(bytes);
        Ok(payload)
    }

    pub(crate) fn as_slice(&self) -> &[u8] {
        &self.bytes[..usize::from(self.len)]
    }
}

/// Structure-specific logic; the common manager never knows field offsets.
pub(crate) trait PayloadCodec {
    const KIND: StructureKind;
    const PAYLOAD_SIZE: usize;

    fn validate<S: ByteSource>(payload: S) -> Result<()>;
    fn membership_id(payload: &Payload) -> u32;
    fn is_absent(payload: &Payload) -> bool;
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct Record {
    pub(crate) id: u32,
    pub(crate) refcount: u64,
    pub(crate) digest: [u8; 32],
    pub(crate) payload: Payload,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub(crate) struct HashKey {
    pub(crate) digest: [u8; 32],
    pub(crate) id: u32,
}

pub(crate) struct HashCodec<P>(PhantomData<P>);

impl<P: PayloadCodec> TreeCodec for HashCodec<P> {
    type Key = HashKey;
    type Leaf = HashKey;

    const BRANCH_TYPE: u8 = HASH_BRANCH_TYPE;
    const LEAF_TYPE: u8 = HASH_LEAF_TYPE;
    const AUX: u32 = P::KIND as u32;
    const KEY_SIZE: usize = HASH_KEY_SIZE;
    const LEAF_SIZE: usize = HASH_KEY_SIZE;

    fn read_key<S: ByteSource>(cell: S, level: u16) -> Result<Self::Key> {
        if level == 0 {
            decode_hash(cell)
        } else {
            decode_hash(
                ByteRange::new(cell, 0, HASH_KEY_SIZE)
                    .ok_or_else(|| Error::corrupt("structure hash branch record is malformed"))?,
            )
        }
    }

    fn read_leaf<S: ByteSource>(cell: S) -> Result<Self::Leaf> {
        decode_hash(cell)
    }

    fn write_key(key: Self::Key, output: &mut [u8]) {
        write_hash(key, output);
    }
}

pub(crate) fn encode<P: PayloadCodec>(
    id: u32,
    digest: [u8; 32],
    payload: Payload,
) -> Result<([u8; MAX_RECORD_SIZE], usize)> {
    require_payload::<P>(&payload)?;
    if id == 0 {
        return Err(Error::InvalidArgument("structure ID zero is reserved"));
    }
    let len = PAYLOAD_OFFSET + P::PAYLOAD_SIZE;
    let mut record = [0; MAX_RECORD_SIZE];
    record[LENGTH_OFFSET..RESERVED_OFFSET].copy_from_slice(&(len as u16).to_le_bytes());
    record[ID_OFFSET..REFCOUNT_OFFSET].copy_from_slice(&id.to_le_bytes());
    record[DIGEST_OFFSET..PAYLOAD_OFFSET].copy_from_slice(&digest);
    record[PAYLOAD_OFFSET..len].copy_from_slice(payload.as_slice());
    Ok((record, len))
}

pub(crate) fn encode_hash(key: HashKey) -> [u8; HASH_KEY_SIZE] {
    let mut record = [0; HASH_KEY_SIZE];
    write_hash(key, &mut record);
    record
}

pub(crate) const fn record_size<P: PayloadCodec>() -> usize {
    PAYLOAD_OFFSET + P::PAYLOAD_SIZE
}

pub(crate) fn decode_record<P: PayloadCodec, S: ByteSource>(cell: S) -> Result<Record> {
    let id = u32_le(cell, ID_OFFSET);
    let payload_source = payload_source::<P, _>(cell, id)?;
    P::validate(payload_source)?;
    let mut bytes = [0; MAX_PAYLOAD_SIZE];
    if !payload_source.copy_range_to(0, &mut bytes[..P::PAYLOAD_SIZE]) {
        return Err(Error::Corrupt("structure payload is malformed"));
    }
    Ok(Record {
        id,
        refcount: u64_le(cell, REFCOUNT_OFFSET),
        digest: cell
            .array(DIGEST_OFFSET)
            .ok_or_else(|| Error::corrupt("structure digest is malformed"))?,
        payload: Payload {
            bytes,
            len: P::PAYLOAD_SIZE as u16,
        },
    })
}

pub(crate) fn payload_source<P: PayloadCodec, S: ByteSource>(
    cell: S,
    expected_id: u32,
) -> Result<ByteRange<S>> {
    let expected = PAYLOAD_OFFSET + P::PAYLOAD_SIZE;
    if cell.len() != expected
        || usize::from(u16_le(cell, LENGTH_OFFSET)) != expected
        || u16_le(cell, RESERVED_OFFSET) != 0
    {
        return Err(Error::Corrupt("structure dictionary record is malformed"));
    }
    let id = u32_le(cell, ID_OFFSET);
    if id == 0 {
        return Err(Error::Corrupt("structure dictionary contains ID zero"));
    }
    if id != expected_id {
        return Err(Error::Corrupt(
            "structure table record is in the wrong slot",
        ));
    }
    ByteRange::new(cell, PAYLOAD_OFFSET, P::PAYLOAD_SIZE)
        .ok_or_else(|| Error::corrupt("structure payload is malformed"))
}

pub(crate) fn decode_hash<S: ByteSource>(cell: S) -> Result<HashKey> {
    if cell.len() != HASH_KEY_SIZE {
        return Err(Error::Corrupt("structure hash record is malformed"));
    }
    let id = u32_le(cell, HASH_ID_OFFSET);
    if id == 0 {
        return Err(Error::Corrupt("structure hash contains ID zero"));
    }
    Ok(HashKey {
        digest: cell
            .array(HASH_DIGEST_OFFSET)
            .ok_or_else(|| Error::corrupt("structure hash record is malformed"))?,
        id,
    })
}

pub(crate) fn decode_hash_branch<S: ByteSource>(cell: S) -> Result<(HashKey, u32)> {
    if cell.len() != HASH_BRANCH_SIZE {
        return Err(Error::Corrupt("structure hash branch record is malformed"));
    }
    let key = decode_hash(
        ByteRange::new(cell, 0, HASH_KEY_SIZE)
            .ok_or_else(|| Error::corrupt("structure hash branch record is malformed"))?,
    )?;
    Ok((key, u32_le(cell, HASH_KEY_SIZE)))
}

fn write_hash(key: HashKey, output: &mut [u8]) {
    output[HASH_DIGEST_OFFSET..HASH_ID_OFFSET].copy_from_slice(&key.digest);
    output[HASH_ID_OFFSET..HASH_KEY_SIZE].copy_from_slice(&key.id.to_le_bytes());
}

fn require_payload<P: PayloadCodec>(payload: &Payload) -> Result<()> {
    if payload.as_slice().len() != P::PAYLOAD_SIZE || P::PAYLOAD_SIZE > MAX_PAYLOAD_SIZE {
        return Err(Error::InvalidArgument(
            "structure payload does not match its hardcoded kind",
        ));
    }
    P::validate(payload.as_slice())
}

const _: () = assert!(MAX_RECORD_SIZE <= crate::contract::PAGE_SIZE);
