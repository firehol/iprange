//! Named membership construction with private mapped normalization.
use super::unordered::workspace::Workspace;
use super::valued::{early, ordered_push, prepared, workspace_meta, MapKey, Stats};
use super::{MembershipWords, OutputSpec};
use crate::immutable_map::{
    ImmutableBuildBudget, ImmutableBuildOutcome, ImmutableInputOrder, ImmutableMembershipSource,
};
use crate::membership_dictionary as dictionary;
use crate::{
    CancellationToken, DirectRange, Error, FeedName, PublicationPolicy, Result, StructureKind,
    ValueKind, ValueTag,
};
use std::path::Path;

struct Words<'a>(&'a [u64], &'a CancellationToken);
impl MembershipWords for Words<'_> {
    fn word_count(&self) -> u32 {
        self.0.len() as u32
    }
    fn read_words(&self, start: u32, output: &mut [u64]) -> Result<()> {
        self.1.check()?;
        output.copy_from_slice(
            self.0
                .get(start as usize..start as usize + output.len())
                .ok_or(Error::Corrupt("membership word bounds"))?,
        );
        Ok(())
    }
}
impl dictionary::Words<Workspace> for Words<'_> {
    fn word_count(&self) -> u32 {
        MembershipWords::word_count(self)
    }
    fn read_words(&self, _: &Workspace, start: u32, output: &mut [u64]) -> Result<()> {
        MembershipWords::read_words(self, start, output)
    }
}
struct Stored<'a> {
    cancel: &'a CancellationToken,
    workspace: &'a Workspace,
    root: u32,
    id: u32,
    count: u32,
}
impl MembershipWords for Stored<'_> {
    fn word_count(&self) -> u32 {
        self.count
    }
    fn read_words(&self, start: u32, output: &mut [u64]) -> Result<()> {
        self.cancel.check()?;
        dictionary::read_words(self.workspace, self.root, self.id, start, output)
    }
}

#[allow(clippy::too_many_arguments)]
pub(crate) fn create<K: MapKey, S: ImmutableMembershipSource<K>>(
    destination: &Path,
    tag: ValueTag,
    metadata: Option<&[u8]>,
    policy: PublicationPolicy,
    feeds: &[FeedName],
    source: &mut S,
    order: ImmutableInputOrder,
    budget: &ImmutableBuildBudget,
    cancel: &CancellationToken,
) -> ImmutableBuildOutcome {
    cancel.check().map_err(early)?;
    let mut budget = prepared(budget, order).map_err(early)?;
    let feed_count = u32::try_from(feeds.len())
        .map_err(|_| early(Error::InvalidArgument("too many feed names")))?;
    let spec = OutputSpec::fresh(
        K::FAMILY,
        ValueKind::Membership,
        StructureKind::None,
        tag,
        u64::from(feed_count),
    )
    .map_err(early)?;
    let mut allocation = crate::heap::Heap::new(budget.max_heap_bytes);
    let mut words = allocation
        .filled(
            (u64::from(feed_count).div_ceil(64)) as usize,
            0u64,
            "immutable membership input words",
        )
        .map_err(early)?;
    let input_heap = allocation.used();
    budget.max_heap_bytes = allocation.remaining();
    crate::immutable_feed::publish_built(destination, policy, cancel, |file| {
        super::construction::build(
            file,
            spec,
            budget,
            order == ImmutableInputOrder::Unordered,
            |builder, mut workspace, heap| {
                for (index, &name) in feeds.iter().enumerate() {
                    cancel.check()?;
                    if builder.feed_index(&name)?.is_some() {
                        return Err(Error::InvalidArgument(
                            "immutable feed catalog contains a duplicate name",
                        ));
                    }
                    builder.push_feed(name, index as u32)?;
                }
                let mut previous_length = 0;
                let mut state = dictionary::State {
                    id_root: 0,
                    hash_root: 0,
                    used_root: 0,
                    entry_count: 0,
                    id_limit: 1,
                };
                let (mut root, mut count) = (0, 0);
                if let Some(w) = workspace.as_deref_mut() {
                    w.track_memberships();
                }
                let mut stats = Stats::default();
                let mut pending = None;
                crate::work::source_pass(1);
                crate::work::input_source_pass(1);
                loop {
                    cancel.check()?;
                    let Some(range) = source.next_range()? else {
                        break;
                    };
                    if range.from > range.to {
                        return Err(Error::InvalidArgument("range start is after its end"));
                    }
                    if range.feeds.is_empty() {
                        return Err(Error::InvalidArgument("membership input has no labels"));
                    }
                    words[..previous_length].fill(0);
                    let mut length = 0;
                    for name in range.feeds {
                        cancel.check()?;
                        let index = builder.feed_index(name)?.ok_or(Error::InvalidArgument(
                            "membership input references an unknown feed",
                        ))?;
                        let word = index as usize / 64;
                        words[word] |= 1u64 << (index % 64);
                        length = length.max(word + 1);
                    }
                    previous_length = length;
                    let input = Words(&words[..length], cancel);
                    if let Some(w) = workspace.as_deref_mut() {
                        let incoming = dictionary::intern(w, &mut state, &input)?;
                        // No dictionary GC runs until every segment has been transformed.
                        // Register unused operands too, so zero-reference entries are reclaimed.
                        w.track_delta(incoming.id, 0)?;
                        crate::range_mutation::transform::<K, _, _>(
                            w,
                            &mut root,
                            &mut count,
                            range.from,
                            range.to,
                            |w, previous| {
                                cancel.check()?;
                                Ok(Some(
                                    dictionary::combine(
                                        w,
                                        &mut state,
                                        previous.unwrap_or(0),
                                        incoming.id,
                                        incoming.word_count,
                                        crate::contract::MembershipOperation::Union,
                                    )?
                                    .id,
                                ))
                            },
                        )?;
                        w.apply_membership_deltas(&mut state, cancel)?;
                    } else {
                        let value = builder.intern_membership_value(&input)?;
                        ordered_push(
                            &mut pending,
                            DirectRange {
                                from: range.from,
                                to: range.to,
                                value,
                            },
                            |r| stats.emit(builder, r, true),
                        )?;
                    }
                    stats.input = stats
                        .input
                        .checked_add(1)
                        .ok_or(Error::ArithmeticOverflow("immutable input count"))?;
                    crate::work::range_consumed(1);
                }
                drop(words);
                if let Some(w) = workspace {
                    let mut allocation = crate::heap::Heap::new(heap + input_heap);
                    let capacity = ((allocation.remaining()
                        / std::mem::size_of::<(u32, u32)>() as u64)
                        .min(1024)) as usize;
                    let mut translations = allocation.filled(
                        capacity,
                        (0u32, 0u32),
                        "immutable membership translations",
                    )?;
                    let meta = workspace_meta(spec, w.page_count(), root, count);
                    let mut cursor = crate::range_store_cursor::Cursor::<K>::new(w, &meta, false)?;
                    while let Some(range) = cursor.next(w)? {
                        cancel.check()?;
                        let slot = if translations.is_empty() {
                            None
                        } else {
                            Some(range.value as usize % translations.len())
                        };
                        let cached = slot.and_then(|slot| {
                            let (source, destination) = translations[slot];
                            (source == range.value).then_some(destination)
                        });
                        let value = if let Some(value) = cached {
                            value
                        } else {
                            let source = Stored {
                                cancel,
                                workspace: w,
                                root: state.id_root,
                                id: range.value,
                                count: dictionary::word_count(w, state.id_root, range.value)?,
                            };
                            let value = builder.intern_membership_value(&source)?;
                            if let Some(slot) = slot {
                                translations[slot] = (range.value, value);
                            }
                            value
                        };
                        stats.emit(
                            builder,
                            DirectRange {
                                from: range.from,
                                to: range.to,
                                value,
                            },
                            true,
                        )?;
                    }
                } else if let Some(range) = pending {
                    stats.emit(builder, range, true)?;
                }
                cancel.check()?;
                if let Some(metadata) = metadata {
                    builder.write_metadata_with_budget(metadata, heap + input_heap)?;
                }
                Ok(stats.report())
            },
        )
    })
}
