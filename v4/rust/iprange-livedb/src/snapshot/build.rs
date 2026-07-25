//! Bounded logical copy into one private immutable output.

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, MetaV4, ValueKind};
use crate::error::{Error, Result};
use crate::feed_catalog::FeedCursor;
use crate::immutable_output::{Builder, Finished, MembershipWords};
use crate::membership_view::{self, MembershipView};
use crate::metadata;
use crate::range_cursor::{Cursor, RangeDirection};

use super::SnapshotBudget;

pub(super) struct Failure {
    pub builder: Builder,
    pub cause: Error,
}

pub(super) fn copy(
    file: &std::fs::File,
    meta: MetaV4,
    mut builder: Builder,
    budget: &SnapshotBudget,
    cancellation: &CancellationToken,
) -> std::result::Result<Finished, Box<Failure>> {
    let result = copy_logical(file, meta, &mut builder, budget, cancellation);
    if let Err(cause) = result {
        return Err(Box::new(Failure { builder, cause }));
    }
    builder.finish_owned().map_err(|failure| {
        Box::new(Failure {
            builder: failure.builder,
            cause: failure.cause,
        })
    })
}

fn copy_logical(
    file: &std::fs::File,
    meta: MetaV4,
    builder: &mut Builder,
    budget: &SnapshotBudget,
    cancellation: &CancellationToken,
) -> Result<()> {
    if meta.value_kind == ValueKind::Membership {
        copy_feeds(file, meta, builder, cancellation)?;
    }
    match (meta.address_family, meta.value_kind) {
        (AddressFamily::Ipv4, ValueKind::Direct) => {
            copy_direct_v4(file, meta, builder, cancellation)?
        }
        (AddressFamily::Ipv6, ValueKind::Direct) => {
            copy_direct_v6(file, meta, builder, cancellation)?
        }
        (AddressFamily::Ipv4, ValueKind::Membership) => {
            copy_membership_v4(file, meta, builder, cancellation)?
        }
        (AddressFamily::Ipv6, ValueKind::Membership) => {
            copy_membership_v6(file, meta, builder, cancellation)?
        }
    }
    copy_metadata(file, meta, builder, budget, cancellation)
}

fn copy_feeds(
    file: &std::fs::File,
    meta: MetaV4,
    builder: &mut Builder,
    cancellation: &CancellationToken,
) -> Result<()> {
    let mut cursor = FeedCursor::new(file, &meta)?;
    while let Some(feed) = next_feed(&mut cursor, cancellation)? {
        builder.push_feed(feed.name, feed.index)?;
    }
    Ok(())
}

fn next_feed(
    cursor: &mut FeedCursor<'_>,
    cancellation: &CancellationToken,
) -> Result<Option<crate::FeedEntry>> {
    cancellation.check()?;
    cursor.next_feed()
}

fn copy_direct_v4(
    file: &std::fs::File,
    meta: MetaV4,
    builder: &mut Builder,
    cancellation: &CancellationToken,
) -> Result<()> {
    let mut cursor = Cursor::new(file, &meta, RangeDirection::Forward, None)?;
    while let Some(range) = next_range(&mut cursor, cancellation)? {
        builder.push_direct_v4(range.from, range.to, range.value)?;
    }
    Ok(())
}

fn copy_direct_v6(
    file: &std::fs::File,
    meta: MetaV4,
    builder: &mut Builder,
    cancellation: &CancellationToken,
) -> Result<()> {
    let mut cursor = Cursor::new(file, &meta, RangeDirection::Forward, None)?;
    while let Some(range) = next_range(&mut cursor, cancellation)? {
        builder.push_direct_v6(range.from, range.to, range.value)?;
    }
    Ok(())
}

fn copy_membership_v4(
    file: &std::fs::File,
    meta: MetaV4,
    builder: &mut Builder,
    cancellation: &CancellationToken,
) -> Result<()> {
    let mut cursor = Cursor::new(file, &meta, RangeDirection::Forward, None)?;
    while let Some(range) = next_range(&mut cursor, cancellation)? {
        let words = SnapshotWords::new(file, meta, range.value, cancellation)?;
        builder.push_membership_v4(range.from, range.to, &words)?;
    }
    Ok(())
}

fn copy_membership_v6(
    file: &std::fs::File,
    meta: MetaV4,
    builder: &mut Builder,
    cancellation: &CancellationToken,
) -> Result<()> {
    let mut cursor = Cursor::new(file, &meta, RangeDirection::Forward, None)?;
    while let Some(range) = next_range(&mut cursor, cancellation)? {
        let words = SnapshotWords::new(file, meta, range.value, cancellation)?;
        builder.push_membership_v6(range.from, range.to, &words)?;
    }
    Ok(())
}

fn next_range<K: crate::key::IpKey>(
    cursor: &mut Cursor<'_, K>,
    cancellation: &CancellationToken,
) -> Result<Option<crate::DirectRange<K>>> {
    cancellation.check()?;
    cursor.next()
}

struct SnapshotWords<'a> {
    view: MembershipView<'a>,
    word_count: u32,
    cancellation: &'a CancellationToken,
}

impl<'a> SnapshotWords<'a> {
    fn new(
        file: &'a std::fs::File,
        meta: MetaV4,
        id: u32,
        cancellation: &'a CancellationToken,
    ) -> Result<Self> {
        cancellation.check()?;
        let view = membership_view::by_id(file, &meta, id, None)?;
        let word_count = view.word_count()?;
        Ok(Self {
            view,
            word_count,
            cancellation,
        })
    }
}

impl MembershipWords for SnapshotWords<'_> {
    fn word_count(&self) -> u32 {
        self.word_count
    }

    fn read_words(&self, start: u32, output: &mut [u64]) -> Result<()> {
        self.cancellation.check()?;
        let copied = self.view.read_words(start, output)?;
        if copied != output.len() {
            return Err(Error::Corrupt("membership length changed while copying"));
        }
        Ok(())
    }
}

fn copy_metadata(
    file: &std::fs::File,
    meta: MetaV4,
    builder: &mut Builder,
    budget: &SnapshotBudget,
    cancellation: &CancellationToken,
) -> Result<()> {
    if meta.metadata_root == 0 {
        return Ok(());
    }
    cancellation.check()?;
    let length = usize::try_from(meta.metadata_uncompressed_len)
        .map_err(|_| Error::Corrupt("metadata length is not addressable"))?;
    if length as u64 > budget.max_heap_bytes {
        return Err(Error::BudgetExceeded("snapshot metadata input heap"));
    }
    let mut input = Vec::new();
    input
        .try_reserve_exact(length)
        .map_err(|_| Error::BudgetExceeded("snapshot metadata input heap"))?;
    input.resize(length, 0);
    let charged = input.capacity() as u64;
    if charged > budget.max_heap_bytes {
        return Err(Error::BudgetExceeded("snapshot metadata input heap"));
    }
    if metadata::read(file, &meta, &mut input)? != Some(length) {
        return Err(Error::Corrupt("metadata length changed while copying"));
    }
    cancellation.check()?;
    builder.write_metadata_with_budget(&input, budget.max_heap_bytes - charged)
}
