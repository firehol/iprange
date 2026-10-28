//! Equal-output end-to-end immutable construction comparisons.
use super::{
    close_writer, immutable_result, immutable_snapshot, require_committed, ImmutableResultSpec,
    ScenarioResult,
};
use crate::{
    measure,
    model::{transaction_budget, TestDatabase},
};
use iprange_livedb::*;
fn display(e: impl std::fmt::Display) -> String {
    e.to_string()
}
fn v4(n: usize) -> Ipv4Key {
    Ipv4Key(n as u32)
}
fn v6(n: usize) -> Ipv6Key {
    Ipv6Key::from_u128((1u128 << 100) + n as u128)
}
macro_rules! family {
    ($run:ident,$source:ident,$key:ty,$convert:ident,$family:expr,$create:ident,$add:ident,$cursor:ident) => {
        struct $source {
            size: usize,
            next: usize,
            reverse: bool,
            batch: [DirectRange<$key>; 256],
        }
        impl RangeSource<DirectRange<$key>> for $source {
            fn next_batch(&mut self) -> iprange_livedb::Result<Option<&[DirectRange<$key>]>> {
                if self.next == self.size {
                    return Ok(None);
                }
                let count = (self.size - self.next).min(self.batch.len());
                for (offset, r) in self.batch[..count].iter_mut().enumerate() {
                    let n = if self.reverse {
                        self.size - 1 - self.next - offset
                    } else {
                        self.next + offset
                    };
                    *r = DirectRange {
                        from: $convert(n * 2),
                        to: $convert(n * 2),
                        value: (n % 17) as u32,
                    };
                }
                self.next += count;
                Ok(Some(&self.batch[..count]))
            }
        }
        pub(super) fn $run(
            name: &'static str,
            size: usize,
            legacy: bool,
            reverse: bool,
        ) -> std::result::Result<ScenarioResult, String> {
            if size > u32::MAX as usize / 2 {
                return Err("benchmark size exceeds address space".into());
            }
            let db = TestDatabase::new(name)?;
            let mut source = $source {
                size,
                next: 0,
                reverse,
                batch: [DirectRange {
                    from: $convert(0),
                    to: $convert(0),
                    value: 0,
                }; 256],
            };
            let cancel = CancellationToken::new();
            let tag = ValueTag::new(b"provider").unwrap();
            let (operation, measurement) =
                measure::operation(|| -> std::result::Result<_, String> {
                    if legacy {
                        create_live(
                            db.main(),
                            $family,
                            ValueKind::Direct,
                            StructureKind::None,
                            tag,
                            1,
                            &cancel,
                            true,
                        )
                        .map_err(display)?;
                        let mut writer =
                            LiveWriter::open(db.main(), transaction_budget(size, 1), &cancel)
                                .map_err(display)?;
                        let mut workflow =
                            writer.begin_direct_replacement(&cancel).map_err(display)?;
                        workflow.$add(&mut source).map_err(display)?;
                        let FinishedWorkflow::Changed(prepared) =
                            workflow.finish_input().map_err(display)?
                        else {
                            return Err("empty benchmark".into());
                        };
                        require_committed(prepared.commit().map_err(display)?)?;
                        close_writer(&mut writer)?;
                        immutable_snapshot(&db, size)
                    } else {
                        let output = db.snapshot();
                        let budget = ImmutableBuildBudget::new(
                            64 * 1024 * 1024,
                            size.saturating_mul(16).max(20000) as u64,
                            size.saturating_mul(16).max(20000) as u64,
                            3,
                        );
                        let order = if reverse {
                            ImmutableInputOrder::Unordered
                        } else {
                            ImmutableInputOrder::OrderedDisjoint
                        };
                        let result = $create(
                            &output,
                            tag,
                            None,
                            PublicationPolicy::FailIfExists,
                            &mut source,
                            order,
                            &budget,
                            &cancel,
                        )
                        .map_err(|e| format!("{e:?}"))?;
                        if result.publication.publication != PublicationStatus::Published
                            || result.cleanup_state() != publication::CleanupState::Clean
                        {
                            return Err(format!("incomplete publication: {result:?}"));
                        }
                        Ok(output)
                    }
                });
            let output = operation?;
            let reader = ImmutableReader::open(&output).map_err(display)?;
            let mut cursor = reader.$cursor(RangeDirection::Forward).map_err(display)?;
            for n in 0..size {
                if cursor.next_range().map_err(display)?
                    != Some(DirectRange {
                        from: $convert(n * 2),
                        to: $convert(n * 2),
                        value: (n % 17) as u32,
                    })
                {
                    return Err("benchmark output differs from scalar model".into());
                }
            }
            if cursor.next_range().map_err(display)?.is_some() {
                return Err("extra benchmark range".into());
            }
            immutable_result(
                ImmutableResultSpec {
                    name,
                    size,
                    auxiliary: 0,
                    work_units: size as u64,
                    emitted_units: size as u64,
                },
                &db,
                measurement,
                &output,
            )
        }
    };
}
family!(
    v4_run,
    V4Source,
    Ipv4Key,
    v4,
    AddressFamily::Ipv4,
    create_immutable_direct_v4,
    add_ranges_v4,
    direct_cursor_v4
);
family!(
    v6_run,
    V6Source,
    Ipv6Key,
    v6,
    AddressFamily::Ipv6,
    create_immutable_direct_v6,
    add_ranges_v6,
    direct_cursor_v6
);

macro_rules! membership_family {
    ($run:ident,$source:ident,$key:ty,$convert:ident,$family:expr,$create:ident,$apply:ident,$lookup:ident) => {
        struct $source {
            size: usize,
            next: usize,
            reverse: bool,
            names: [FeedName; 8],
            labels: [FeedName; 2],
        }
        impl ImmutableMembershipSource<$key> for $source {
            fn next_range(
                &mut self,
            ) -> iprange_livedb::Result<Option<ImmutableMembershipRange<'_, $key>>> {
                if self.next == self.size {
                    return Ok(None);
                }
                let n = if self.reverse {
                    self.size - 1 - self.next
                } else {
                    self.next
                };
                self.next += 1;
                self.labels = [self.names[n % 8], self.names[(n + 1) % 8]];
                Ok(Some(ImmutableMembershipRange {
                    from: $convert(n * 2),
                    to: $convert(n * 2 + usize::from(self.reverse) * 2),
                    feeds: &self.labels,
                }))
            }
        }
        pub(super) fn $run(
            name: &'static str,
            size: usize,
            legacy: bool,
            overlap: bool,
        ) -> std::result::Result<ScenarioResult, String> {
            if size > u32::MAX as usize / 2 {
                return Err("benchmark size exceeds address space".into());
            }
            let names = std::array::from_fn(|i| FeedName::new(&format!("country{i}")).unwrap());
            let mut source = $source {
                size,
                next: 0,
                reverse: overlap,
                names,
                labels: [names[0]; 2],
            };
            let db = TestDatabase::new(name)?;
            let cancel = CancellationToken::new();
            let tag = ValueTag::new(b"country").unwrap();
            let (operation, measurement) =
                measure::operation(|| -> std::result::Result<_, String> {
                    if legacy {
                        create_live(
                            db.main(),
                            $family,
                            ValueKind::Membership,
                            StructureKind::None,
                            tag,
                            1,
                            &cancel,
                            true,
                        )
                        .map_err(display)?;
                        let mut writer =
                            LiveWriter::open(db.main(), transaction_budget(size, 8), &cancel)
                                .map_err(display)?;
                        let mut transaction = writer
                            .begin_membership_transaction(&cancel)
                            .map_err(display)?;
                        let mut feeds = Vec::with_capacity(8);
                        for name in names {
                            feeds.push(transaction.ensure_feed(name).map_err(display)?);
                        }
                        while let Some(range) = source.next_range().map_err(display)? {
                            let mut member = transaction.empty_membership().map_err(display)?;
                            for label in range.feeds {
                                let feed = feeds.iter().find(|feed| feed.name() == *label).unwrap();
                                member = transaction.add_feed(member, *feed).map_err(display)?;
                            }
                            transaction
                                .$apply(range.from, range.to, member, MembershipOperation::Union)
                                .map_err(display)?;
                        }
                        require_committed(transaction.commit().map_err(display)?)?;
                        close_writer(&mut writer)?;
                        immutable_snapshot(&db, size)
                    } else {
                        let output = db.snapshot();
                        let budget = ImmutableBuildBudget::new(
                            64 * 1024 * 1024,
                            size.saturating_mul(16).max(20000) as u64,
                            size.saturating_mul(16).max(20000) as u64,
                            3,
                        );
                        let order = if overlap {
                            ImmutableInputOrder::Unordered
                        } else {
                            ImmutableInputOrder::OrderedDisjoint
                        };
                        let result = $create(
                            &output,
                            tag,
                            None,
                            PublicationPolicy::FailIfExists,
                            &names,
                            &mut source,
                            order,
                            &budget,
                            &cancel,
                        )
                        .map_err(|e| format!("{e:?}"))?;
                        if result.publication.publication != PublicationStatus::Published
                            || result.cleanup_state() != publication::CleanupState::Clean
                        {
                            return Err(format!("incomplete publication: {result:?}"));
                        }
                        Ok(output)
                    }
                });
            let output = operation?;
            let reader = ImmutableReader::open(&output).map_err(display)?;
            let indexes = names
                .iter()
                .map(|name| {
                    reader
                        .lookup_feed(name.as_str())
                        .map_err(display)?
                        .map(|feed| feed.index)
                        .ok_or("missing feed".to_owned())
                })
                .collect::<std::result::Result<Vec<_>, String>>()?;
            for at in 0..size * 2 + 2 {
                let actual = reader.$lookup($convert(at)).map_err(display)?;
                for (label, index) in indexes.iter().enumerate() {
                    let expected = (at % 2 == 0 || overlap)
                        && (0..=usize::from(overlap)).any(|back| {
                            at / 2 >= back && {
                                let n = at / 2 - back;
                                n < size
                                    && at >= n * 2
                                    && at <= n * 2 + usize::from(overlap) * 2
                                    && (label == n % 8 || label == (n + 1) % 8)
                            }
                        });
                    let found = match actual {
                        Some(ref member) => member.contains_index(*index).map_err(display)?,
                        None => false,
                    };
                    if found != expected {
                        return Err(format!("membership mismatch address {at}, label {label}"));
                    }
                }
            }
            let intervals = reader.info().range_record_count;
            immutable_result(
                ImmutableResultSpec {
                    name,
                    size,
                    auxiliary: 8,
                    work_units: size as u64,
                    emitted_units: intervals,
                },
                &db,
                measurement,
                &output,
            )
        }
    };
}
membership_family!(
    membership_v4,
    NamedV4,
    Ipv4Key,
    v4,
    AddressFamily::Ipv4,
    create_immutable_membership_v4,
    apply_v4,
    lookup_membership_v4
);
membership_family!(
    membership_v6,
    NamedV6,
    Ipv6Key,
    v6,
    AddressFamily::Ipv6,
    create_immutable_membership_v6,
    apply_v6,
    lookup_membership_v6
);
