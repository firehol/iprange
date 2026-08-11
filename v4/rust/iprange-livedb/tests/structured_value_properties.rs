#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

use std::fs;
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::validation::{
    validate, ValidationBudget, ValidationMode, ValidationSinkControl,
};
use iprange_livedb::{
    create_live, CancellationToken, CommitDurability, FeedName, Ipv4Key, LiveReader, LiveWriter,
    NetworkEnrichmentV1, NetworkEnrichmentV1Location, RangeDirection, StructureKind,
    StructuredTransaction, TransactionBudget, ValueKind, ValueTag,
};

const DOMAIN: usize = 128;
const ROUNDS: usize = 24;
const FEEDS: [&str; 6] = ["botnet", "scanner", "phishing", "proxy", "spam", "tor"];
const SEEDS: [u64; 3] = [
    0x0d49_2f18_a73c_65e1,
    0x8a1e_d930_5b74_c26f,
    0xf671_04ac_39d8_b52e,
];

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct Expected {
    value: NetworkEnrichmentV1,
    threat_mask: u8,
}

const fn profile(
    asn: u32,
    country_id: u32,
    state_id: u32,
    city_id: u32,
    location: Option<(i32, i32)>,
    threat_mask: u8,
) -> Expected {
    Expected {
        value: NetworkEnrichmentV1 {
            asn,
            country_id,
            state_id,
            city_id,
            location: match location {
                Some((latitude_microdegrees, longitude_microdegrees)) => {
                    Some(NetworkEnrichmentV1Location {
                        latitude_microdegrees,
                        longitude_microdegrees,
                    })
                }
                None => None,
            },
        },
        threat_mask,
    }
}

const PROFILES: [Expected; 7] = [
    profile(64_512, 0, 0, 0, None, 0),
    profile(64_512, 0, 0, 0, None, 0b00_0001),
    profile(64_513, 1, 2, 3, Some((0, 0)), 0b00_0011),
    profile(
        64_514,
        8,
        13,
        21,
        Some((-90_000_000, -180_000_000)),
        0b10_1010,
    ),
    profile(
        64_515,
        u32::MAX,
        u32::MAX - 1,
        u32::MAX - 2,
        Some((90_000_000, 180_000_000)),
        0b11_1111,
    ),
    profile(0, 34, 55, 89, None, 0b01_0100),
    profile(0, 0, 0, 0, None, 0b00_0100),
];

struct TestDatabase {
    main: PathBuf,
}

impl TestDatabase {
    fn new(seed_index: usize) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        Self {
            main: std::env::temp_dir().join(format!(
                "iprange-v4-structured-property-{seed_index}-{}-{unique}",
                std::process::id()
            )),
        }
    }

    fn sidecar(&self) -> PathBuf {
        let mut name = self.main.file_name().unwrap().to_os_string();
        name.push(".readers");
        self.main.with_file_name(name)
    }
}

impl Drop for TestDatabase {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.main);
        let _ = fs::remove_file(self.sidecar());
    }
}

#[test]
fn randomized_structured_transactions_match_independent_address_model() {
    for (seed_index, seed) in SEEDS.into_iter().enumerate() {
        run_seed(seed_index, seed);
    }
}

fn run_seed(seed_index: usize, seed: u64) {
    let files = TestDatabase::new(seed_index);
    let cancellation = CancellationToken::new();
    create_live(
        &files.main,
        iprange_livedb::AddressFamily::Ipv4,
        ValueKind::Structured,
        StructureKind::NetworkEnrichmentV1,
        ValueTag::new(b"enrichment").unwrap(),
        8,
        &cancellation,
    )
    .unwrap();

    let mut writer = LiveWriter::open(&files.main, budget(), &cancellation).unwrap();
    create_feeds(&mut writer, &cancellation);
    let mut committed = [None; DOMAIN];
    assert_database(&files.main, &committed, seed, usize::MAX);
    validate_clean(&files.main, seed, usize::MAX);

    let mut random = Random(seed);
    for round in 0..ROUNDS {
        let mut draft = committed;
        let mut transaction = writer.begin_structured_transaction(&cancellation).unwrap();
        let feeds = transaction_feeds(&mut transaction);
        let mut structures = [None; PROFILES.len() + 1];

        let broad = PROFILES
            .iter()
            .position(|profile| Some(*profile) != committed[0])
            .unwrap();
        assign(
            &mut transaction,
            &feeds,
            &mut structures,
            0,
            DOMAIN - 1,
            broad,
            &mut draft,
        );
        assign(
            &mut transaction,
            &feeds,
            &mut structures,
            DOMAIN / 4,
            DOMAIN * 3 / 4 - 1,
            (broad + 1) % PROFILES.len(),
            &mut draft,
        );
        clear(&mut transaction, DOMAIN / 2 - 8, DOMAIN / 2 + 7, &mut draft);

        let extra_operations = 12 + random.below(21) as usize;
        for _ in 0..extra_operations {
            let (from, to) = random.range();
            match random.below(5) {
                0 => clear(&mut transaction, from, to, &mut draft),
                1 => assign(
                    &mut transaction,
                    &feeds,
                    &mut structures,
                    from,
                    to,
                    PROFILES.len(),
                    &mut draft,
                ),
                _ => {
                    let profile = random.below(PROFILES.len() as u32) as usize;
                    assign(
                        &mut transaction,
                        &feeds,
                        &mut structures,
                        from,
                        to,
                        profile,
                        &mut draft,
                    );
                }
            }
        }

        if (round + seed_index) % 4 == 3 {
            transaction.abort().unwrap();
        } else {
            assert_eq!(
                transaction.commit().unwrap().durability,
                CommitDurability::Committed,
                "seed={seed:#018x} round={round}"
            );
            committed = draft;
        }

        assert_database(&files.main, &committed, seed, round);
        validate_clean(&files.main, seed, round);
    }
    writer.close().unwrap();
}

fn create_feeds(writer: &mut LiveWriter, cancellation: &CancellationToken) {
    let mut transaction = writer.begin_structured_transaction(cancellation).unwrap();
    for name in FEEDS {
        transaction
            .ensure_feed(FeedName::new(name).unwrap())
            .unwrap();
    }
    assert_eq!(
        transaction.commit().unwrap().durability,
        CommitDurability::Committed
    );
}

fn transaction_feeds(transaction: &mut StructuredTransaction<'_>) -> Vec<iprange_livedb::FeedRef> {
    FEEDS
        .iter()
        .map(|name| {
            transaction
                .lookup_feed(FeedName::new(name).unwrap())
                .unwrap()
                .unwrap()
        })
        .collect()
}

fn assign(
    transaction: &mut StructuredTransaction<'_>,
    feeds: &[iprange_livedb::FeedRef],
    structures: &mut [Option<iprange_livedb::StructureRef>],
    from: usize,
    to: usize,
    profile: usize,
    model: &mut [Option<Expected>; DOMAIN],
) {
    let structure = match structures[profile] {
        Some(structure) => structure,
        None => {
            let expected = PROFILES.get(profile).copied();
            let membership = expected
                .filter(|value| value.threat_mask != 0)
                .map(|value| membership(transaction, feeds, value.threat_mask));
            let structure = transaction
                .intern_network_enrichment_v1(
                    expected.map_or_else(NetworkEnrichmentV1::default, |value| value.value),
                    membership,
                )
                .unwrap();
            structures[profile] = Some(structure);
            structure
        }
    };
    transaction
        .assign_v4(Ipv4Key(from as u32), Ipv4Key(to as u32), structure)
        .unwrap();
    model[from..=to].fill(PROFILES.get(profile).copied());
}

fn membership(
    transaction: &mut StructuredTransaction<'_>,
    feeds: &[iprange_livedb::FeedRef],
    mask: u8,
) -> iprange_livedb::MembershipRef {
    let mut membership = transaction.empty_membership().unwrap();
    for (bit, feed) in feeds.iter().copied().enumerate() {
        if mask & (1 << bit) != 0 {
            membership = transaction.add_feed(membership, feed).unwrap();
        }
    }
    membership
}

fn clear(
    transaction: &mut StructuredTransaction<'_>,
    from: usize,
    to: usize,
    model: &mut [Option<Expected>; DOMAIN],
) {
    transaction
        .clear_v4(Ipv4Key(from as u32), Ipv4Key(to as u32))
        .unwrap();
    model[from..=to].fill(None);
}

fn assert_database(path: &Path, expected: &[Option<Expected>; DOMAIN], seed: u64, round: usize) {
    let cancellation = CancellationToken::new();
    let mut reader = LiveReader::open(path, &cancellation).unwrap();
    let feed_indexes: Vec<_> = FEEDS
        .iter()
        .map(|name| reader.lookup_feed(name).unwrap().unwrap().index)
        .collect();

    for (address, wanted) in expected.iter().copied().enumerate() {
        let actual = reader
            .lookup_network_enrichment_v1_v4(Ipv4Key(address as u32))
            .unwrap();
        match (wanted, actual) {
            (None, None) => {}
            (Some(wanted), Some(actual)) => {
                assert_eq!(
                    actual.value(),
                    wanted.value,
                    "seed={seed:#018x} round={round} address={address}"
                );
                assert_membership(
                    actual.threat_membership().unwrap(),
                    wanted.threat_mask,
                    &feed_indexes,
                    seed,
                    round,
                    address,
                );
            }
            _ => panic!(
                "structured presence mismatch: seed={seed:#018x} round={round} address={address}"
            ),
        }
    }

    assert_structured_ranges(&reader, expected, &feed_indexes, seed, round);
    assert_feed_ranges(&reader, expected, seed, round);
    reader.close().unwrap();
}

fn assert_structured_ranges(
    reader: &LiveReader,
    expected: &[Option<Expected>; DOMAIN],
    feed_indexes: &[u32],
    seed: u64,
    round: usize,
) {
    let wanted = structured_runs(expected);
    let mut cursor = reader
        .network_enrichment_v1_cursor_v4(RangeDirection::Forward)
        .unwrap();
    for (from, to, value) in wanted {
        let actual = cursor
            .next_range()
            .unwrap()
            .unwrap_or_else(|| panic!("missing structured range: seed={seed:#018x} round={round}"));
        assert_eq!((actual.from, actual.to), (Ipv4Key(from), Ipv4Key(to)));
        assert_eq!(actual.value.value(), value.value);
        assert_membership(
            actual.value.threat_membership().unwrap(),
            value.threat_mask,
            feed_indexes,
            seed,
            round,
            from as usize,
        );
    }
    assert!(
        cursor.next_range().unwrap().is_none(),
        "extra structured range: seed={seed:#018x} round={round}"
    );
}

fn assert_feed_ranges(
    reader: &LiveReader,
    expected: &[Option<Expected>; DOMAIN],
    seed: u64,
    round: usize,
) {
    for (bit, name) in FEEDS.iter().enumerate() {
        let wanted = boolean_runs(expected, bit);
        let mut cursor = reader
            .feed_range_cursor_v4(name, RangeDirection::Forward)
            .unwrap();
        for (from, to) in wanted {
            let actual = cursor.next_range().unwrap().unwrap_or_else(|| {
                panic!("missing feed range: seed={seed:#018x} round={round} feed={name}")
            });
            assert_eq!((actual.from, actual.to), (Ipv4Key(from), Ipv4Key(to)));
        }
        assert!(
            cursor.next_range().unwrap().is_none(),
            "extra feed range: seed={seed:#018x} round={round} feed={name}"
        );
    }
}

fn assert_membership(
    actual: Option<iprange_livedb::MembershipView<'_>>,
    wanted: u8,
    feed_indexes: &[u32],
    seed: u64,
    round: usize,
    address: usize,
) {
    if wanted == 0 {
        assert!(
            actual.is_none(),
            "unexpected membership: seed={seed:#018x} round={round} address={address}"
        );
        return;
    }
    let actual = actual.unwrap_or_else(|| {
        panic!("missing membership: seed={seed:#018x} round={round} address={address}")
    });
    for (bit, index) in feed_indexes.iter().copied().enumerate() {
        assert_eq!(
            actual.contains_index(index).unwrap(),
            wanted & (1 << bit) != 0,
            "seed={seed:#018x} round={round} address={address} feed={bit}"
        );
    }
}

fn structured_runs(expected: &[Option<Expected>; DOMAIN]) -> Vec<(u32, u32, Expected)> {
    let mut output = Vec::new();
    let mut index = 0;
    while index < DOMAIN {
        let Some(value) = expected[index] else {
            index += 1;
            continue;
        };
        let from = index;
        while index + 1 < DOMAIN && expected[index + 1] == Some(value) {
            index += 1;
        }
        output.push((from as u32, index as u32, value));
        index += 1;
    }
    output
}

fn boolean_runs(expected: &[Option<Expected>; DOMAIN], bit: usize) -> Vec<(u32, u32)> {
    let present =
        |index: usize| expected[index].is_some_and(|value| value.threat_mask & (1 << bit) != 0);
    let mut output = Vec::new();
    let mut index = 0;
    while index < DOMAIN {
        if !present(index) {
            index += 1;
            continue;
        }
        let from = index;
        while index + 1 < DOMAIN && present(index + 1) {
            index += 1;
        }
        output.push((from as u32, index as u32));
        index += 1;
    }
    output
}

fn validate_clean(path: &Path, seed: u64, round: usize) {
    let mut findings = Vec::new();
    let result = validate(
        path,
        ValidationMode::LiveCurrent,
        &ValidationBudget::heap_only(8 * 1024 * 1024, 2),
        &CancellationToken::new(),
        &mut |finding: &_| {
            findings.push(*finding);
            Ok(ValidationSinkControl::Continue)
        },
    )
    .unwrap();
    assert!(
        result.valid,
        "seed={seed:#018x} round={round} findings={findings:?}"
    );
}

fn budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 2 * 1024 * 1024,
        max_private_pages: 20_000,
        max_file_growth_pages: 20_000,
        max_open_files: 2,
    }
}

struct Random(u64);

impl Random {
    fn next(&mut self) -> u32 {
        self.0 ^= self.0 << 13;
        self.0 ^= self.0 >> 7;
        self.0 ^= self.0 << 17;
        self.0 as u32
    }

    fn below(&mut self, limit: u32) -> u32 {
        self.next() % limit
    }

    fn range(&mut self) -> (usize, usize) {
        let left = self.below(DOMAIN as u32) as usize;
        let right = self.below(DOMAIN as u32) as usize;
        (left.min(right), left.max(right))
    }
}
