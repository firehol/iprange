//! Allocation-free comparison of two canonical range maps.

use std::fs::File;

use crate::bootstrap::Bootstrap;
use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::MetaV4;
use crate::error::{Error, Result};
use crate::key::IpKey;
use crate::range_cursor::{Cursor, DirectRange, RangeDirection};

use super::Comparison;

pub(crate) fn coverage<K: IpKey>(
    file: &File,
    meta: &MetaV4,
    cancellation: &CancellationToken,
) -> Result<Cardinality129> {
    cancellation.check()?;
    let mut cursor = Cursor::<K>::new(file, meta, RangeDirection::Forward, None)?;
    let mut total = Cardinality129::ZERO;
    while let Some(range) = cursor.next()? {
        cancellation.check()?;
        total = add(total, length(range)?)?;
    }
    Ok(total)
}

pub(crate) fn maps<K: IpKey>(
    file: &File,
    before: &Bootstrap,
    after: &MetaV4,
    cancellation: &CancellationToken,
) -> Result<Comparison> {
    Sweep::<K>::new(file, before, after)?.run(cancellation)
}

struct Sweep<'a, K: IpKey> {
    old_cursor: Cursor<'a, K>,
    new_cursor: Cursor<'a, K>,
    old: Option<DirectRange<K>>,
    new: Option<DirectRange<K>>,
    result: Comparison,
}

impl<'a, K: IpKey> Sweep<'a, K> {
    fn new(file: &'a File, before: &Bootstrap, after: &MetaV4) -> Result<Self> {
        let mut old_cursor = Cursor::<K>::new(file, &before.meta, RangeDirection::Forward, None)?;
        let mut new_cursor = Cursor::<K>::new(file, after, RangeDirection::Forward, None)?;
        let old = old_cursor.next()?;
        let new = new_cursor.next()?;
        Ok(Self {
            old_cursor,
            new_cursor,
            old,
            new,
            result: Comparison::default(),
        })
    }

    fn run(mut self, cancellation: &CancellationToken) -> Result<Comparison> {
        cancellation.check()?;
        while self.old.is_some() || self.new.is_some() {
            cancellation.check()?;
            self.step()?;
        }
        verify(&self.result)?;
        Ok(self.result)
    }

    fn step(&mut self) -> Result<()> {
        match (self.old, self.new) {
            (Some(left), Some(right)) => self.step_pair(left, right),
            (Some(left), None) => {
                add_removed(&mut self.result, length(left)?)?;
                self.old = self.old_cursor.next()?;
                Ok(())
            }
            (None, Some(right)) => {
                add_added(&mut self.result, length(right)?)?;
                self.new = self.new_cursor.next()?;
                Ok(())
            }
            (None, None) => Ok(()),
        }
    }

    fn step_pair(&mut self, left: DirectRange<K>, right: DirectRange<K>) -> Result<()> {
        let step = compare_pair(left, right, &mut self.result)?;
        self.old = advance(&mut self.old_cursor, left, step.left)?;
        self.new = advance(&mut self.new_cursor, right, step.right)?;
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

fn advance<K: IpKey>(
    cursor: &mut Cursor<'_, K>,
    mut range: DirectRange<K>,
    action: Advance<K>,
) -> Result<Option<DirectRange<K>>> {
    match action {
        Advance::Keep => Ok(Some(range)),
        Advance::Consume => cursor.next(),
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
