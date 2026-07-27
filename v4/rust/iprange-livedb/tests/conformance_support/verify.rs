use std::collections::HashSet;
use std::fs::{self, OpenOptions};
use std::io::{Seek, SeekFrom, Write};
use std::path::{Component, Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::validation::{
    validate, ValidationBudget, ValidationMode, ValidationSinkControl,
};
use iprange_livedb::{
    AddressFamily, CancellationToken, Cardinality129, ErrorCode, ImmutableReader, Ipv4Key, Ipv6Key,
    MetaSelection, RangeDirection, ValueKind, ValueTag,
};

use super::{Corpus, Family, Fixture, InvalidCase, InvalidMutation, Kind, MetadataExpectation};

pub(crate) fn corpus(root: &Path, corpus: &Corpus) {
    assert_eq!(
        corpus
            .fixtures
            .iter()
            .filter(|fixture| fixture.producer == "rust")
            .count(),
        4,
        "the Rust-first corpus must retain its four foundation fixtures"
    );
    assert_eq!(corpus.invalid_cases.len(), 3);
    assert_fixture_inventory(root, corpus);
    for fixture in &corpus.fixtures {
        assert!(
            matches!(fixture.producer.as_str(), "rust" | "go"),
            "unknown fixture producer {}",
            fixture.producer
        );
        fixture_at(&root.join(&fixture.file), fixture);
    }
    invalid_cases(root, &corpus.invalid_cases);
}

pub(crate) fn fixture_at(path: &Path, fixture: &Fixture) {
    assert!(path.is_file(), "missing fixture {}", path.display());
    assert!(
        !sidecar(path).exists(),
        "immutable fixture has a sidecar: {}",
        path.display()
    );
    let reader = ImmutableReader::open(path)
        .unwrap_or_else(|error| panic!("failed to open {}: {error}", path.display()));
    let info = reader.info();
    assert_eq!(info.address_family, address_family(fixture.family));
    assert_eq!(info.value_kind, value_kind(fixture.kind));
    assert_eq!(
        info.value_tag,
        ValueTag::new(fixture.tag.as_bytes()).expect("manifest value tag is valid")
    );
    assert_eq!(info.meta_selection, MetaSelection::ProvenCurrent);
    assert_eq!(
        fs::metadata(path).unwrap().len(),
        info.page_count * 4096,
        "metadata page count disagrees with file length"
    );
    assert_metadata(&reader, &fixture.metadata);
    assert_valid(path);

    match fixture.kind {
        Kind::Direct => assert_direct(&reader, fixture),
        Kind::Membership => assert_membership(&reader, fixture),
    }
}

fn assert_fixture_inventory(root: &Path, corpus: &Corpus) {
    let mut expected = HashSet::new();
    for fixture in &corpus.fixtures {
        let relative = PathBuf::from(&fixture.file);
        assert!(
            relative
                .components()
                .all(|component| matches!(component, Component::Normal(_))),
            "fixture path must be local and normalized: {}",
            fixture.file
        );
        assert_eq!(
            relative.extension().and_then(|value| value.to_str()),
            Some("iprdb")
        );
        assert!(expected.insert(relative), "duplicate fixture path");
    }
    let mut actual = HashSet::new();
    collect_fixtures(root, root, &mut actual);
    assert_eq!(actual, expected, "manifest and committed fixtures differ");
}

fn collect_fixtures(root: &Path, directory: &Path, output: &mut HashSet<PathBuf>) {
    for entry in fs::read_dir(directory).unwrap() {
        let entry = entry.unwrap();
        let path = entry.path();
        let file_type = entry.file_type().unwrap();
        if file_type.is_dir() {
            collect_fixtures(root, &path, output);
        } else if path.extension().and_then(|value| value.to_str()) == Some("iprdb") {
            assert!(file_type.is_file(), "fixture must be a regular file");
            output.insert(path.strip_prefix(root).unwrap().to_path_buf());
        }
    }
}

fn assert_metadata(reader: &ImmutableReader, expected: &MetadataExpectation) {
    let expected = expected.bytes();
    assert_eq!(
        reader.metadata_json_len(),
        expected.as_ref().map(|bytes| bytes.len() as u64)
    );
    assert_eq!(reader.metadata_json().unwrap(), expected);
}

fn assert_valid(path: &Path) {
    let mut findings = Vec::new();
    let result = validate(
        path,
        ValidationMode::ImmutableCurrent,
        &ValidationBudget::heap_only(16 * 1024 * 1024, 1),
        &CancellationToken::new(),
        &mut |finding: &iprange_livedb::validation::ValidationFinding| {
            findings.push(*finding);
            Ok(ValidationSinkControl::Continue)
        },
    )
    .unwrap_or_else(|failure| {
        panic!(
            "validation failed for {}: {}",
            path.display(),
            failure.cause
        )
    });
    assert!(findings.is_empty(), "{findings:?}");
    assert!(result.valid);
    assert!(result.generation.is_some());
}

fn assert_direct(reader: &ImmutableReader, fixture: &Fixture) {
    assert!(fixture.feeds.is_empty());
    assert!(fixture.membership_ranges.is_empty());
    let expected: Vec<_> = fixture
        .direct_ranges
        .iter()
        .map(|range| {
            (
                fixture.family.parse(&range.from),
                fixture.family.parse(&range.to),
                range.value,
            )
        })
        .collect();
    assert_canonical_direct(fixture.family, &expected);
    let actual = match fixture.family {
        Family::Ipv4 => {
            let mut cursor = reader.direct_cursor_v4(RangeDirection::Forward).unwrap();
            let mut ranges = Vec::new();
            while let Some(range) = cursor.next_range().unwrap() {
                ranges.push((
                    u128::from(range.from.0),
                    u128::from(range.to.0),
                    range.value,
                ));
            }
            ranges
        }
        Family::Ipv6 => {
            let mut cursor = reader.direct_cursor_v6(RangeDirection::Forward).unwrap();
            let mut ranges = Vec::new();
            while let Some(range) = cursor.next_range().unwrap() {
                ranges.push((range.from.to_u128(), range.to.to_u128(), range.value));
            }
            ranges
        }
    };
    assert_eq!(actual, expected);
    assert_eq!(reader.info().range_record_count, expected.len() as u64);
    assert_eq!(reader.info().active_feed_count, 0);
    assert_eq!(
        cardinality(fixture.family, &expected),
        fixture.address_count
    );
}

fn assert_canonical_direct(family: Family, ranges: &[(u128, u128, u32)]) {
    let mut previous: Option<(u128, u32)> = None;
    for &(from, to, value) in ranges {
        assert!(from <= to && to <= family.maximum());
        if let Some((previous_to, previous_value)) = previous {
            assert!(previous_to < from, "direct manifest ranges overlap");
            assert!(
                previous_to.checked_add(1) != Some(from) || previous_value != value,
                "direct manifest contains an uncoalesced range"
            );
        }
        previous = Some((to, value));
    }
}

fn assert_membership(reader: &ImmutableReader, fixture: &Fixture) {
    assert!(fixture.direct_ranges.is_empty());
    let expected_catalog: Vec<_> = fixture
        .feeds
        .iter()
        .map(|feed| (feed.name.clone(), feed.index))
        .collect();
    let mut actual_catalog = Vec::new();
    let mut cursor = reader.feed_cursor().unwrap();
    while let Some(feed) = cursor.next_feed().unwrap() {
        actual_catalog.push((feed.name.as_str().to_owned(), feed.index));
    }
    assert_eq!(actual_catalog, expected_catalog);
    assert_eq!(
        reader.info().active_feed_count,
        expected_catalog.len() as u64
    );
    for (name, index) in &expected_catalog {
        assert_eq!(reader.lookup_feed(name).unwrap().unwrap().index, *index);
        assert_eq!(
            feed_projection(reader, fixture.family, name),
            expected_projection(fixture, name)
        );
    }

    let ranges = parsed_memberships(fixture);
    assert_canonical_memberships(fixture.family, &ranges);
    assert_eq!(reader.info().range_record_count, ranges.len() as u64);
    assert_eq!(
        membership_cardinality(fixture.family, &ranges),
        fixture.address_count
    );
    for address in membership_probes(fixture.family, &ranges) {
        assert_membership_at(reader, fixture, address, expected_at(&ranges, address));
    }
}

type MembershipRange = (u128, u128, Vec<String>);

fn parsed_memberships(fixture: &Fixture) -> Vec<MembershipRange> {
    fixture
        .membership_ranges
        .iter()
        .map(|range| {
            (
                fixture.family.parse(&range.from),
                fixture.family.parse(&range.to),
                range.feeds.clone(),
            )
        })
        .collect()
}

fn assert_canonical_memberships(family: Family, ranges: &[MembershipRange]) {
    let mut previous: Option<(u128, &[String])> = None;
    for (from, to, feeds) in ranges {
        assert!(*from <= *to && *to <= family.maximum());
        assert!(!feeds.is_empty());
        if let Some((previous_to, previous_feeds)) = previous {
            assert!(previous_to < *from, "membership manifest ranges overlap");
            assert!(
                previous_to.checked_add(1) != Some(*from) || previous_feeds != feeds,
                "membership manifest contains an uncoalesced range"
            );
        }
        previous = Some((*to, feeds));
    }
}

fn expected_projection(fixture: &Fixture, name: &str) -> Vec<(u128, u128)> {
    let mut output: Vec<(u128, u128)> = Vec::new();
    for range in &fixture.membership_ranges {
        if !range.feeds.iter().any(|feed| feed == name) {
            continue;
        }
        let from = fixture.family.parse(&range.from);
        let to = fixture.family.parse(&range.to);
        if let Some(previous) = output.last_mut() {
            if previous.1.checked_add(1) == Some(from) {
                previous.1 = to;
                continue;
            }
        }
        output.push((from, to));
    }
    output
}

fn feed_projection(reader: &ImmutableReader, family: Family, name: &str) -> Vec<(u128, u128)> {
    match family {
        Family::Ipv4 => {
            let mut cursor = reader
                .feed_range_cursor_v4(name, RangeDirection::Forward)
                .unwrap();
            let mut output = Vec::new();
            while let Some(range) = cursor.next_range().unwrap() {
                output.push((u128::from(range.from.0), u128::from(range.to.0)));
            }
            output
        }
        Family::Ipv6 => {
            let mut cursor = reader
                .feed_range_cursor_v6(name, RangeDirection::Forward)
                .unwrap();
            let mut output = Vec::new();
            while let Some(range) = cursor.next_range().unwrap() {
                output.push((range.from.to_u128(), range.to.to_u128()));
            }
            output
        }
    }
}

fn membership_probes(family: Family, ranges: &[MembershipRange]) -> Vec<u128> {
    let mut probes = vec![family.minimum(), family.maximum()];
    for (from, to, _) in ranges {
        probes.extend([*from, *to]);
        if *from != family.minimum() {
            probes.push(from - 1);
        }
        if *to != family.maximum() {
            probes.push(to + 1);
        }
    }
    probes.sort_unstable();
    probes.dedup();
    probes
}

fn expected_at(ranges: &[MembershipRange], address: u128) -> &[String] {
    ranges
        .iter()
        .find(|(from, to, _)| *from <= address && address <= *to)
        .map_or(&[], |(_, _, feeds)| feeds.as_slice())
}

fn assert_membership_at(
    reader: &ImmutableReader,
    fixture: &Fixture,
    address: u128,
    expected: &[String],
) {
    let view = match fixture.family {
        Family::Ipv4 => reader
            .lookup_membership_v4(Ipv4Key(address as u32))
            .unwrap(),
        Family::Ipv6 => reader
            .lookup_membership_v6(Ipv6Key::from_u128(address))
            .unwrap(),
    };
    if expected.is_empty() {
        assert!(view.is_none(), "unexpected membership at {address}");
        return;
    }
    let view = view.unwrap_or_else(|| panic!("missing membership at {address}"));
    let highest = fixture
        .feeds
        .iter()
        .filter(|feed| expected.contains(&feed.name))
        .map(|feed| feed.index)
        .max()
        .unwrap();
    let mut expected_words = vec![0u64; (highest / 64 + 1) as usize];
    for feed in &fixture.feeds {
        let present = expected.contains(&feed.name);
        assert_eq!(view.contains_index(feed.index).unwrap(), present);
        if present {
            expected_words[(feed.index / 64) as usize] |= 1u64 << (feed.index % 64);
        }
    }
    assert_eq!(view.word_count().unwrap() as usize, expected_words.len());
    let mut words = vec![0; expected_words.len()];
    assert_eq!(view.read_words(0, &mut words).unwrap(), words.len());
    assert_eq!(words, expected_words);
}

fn cardinality(family: Family, ranges: &[(u128, u128, u32)]) -> String {
    sum_cardinality(family, ranges.iter().map(|(from, to, _)| (*from, *to)))
}

fn membership_cardinality(family: Family, ranges: &[MembershipRange]) -> String {
    sum_cardinality(family, ranges.iter().map(|(from, to, _)| (*from, *to)))
}

fn sum_cardinality(family: Family, ranges: impl Iterator<Item = (u128, u128)>) -> String {
    let mut total = Cardinality129::ZERO;
    for (from, to) in ranges {
        let count = match family {
            Family::Ipv4 => Cardinality129::ipv4_inclusive(from as u32, to as u32).unwrap(),
            Family::Ipv6 => {
                let from = Ipv6Key::from_u128(from);
                let to = Ipv6Key::from_u128(to);
                Cardinality129::ipv6_inclusive(from.hi, from.lo, to.hi, to.lo).unwrap()
            }
        };
        total = total.checked_add(count).unwrap();
    }
    total.to_string()
}

fn invalid_cases(root: &Path, cases: &[InvalidCase]) {
    let scratch = Scratch::new();
    for (index, case) in cases.iter().enumerate() {
        let source = root.join(&case.source);
        let target = scratch.path.join(format!("case-{index}.iprdb"));
        fs::copy(&source, &target).unwrap();
        mutate(&target, case.mutation);
        let error = ImmutableReader::open(&target).unwrap_err();
        let expected = match case.expected_error {
            super::model::ExpectedError::FormatInvalid => ErrorCode::FormatInvalid,
        };
        assert_eq!(error.code(), expected, "wrong rejection for {case:?}");
    }
}

fn mutate(path: &Path, mutation: InvalidMutation) {
    match mutation {
        InvalidMutation::WrongMagic => {
            let mut file = OpenOptions::new()
                .read(true)
                .write(true)
                .open(path)
                .unwrap();
            for offset in [0, 4096] {
                file.seek(SeekFrom::Start(offset)).unwrap();
                file.write_all(b"X").unwrap();
            }
            file.sync_all().unwrap();
        }
        InvalidMutation::Short => {
            OpenOptions::new()
                .write(true)
                .open(path)
                .unwrap()
                .set_len(4096)
                .unwrap();
        }
        InvalidMutation::Unaligned => {
            let mut file = OpenOptions::new().append(true).open(path).unwrap();
            file.write_all(&[0]).unwrap();
            file.sync_all().unwrap();
        }
    }
}

fn address_family(family: Family) -> AddressFamily {
    match family {
        Family::Ipv4 => AddressFamily::Ipv4,
        Family::Ipv6 => AddressFamily::Ipv6,
    }
}

fn value_kind(kind: Kind) -> ValueKind {
    match kind {
        Kind::Direct => ValueKind::Direct,
        Kind::Membership => ValueKind::Membership,
    }
}

fn sidecar(path: &Path) -> PathBuf {
    let mut name = path.file_name().unwrap().to_os_string();
    name.push(".readers");
    path.with_file_name(name)
}

struct Scratch {
    path: PathBuf,
}

impl Scratch {
    fn new() -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "iprange-v4-conformance-verify-{}-{unique}",
            std::process::id()
        ));
        fs::create_dir(&path).unwrap();
        Self { path }
    }
}

impl Drop for Scratch {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.path);
    }
}
