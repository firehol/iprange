//! Allocation-free comparison of two canonical range maps.

use crate::bootstrap::Bootstrap;
use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::MetaV4;
use crate::error::{Error, Result};
use crate::feed::FeedEntry;
use crate::feed_range_cursor::ProjectionCursor;
use crate::key::IpKey;
use crate::mapping::Mapping;
use crate::range_cursor::{Cursor, DirectRange, RangeDirection};

use super::Comparison;

pub(crate) fn coverage<K: IpKey>(
    mapping: &Mapping,
    meta: &MetaV4,
    cancellation: &CancellationToken,
) -> Result<Cardinality129> {
    cancellation.check()?;
    let mut cursor = Cursor::<K>::new(mapping, meta, RangeDirection::Forward, None)?;
    let mut total = Cardinality129::ZERO;
    while let Some(range) = cursor.next()? {
        cancellation.check()?;
        total = add(total, length(range)?)?;
    }
    Ok(total)
}

pub(crate) fn maps<K: IpKey>(
    mapping: &Mapping,
    before: &Bootstrap,
    after: &MetaV4,
    cancellation: &CancellationToken,
) -> Result<Comparison> {
    let old = MapStream::<K>::new(mapping, &before.meta)?;
    let new = MapStream::<K>::new(mapping, after)?;
    Ok(Sweep::new(old, new, cancellation)?
        .run(cancellation)?
        .comparison)
}

pub(crate) fn feeds<K: IpKey>(
    mapping: &Mapping,
    before: &MetaV4,
    before_feed: Option<FeedEntry>,
    after: &MetaV4,
    after_feed: FeedEntry,
    cancellation: &CancellationToken,
) -> Result<ScannedComparison> {
    let old = FeedStream::<K>::new(mapping, before, before_feed)?;
    let new = FeedStream::<K>::new(mapping, after, Some(after_feed))?;
    Sweep::new(old, new, cancellation)?.run(cancellation)
}

pub(crate) struct ScannedComparison {
    pub(crate) comparison: Comparison,
    pub(crate) before_intervals: u64,
    pub(crate) after_intervals: u64,
}

trait RangeStream<K> {
    fn next(&mut self, cancellation: &CancellationToken) -> Result<Option<DirectRange<K>>>;
}

struct MapStream<'a, K> {
    cursor: Cursor<'a, K>,
}

impl<'a, K: IpKey> MapStream<'a, K> {
    fn new(mapping: &'a Mapping, meta: &MetaV4) -> Result<Self> {
        Ok(Self {
            cursor: Cursor::new(mapping, meta, RangeDirection::Forward, None)?,
        })
    }
}

impl<K: IpKey> RangeStream<K> for MapStream<'_, K> {
    fn next(&mut self, cancellation: &CancellationToken) -> Result<Option<DirectRange<K>>> {
        cancellation.check()?;
        self.cursor.next()
    }
}

struct FeedStream<'a, K> {
    cursor: Option<ProjectionCursor<'a, K>>,
}

impl<'a, K: IpKey> FeedStream<'a, K> {
    fn new(mapping: &'a Mapping, meta: &MetaV4, feed: Option<FeedEntry>) -> Result<Self> {
        let cursor = feed
            .map(|feed| {
                ProjectionCursor::new(mapping, meta, feed.index, RangeDirection::Forward, None)
            })
            .transpose()?;
        Ok(Self { cursor })
    }
}

impl<K: IpKey> RangeStream<K> for FeedStream<'_, K> {
    fn next(&mut self, cancellation: &CancellationToken) -> Result<Option<DirectRange<K>>> {
        let Some(cursor) = &mut self.cursor else {
            return Ok(None);
        };
        Ok(cursor
            .next_with(&mut || cancellation.check())?
            .map(|range| DirectRange {
                from: range.from,
                to: range.to,
                value: 1,
            }))
    }
}

struct Counted<S> {
    inner: S,
    intervals: u64,
}

impl<S> Counted<S> {
    fn new(inner: S) -> Self {
        Self {
            inner,
            intervals: 0,
        }
    }
}

impl<K, S: RangeStream<K>> RangeStream<K> for Counted<S> {
    fn next(&mut self, cancellation: &CancellationToken) -> Result<Option<DirectRange<K>>> {
        let next = self.inner.next(cancellation)?;
        if next.is_some() {
            self.intervals = self
                .intervals
                .checked_add(1)
                .ok_or(Error::ArithmeticOverflow("workflow interval count"))?;
        }
        Ok(next)
    }
}

struct Sweep<K, L, R> {
    old_cursor: Counted<L>,
    new_cursor: Counted<R>,
    old: Option<DirectRange<K>>,
    new: Option<DirectRange<K>>,
    result: Comparison,
}

impl<K: IpKey, L: RangeStream<K>, R: RangeStream<K>> Sweep<K, L, R> {
    fn new(old_cursor: L, new_cursor: R, cancellation: &CancellationToken) -> Result<Self> {
        let mut old_cursor = Counted::new(old_cursor);
        let mut new_cursor = Counted::new(new_cursor);
        let old = old_cursor.next(cancellation)?;
        let new = new_cursor.next(cancellation)?;
        Ok(Self {
            old_cursor,
            new_cursor,
            old,
            new,
            result: Comparison::default(),
        })
    }

    fn run(mut self, cancellation: &CancellationToken) -> Result<ScannedComparison> {
        cancellation.check()?;
        while self.old.is_some() || self.new.is_some() {
            cancellation.check()?;
            self.step(cancellation)?;
        }
        verify(&self.result)?;
        Ok(ScannedComparison {
            comparison: self.result,
            before_intervals: self.old_cursor.intervals,
            after_intervals: self.new_cursor.intervals,
        })
    }

    fn step(&mut self, cancellation: &CancellationToken) -> Result<()> {
        match (self.old, self.new) {
            (Some(left), Some(right)) => self.step_pair(left, right, cancellation),
            (Some(left), None) => {
                add_removed(&mut self.result, length(left)?)?;
                self.old = self.old_cursor.next(cancellation)?;
                Ok(())
            }
            (None, Some(right)) => {
                add_added(&mut self.result, length(right)?)?;
                self.new = self.new_cursor.next(cancellation)?;
                Ok(())
            }
            (None, None) => Ok(()),
        }
    }

    fn step_pair(
        &mut self,
        left: DirectRange<K>,
        right: DirectRange<K>,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        let step = compare_pair(left, right, &mut self.result)?;
        self.old = advance(&mut self.old_cursor, left, step.left, cancellation)?;
        self.new = advance(&mut self.new_cursor, right, step.right, cancellation)?;
        Ok(())
    }
}

struct Step<K> {
    left: Advance<K>,
    right: Advance<K>,
}

#[derive(Clone, Copy)]
enum Advance<K> {
    Keep,
    Consume,
    After(K),
}

fn compare_pair<K: IpKey>(
    mut left: DirectRange<K>,
    mut right: DirectRange<K>,
    result: &mut Comparison,
) -> Result<Step<K>> {
    if left.to < right.from {
        return left_before_right(left, result);
    }
    if right.to < left.from {
        return right_before_left(right, result);
    }
    compare_overlap(&mut left, &mut right, result)
}

fn compare_overlap<K: IpKey>(
    left: &mut DirectRange<K>,
    right: &mut DirectRange<K>,
    result: &mut Comparison,
) -> Result<Step<K>> {
    align_starts(left, right, result)?;
    let end = left.to.min(right.to);
    let overlap = left.from.inclusive_cardinality(end)?;
    result.before = add(result.before, overlap)?;
    result.after = add(result.after, overlap)?;
    classify_overlap(left.value, right.value, overlap, result)?;
    Ok(Step {
        left: after_or_consume(left.to, end),
        right: after_or_consume(right.to, end),
    })
}

fn classify_overlap(
    left: u32,
    right: u32,
    overlap: Cardinality129,
    result: &mut Comparison,
) -> Result<()> {
    if left == right {
        result.unchanged = add(result.unchanged, overlap)?;
    } else {
        result.changed = add(result.changed, overlap)?;
    }
    Ok(())
}

fn after_or_consume<K: Copy + Eq>(to: K, end: K) -> Advance<K> {
    if to == end {
        Advance::Consume
    } else {
        Advance::After(end)
    }
}

fn left_before_right<K: IpKey>(left: DirectRange<K>, result: &mut Comparison) -> Result<Step<K>> {
    add_removed(result, length(left)?)?;
    Ok(Step {
        left: Advance::Consume,
        right: Advance::Keep,
    })
}

fn right_before_left<K: IpKey>(right: DirectRange<K>, result: &mut Comparison) -> Result<Step<K>> {
    add_added(result, length(right)?)?;
    Ok(Step {
        left: Advance::Keep,
        right: Advance::Consume,
    })
}

fn align_starts<K: IpKey>(
    left: &mut DirectRange<K>,
    right: &mut DirectRange<K>,
    result: &mut Comparison,
) -> Result<()> {
    if left.from < right.from {
        let end = right
            .from
            .checked_previous()
            .ok_or(Error::ArithmeticOverflow("range comparison prefix"))?;
        let prefix = left.from.inclusive_cardinality(end)?;
        add_removed(result, prefix)?;
        left.from = right.from;
    } else if right.from < left.from {
        let end = left
            .from
            .checked_previous()
            .ok_or(Error::ArithmeticOverflow("range comparison prefix"))?;
        let prefix = right.from.inclusive_cardinality(end)?;
        add_added(result, prefix)?;
        right.from = left.from;
    }
    Ok(())
}

fn advance<K: IpKey, S: RangeStream<K>>(
    cursor: &mut S,
    mut range: DirectRange<K>,
    action: Advance<K>,
    cancellation: &CancellationToken,
) -> Result<Option<DirectRange<K>>> {
    match action {
        Advance::Keep => Ok(Some(range)),
        Advance::Consume => cursor.next(cancellation),
        Advance::After(end) => {
            range.from = end
                .checked_next()
                .ok_or(Error::ArithmeticOverflow("range comparison cursor"))?;
            Ok(Some(range))
        }
    }
}

fn length<K: IpKey>(range: DirectRange<K>) -> Result<Cardinality129> {
    range.from.inclusive_cardinality(range.to)
}

fn add_added(result: &mut Comparison, count: Cardinality129) -> Result<()> {
    result.after = add(result.after, count)?;
    result.added = add(result.added, count)?;
    Ok(())
}

fn add_removed(result: &mut Comparison, count: Cardinality129) -> Result<()> {
    result.before = add(result.before, count)?;
    result.removed = add(result.removed, count)?;
    Ok(())
}

fn add(left: Cardinality129, right: Cardinality129) -> Result<Cardinality129> {
    left.checked_add(right)
        .map_err(|_| Error::ArithmeticOverflow("workflow address count"))
}

fn verify(result: &Comparison) -> Result<()> {
    let before = add(add(result.unchanged, result.changed)?, result.removed)?;
    let after = add(add(result.unchanged, result.changed)?, result.added)?;
    if before != result.before || after != result.after {
        return Err(Error::Corrupt("workflow address classes do not balance"));
    }
    Ok(())
}
