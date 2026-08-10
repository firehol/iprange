//! Bounded logical copy into one private immutable output.

use std::fs::File;

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, ValueKind};
use crate::error::{Error, Result};
use crate::heap::Heap;
use crate::immutable_output::{Builder, Finished, MembershipWords, OutputBudget};
use crate::membership_view::MembershipView;
use crate::range_cursor::RangeDirection;
use crate::reader_core::GenerationReader;
use crate::recovery::source_guard::Source;

use super::{source, SnapshotBudget};

pub(super) struct Failure {
    pub file: File,
    pub cause: Error,
}

pub(super) fn copy(
    source: &Source,
    file: File,
    budget: &SnapshotBudget,
    cancellation: &CancellationToken,
) -> std::result::Result<Finished, Box<Failure>> {
    let reader = source::reader(source);
    let mut heap = Heap::new(budget.max_heap_bytes);
    let builder = Builder::new_owned_with_heap(
        file,
        reader.output_spec(),
        OutputBudget {
            max_output_pages: budget.max_output_pages,
        },
        &mut heap,
    )
    .map_err(|failure| {
        Box::new(Failure {
            file: failure.file,
            cause: failure.cause,
        })
    })?;
    let available = SnapshotBudget {
        max_heap_bytes: heap.remaining(),
        ..*budget
    };
    copy_into(reader, builder, &available, cancellation)
}

fn copy_into(
    reader: GenerationReader<'_>,
    mut builder: Builder,
    budget: &SnapshotBudget,
    cancellation: &CancellationToken,
) -> std::result::Result<Finished, Box<Failure>> {
    if let Err(cause) = copy_logical(reader, &mut builder, budget, cancellation) {
        return Err(Box::new(Failure {
            file: builder.into_file(),
            cause,
        }));
    }
    builder.finish_owned().map_err(|failure| {
        Box::new(Failure {
            file: failure.builder.into_file(),
            cause: failure.cause,
        })
    })
}

fn copy_logical(
    reader: GenerationReader<'_>,
    builder: &mut Builder,
    budget: &SnapshotBudget,
    cancellation: &CancellationToken,
) -> Result<()> {
    let spec = reader.output_spec();
    if spec.value_kind == ValueKind::Membership {
        copy_feeds(reader, builder, cancellation)?;
    }
    match (spec.address_family, spec.value_kind) {
        (AddressFamily::Ipv4, ValueKind::Direct) => copy_direct_v4(reader, builder, cancellation)?,
        (AddressFamily::Ipv6, ValueKind::Direct) => copy_direct_v6(reader, builder, cancellation)?,
        (AddressFamily::Ipv4, ValueKind::Membership) => {
            copy_membership_v4(reader, builder, cancellation)?
        }
        (AddressFamily::Ipv6, ValueKind::Membership) => {
            copy_membership_v6(reader, builder, cancellation)?
        }
    }
    copy_metadata(reader, builder, budget, cancellation)
}

fn copy_feeds(
    reader: GenerationReader<'_>,
    builder: &mut Builder,
    cancellation: &CancellationToken,
) -> Result<()> {
    let mut cursor = reader.feed_cursor()?;
    loop {
        cancellation.check()?;
        let Some(feed) = cursor.next_feed()? else {
            return Ok(());
        };
        builder.push_feed(feed.name, feed.index)?;
    }
}

fn copy_direct_v4(
    reader: GenerationReader<'_>,
    builder: &mut Builder,
    cancellation: &CancellationToken,
) -> Result<()> {
    let mut cursor = reader.direct_cursor_v4(RangeDirection::Forward)?;
    loop {
        cancellation.check()?;
        let Some(range) = cursor.next_range()? else {
            return Ok(());
        };
        builder.push_direct_v4(range.from, range.to, range.value)?;
    }
}

fn copy_direct_v6(
    reader: GenerationReader<'_>,
    builder: &mut Builder,
    cancellation: &CancellationToken,
) -> Result<()> {
    let mut cursor = reader.direct_cursor_v6(RangeDirection::Forward)?;
    loop {
        cancellation.check()?;
        let Some(range) = cursor.next_range()? else {
            return Ok(());
        };
        builder.push_direct_v6(range.from, range.to, range.value)?;
    }
}

fn copy_membership_v4(
    reader: GenerationReader<'_>,
    builder: &mut Builder,
    cancellation: &CancellationToken,
) -> Result<()> {
    let mut cursor = reader.membership_ranges::<crate::Ipv4Key>()?;
    loop {
        cancellation.check()?;
        let Some(range) = cursor.next()? else {
            return Ok(());
        };
        let words = SnapshotWords::new(reader.membership(range.membership)?, cancellation)?;
        builder.push_membership_v4(range.from, range.to, &words)?;
    }
}

fn copy_membership_v6(
    reader: GenerationReader<'_>,
    builder: &mut Builder,
    cancellation: &CancellationToken,
) -> Result<()> {
    let mut cursor = reader.membership_ranges::<crate::Ipv6Key>()?;
    loop {
        cancellation.check()?;
        let Some(range) = cursor.next()? else {
            return Ok(());
        };
        let words = SnapshotWords::new(reader.membership(range.membership)?, cancellation)?;
        builder.push_membership_v6(range.from, range.to, &words)?;
    }
}

struct SnapshotWords<'a> {
    view: MembershipView<'a>,
    word_count: u32,
    cancellation: &'a CancellationToken,
}

impl<'a> SnapshotWords<'a> {
    fn new(view: MembershipView<'a>, cancellation: &'a CancellationToken) -> Result<Self> {
        cancellation.check()?;
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
    reader: GenerationReader<'_>,
    builder: &mut Builder,
    budget: &SnapshotBudget,
    cancellation: &CancellationToken,
) -> Result<()> {
    let Some(length) = reader.metadata_json_len() else {
        return Ok(());
    };
    cancellation.check()?;
    let length = usize::try_from(length)
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
    if reader.read_metadata_json(&mut input)? != Some(length) {
        return Err(Error::Corrupt("metadata length changed while copying"));
    }
    cancellation.check()?;
    builder.write_metadata_with_budget(&input, budget.max_heap_bytes - charged)
}
