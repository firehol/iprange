//! Name-based import from one pinned membership generation.

#[path = "membership_import/report.rs"]
mod report;

use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::{AddressFamily, ValueKind};
use crate::database::DatabaseInfo;
use crate::error::{Error, Result};
use crate::feed::FeedEntry;
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::live_namespace::Identity;
use crate::live_reader::LiveReader;
use crate::reader_core::{MembershipRange, MembershipRangeCursor, MembershipToken, ReaderCore};
use crate::workflow::Comparison;
use crate::writer_core::{ImportCache, ImportWords, TranslatedMembership};
use crate::{FeedCursor, ImmutableReader, MembershipView};

use super::workflow::{complete_workflow, FinishedState};
use super::{FinishedWorkflow, LiveWriter};

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
    core: &'a ReaderCore,
    info: DatabaseInfo,
    membership_entry_count: u64,
    identity: Identity,
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
    comparison: Comparison,
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
    writer.mutate(|store| store.finalize_membership_workflow(&cancellation))?;
    let report = report::prepare(writer, stats, &cancellation)?;
    complete_workflow(writer, report, cancellation, |edit, cancellation| {
        edit.finish_membership_workflow(cancellation)
    })
}

fn require_active(writer: &LiveWriter) -> Result<()> {
    if writer.core.workflow_input_open() {
        writer.require_healthy()
    } else {
        Err(Error::WrongState("membership import is not active"))
    }
}

impl<'a> Source<'a> {
    pub(crate) fn new(source: MembershipImportSource<'a>) -> Result<Self> {
        let core = match source {
            MembershipImportSource::Immutable(reader) => reader.core(),
            MembershipImportSource::Live(reader) => reader.core()?,
        };
        Ok(Self {
            core,
            info: core.info(),
            membership_entry_count: core.read().membership_entry_count(),
            identity: core.file_identity()?,
        })
    }

    fn feed_cursor(self) -> Result<FeedCursor<'a>> {
        self.core.read().feed_cursor()
    }

    fn range_cursor<K: IpKey>(self) -> Result<MembershipRangeCursor<'a, K>> {
        self.core.read().membership_ranges()
    }

    fn membership(self, token: MembershipToken) -> Result<MembershipView<'a>> {
        self.core.read().membership(token)
    }
}

fn require_compatible_source(writer: &LiveWriter, source: &Source<'_>) -> Result<()> {
    if source.info.value_kind != ValueKind::Membership {
        return Err(Error::WrongValueKind(
            "membership import requires a membership source",
        ));
    }
    let destination = writer.core.base_info();
    if source.info.address_family != destination.address_family {
        return Err(Error::WrongAddressFamily(
            "membership import source family differs",
        ));
    }
    if source.info.value_tag != destination.value_tag {
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
    match source.info.address_family {
        AddressFamily::Ipv4 => {
            import_ranges::<Ipv4Key>(writer, source, &mut cache, &mut stats, cancellation)?
        }
        AddressFamily::Ipv6 => {
            import_ranges::<Ipv6Key>(writer, source, &mut cache, &mut stats, cancellation)?
        }
    }
    verify_source_counts(writer, source, &cache, &stats)?;
    stats.source_memberships = cache.source_memberships();
    stats.translated_memberships = cache.translated_memberships();
    writer.mutate(|edit| edit.release_import_cache(&mut cache, cancellation))?;
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
    writer.mutate(|store| {
        let (destination, created) = store.ensure_feed(feed.name)?;
        store.map_import_feed(cache, feed, destination)?;
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
    let by_name = external(writer, source.core.read().lookup_feed_name(&feed.name))?;
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
    let mut merge = writer.mutate(|edit| edit.begin_import_merge::<K>(cancellation))?;
    let mut previous = None;
    loop {
        external(writer, cancellation.check())?;
        let Some(range) = external(writer, cursor.next())? else {
            stats.comparison =
                writer.mutate(|edit| edit.finish_import_merge(merge, cancellation))?;
            return Ok(());
        };
        require_canonical_source_range(writer, previous, range)?;
        let membership =
            translate_membership(writer, source, cache, range.membership, cancellation)?;
        writer.mutate(|edit| {
            edit.push_import_range(&mut merge, range.from, range.to, membership, cancellation)
        })?;
        record_input_range(writer, stats, range)?;
        previous = Some(range);
    }
}

fn translate_membership(
    writer: &mut LiveWriter,
    source: Source<'_>,
    cache: &mut ImportCache,
    source_membership: MembershipToken,
    cancellation: &CancellationToken,
) -> Result<TranslatedMembership> {
    if let Some(translated) = cache.last_translation(source_membership) {
        return Ok(translated);
    }
    if let Some(translated) =
        writer.mutate(|edit| edit.cached_import_membership(cache, source_membership))?
    {
        return Ok(translated);
    }
    let view = external(writer, source.membership(source_membership))?;
    let mut words = translate_words(writer, cache, &view, cancellation)?;
    writer.mutate(|edit| {
        edit.finish_import_membership(cache, source_membership, &mut words, cancellation)
    })
}

fn translate_words(
    writer: &mut LiveWriter,
    cache: &ImportCache,
    view: &MembershipView<'_>,
    cancellation: &CancellationToken,
) -> Result<ImportWords> {
    let source_words = external(writer, view.word_count())?;
    let mut words = ImportWords::new();
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
    if words.is_empty() {
        return Err(writer.abort_after_source(Error::Corrupt("source membership is empty")));
    }
    Ok(words)
}

fn translate_word_batch(
    writer: &mut LiveWriter,
    cache: &ImportCache,
    view: &MembershipView<'_>,
    words: &mut ImportWords,
    start: u32,
    buffer: &mut [u64],
    cancellation: &CancellationToken,
) -> Result<u32> {
    external(writer, cancellation.check())?;
    let read = external(writer, view.read_words(start, buffer))?;
    if read != buffer.len() {
        return Err(writer.abort_after_source(Error::Corrupt("source membership read ended early")));
    }
    let missing = writer.mutate(|edit| {
        edit.map_import_word_batch(cache, words, start, &buffer[..read], cancellation)
    })?;
    if missing {
        return Err(writer.abort_after_source(Error::Corrupt(
            "source membership names an inactive feed index",
        )));
    }
    start
        .checked_add(read as u32)
        .ok_or_else(|| writer.abort_after_source(Error::ArithmeticOverflow("word index")))
}

fn require_canonical_source_range<K: IpKey>(
    writer: &mut LiveWriter,
    previous: Option<MembershipRange<K>>,
    current: MembershipRange<K>,
) -> Result<()> {
    let invalid = current.from > current.to
        || previous.is_some_and(|prior| {
            prior.from >= current.from
                || prior.to >= current.from
                || (prior.membership == current.membership
                    && prior.to.checked_next() == Some(current.from))
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
    range: MembershipRange<K>,
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
    source: Source<'_>,
    cache: &ImportCache,
    stats: &ImportStats,
) -> Result<()> {
    let feed_sum = stats
        .matched_feeds
        .checked_add(stats.created_feeds)
        .ok_or_else(|| writer.abort_after_source(Error::ArithmeticOverflow("source feeds")))?;
    let valid = stats.source_feeds == source.info.active_feed_count
        && feed_sum == stats.source_feeds
        && stats.input_records == source.info.range_record_count
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

#[cfg(all(test, any(target_os = "linux", target_vendor = "apple", windows)))]
#[path = "membership_import/tests.rs"]
mod tests;
