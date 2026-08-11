//! Common structure identity, interning, refcount, and ID lifetime.

use sha2::{Digest, Sha256};

use crate::contract::MetaV4;
use crate::error::{Error, Result};
use crate::fixed_tree::{self, PageSource, RetiredPages, RetiringStore};
use crate::membership_delta::Delta;
use crate::used_bitmap::{self, Kind};

use super::codec::{encode, encode_hash, HashCodec, HashKey, Payload, PayloadCodec, Record};
use super::table;

#[derive(Clone, Copy, Debug)]
pub(crate) struct State {
    pub(crate) id_root: u32,
    pub(crate) hash_root: u32,
    pub(crate) used_root: u32,
    pub(crate) entry_count: u64,
    pub(crate) id_limit: u64,
}

impl State {
    pub(crate) const fn from_meta(meta: &MetaV4) -> Self {
        Self {
            id_root: meta.structure_id_root,
            hash_root: meta.structure_hash_root,
            used_root: meta.structure_used_root,
            entry_count: meta.structure_entry_count,
            id_limit: meta.structure_id_limit,
        }
    }

    pub(crate) fn write_to(self, meta: &mut MetaV4) {
        meta.structure_id_root = self.id_root;
        meta.structure_hash_root = self.hash_root;
        meta.structure_used_root = self.used_root;
        meta.structure_entry_count = self.entry_count;
        meta.structure_id_limit = self.id_limit;
    }
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct Interned {
    pub(crate) id: u32,
    pub(crate) membership_id: u32,
    pub(crate) created: bool,
}

pub(crate) fn intern<P: PayloadCodec, S: RetiringStore>(
    store: &mut S,
    state: &mut State,
    payload: Payload,
) -> Result<Interned> {
    crate::work::structure_intern(1);
    P::validate(payload.as_slice())?;
    let membership_id = P::membership_id(&payload);
    if P::is_absent(&payload) {
        return Ok(Interned {
            id: 0,
            membership_id: 0,
            created: false,
        });
    }
    let digest = payload_digest::<P>(&payload)?;
    if let Some(id) = find_equal::<P, S>(store, state, &payload, digest)? {
        return Ok(Interned {
            id,
            membership_id,
            created: false,
        });
    }
    let id = allocate_id(store, state)?;
    let (record, len) = encode::<P>(id, digest, payload)?;
    table::insert::<P, S>(store, &mut state.id_root, state.id_limit, &record[..len])?;
    insert::<HashCodec<P>, S>(
        store,
        &mut state.hash_root,
        &encode_hash(HashKey { digest, id }),
    )?;
    state.entry_count = state
        .entry_count
        .checked_add(1)
        .ok_or(Error::ArithmeticOverflow("structure entry count"))?;
    Ok(Interned {
        id,
        membership_id,
        created: true,
    })
}

pub(crate) fn find<P: PayloadCodec, S: PageSource>(
    store: &S,
    root: u32,
    id_limit: u64,
    id: u32,
) -> Result<Option<Record>> {
    table::find::<P, S>(store, root, id_limit, id)
}

/// Apply one aggregated range-refcount delta; return membership released on deletion.
pub(crate) fn apply_delta<P: PayloadCodec, S: RetiringStore>(
    store: &mut S,
    state: &mut State,
    delta: Delta,
) -> Result<Option<u32>> {
    let (record, deleted) = table::change_refcount::<P, S>(
        store,
        &mut state.id_root,
        state.id_limit,
        delta.id,
        delta.change,
    )?;
    if !deleted {
        return Ok(None);
    }
    fixed_tree::delete_retiring::<HashCodec<P>, S>(
        store,
        &mut state.hash_root,
        HashKey {
            digest: record.digest,
            id: record.id,
        },
    )?;
    clear_used_id(store, state, record.id)?;
    state.entry_count = state
        .entry_count
        .checked_sub(1)
        .ok_or(Error::ArithmeticOverflow("structure entry count"))?;
    state.id_limit = used_bitmap::shrink_structure(store, &mut state.used_root, state.id_limit)?;
    table::shrink::<P, S>(store, &mut state.id_root, state.id_limit)?;
    Ok((P::membership_id(&record.payload) != 0).then(|| P::membership_id(&record.payload)))
}

pub(crate) fn payload_digest<P: PayloadCodec>(payload: &Payload) -> Result<[u8; 32]> {
    let payload_len = u16::try_from(payload.as_slice().len())
        .map_err(|_| Error::InvalidArgument("structure payload is too large"))?;
    let mut hasher = Sha256::new();
    hasher.update(b"IPR4STRUCT");
    hasher.update([P::KIND as u8]);
    hasher.update(payload_len.to_le_bytes());
    hasher.update(payload.as_slice());
    Ok(hasher.finalize().into())
}

fn find_equal<P: PayloadCodec, S: PageSource>(
    store: &S,
    state: &State,
    payload: &Payload,
    digest: [u8; 32],
) -> Result<Option<u32>> {
    if state.hash_root == 0 {
        return Ok(None);
    }
    let mut key = HashKey { digest, id: 1 };
    loop {
        let Some(candidate) =
            fixed_tree::at_or_after::<HashCodec<P>, S>(store, state.hash_root, key)?
        else {
            return Ok(None);
        };
        if candidate.digest != digest {
            return Ok(None);
        }
        let record = find::<P, S>(store, state.id_root, state.id_limit, candidate.id)?
            .ok_or(Error::Corrupt("structure hash points to a missing ID"))?;
        if record.payload == *payload {
            return Ok(Some(candidate.id));
        }
        let Some(id) = candidate.id.checked_add(1) else {
            return Ok(None);
        };
        key.id = id;
    }
}

fn allocate_id<S: RetiringStore>(store: &mut S, state: &mut State) -> Result<u32> {
    used_bitmap::allocate_lowest_id(
        store,
        &mut state.used_root,
        &mut state.id_limit,
        state.entry_count,
        Kind::Structure,
        Error::StructureIdExhausted,
    )
}

fn clear_used_id<S: RetiringStore>(store: &mut S, state: &mut State, id: u32) -> Result<()> {
    let mut retired = RetiredPages::new();
    if !used_bitmap::clear(
        store,
        &mut state.used_root,
        state.id_limit,
        Kind::Structure,
        id,
        &mut retired,
    )? {
        return Err(Error::Corrupt("structure used bit is missing"));
    }
    store.retire_pages(retired.as_slice())
}

fn insert<C: crate::fixed_tree::Codec, S: RetiringStore>(
    store: &mut S,
    root: &mut u32,
    record: &[u8],
) -> Result<()> {
    if !fixed_tree::insert_retiring::<C, S>(store, root, record)? {
        return Err(Error::Corrupt("structure dictionary key already exists"));
    }
    Ok(())
}

#[cfg(test)]
#[path = "manager_tests.rs"]
mod tests;
