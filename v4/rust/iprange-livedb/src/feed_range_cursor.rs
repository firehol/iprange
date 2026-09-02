//! Allocation-free ordered projection of one named feed.

use std::fmt;

use crate::contract::{MetaV4, ValueKind};
use crate::error::{Error, Result};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::mapping::Mapping;
use crate::membership_view;
use crate::process_identity::ProcessIdentity;
use crate::range_cursor::{CursorState, RangeDirection};
use crate::structured_value;
use crate::workflow::AddressRange;

pub(crate) struct ProjectionState<K> {
    meta: MetaV4,
    feed_index: u32,
    direction: RangeDirection,
    inner: CursorState<K>,
    pending: Option<AddressRange<K>>,
    membership: Option<(u32, bool)>,
    raw_finished: bool,
    finished: bool,
}

impl<K: IpKey> ProjectionState<K> {
    pub(crate) fn new(
        mapping: &Mapping,
        meta: &MetaV4,
        feed_index: u32,
        direction: RangeDirection,
        owner_identity: Option<ProcessIdentity>,
    ) -> Result<Self> {
        require_feed(meta, feed_index)?;
        Ok(Self {
            meta: *meta,
            feed_index,
            direction,
            inner: CursorState::new(mapping, meta, direction, owner_identity)?,
            pending: None,
            membership: None,
            raw_finished: false,
            finished: false,
        })
    }

    pub(crate) fn seek(&mut self, mapping: &Mapping, target: K) -> Result<()> {
        self.inner.seek(mapping, target)?;
        self.pending = None;
        self.raw_finished = false;
        self.finished = false;
        Ok(())
    }

    pub(crate) fn next_with<F>(
        &mut self,
        mapping: &Mapping,
        checkpoint: &mut F,
    ) -> Result<Option<AddressRange<K>>>
    where
        F: FnMut() -> Result<()>,
    {
        if self.finished {
            return Ok(None);
        }
        match self.next_inner(mapping, checkpoint) {
            Ok(next) => {
                self.finished = next.is_none()
                    || (self.raw_finished && self.pending.is_none() && next.is_some());
                Ok(next)
            }
            Err(error) => {
                self.finished = true;
                Err(error)
            }
        }
    }

    fn next_inner<F>(
        &mut self,
        mapping: &Mapping,
        checkpoint: &mut F,
    ) -> Result<Option<AddressRange<K>>>
    where
        F: FnMut() -> Result<()>,
    {
        loop {
            let Some(current) = self.next_member(mapping, checkpoint)? else {
                return Ok(self.pending.take());
            };
            let Some(pending) = self.pending else {
                self.pending = Some(current);
                continue;
            };
            if let Some(merged) = merge(self.direction, pending, current) {
                self.pending = Some(merged);
                continue;
            }
            self.pending = Some(current);
            return Ok(Some(pending));
        }
    }

    fn next_member<F>(
        &mut self,
        mapping: &Mapping,
        checkpoint: &mut F,
    ) -> Result<Option<AddressRange<K>>>
    where
        F: FnMut() -> Result<()>,
    {
        while !self.raw_finished {
            checkpoint()?;
            let Some(range) = self.inner.next(mapping)? else {
                self.raw_finished = true;
                break;
            };
            let contains = cached_membership(&mut self.membership, range.value, || {
                let membership_id = match self.meta.value_kind {
                    ValueKind::Membership => range.value,
                    ValueKind::Structured => {
                        structured_value::membership_id(mapping, &self.meta, range.value)?
                    }
                    ValueKind::Direct => unreachable!("cursor kind checked at construction"),
                };
                if membership_id == 0 {
                    Ok(false)
                } else {
                    membership_view::id_contains_index(
                        mapping,
                        &self.meta,
                        membership_id,
                        self.feed_index,
                    )
                }
            })?;
            if contains {
                return Ok(Some(AddressRange {
                    from: range.from,
                    to: range.to,
                }));
            }
        }
        Ok(None)
    }
}

fn cached_membership<F>(cache: &mut Option<(u32, bool)>, id: u32, load: F) -> Result<bool>
where
    F: FnOnce() -> Result<bool>,
{
    if let Some((cached_id, contains)) = *cache {
        if cached_id == id {
            return Ok(contains);
        }
    }
    let contains = load()?;
    *cache = Some((id, contains));
    Ok(contains)
}

pub(crate) struct ProjectionCursor<'a, K> {
    mapping: &'a Mapping,
    state: ProjectionState<K>,
}

impl<'a, K: IpKey> ProjectionCursor<'a, K> {
    pub(crate) fn new(
        mapping: &'a Mapping,
        meta: &MetaV4,
        feed_index: u32,
        direction: RangeDirection,
        owner_identity: Option<ProcessIdentity>,
    ) -> Result<Self> {
        Ok(Self {
            mapping,
            state: ProjectionState::new(mapping, meta, feed_index, direction, owner_identity)?,
        })
    }

    /// Reposition the projection at the interval containing `target` or,
    /// when no interval contains it, the nearest interval in the cursor's
    /// direction. Membership filtering applies from the repositioned point
    /// onward; already-consumed state is discarded.
    pub(crate) fn seek(&mut self, target: K) -> Result<()> {
        self.state.seek(self.mapping, target)
    }

    pub(crate) fn next_with<F>(&mut self, checkpoint: &mut F) -> Result<Option<AddressRange<K>>>
    where
        F: FnMut() -> Result<()>,
    {
        self.state.next_with(self.mapping, checkpoint)
    }
}

fn merge<K: IpKey>(
    direction: RangeDirection,
    mut pending: AddressRange<K>,
    current: AddressRange<K>,
) -> Option<AddressRange<K>> {
    let adjacent = match direction {
        RangeDirection::Forward => pending.to.checked_next() == Some(current.from),
        RangeDirection::Backward => current.to.checked_next() == Some(pending.from),
    };
    if !adjacent {
        return None;
    }
    match direction {
        RangeDirection::Forward => pending.to = current.to,
        RangeDirection::Backward => pending.from = current.from,
    }
    Some(pending)
}

fn require_feed(meta: &MetaV4, feed_index: u32) -> Result<()> {
    if !matches!(
        meta.value_kind,
        ValueKind::Membership | ValueKind::Structured
    ) {
        return Err(Error::WrongValueKind(
            "named-feed cursor requires a membership-capable database",
        ));
    }
    if u64::from(feed_index) >= meta.feed_index_limit {
        return Err(Error::Corrupt("feed index exceeds the catalog namespace"));
    }
    Ok(())
}

macro_rules! public_cursor {
    ($name:ident, $key:ty) => {
        pub struct $name<'a> {
            inner: ProjectionCursor<'a, $key>,
        }

        impl<'a> $name<'a> {
            pub(crate) fn new(
                mapping: &'a Mapping,
                meta: &MetaV4,
                feed_index: u32,
                direction: RangeDirection,
                owner_identity: Option<ProcessIdentity>,
            ) -> Result<Self> {
                Ok(Self {
                    inner: ProjectionCursor::new(
                        mapping,
                        meta,
                        feed_index,
                        direction,
                        owner_identity,
                    )?,
                })
            }

            /// Reposition to the interval containing `target` or the nearest
            /// interval in the cursor's direction (forward: at or after;
            /// backward: at or before). Values already visited are never
            /// revisited; subsequent `next_range` calls continue from the
            /// repositioned interval.
            pub fn seek(&mut self, target: $key) -> Result<()> {
                self.inner.seek(target)
            }

            /// Return the next coalesced interval belonging to this feed.
            pub fn next_range(&mut self) -> Result<Option<AddressRange<$key>>> {
                self.inner.next_with(&mut || Ok(()))
            }
        }

        impl fmt::Debug for $name<'_> {
            fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
                output
                    .debug_struct(stringify!($name))
                    .field("direction", &self.inner.state.direction)
                    .field("finished", &self.inner.state.finished)
                    .finish_non_exhaustive()
            }
        }
    };
}

public_cursor!(FeedRangeCursorV4, Ipv4Key);
public_cursor!(FeedRangeCursorV6, Ipv6Key);

#[cfg(test)]
mod tests {
    use super::cached_membership;
    use crate::{
        create_immutable_feed_v4, AddressRange, CancellationToken, FeedName, ImmutableFeedBudget,
        ImmutableReader, Ipv4Key, PublicationPolicy, RangeDirection, SliceSource, ValueTag,
    };
    use std::fs;
    use std::path::PathBuf;
    use std::time::{SystemTime, UNIX_EPOCH};

    fn fixture_path(label: &str) -> PathBuf {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        std::env::temp_dir().join(format!(
            "iprange-feed-cursor-{label}-{}-{unique}",
            std::process::id()
        ))
    }

    fn crafted_ranges() -> Vec<AddressRange<Ipv4Key>> {
        // [10,20] and [21,30] are adjacent and normalize into one run
        // at publication; the other runs are separated by real gaps.
        vec![
            AddressRange {
                from: Ipv4Key(10),
                to: Ipv4Key(20),
            },
            AddressRange {
                from: Ipv4Key(21),
                to: Ipv4Key(30),
            },
            AddressRange {
                from: Ipv4Key(35),
                to: Ipv4Key(40),
            },
            AddressRange {
                from: Ipv4Key(50),
                to: Ipv4Key(50),
            },
        ]
    }

    fn open_feed(label: &str, ranges: &[AddressRange<Ipv4Key>]) -> (PathBuf, ImmutableReader) {
        let path = fixture_path(label);
        create_immutable_feed_v4(
            &path,
            ValueTag::new(b"feeds").unwrap(),
            FeedName::new("feed-a").unwrap(),
            None,
            PublicationPolicy::FailIfExists,
            &mut SliceSource::new(ranges),
            &ImmutableFeedBudget::new(2 * 1024 * 1024, 10_000, 10_000, 3),
            &CancellationToken::new(),
        )
        .unwrap();
        let reader = ImmutableReader::open(&path).unwrap();
        (path, reader)
    }

    #[test]
    fn forward_seek_lands_on_containing_or_next_interval() {
        let (path, reader) = open_feed("seek-forward", &crafted_ranges());
        let mut cursor = reader
            .feed_range_cursor_v4("feed-a", RangeDirection::Forward)
            .unwrap();

        // A fresh cursor starts at the first interval; the adjacent
        // source records coalesce into one run.
        assert_eq!(
            cursor.next_range().unwrap(),
            Some(AddressRange {
                from: Ipv4Key(10),
                to: Ipv4Key(30),
            })
        );

        // Seek inside an interval returns that interval next.
        cursor.seek(Ipv4Key(15)).unwrap();
        assert_eq!(
            cursor.next_range().unwrap(),
            Some(AddressRange {
                from: Ipv4Key(10),
                to: Ipv4Key(30),
            })
        );

        // Seek into the gap after a run lands on the next run.
        cursor.seek(Ipv4Key(31)).unwrap();
        assert_eq!(
            cursor.next_range().unwrap(),
            Some(AddressRange {
                from: Ipv4Key(35),
                to: Ipv4Key(40),
            })
        );

        // Seek exactly at an interval start returns that interval.
        cursor.seek(Ipv4Key(50)).unwrap();
        assert_eq!(
            cursor.next_range().unwrap(),
            Some(AddressRange {
                from: Ipv4Key(50),
                to: Ipv4Key(50),
            })
        );

        // Seek past the end finishes the cursor.
        cursor.seek(Ipv4Key(51)).unwrap();
        assert_eq!(cursor.next_range().unwrap(), None);

        // Seek before the first interval restarts at the first interval.
        cursor.seek(Ipv4Key(0)).unwrap();
        assert_eq!(
            cursor.next_range().unwrap(),
            Some(AddressRange {
                from: Ipv4Key(10),
                to: Ipv4Key(30),
            })
        );

        fs::remove_file(path).unwrap();
    }

    #[test]
    fn backward_seek_lands_on_containing_or_previous_interval() {
        let (path, reader) = open_feed("seek-backward", &crafted_ranges());
        let mut cursor = reader
            .feed_range_cursor_v4("feed-a", RangeDirection::Backward)
            .unwrap();

        // A fresh backward cursor starts at the last interval.
        assert_eq!(
            cursor.next_range().unwrap(),
            Some(AddressRange {
                from: Ipv4Key(50),
                to: Ipv4Key(50),
            })
        );
        assert_eq!(
            cursor.next_range().unwrap(),
            Some(AddressRange {
                from: Ipv4Key(35),
                to: Ipv4Key(40),
            })
        );
        assert_eq!(
            cursor.next_range().unwrap(),
            Some(AddressRange {
                from: Ipv4Key(10),
                to: Ipv4Key(30),
            })
        );
        assert_eq!(cursor.next_range().unwrap(), None);

        // Seek inside the trailing run returns the coalesced run.
        cursor.seek(Ipv4Key(25)).unwrap();
        assert_eq!(
            cursor.next_range().unwrap(),
            Some(AddressRange {
                from: Ipv4Key(10),
                to: Ipv4Key(30),
            })
        );

        // Seek in the gap after a run lands on the run before it.
        cursor.seek(Ipv4Key(49)).unwrap();
        assert_eq!(
            cursor.next_range().unwrap(),
            Some(AddressRange {
                from: Ipv4Key(35),
                to: Ipv4Key(40),
            })
        );

        // Seek past the end lands on the last interval.
        cursor.seek(Ipv4Key(51)).unwrap();
        assert_eq!(
            cursor.next_range().unwrap(),
            Some(AddressRange {
                from: Ipv4Key(50),
                to: Ipv4Key(50),
            })
        );

        // Seek before the first interval finishes the cursor.
        cursor.seek(Ipv4Key(9)).unwrap();
        assert_eq!(cursor.next_range().unwrap(), None);

        fs::remove_file(path).unwrap();
    }

    #[test]
    fn seek_reads_only_the_target_interval() {
        // Thousands of intervals; seeking to the last one must perform
        // one bounded tree lookup, never walk every preceding interval.
        // A linear reopen-and-skip page would consume the whole prefix.
        let ranges: Vec<AddressRange<Ipv4Key>> = (0..5000u32)
            .map(|i| AddressRange {
                from: Ipv4Key(1 + i * 4),
                to: Ipv4Key(1 + i * 4 + 2),
            })
            .collect();
        let (path, reader) = open_feed("seek-work", &ranges);
        let mut cursor = reader
            .feed_range_cursor_v4("feed-a", RangeDirection::Forward)
            .unwrap();
        let target = Ipv4Key(1 + 4999 * 4);
        let ((), work) = crate::work::measure(|| {
            cursor.seek(target).unwrap();
            assert_eq!(
                cursor.next_range().unwrap(),
                Some(AddressRange {
                    from: target,
                    to: Ipv4Key(target.0 + 2),
                })
            );
        });
        // One lookup for the range-tree seek plus one for the
        // membership-dictionary lookup; both are constant depth.
        assert!(
            work.tree_lookups <= 2,
            "seek performed {} tree lookups, expected a bounded read",
            work.tree_lookups
        );
        // One consumed tree range for the target record; a linear skip
        // would have consumed all 5000 earlier ranges.
        assert!(
            work.ranges_consumed <= 3,
            "seek consumed {} ranges, expected a bounded read",
            work.ranges_consumed
        );
        fs::remove_file(path).unwrap();
    }

    #[test]
    fn consecutive_membership_id_is_resolved_once() {
        let mut cache = None;
        let mut loads = 0;
        assert!(cached_membership(&mut cache, 7, || {
            loads += 1;
            Ok(true)
        })
        .unwrap());
        assert!(cached_membership(&mut cache, 7, || {
            loads += 1;
            Ok(false)
        })
        .unwrap());
        assert!(!cached_membership(&mut cache, 8, || {
            loads += 1;
            Ok(false)
        })
        .unwrap());
        assert_eq!(loads, 2);
    }
}
