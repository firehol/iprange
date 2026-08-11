//! Explicit structure dictionary, reverse index, and ownership validation.

use crate::contract::{StructureKind, ValueKind};
use crate::error::Result;
use crate::format::Generation;
use crate::mapping::ByteSource;
use crate::membership_view;
use crate::structured_value::codec::{self, HashKey};
use crate::structured_value::{self, NetworkEnrichmentV1Codec, PayloadCodec};

use super::bitmap::{self, Kind, WordCache};
use super::context::Context;
use super::membership_table::{CountResult, InsertResult, Slot};
use super::structure_table;
use super::tree::{self, CellLayout, Codec};
use super::{ValidationObject, ValidationReason, ValidationSink};

struct HashCodec<P>(std::marker::PhantomData<P>);

impl<P: PayloadCodec> Codec for HashCodec<P> {
    type Key = HashKey;

    const BRANCH_TYPE: u8 = codec::HASH_BRANCH_TYPE;
    const LEAF_TYPE: u8 = codec::HASH_LEAF_TYPE;
    const AUX: u32 = P::KIND as u32;
    const BRANCH_LAYOUT: CellLayout = CellLayout::Fixed(codec::HASH_BRANCH_SIZE);
    const LEAF_LAYOUT: CellLayout = CellLayout::Fixed(codec::HASH_KEY_SIZE);
    const BRANCH_INVALID: ValidationReason = ValidationReason::StructureHashInvalid;
    const LEAF_INVALID: ValidationReason = ValidationReason::StructureHashInvalid;

    fn branch_key<B: ByteSource>(cell: B) -> Option<Self::Key> {
        codec::decode_hash_branch(cell).ok().map(|(key, _)| key)
    }

    fn branch_child<B: ByteSource>(cell: B) -> Option<u32> {
        codec::decode_hash_branch(cell).ok().map(|(_, child)| child)
    }

    fn leaf_key<B: ByteSource>(cell: B) -> Option<Self::Key> {
        codec::decode_hash(cell).ok()
    }
}

pub(crate) fn validate<S: ValidationSink>(context: &mut Context<'_, S>) -> Result<()> {
    if context.meta.value_kind != ValueKind::Structured {
        return Ok(());
    }
    match context.meta.structure_kind() {
        Some(StructureKind::NetworkEnrichmentV1) => {
            validate_kind::<NetworkEnrichmentV1Codec, S>(context)
        }
        Some(StructureKind::None) | None => context.emit(
            ValidationReason::StructureInvalid,
            ValidationObject::StructureDictionary,
            None,
            None,
            None,
        ),
    }
}

fn validate_kind<P: PayloadCodec, S: ValidationSink>(context: &mut Context<'_, S>) -> Result<()> {
    let mut maximum_id = 0;
    let id_root = context.meta.structure_id_root;
    let ids =
        structure_table::walk::<P, S, _>(context, id_root, |context, page, expected_id, cell| {
            validate_record::<P, S, _>(context, page, expected_id, cell, &mut maximum_id)
        })?;
    if ids.records != context.meta.structure_entry_count {
        count_mismatch(context, ValidationObject::StructureDictionary)?;
    }

    let mut previous = None;
    let hash_root = context.meta.structure_hash_root;
    let hashes = tree::walk::<HashCodec<P>, S, _>(
        context,
        hash_root,
        ValidationObject::StructureReverseIndex,
        |context, page, cell| validate_hash::<P, S, _>(context, page, cell, &mut previous),
    )?;
    if hashes.records != context.meta.structure_entry_count {
        count_mismatch(context, ValidationObject::StructureReverseIndex)?;
    }

    let used = bitmap::validate(
        context,
        context.meta.structure_used_root,
        context.meta.structure_id_limit,
        Kind::Structure,
    )?;
    finish(context, ids.records, used, maximum_id)
}

fn validate_record<P: PayloadCodec, S: ValidationSink, B: ByteSource>(
    context: &mut Context<'_, S>,
    page_number: u32,
    expected_id: u64,
    cell: B,
    maximum_id: &mut u32,
) -> Result<()> {
    let Ok(record) = codec::decode_record::<P, _>(cell) else {
        payload_finding(context, Some(page_number))?;
        return Ok(());
    };
    *maximum_id = (*maximum_id).max(record.id);
    if u64::from(record.id) != expected_id || expected_id >= context.meta.structure_id_limit {
        structure_finding(context, Some(page_number))?;
    }
    if record.refcount == 0 {
        refcount_finding(context, Some(page_number))?;
    }
    if structured_value::payload_digest::<P>(&record.payload)? != record.digest {
        hash_finding(context, Some(page_number))?;
    }
    let membership_id = P::membership_id(&record.payload);
    if !matches!(
        context.define_structure(record.id, record.refcount, membership_id, record.digest)?,
        InsertResult::Inserted
    ) {
        structure_finding(context, Some(page_number))?;
    }
    if membership_id != 0 {
        match context.count_membership_owner(membership_id) {
            CountResult::Full => membership_finding(context, Some(page_number))?,
            CountResult::Cancelled => return Err(crate::error::Error::Cancelled),
            CountResult::Unavailable => membership_finding(context, Some(page_number))?,
            CountResult::Inserted | CountResult::Existing => {}
        }
        if membership_view::by_id(context.mapping, &context.meta, membership_id, None).is_err() {
            membership_finding(context, Some(page_number))?;
        }
    }
    Ok(())
}

fn payload_finding<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page: Option<u32>,
) -> Result<()> {
    context.emit(
        ValidationReason::StructurePayloadInvalid,
        ValidationObject::StructureDictionary,
        page,
        None,
        None,
    )
}

fn validate_hash<P: PayloadCodec, S: ValidationSink, B: ByteSource>(
    context: &mut Context<'_, S>,
    page_number: u32,
    cell: B,
    previous: &mut Option<HashKey>,
) -> Result<()> {
    let Ok(key) = codec::decode_hash(cell) else {
        return Ok(());
    };
    if u64::from(key.id) >= context.meta.structure_id_limit
        || !context.mark_structure_reverse(key.id, key.digest)?
    {
        reverse_finding(context, Some(page_number))?;
    }
    if previous.is_some_and(|prior| prior.digest == key.digest) {
        let prior = previous.expect("checked above");
        if equal_payloads::<P, S>(context, prior.id, key.id).unwrap_or(true) {
            hash_finding(context, Some(page_number))?;
        }
    }
    *previous = Some(key);
    Ok(())
}

fn equal_payloads<P: PayloadCodec, S: ValidationSink>(
    context: &Context<'_, S>,
    left: u32,
    right: u32,
) -> Result<bool> {
    let generation = Generation::new(context.mapping, context.meta);
    let left = structured_value::find::<P, _>(
        &generation,
        context.meta.structure_id_root,
        context.meta.structure_id_limit,
        left,
    )?;
    let right = structured_value::find::<P, _>(
        &generation,
        context.meta.structure_id_root,
        context.meta.structure_id_limit,
        right,
    )?;
    Ok(matches!((left, right), (Some(left), Some(right)) if left.payload == right.payload))
}

fn finish<S: ValidationSink>(
    context: &mut Context<'_, S>,
    records: u64,
    used_bits: u64,
    maximum_id: u32,
) -> Result<()> {
    let mut defined = 0;
    let mut used = WordCache::new(
        context.meta.structure_used_root,
        context.meta.structure_id_limit,
        Kind::Structure,
    );
    for index in 0..context.structure_slots()? {
        context.checkpoint()?;
        let Some(slot) = context.structure_slot(index)? else {
            continue;
        };
        defined += u64::from(slot.defined);
        validate_slot(context, slot, &mut used)?;
    }
    let expected_limit = if maximum_id == 0 {
        1
    } else {
        u64::from(maximum_id) + 1
    };
    if defined != records
        || defined != context.meta.structure_entry_count
        || used_bits != defined
        || context.meta.structure_id_limit != expected_limit
    {
        structure_finding(context, None)?;
    }
    Ok(())
}

fn validate_slot<S: ValidationSink>(
    context: &mut Context<'_, S>,
    slot: Slot,
    used: &mut WordCache,
) -> Result<()> {
    if !slot.defined || slot.stored_refcount == 0 || slot.stored_refcount != slot.range_count {
        refcount_finding(context, None)?;
    }
    if slot.defined && !slot.reverse_seen {
        reverse_finding(context, None)?;
    }
    if slot.defined {
        let word = used.word(context, slot.id / 64).unwrap_or(0);
        if word & (1u64 << (slot.id % 64)) == 0 {
            structure_finding(context, None)?;
        }
    }
    Ok(())
}

fn count_mismatch<S: ValidationSink>(
    context: &mut Context<'_, S>,
    object: ValidationObject,
) -> Result<()> {
    context.emit(ValidationReason::RootCountInvalid, object, None, None, None)
}

fn structure_finding<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page: Option<u32>,
) -> Result<()> {
    context.emit(
        ValidationReason::StructureInvalid,
        ValidationObject::StructureDictionary,
        page,
        None,
        None,
    )
}

fn hash_finding<S: ValidationSink>(context: &mut Context<'_, S>, page: Option<u32>) -> Result<()> {
    context.emit(
        ValidationReason::StructureHashInvalid,
        ValidationObject::StructureDictionary,
        page,
        None,
        None,
    )
}

fn reverse_finding<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page: Option<u32>,
) -> Result<()> {
    context.emit(
        ValidationReason::StructureReverseIndexInvalid,
        ValidationObject::StructureReverseIndex,
        page,
        None,
        None,
    )
}

fn refcount_finding<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page: Option<u32>,
) -> Result<()> {
    context.emit(
        ValidationReason::StructureRefcountInvalid,
        ValidationObject::StructureDictionary,
        page,
        None,
        None,
    )
}

fn membership_finding<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page: Option<u32>,
) -> Result<()> {
    context.emit(
        ValidationReason::StructureMembershipInvalid,
        ValidationObject::StructureDictionary,
        page,
        None,
        None,
    )
}
