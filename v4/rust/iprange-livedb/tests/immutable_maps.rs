#![cfg(any(unix, windows))]
use iprange_livedb::*;
use std::{
    fs,
    path::PathBuf,
    sync::atomic::{AtomicU64, Ordering},
};
struct File(PathBuf);
impl File {
    fn new() -> Self {
        static ID: AtomicU64 = AtomicU64::new(0);
        Self(std::env::temp_dir().join(format!(
            "immutable-maps-{}-{}",
            std::process::id(),
            ID.fetch_add(1, Ordering::Relaxed)
        )))
    }
}
impl Drop for File {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.0);
    }
}
fn tag() -> ValueTag {
    ValueTag::new(b"provider").unwrap()
}
fn budget() -> ImmutableBuildBudget {
    ImmutableBuildBudget::new(2 * 1024 * 1024, 2000, 2000, 3)
}
fn ordered_budget() -> ImmutableBuildBudget {
    ImmutableBuildBudget::new(2 * 1024 * 1024, 2000, 0, 3)
}
fn validate(path: &std::path::Path) {
    use iprange_livedb::validation::*;
    let report = validate(
        path,
        ValidationMode::ImmutableCurrent,
        &ValidationBudget::heap_only(2 * 1024 * 1024, 4),
        &CancellationToken::new(),
        &mut |_: &ValidationFinding| Ok(ValidationSinkControl::Continue),
    )
    .unwrap();
    assert!(report.valid, "{report:?}");
}
struct Named<K> {
    records: std::vec::IntoIter<(K, K, Vec<FeedName>)>,
    current: Option<(K, K, Vec<FeedName>)>,
}
impl<K> Named<K> {
    fn new(records: Vec<(K, K, Vec<FeedName>)>) -> Self {
        Self {
            records: records.into_iter(),
            current: None,
        }
    }
}
impl<K: Copy> ImmutableMembershipSource<K> for Named<K> {
    fn next_range(&mut self) -> Result<Option<ImmutableMembershipRange<'_, K>>> {
        self.current = self.records.next();
        Ok(self
            .current
            .as_ref()
            .map(|(from, to, feeds)| ImmutableMembershipRange {
                from: *from,
                to: *to,
                feeds,
            }))
    }
}
fn v4(n: u32) -> Ipv4Key {
    Ipv4Key(n)
}
fn v6(n: u32) -> Ipv6Key {
    Ipv6Key::from_u128((1u128 << 100) + u128::from(n))
}
macro_rules! suite {
    ($module:ident,$key:ty,$key_fn:ident,$direct:ident,$membership:ident,$lookup:ident,$cursor:ident) => {
        mod $module {
            use super::*;
            #[test]
            fn ordered_direct_coalesces_and_unordered_is_last_assignment() {
                let ordered = [
                    DirectRange {
                        from: $key_fn(0),
                        to: $key_fn(5),
                        value: 0,
                    },
                    DirectRange {
                        from: $key_fn(6),
                        to: $key_fn(10),
                        value: 0,
                    },
                    DirectRange {
                        from: $key_fn(11),
                        to: $key_fn(20),
                        value: 9,
                    },
                ];
                let unordered = [
                    DirectRange {
                        from: $key_fn(0),
                        to: $key_fn(20),
                        value: 9,
                    },
                    DirectRange {
                        from: $key_fn(0),
                        to: $key_fn(10),
                        value: 0,
                    },
                ];
                for (ranges, order, budget) in [
                    (
                        &ordered[..],
                        ImmutableInputOrder::OrderedDisjoint,
                        ordered_budget(),
                    ),
                    (&unordered[..], ImmutableInputOrder::Unordered, budget()),
                ] {
                    let f = File::new();
                    let result = $direct(
                        &f.0,
                        tag(),
                        Some(br#"{"provider":"test"}"#),
                        PublicationPolicy::FailIfExists,
                        &mut SliceSource::new(ranges),
                        order,
                        &budget,
                        &CancellationToken::new(),
                    )
                    .unwrap();
                    assert_eq!(result.report.normalized_interval_count, 2);
                    assert_eq!(result.report.addresses, Cardinality129::from_u64(21));
                    let r = ImmutableReader::open(&f.0).unwrap();
                    for i in 0..30 {
                        assert_eq!(
                            r.$lookup($key_fn(i)).unwrap(),
                            if i <= 10 {
                                Some(0)
                            } else if i <= 20 {
                                Some(9)
                            } else {
                                None
                            }
                        );
                    }
                    assert_eq!(
                        r.metadata_json().unwrap().unwrap(),
                        br#"{"provider":"test"}"#
                    );
                    validate(&f.0);
                }
            }
            #[test]
            fn membership_union_matches_scalar_model_and_reclaims_dictionary() {
                let names = (0..70)
                    .map(|i| FeedName::new(&format!("label{i:02}")).unwrap())
                    .collect::<Vec<_>>();
                let mut records = Vec::new();
                let mut expected = vec![vec![false; 70]; 128];
                let mut seed = 17u32;
                for i in 0..500 {
                    seed = seed.wrapping_mul(1664525).wrapping_add(1013904223);
                    let lo = (seed % 128) as usize;
                    let hi = (lo + (seed as usize / 128) % 16).min(127);
                    let indexes = [i % 70, (i * 13 + 1) % 70];
                    for row in &mut expected[lo..=hi] {
                        for &j in &indexes {
                            row[j] = true;
                        }
                    }
                    records.push((
                        $key_fn(lo as u32),
                        $key_fn(hi as u32),
                        indexes.iter().map(|&j| names[j]).collect(),
                    ));
                }
                let f = File::new();
                $membership(
                    &f.0,
                    tag(),
                    None,
                    PublicationPolicy::FailIfExists,
                    &names,
                    &mut Named::new(records),
                    ImmutableInputOrder::Unordered,
                    &budget(),
                    &CancellationToken::new(),
                )
                .unwrap();
                let r = ImmutableReader::open(&f.0).unwrap();
                for (j, name) in names.iter().enumerate() {
                    let mut found = [false; 128];
                    let mut cursor = r.$cursor(name.as_str(), RangeDirection::Forward).unwrap();
                    while let Some(range) = cursor.next_range().unwrap() {
                        for (i, present) in found.iter_mut().enumerate() {
                            if $key_fn(i as u32) >= range.from && $key_fn(i as u32) <= range.to {
                                *present = true;
                            }
                        }
                    }
                    for i in 0..128 {
                        assert_eq!(found[i], expected[i][j], "address {i}, label {j}");
                    }
                }
                validate(&f.0);
            }
            #[test]
            fn ordered_named_coalesces_repeated_labels_and_preserves_empty_feeds() {
                let names = [
                    FeedName::new("a").unwrap(),
                    FeedName::new("b").unwrap(),
                    FeedName::new("unused").unwrap(),
                ];
                let f = File::new();
                let records = vec![
                    ($key_fn(1), $key_fn(5), vec![names[0], names[1], names[0]]),
                    ($key_fn(6), $key_fn(9), vec![names[1], names[0]]),
                ];
                let result = $membership(
                    &f.0,
                    tag(),
                    None,
                    PublicationPolicy::FailIfExists,
                    &names,
                    &mut Named::new(records),
                    ImmutableInputOrder::OrderedDisjoint,
                    &ordered_budget(),
                    &CancellationToken::new(),
                )
                .unwrap();
                assert_eq!(result.report.normalized_interval_count, 1);
                let r = ImmutableReader::open(&f.0).unwrap();
                assert_eq!(r.info().active_feed_count, 3);
                validate(&f.0);
            }
            #[test]
            fn invalid_order_and_unknown_names_preserve_destination() {
                let f = File::new();
                $direct(
                    &f.0,
                    tag(),
                    None,
                    PublicationPolicy::FailIfExists,
                    &mut SliceSource::new(&[]),
                    ImmutableInputOrder::OrderedDisjoint,
                    &ordered_budget(),
                    &CancellationToken::new(),
                )
                .unwrap();
                let previous = fs::read(&f.0).unwrap();
                let ranges = [
                    DirectRange {
                        from: $key_fn(4),
                        to: $key_fn(8),
                        value: 1,
                    },
                    DirectRange {
                        from: $key_fn(3),
                        to: $key_fn(5),
                        value: 2,
                    },
                ];
                assert!($direct(
                    &f.0,
                    tag(),
                    None,
                    PublicationPolicy::ReplaceExisting,
                    &mut SliceSource::new(&ranges),
                    ImmutableInputOrder::OrderedDisjoint,
                    &ordered_budget(),
                    &CancellationToken::new()
                )
                .is_err());
                let unknown = FeedName::new("unknown").unwrap();
                assert!($membership(
                    &f.0,
                    tag(),
                    None,
                    PublicationPolicy::ReplaceExisting,
                    &[],
                    &mut Named::new(vec![($key_fn(0), $key_fn(1), vec![unknown])]),
                    ImmutableInputOrder::Unordered,
                    &budget(),
                    &CancellationToken::new()
                )
                .is_err());
                assert_eq!(fs::read(&f.0).unwrap(), previous);
            }
        }
    };
}
suite!(
    ipv4,
    Ipv4Key,
    v4,
    create_immutable_direct_v4,
    create_immutable_membership_v4,
    lookup_direct_v4,
    feed_range_cursor_v4
);
suite!(
    ipv6,
    Ipv6Key,
    v6,
    create_immutable_direct_v6,
    create_immutable_membership_v6,
    lookup_direct_v6,
    feed_range_cursor_v6
);
#[test]
fn full_ipv6_and_empty_memberships() {
    for order in [
        ImmutableInputOrder::OrderedDisjoint,
        ImmutableInputOrder::Unordered,
    ] {
        let f = File::new();
        let ranges = [DirectRange {
            from: Ipv6Key::MIN,
            to: Ipv6Key::MAX,
            value: 0,
        }];
        let result = create_immutable_direct_v6(
            &f.0,
            tag(),
            None,
            PublicationPolicy::FailIfExists,
            &mut SliceSource::new(&ranges),
            order,
            &budget(),
            &CancellationToken::new(),
        )
        .unwrap();
        assert_eq!(result.report.addresses, Cardinality129::FULL_IPV6_SPACE);
        validate(&f.0);
        let f = File::new();
        let names = [FeedName::new("entire").unwrap()];
        let result = create_immutable_membership_v6(
            &f.0,
            tag(),
            None,
            PublicationPolicy::FailIfExists,
            &names,
            &mut Named::new(vec![(Ipv6Key::MIN, Ipv6Key::MAX, names.to_vec())]),
            order,
            &budget(),
            &CancellationToken::new(),
        )
        .unwrap();
        assert_eq!(result.report.addresses, Cardinality129::FULL_IPV6_SPACE);
        validate(&f.0);
        let f = File::new();
        let result = create_immutable_membership_v6(
            &f.0,
            tag(),
            None,
            PublicationPolicy::FailIfExists,
            &[],
            &mut Named::new(vec![]),
            order,
            &budget(),
            &CancellationToken::new(),
        )
        .unwrap();
        assert_eq!(result.report.addresses, Cardinality129::ZERO);
        validate(&f.0);
    }
}

#[test]
fn optional_batch_never_starves_mandatory_membership_scratch() {
    let names = [FeedName::new("label").unwrap()];
    for heap in [8, 32, 64, 32768] {
        for order in [
            ImmutableInputOrder::OrderedDisjoint,
            ImmutableInputOrder::Unordered,
        ] {
            let f = File::new();
            let budget = ImmutableBuildBudget::new(heap, 128, 128, 3);
            create_immutable_membership_v4(
                &f.0,
                tag(),
                None,
                PublicationPolicy::FailIfExists,
                &names,
                &mut Named::new(vec![(v4(1), v4(2), names.to_vec())]),
                order,
                &budget,
                &CancellationToken::new(),
            )
            .unwrap();
            validate(&f.0);
        }
    }
}

#[test]
fn repeated_subset_operands_are_reclaimed_in_tiny_workspace() {
    let names = (0..10)
        .map(|n| FeedName::new(&format!("label{n}")).unwrap())
        .collect::<Vec<_>>();
    let mut records = vec![(v4(0), v4(100), names.clone())];
    for i in 1..2000u32 {
        let subset = names
            .iter()
            .enumerate()
            .filter(|(bit, _)| i & (1 << bit) != 0)
            .map(|(_, &name)| name)
            .collect::<Vec<_>>();
        if !subset.is_empty() {
            records.push((v4(0), v4(100), subset));
        }
    }
    let f = File::new();
    let budget = ImmutableBuildBudget::new(64, 128, 24, 3);
    let result = create_immutable_membership_v4(
        &f.0,
        tag(),
        None,
        PublicationPolicy::FailIfExists,
        &names,
        &mut Named::new(records),
        ImmutableInputOrder::Unordered,
        &budget,
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(result.report.normalized_interval_count, 1);
    validate(&f.0);
}

#[test]
fn source_failure_cancellation_and_exhaustion_preserve_destination() {
    struct Fail;
    impl RangeSource<DirectRange<Ipv4Key>> for Fail {
        fn next_batch(&mut self) -> Result<Option<&[DirectRange<Ipv4Key>]>> {
            Err(Error::InvalidArgument("source failure"))
        }
    }
    for order in [
        ImmutableInputOrder::OrderedDisjoint,
        ImmutableInputOrder::Unordered,
    ] {
        let f = File::new();
        create_immutable_direct_v4(
            &f.0,
            tag(),
            None,
            PublicationPolicy::FailIfExists,
            &mut SliceSource::new(&[]),
            ImmutableInputOrder::OrderedDisjoint,
            &ordered_budget(),
            &CancellationToken::new(),
        )
        .unwrap();
        let previous = fs::read(&f.0).unwrap();
        let error = create_immutable_direct_v4(
            &f.0,
            tag(),
            None,
            PublicationPolicy::ReplaceExisting,
            &mut Fail,
            order,
            &budget(),
            &CancellationToken::new(),
        )
        .unwrap_err();
        assert_eq!(error.cause.code, ErrorCode::InvalidArgument);
        let token = CancellationToken::new();
        token.cancel();
        assert!(create_immutable_direct_v4(
            &f.0,
            tag(),
            None,
            PublicationPolicy::ReplaceExisting,
            &mut Fail,
            order,
            &budget(),
            &token
        )
        .is_err());
        let records = [DirectRange {
            from: v4(0),
            to: v4(10),
            value: 1,
        }];
        assert!(create_immutable_direct_v4(
            &f.0,
            tag(),
            None,
            PublicationPolicy::ReplaceExisting,
            &mut SliceSource::new(&records),
            order,
            &ImmutableBuildBudget::new(1024, 2, 0, 3),
            &CancellationToken::new()
        )
        .is_err());
        assert_eq!(fs::read(&f.0).unwrap(), previous);
    }
}

#[test]
fn named_source_cancellation_and_invalid_catalog_leave_valid_destination() {
    let file = File::new();
    let names = [FeedName::new("source").unwrap()];
    create_immutable_membership_v4(
        &file.0,
        tag(),
        None,
        PublicationPolicy::FailIfExists,
        &names,
        &mut Named::new(vec![]),
        ImmutableInputOrder::OrderedDisjoint,
        &budget(),
        &CancellationToken::new(),
    )
    .unwrap();
    let previous = fs::read(&file.0).unwrap();
    for (catalog, labels) in [
        (&names[..], vec![]),
        (&[names[0], names[0]][..], names.to_vec()),
    ] {
        let error = create_immutable_membership_v4(
            &file.0,
            tag(),
            None,
            PublicationPolicy::ReplaceExisting,
            catalog,
            &mut Named::new(vec![(v4(0), v4(2), labels)]),
            ImmutableInputOrder::Unordered,
            &budget(),
            &CancellationToken::new(),
        )
        .unwrap_err();
        assert_eq!(error.cause.code, ErrorCode::InvalidArgument);
        assert_eq!(error.cleanup_state(), publication::CleanupState::Clean);
        assert_eq!(fs::read(&file.0).unwrap(), previous);
    }
    struct Cancelling {
        token: CancellationToken,
        labels: [FeedName; 1],
        sent: bool,
    }
    impl ImmutableMembershipSource<Ipv4Key> for Cancelling {
        fn next_range(&mut self) -> Result<Option<ImmutableMembershipRange<'_, Ipv4Key>>> {
            if self.sent {
                self.token.cancel();
                return Ok(None);
            }
            self.sent = true;
            Ok(Some(ImmutableMembershipRange {
                from: v4(0),
                to: v4(10),
                feeds: &self.labels,
            }))
        }
    }
    for order in [
        ImmutableInputOrder::OrderedDisjoint,
        ImmutableInputOrder::Unordered,
    ] {
        let token = CancellationToken::new();
        let error = create_immutable_membership_v4(
            &file.0,
            tag(),
            None,
            PublicationPolicy::ReplaceExisting,
            &names,
            &mut Cancelling {
                token: token.clone(),
                labels: names,
                sent: false,
            },
            order,
            &budget(),
            &token,
        )
        .unwrap_err();
        assert_eq!(error.cause.code, ErrorCode::Cancelled);
        assert_eq!(error.cleanup_state(), publication::CleanupState::Clean);
        assert_eq!(fs::read(&file.0).unwrap(), previous);
    }
}

#[test]
fn single_slot_translation_collisions_preserve_all_memberships() {
    let names = [FeedName::new("a").unwrap(), FeedName::new("b").unwrap()];
    let records = (0..99)
        .map(|n| {
            let labels = match n % 3 {
                0 => vec![names[0]],
                1 => vec![names[1]],
                _ => names.to_vec(),
            };
            (v4(n * 2), v4(n * 2), labels)
        })
        .collect();
    let file = File::new();
    create_immutable_membership_v4(
        &file.0,
        tag(),
        None,
        PublicationPolicy::FailIfExists,
        &names,
        &mut Named::new(records),
        ImmutableInputOrder::Unordered,
        &ImmutableBuildBudget::new(8, 128, 128, 3),
        &CancellationToken::new(),
    )
    .unwrap();
    let reader = ImmutableReader::open(&file.0).unwrap();
    for n in 0..99 {
        let member = reader.lookup_membership_v4(v4(n * 2)).unwrap().unwrap();
        assert_eq!(member.contains_index(0).unwrap(), n % 3 != 1);
        assert_eq!(member.contains_index(1).unwrap(), n % 3 != 0);
        assert!(reader
            .lookup_membership_v4(v4(n * 2 + 1))
            .unwrap()
            .is_none());
    }
    validate(&file.0);
}
