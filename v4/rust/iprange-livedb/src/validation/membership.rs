use sha2::{Digest, Sha256};

use crate::contract::{u32_le, ValueKind, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::format::page_type;
use crate::mapping::{ByteRange, ByteSource};
use crate::membership_view;

use super::bitmap::{self, Kind, WordCache};
use super::blob;
use super::context::Context;
use super::membership_table::InsertResult;
use super::tree::{self, CellLayout, Codec};
use super::{ValidationObject, ValidationReason, ValidationSink};

mod record;

use record::{decode_hash, decode_record, HashKey, Record, Storage};

const ID_BASE: usize = 64;
const MAX_ID_RECORD: usize = PAGE_SIZE - 34;
const COMPARE_WORDS: usize = 64;

struct IdCodec;

impl Codec for IdCodec {
    type Key = u32;

    const BRANCH_TYPE: u8 = page_type::MEMBERSHIP_ID_BRANCH;
    const LEAF_TYPE: u8 = page_type::MEMBERSHIP_ID_LEAF;
    const AUX: u32 = 0;
    const BRANCH_LAYOUT: CellLayout = CellLayout::Fixed(8);
    const LEAF_LAYOUT: CellLayout = CellLayout::Variable {
        minimum: ID_BASE,
        maximum: MAX_ID_RECORD,
    };
    const LEAF_INVALID: ValidationReason = ValidationReason::MembershipBitmapInvalid;

    fn branch_key<P: ByteSource>(cell: P) -> Option<Self::Key> {
        Some(u32_le(cell, 0))
    }

    fn branch_child<P: ByteSource>(cell: P) -> Option<u32> {
        Some(u32_le(cell, 4))
    }

    fn leaf_key<P: ByteSource>(cell: P) -> Option<Self::Key> {
        decode_record(cell).map(|record| record.id)
    }
}

struct HashCodec;

impl Codec for HashCodec {
    type Key = HashKey;

    const BRANCH_TYPE: u8 = page_type::MEMBERSHIP_HASH_BRANCH;
    const LEAF_TYPE: u8 = page_type::MEMBERSHIP_HASH_LEAF;
    const AUX: u32 = 0;
    const BRANCH_LAYOUT: CellLayout = CellLayout::Fixed(44);
    const LEAF_LAYOUT: CellLayout = CellLayout::Fixed(40);
    const BRANCH_INVALID: ValidationReason = ValidationReason::MembershipHashInvalid;
    const LEAF_INVALID: ValidationReason = ValidationReason::MembershipHashInvalid;

    fn branch_key<P: ByteSource>(cell: P) -> Option<Self::Key> {
        decode_hash(ByteRange::new(cell, 0, 40)?)
    }

    fn branch_child<P: ByteSource>(cell: P) -> Option<u32> {
        Some(u32_le(cell, 40))
    }

    fn leaf_key<P: ByteSource>(cell: P) -> Option<Self::Key> {
        decode_hash(cell)
    }
}

pub(crate) fn validate<S: ValidationSink>(context: &mut Context<'_, S>) -> Result<()> {
    if context.meta.value_kind != ValueKind::Membership {
        return Ok(());
    }

    let mut feeds = WordCache::new(
        context.meta.feed_used_root,
        context.meta.feed_index_limit,
        Kind::Feed,
    );
    let mut maximum_id = 0u32;
    let id_result = tree::walk::<IdCodec, S, _>(
        context,
        context.meta.membership_id_root,
        ValidationObject::MembershipDictionary,
        |context, page, cell| validate_record(context, page, cell, &mut feeds, &mut maximum_id),
    )?;
    if id_result.records != context.meta.membership_entry_count {
        count_mismatch(context, ValidationObject::MembershipDictionary)?;
    }

    let mut previous_hash = None;
    let hash_result = tree::walk::<HashCodec, S, _>(
        context,
        context.meta.membership_hash_root,
        ValidationObject::MembershipReverseIndex,
        |context, page, cell| validate_hash(context, page, cell, &mut previous_hash),
    )?;
    if hash_result.records != context.meta.membership_entry_count {
        count_mismatch(context, ValidationObject::MembershipReverseIndex)?;
    }

    let used = bitmap::validate(
        context,
        context.meta.membership_used_root,
        context.meta.membership_id_limit,
        Kind::Membership,
    )?;
    finish(context, id_result.records, used, maximum_id)
}

fn validate_record<S: ValidationSink, P: ByteSource>(
    context: &mut Context<'_, S>,
    page_number: u32,
    cell: P,
    feeds: &mut WordCache,
    maximum_id: &mut u32,
) -> Result<()> {
    let Some(record) = decode_record(cell) else {
        return Ok(());
    };
    *maximum_id = (*maximum_id).max(record.id);
    if u64::from(record.id) >= context.meta.membership_id_limit {
        bitmap_finding(context, Some(page_number))?;
    }
    if record.refcount == 0 {
        refcount_finding(context, Some(page_number))?;
    }
    if !matches!(
        context.define_membership(record.id, record.refcount, record.word_count, record.digest,)?,
        InsertResult::Inserted
    ) {
        bitmap_finding(context, Some(page_number))?;
    }
    validate_record_bitmap(context, page_number, record, feeds)
}

fn validate_record_bitmap<S: ValidationSink, P: ByteSource>(
    context: &mut Context<'_, S>,
    page_number: u32,
    record: Record<P>,
    feeds: &mut WordCache,
) -> Result<()> {
    let mut scan = BitmapScan::new(feeds);
    let complete = match record.storage {
        Storage::Inline(bytes) => {
            scan.consume(context, bytes)?;
            true
        }
        Storage::Blob(root) => blob::scan_membership(
            context,
            root,
            u64::from(record.word_count) * 8,
            |context, bytes| scan.consume(context, bytes),
        )?,
    };
    let length_matches = scan.words == record.word_count;
    report_bitmap_shape(context, page_number, &scan, length_matches)?;
    report_active_feeds(context, page_number, scan.active_invalid)?;
    let digest = scan.finish_digest();
    report_digest(
        context,
        page_number,
        complete,
        length_matches,
        digest,
        record.digest,
    )
}

fn report_bitmap_shape<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    scan: &BitmapScan<'_>,
    length_matches: bool,
) -> Result<()> {
    if !length_matches || scan.last_word == 0 {
        bitmap_finding(context, Some(page_number))?;
    }
    Ok(())
}

fn report_active_feeds<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    invalid: bool,
) -> Result<()> {
    if invalid {
        context.emit(
            ValidationReason::MembershipActiveFeedInvalid,
            ValidationObject::MembershipDictionary,
            Some(page_number),
            None,
            None,
        )?;
    }
    Ok(())
}

fn report_digest<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    complete: bool,
    length_matches: bool,
    actual: [u8; 32],
    expected: [u8; 32],
) -> Result<()> {
    if complete && length_matches && actual != expected {
        context.emit(
            ValidationReason::MembershipHashInvalid,
            ValidationObject::MembershipDictionary,
            Some(page_number),
            None,
            None,
        )?;
    }
    Ok(())
}

struct BitmapScan<'a> {
    feeds: &'a mut WordCache,
    hasher: Sha256,
    words: u32,
    last_word: u64,
    active_invalid: bool,
    feed_reader_failed: bool,
}

impl<'a> BitmapScan<'a> {
    fn new(feeds: &'a mut WordCache) -> Self {
        Self {
            feeds,
            hasher: Sha256::new(),
            words: 0,
            last_word: 0,
            active_invalid: false,
            feed_reader_failed: false,
        }
    }

    fn consume<S: ValidationSink, P: ByteSource>(
        &mut self,
        context: &mut Context<'_, S>,
        bytes: P,
    ) -> Result<()> {
        context.checkpoint()?;
        if bytes.len() % 8 != 0 {
            return Err(Error::Corrupt(
                "validated membership chunk is not word aligned",
            ));
        }
        for offset in (0..bytes.len()).step_by(8) {
            let value = crate::contract::u64_le(bytes, offset);
            self.hasher.update(value.to_le_bytes());
            self.check_active(context, value);
            self.last_word = value;
            self.words = self.words.checked_add(1).ok_or(Error::ArithmeticOverflow(
                "validation membership word count",
            ))?;
        }
        Ok(())
    }

    fn check_active<S: ValidationSink>(&mut self, context: &Context<'_, S>, value: u64) {
        if self.feed_reader_failed {
            return;
        }
        match self.feeds.word(context, self.words) {
            Ok(active) => self.active_invalid |= value & !active != 0,
            Err(_) => {
                self.active_invalid = true;
                self.feed_reader_failed = true;
            }
        }
    }

    fn finish_digest(self) -> [u8; 32] {
        self.hasher.finalize().into()
    }
}

fn validate_hash<S: ValidationSink, P: ByteSource>(
    context: &mut Context<'_, S>,
    page_number: u32,
    cell: P,
    previous: &mut Option<HashKey>,
) -> Result<()> {
    let Some(key) = decode_hash(cell) else {
        return Ok(());
    };
    if u64::from(key.id) >= context.meta.membership_id_limit
        || !context.mark_membership_reverse(key.id, key.word_count, key.digest)?
    {
        reverse_finding(context, Some(page_number))?;
    }
    if previous
        .is_some_and(|prior| prior.digest == key.digest && prior.word_count == key.word_count)
    {
        compare_collision(
            context,
            page_number,
            previous.unwrap().id,
            key.id,
            key.word_count,
        )?;
    }
    *previous = Some(key);
    Ok(())
}

fn compare_collision<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    left: u32,
    right: u32,
    word_count: u32,
) -> Result<()> {
    match equal_memberships(context, left, right, word_count) {
        Ok(true) => context.emit(
            ValidationReason::MembershipHashInvalid,
            ValidationObject::MembershipReverseIndex,
            Some(page_number),
            None,
            None,
        ),
        Ok(false) => Ok(()),
        Err(_) => reverse_finding(context, Some(page_number)),
    }
}

fn equal_memberships<S: ValidationSink>(
    context: &Context<'_, S>,
    left: u32,
    right: u32,
    word_count: u32,
) -> Result<bool> {
    let left = open_membership(context, left)?;
    let right = open_membership(context, right)?;
    if left.word_count()? != word_count || right.word_count()? != word_count {
        return Ok(false);
    }
    compare_words(context, &left, &right, word_count)
}

fn open_membership<'a, S: ValidationSink>(
    context: &'a Context<'_, S>,
    id: u32,
) -> Result<membership_view::MembershipView<'a>> {
    membership_view::by_id(context.mapping, &context.meta, id, None)
}

fn compare_words<S: ValidationSink>(
    context: &Context<'_, S>,
    left: &membership_view::MembershipView<'_>,
    right: &membership_view::MembershipView<'_>,
    word_count: u32,
) -> Result<bool> {
    let mut left_words = [0; COMPARE_WORDS];
    let mut right_words = [0; COMPARE_WORDS];
    let mut start = 0u32;
    while start < word_count {
        context.checkpoint()?;
        let count = usize::try_from((word_count - start).min(COMPARE_WORDS as u32))
            .map_err(|_| Error::ArithmeticOverflow("validation membership comparison"))?;
        left.read_words(start, &mut left_words[..count])?;
        right.read_words(start, &mut right_words[..count])?;
        if left_words[..count] != right_words[..count] {
            return Ok(false);
        }
        start += count as u32;
    }
    Ok(true)
}

fn finish<S: ValidationSink>(
    context: &mut Context<'_, S>,
    dictionary_records: u64,
    used_bits: u64,
    maximum_id: u32,
) -> Result<()> {
    let defined = validate_slots(context)?;
    let expected_limit = expected_id_limit(maximum_id);
    if !totals_match(
        context,
        defined,
        dictionary_records,
        used_bits,
        expected_limit,
    ) {
        bitmap_finding(context, None)?;
    }
    Ok(())
}

fn validate_slots<S: ValidationSink>(context: &mut Context<'_, S>) -> Result<u64> {
    let mut defined = 0;
    let mut used = WordCache::new(
        context.meta.membership_used_root,
        context.meta.membership_id_limit,
        Kind::Membership,
    );
    for index in 0..context.membership_slots()? {
        context.checkpoint()?;
        let Some(slot) = context.membership_slot(index)? else {
            continue;
        };
        if slot.defined {
            defined += 1;
        }
        validate_slot(context, slot, &mut used)?;
    }
    Ok(defined)
}

fn expected_id_limit(maximum_id: u32) -> u64 {
    if maximum_id == 0 {
        1
    } else {
        u64::from(maximum_id) + 1
    }
}

fn totals_match<S: ValidationSink>(
    context: &Context<'_, S>,
    defined: u64,
    dictionary_records: u64,
    used_bits: u64,
    expected_limit: u64,
) -> bool {
    defined == dictionary_records
        && defined == context.meta.membership_entry_count
        && used_bits == defined
        && context.meta.membership_id_limit == expected_limit
}

fn validate_slot<S: ValidationSink>(
    context: &mut Context<'_, S>,
    slot: super::membership_table::Slot,
    used: &mut WordCache,
) -> Result<()> {
    validate_slot_refcount(context, slot)?;
    validate_slot_reverse(context, slot)?;
    validate_slot_used(context, slot, used)
}

fn validate_slot_refcount<S: ValidationSink>(
    context: &mut Context<'_, S>,
    slot: super::membership_table::Slot,
) -> Result<()> {
    if !slot.defined || slot.stored_refcount == 0 || slot.stored_refcount != slot.range_count {
        refcount_finding(context, None)?;
    }
    Ok(())
}

fn validate_slot_reverse<S: ValidationSink>(
    context: &mut Context<'_, S>,
    slot: super::membership_table::Slot,
) -> Result<()> {
    if slot.defined && !slot.reverse_seen {
        reverse_finding(context, None)?;
    }
    Ok(())
}

fn validate_slot_used<S: ValidationSink>(
    context: &mut Context<'_, S>,
    slot: super::membership_table::Slot,
    used: &mut WordCache,
) -> Result<()> {
    if slot.defined {
        let word = used.word(context, slot.id / 64).unwrap_or(0);
        if word & (1u64 << (slot.id % 64)) == 0 {
            bitmap_finding(context, None)?;
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

fn bitmap_finding<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page: Option<u32>,
) -> Result<()> {
    context.emit(
        ValidationReason::MembershipBitmapInvalid,
        ValidationObject::MembershipDictionary,
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
        ValidationReason::MembershipReverseIndexInvalid,
        ValidationObject::MembershipReverseIndex,
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
        ValidationReason::MembershipRefcountInvalid,
        ValidationObject::MembershipDictionary,
        page,
        None,
        None,
    )
}
