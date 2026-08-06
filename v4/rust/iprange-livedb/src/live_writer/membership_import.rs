//! Name-based import from one pinned membership generation.

#[path = "membership_import/cache.rs"]
mod cache;
#[path = "membership_import/report.rs"]
mod report;

use cache::{ImportCache, WordMap};

use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::{AddressFamily, MetaV4, ValueKind};
use crate::error::{Error, Result};
use crate::feed::FeedEntry;
use crate::feed_catalog::{self, FeedCursor};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::live_reader::LiveReader;
use crate::live_sidecar::{self, Identity};
use crate::mapping::Mapping;
use crate::membership_view::{self, MembershipView};
use crate::range_cursor::{Cursor, DirectRange, RangeDirection};
use crate::workflow::LogicalChange;
use crate::ImmutableReader;

use super::workflow::FinishedState;
use super::{FinishedWorkflow, LiveWriter, PreparedState};

const WORD_BATCH: usize = 64;

/// Explicit pinned source mode for one membership import.
#[derive(Clone, Copy, Debug)]
pub enum MembershipImportSource<'a> {
    Immutable(&'a ImmutableReader),
    Live(&'a LiveReader),
}

/// Complete name-based import awaiting `FinishInput`.
#[derive(Debug)]
pub struct MembershipImport<'writer, 'source> {
    writer: &'writer mut LiveWriter,
    source: Source<'source>,
    cancellation: CancellationToken,
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct Source<'a> {
    mapping: &'a Mapping,
    meta: MetaV4,
    identity: Identity,
    owner_pid: Option<u32>,
}

#[derive(Default)]
struct ImportStats {
    input_records: u64,
    input_addresses: Cardinality129,
    source_feeds: u64,
    matched_feeds: u64,
    created_feeds: u64,
    source_memberships: u64,
    translated_memberships: u64,
}

impl LiveWriter {
    /// Begin a complete import from one explicitly pinned membership reader.
    pub fn begin_membership_import<'writer, 'source>(
        &'writer mut self,
        source: MembershipImportSource<'source>,
        cancellation: &CancellationToken,
    ) -> Result<MembershipImport<'writer, 'source>> {
        let source = self.begin_membership_import_state(source, cancellation)?;
        Ok(MembershipImport {
            writer: self,
            source,
            cancellation: cancellation.clone(),
        })
    }

    pub(crate) fn begin_membership_import_state<'source>(
        &mut self,
        source: MembershipImportSource<'source>,
        cancellation: &CancellationToken,
    ) -> Result<Source<'source>> {
        self.require_feed_workflow_ready()?;
        let source = Source::new(source)?;
        require_compatible_source(self, &source)?;
        cancellation.check()?;
        self.start_feed_workflow_draft()?;
        Ok(source)
    }
}

impl<'writer> MembershipImport<'writer, '_> {
    /// Import the complete pinned source and prepare its exact report.
    pub fn finish_input(self) -> Result<FinishedWorkflow<'writer>> {
        let finished = finish_import_state(self.writer, self.source, &self.cancellation)?;
        Ok(finished.bind(self.writer))
    }
}

pub(crate) fn finish_import_state(
    writer: &mut LiveWriter,
    source: Source<'_>,
    cancellation: &CancellationToken,
) -> Result<FinishedState> {
    require_active(writer)?;
    let stats = import_all(writer, source, cancellation)?;
    let cancellation = cancellation.clone();
    writer.mutate_uncached(|store| store.finalize_membership_workflow(&cancellation))?;
    let report = report::prepare(writer, stats, &cancellation)?;
    if report.logical_change == LogicalChange::NoChange {
        writer.discard_draft()?;
        return Ok(FinishedState::NoChange(report));
    }
    writer.mutate_uncached(|store| store.finish_membership_workflow(&cancellation))?;
    Ok(FinishedState::Changed {
        report,
        state: PreparedState::new(cancellation),
    })
}

fn require_active(writer: &LiveWriter) -> Result<()> {
    if writer
        .draft
        .as_ref()
        .is_some_and(|draft| draft.workflow_input_open())
    {
        writer.require_healthy()
    } else {
        Err(Error::WrongState("membership import is not active"))
    }
}

impl<'a> Source<'a> {
    pub(crate) fn new(source: MembershipImportSource<'a>) -> Result<Self> {
        let (mapping, meta, owner_pid) = match source {
            MembershipImportSource::Immutable(reader) => {
                let (mapping, meta) = reader.import_parts();
                (mapping, meta, None)
            }
            MembershipImportSource::Live(reader) => {
                let (mapping, meta) = reader.import_parts()?;
                (mapping, meta, Some(std::process::id()))
            }
        };
        Ok(Self {
            mapping,
            meta,
            identity: live_sidecar::identity(mapping.file())?,
            owner_pid,
        })
    }

    fn feed_cursor(self) -> Result<FeedCursor<'a>> {
        match self.owner_pid {
            Some(owner) => FeedCursor::new_live(self.mapping, &self.meta, owner),
            None => FeedCursor::new(self.mapping, &self.meta),
        }
    }

    fn range_cursor<K: IpKey>(self) -> Result<Cursor<'a, K>> {
        Cursor::new(
            self.mapping,
            &self.meta,
            RangeDirection::Forward,
            self.owner_pid,
        )
    }

    fn membership(self, id: u32) -> Result<MembershipView<'a>> {
        membership_view::by_id(self.mapping, &self.meta, id, self.owner_pid)
    }
}

fn require_compatible_source(writer: &LiveWriter, source: &Source<'_>) -> Result<()> {
    if source.meta.value_kind != ValueKind::Membership {
        return Err(Error::WrongValueKind(
            "membership import requires a membership source",
        ));
    }
    if source.meta.address_family != writer.base.meta.address_family {
        return Err(Error::WrongAddressFamily(
            "membership import source family differs",
        ));
    }
    if source.meta.value_tag != writer.base.meta.value_tag {
        return Err(Error::WrongValueTag(
            "membership import source value tag differs",
        ));
    }
    if source.identity == writer.main_identity {
        return Err(Error::InvalidArgument(
            "membership import source and destination are the same file",
        ));
    }
    Ok(())
}

fn import_all(
    writer: &mut LiveWriter,
    source: Source<'_>,
    cancellation: &CancellationToken,
) -> Result<ImportStats> {
    let mut cache = ImportCache::new();
    let mut stats = ImportStats::default();
    import_catalog(writer, source, &mut cache, &mut stats, cancellation)?;
    match source.meta.address_family {
        AddressFamily::Ipv4 => {
            import_ranges::<Ipv4Key>(writer, source, &mut cache, &mut stats, cancellation)?
        }
        AddressFamily::Ipv6 => {
            import_ranges::<Ipv6Key>(writer, source, &mut cache, &mut stats, cancellation)?
        }
    }
    verify_source_counts(writer, source.meta, &cache, &stats)?;
    stats.source_memberships = cache.source_memberships();
    stats.translated_memberships = cache.translated_memberships();
    writer.mutate_uncached(|store| cache.release(store, &mut || cancellation.check()))?;
    Ok(stats)
}

fn import_catalog(
    writer: &mut LiveWriter,
    source: Source<'_>,
    cache: &mut ImportCache,
    stats: &mut ImportStats,
    cancellation: &CancellationToken,
) -> Result<()> {
    let mut cursor = external(writer, source.feed_cursor())?;
    loop {
        external(writer, cancellation.check())?;
        let Some(feed) = external(writer, cursor.next_feed())? else {
            return Ok(());
        };
        let created = import_feed(writer, source, cache, feed)?;
        record_feed(writer, stats, created)?;
    }
}

fn import_feed(
    writer: &mut LiveWriter,
    source: Source<'_>,
    cache: &mut ImportCache,
    feed: FeedEntry,
) -> Result<bool> {
    require_source_feed(writer, source, feed)?;
    writer.mutate_uncached(|store| {
        let (destination, created) = store.ensure_feed(feed.name)?;
        cache.map_feed(store, feed.index, destination.index)?;
        Ok(created)
    })
}

fn record_feed(writer: &mut LiveWriter, stats: &mut ImportStats, created: bool) -> Result<()> {
    stats.source_feeds = increment(writer, stats.source_feeds, "source feed count")?;
    let (count, label) = if created {
        (&mut stats.created_feeds, "created feed count")
    } else {
        (&mut stats.matched_feeds, "matched feed count")
    };
    *count = increment(writer, *count, label)?;
    Ok(())
}

fn require_source_feed(writer: &mut LiveWriter, source: Source<'_>, feed: FeedEntry) -> Result<()> {
    let by_name = external(
        writer,
        feed_catalog::lookup(source.mapping, &source.meta, &feed.name),
    )?;
    if by_name == Some(feed) {
        Ok(())
    } else {
        Err(writer.abort_after_source(Error::Corrupt("source feed catalog indexes disagree")))
    }
}

fn import_ranges<K: IpKey>(
    writer: &mut LiveWriter,
    source: Source<'_>,
    cache: &mut ImportCache,
    stats: &mut ImportStats,
    cancellation: &CancellationToken,
) -> Result<()> {
    let mut cursor = external(writer, source.range_cursor::<K>())?;
    let mut previous = None;
    loop {
        external(writer, cancellation.check())?;
        let Some(range) = external(writer, cursor.next())? else {
            return Ok(());
        };
        require_canonical_source_range(writer, previous, range)?;
        let membership = translate_membership(writer, source, cache, range.value, cancellation)?;
        writer.mutate_uncached(|store| {
            store.apply_membership_cancellable(
                range.from,
                range.to,
                membership.0,
                membership.1,
                crate::MembershipOperation::Union,
                &mut || cancellation.check(),
            )
        })?;
        record_input_range(writer, stats, range)?;
        previous = Some(range);
    }
}

fn translate_membership(
    writer: &mut LiveWriter,
    source: Source<'_>,
    cache: &mut ImportCache,
    source_id: u32,
    cancellation: &CancellationToken,
) -> Result<(u32, u32)> {
    if let Some(translated) = writer.mutate_uncached(|store| cache.membership(store, source_id))? {
        return Ok(translated);
    }
    let view = external(writer, source.membership(source_id))?;
    let mut words = translate_words(writer, cache, &view, cancellation)?;
    writer.mutate_uncached(|store| {
        let interned = store.intern_membership(&words)?;
        words.release(store, &mut || cancellation.check())?;
        cache.record_membership(store, source_id, interned.id, interned.word_count)?;
        Ok((interned.id, interned.word_count))
    })
}

fn translate_words(
    writer: &mut LiveWriter,
    cache: &ImportCache,
    view: &MembershipView<'_>,
    cancellation: &CancellationToken,
) -> Result<WordMap> {
    let source_words = external(writer, view.word_count())?;
    let mut words = WordMap::new();
    let mut start = 0u32;
    let mut buffer = [0u64; WORD_BATCH];
    while start < source_words {
        let expected = (source_words - start).min(WORD_BATCH as u32) as usize;
        start = translate_word_batch(
            writer,
            cache,
            view,
            &mut words,
            start,
            &mut buffer[..expected],
            cancellation,
        )?;
    }
    if words.word_count() == 0 {
        return Err(writer.abort_after_source(Error::Corrupt("source membership is empty")));
    }
    Ok(words)
}

fn translate_word_batch(
    writer: &mut LiveWriter,
    cache: &ImportCache,
    view: &MembershipView<'_>,
    words: &mut WordMap,
    start: u32,
    buffer: &mut [u64],
    cancellation: &CancellationToken,
) -> Result<u32> {
    external(writer, cancellation.check())?;
    let read = external(writer, view.read_words(start, buffer))?;
    if read != buffer.len() {
        return Err(writer.abort_after_source(Error::Corrupt("source membership read ended early")));
    }
    let missing = writer.mutate_uncached(|store| {
        map_word_batch(store, cache, words, start, &buffer[..read], cancellation)
    })?;
    if missing.is_some() {
        return Err(writer.abort_after_source(Error::Corrupt(
            "source membership names an inactive feed index",
        )));
    }
    start
        .checked_add(read as u32)
        .ok_or_else(|| writer.abort_after_source(Error::ArithmeticOverflow("word index")))
}

fn map_word_batch(
    store: &mut crate::draft_store::DraftStore<'_>,
    cache: &ImportCache,
    words: &mut WordMap,
    start: u32,
    source_words: &[u64],
    cancellation: &CancellationToken,
) -> Result<Option<u32>> {
    for (offset, &source_word) in source_words.iter().enumerate() {
        let word_index = start
            .checked_add(offset as u32)
            .ok_or(Error::ArithmeticOverflow("source membership word index"))?;
        if let Some(missing) =
            map_source_word(store, cache, words, word_index, source_word, cancellation)?
        {
            return Ok(Some(missing));
        }
    }
    Ok(None)
}

fn map_source_word(
    store: &mut crate::draft_store::DraftStore<'_>,
    cache: &ImportCache,
    words: &mut WordMap,
    word_index: u32,
    mut source_word: u64,
    cancellation: &CancellationToken,
) -> Result<Option<u32>> {
    while source_word != 0 {
        cancellation.check()?;
        let bit = source_word.trailing_zeros();
        let source_index = word_index
            .checked_mul(64)
            .and_then(|base| base.checked_add(bit))
            .ok_or(Error::ArithmeticOverflow("source feed index"))?;
        let Some(destination_index) = cache.feed(store, source_index)? else {
            return Ok(Some(source_index));
        };
        words.set_bit(store, destination_index)?;
        source_word &= source_word - 1;
    }
    Ok(None)
}

fn require_canonical_source_range<K: IpKey>(
    writer: &mut LiveWriter,
    previous: Option<DirectRange<K>>,
    current: DirectRange<K>,
) -> Result<()> {
    let invalid = current.from > current.to
        || previous.is_some_and(|prior| {
            prior.from >= current.from
                || prior.to >= current.from
                || (prior.value == current.value && prior.to.checked_next() == Some(current.from))
        });
    if invalid {
        Err(writer.abort_after_source(Error::Corrupt("source membership ranges are not canonical")))
    } else {
        Ok(())
    }
}

fn record_input_range<K: IpKey>(
    writer: &mut LiveWriter,
    stats: &mut ImportStats,
    range: DirectRange<K>,
) -> Result<()> {
    stats.input_records = increment(writer, stats.input_records, "source range record count")?;
    let length = external(writer, range.from.inclusive_cardinality(range.to))?;
    stats.input_addresses = stats
        .input_addresses
        .checked_add(length)
        .map_err(|_| writer.abort_after_source(Error::ArithmeticOverflow("source addresses")))?;
    Ok(())
}

fn verify_source_counts(
    writer: &mut LiveWriter,
    source: MetaV4,
    cache: &ImportCache,
    stats: &ImportStats,
) -> Result<()> {
    let feed_sum = stats
        .matched_feeds
        .checked_add(stats.created_feeds)
        .ok_or_else(|| writer.abort_after_source(Error::ArithmeticOverflow("source feeds")))?;
    let valid = stats.source_feeds == source.active_feed_count
        && feed_sum == stats.source_feeds
        && stats.input_records == source.range_record_count
        && cache.source_memberships() == source.membership_entry_count;
    if valid {
        Ok(())
    } else {
        Err(writer.abort_after_source(Error::Corrupt("source membership counts disagree")))
    }
}

fn increment(writer: &mut LiveWriter, value: u64, label: &'static str) -> Result<u64> {
    value
        .checked_add(1)
        .ok_or_else(|| writer.abort_after_source(Error::ArithmeticOverflow(label)))
}

fn external<T>(writer: &mut LiveWriter, result: Result<T>) -> Result<T> {
    result.map_err(|error| writer.abort_after_source(error))
}

#[cfg(test)]
#[path = "membership_import/tests.rs"]
mod tests;
