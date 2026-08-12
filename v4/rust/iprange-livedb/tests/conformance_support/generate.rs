use std::fs::{self, OpenOptions};
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, snapshot_to, AddressFamily, AddressRange, CancellationToken, FeedName,
    FinishedWorkflow, Ipv4Key, Ipv6Key, LiveWriter, MembershipOperation, NetworkEnrichmentV1,
    NetworkEnrichmentV1Location, SnapshotBudget, SnapshotPublicationPolicy, SnapshotSourceMode,
    StructureKind, TransactionBudget, ValueKind, ValueTag,
};

use super::{verify, Corpus, Family, Fixture, Kind, Structure};

pub(crate) fn rust_fixtures(root: &Path, corpus: &Corpus) {
    let scratch = Scratch::new();
    for fixture in corpus
        .fixtures
        .iter()
        .filter(|fixture| fixture.producer == "rust")
    {
        let output = scratch.path.join(&fixture.file);
        fs::create_dir_all(output.parent().unwrap()).unwrap();
        generate(&scratch.path, &output, fixture);
        verify::fixture_at(&output, fixture);
    }
    for (index, fixture) in corpus
        .fixtures
        .iter()
        .filter(|fixture| fixture.producer == "rust")
        .enumerate()
    {
        publish(&scratch.path.join(&fixture.file), root, fixture, index);
    }
}

fn generate(scratch: &Path, output: &Path, fixture: &Fixture) {
    let label = fixture
        .file
        .strip_prefix("rust/")
        .expect("Rust fixture path starts with rust/")
        .replace('/', "-");
    let live = scratch.join(format!("live-{label}"));
    create_live(
        &live,
        address_family(fixture.family),
        value_kind(fixture.kind),
        structure_kind(fixture),
        ValueTag::new(fixture.tag.as_bytes()).expect("manifest value tag is valid"),
        4,
        &CancellationToken::new(),
    )
    .unwrap();
    let mut writer =
        LiveWriter::open(&live, transaction_budget(), &CancellationToken::new()).unwrap();
    match fixture.file.as_str() {
        "rust/direct-ipv4.iprdb" => direct_ipv4(&mut writer, fixture),
        "rust/first-seen-ipv6.iprdb" => first_seen_ipv6(&mut writer, fixture),
        "rust/membership-ipv4.iprdb" => membership_ipv4(&mut writer),
        "rust/membership-ipv6.iprdb" => membership_ipv6(&mut writer, fixture),
        "rust/structured-ipv4.iprdb" => structured_ipv4(&mut writer, fixture),
        "rust/structured-ipv4-nothreat.iprdb" => structured_ipv4_nothreat(&mut writer, fixture),
        other => panic!("no Rust fixture generator for {other}"),
    }
    writer.close().unwrap();
    snapshot_to(
        &live,
        SnapshotSourceMode::Live,
        output,
        SnapshotPublicationPolicy::FailIfExists,
        &SnapshotBudget::new(32 * 1024 * 1024, 200_000, 3),
        &CancellationToken::new(),
    )
    .unwrap();
}

fn structured_ipv4(writer: &mut LiveWriter, fixture: &Fixture) {
    let cancellation = CancellationToken::new();
    let mut transaction = writer.begin_structured_transaction(&cancellation).unwrap();
    let botnet = transaction
        .ensure_feed(FeedName::new("botnet").unwrap())
        .unwrap();
    let scanner = transaction
        .ensure_feed(FeedName::new("scanner").unwrap())
        .unwrap();
    let empty = transaction.empty_membership().unwrap();
    let botnet_membership = transaction.add_feed(empty, botnet).unwrap();
    let scanner_membership = transaction.add_feed(empty, scanner).unwrap();
    let broad = transaction
        .intern_network_enrichment_v1(
            NetworkEnrichmentV1 {
                asn: 64512,
                country_id: 1,
                state_id: 2,
                city_id: 3,
                location: Some(NetworkEnrichmentV1Location {
                    latitude_microdegrees: 37_983_810,
                    longitude_microdegrees: 23_727_539,
                }),
            },
            Some(botnet_membership),
        )
        .unwrap();
    let narrow = transaction
        .intern_network_enrichment_v1(
            NetworkEnrichmentV1 {
                asn: 64513,
                country_id: 4,
                state_id: 5,
                city_id: 6,
                location: None,
            },
            Some(scanner_membership),
        )
        .unwrap();
    transaction
        .assign_v4(key4("10.1.0.0"), key4("10.1.0.255"), broad)
        .unwrap();
    transaction
        .assign_v4(key4("10.1.0.64"), key4("10.1.0.127"), narrow)
        .unwrap();
    transaction
        .clear_v4(key4("10.1.0.100"), key4("10.1.0.109"))
        .unwrap();
    transaction
        .set_metadata_json(&fixture.metadata.bytes().unwrap())
        .unwrap();
    transaction.commit().unwrap();
}

/// No-threat structured values: every interned enrichment carries
/// membership id zero (feeds absent), pinning the canonical absence result
/// in both readers (binary-format-v4.md section 9A).
fn structured_ipv4_nothreat(writer: &mut LiveWriter, fixture: &Fixture) {
    let cancellation = CancellationToken::new();
    let mut transaction = writer.begin_structured_transaction(&cancellation).unwrap();
    let plain = transaction
        .intern_network_enrichment_v1(
            NetworkEnrichmentV1 {
                asn: 64514,
                country_id: 7,
                state_id: 8,
                city_id: 9,
                location: Some(NetworkEnrichmentV1Location {
                    latitude_microdegrees: 40_640_060,
                    longitude_microdegrees: 22_944_420,
                }),
            },
            None,
        )
        .unwrap();
    let bare = transaction
        .intern_network_enrichment_v1(
            NetworkEnrichmentV1 {
                asn: 64515,
                country_id: 10,
                state_id: 11,
                city_id: 12,
                location: None,
            },
            None,
        )
        .unwrap();
    transaction
        .assign_v4(key4("10.2.0.0"), key4("10.2.0.127"), plain)
        .unwrap();
    transaction
        .assign_v4(key4("10.2.0.128"), key4("10.2.0.255"), bare)
        .unwrap();
    if let Some(metadata) = fixture.metadata.bytes() {
        transaction.set_metadata_json(&metadata).unwrap();
    }
    transaction.commit().unwrap();
}

fn direct_ipv4(writer: &mut LiveWriter, fixture: &Fixture) {
    let cancellation = CancellationToken::new();
    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    transaction
        .assign_v4(v4(fixture, "10.0.0.20"), v4(fixture, "10.0.0.29"), 1)
        .unwrap();
    transaction
        .assign_v4(v4(fixture, "10.0.0.10"), v4(fixture, "10.0.0.25"), 2)
        .unwrap();
    transaction
        .assign_v4(v4(fixture, "10.0.0.15"), v4(fixture, "10.0.0.17"), 3)
        .unwrap();
    transaction
        .clear_v4(v4(fixture, "10.0.0.22"), v4(fixture, "10.0.0.27"))
        .unwrap();
    transaction
        .assign_v4(v4(fixture, "10.0.0.30"), v4(fixture, "10.0.0.31"), 1)
        .unwrap();
    transaction
        .set_metadata_json(&fixture.metadata.bytes().unwrap())
        .unwrap();
    transaction.commit().unwrap();
}

fn first_seen_ipv6(writer: &mut LiveWriter, fixture: &Fixture) {
    let cancellation = CancellationToken::new();
    let mut workflow = writer
        .begin_first_seen_refresh(1_700_000_000, &cancellation)
        .unwrap();
    workflow
        .add_ranges_v6_slice(&[AddressRange {
            from: Ipv6Key::MIN,
            to: Ipv6Key::MAX,
        }])
        .unwrap();
    commit_changed(workflow.finish_input().unwrap(), fixture);
}

fn membership_ipv4(writer: &mut LiveWriter) {
    let cancellation = CancellationToken::new();
    let mut transaction = writer.begin_membership_transaction(&cancellation).unwrap();
    for index in 0..70 {
        let name = format!("feed-{index:03}");
        let feed = transaction
            .ensure_feed(FeedName::new(&name).unwrap())
            .unwrap();
        assert_eq!(feed.index(), index);
    }
    transaction.commit().unwrap();

    let mut transaction = writer.begin_membership_transaction(&cancellation).unwrap();
    let removed = transaction
        .lookup_feed(FeedName::new("feed-005").unwrap())
        .unwrap()
        .unwrap();
    transaction.delete_feed(removed).unwrap();
    let reused = transaction
        .ensure_feed(FeedName::new("feed-reused").unwrap())
        .unwrap();
    assert_eq!(reused.index(), 5);

    let a = membership(
        &mut transaction,
        &[
            "feed-000",
            "feed-reused",
            "feed-063",
            "feed-064",
            "feed-069",
        ],
    );
    let b = membership(&mut transaction, &["feed-001", "feed-065"]);
    transaction
        .apply_v4(
            key4("10.0.0.0"),
            key4("10.0.0.255"),
            a,
            MembershipOperation::Replace,
        )
        .unwrap();
    transaction
        .apply_v4(
            key4("10.0.1.0"),
            key4("10.0.1.255"),
            b,
            MembershipOperation::Replace,
        )
        .unwrap();
    transaction
        .apply_v4(
            key4("10.0.1.0"),
            key4("10.0.1.127"),
            a,
            MembershipOperation::Union,
        )
        .unwrap();
    transaction.commit().unwrap();
}

fn membership_ipv6(writer: &mut LiveWriter, fixture: &Fixture) {
    let cancellation = CancellationToken::new();
    let mut transaction = writer.begin_membership_transaction(&cancellation).unwrap();
    let global = transaction
        .ensure_feed(FeedName::new("global").unwrap())
        .unwrap();
    let special = transaction
        .ensure_feed(FeedName::new("special").unwrap())
        .unwrap();
    let empty = transaction.empty_membership().unwrap();
    let global = transaction.add_feed(empty, global).unwrap();
    let special = transaction.add_feed(empty, special).unwrap();
    transaction
        .apply_v6(
            Ipv6Key::MIN,
            Ipv6Key::MAX,
            global,
            MembershipOperation::Replace,
        )
        .unwrap();
    transaction
        .apply_v6(
            key6("2001:db8::"),
            key6("2001:db8::ffff"),
            special,
            MembershipOperation::Union,
        )
        .unwrap();
    transaction
        .set_metadata_json(&fixture.metadata.bytes().unwrap())
        .unwrap();
    transaction.commit().unwrap();
}

fn membership(
    transaction: &mut iprange_livedb::MembershipTransaction<'_>,
    names: &[&str],
) -> iprange_livedb::MembershipRef {
    let mut membership = transaction.empty_membership().unwrap();
    for name in names {
        let feed = transaction
            .lookup_feed(FeedName::new(name).unwrap())
            .unwrap()
            .unwrap();
        membership = transaction.add_feed(membership, feed).unwrap();
    }
    membership
}

fn commit_changed(finished: FinishedWorkflow<'_>, fixture: &Fixture) {
    let mut prepared = match finished {
        FinishedWorkflow::Changed(prepared) => prepared,
        FinishedWorkflow::NoChange(report) => {
            panic!("fixture workflow unexpectedly made no change: {report:?}")
        }
    };
    if let Some(metadata) = fixture.metadata.bytes() {
        prepared.set_metadata_json(&metadata).unwrap();
    }
    prepared.commit().unwrap();
}

fn v4(fixture: &Fixture, text: &str) -> Ipv4Key {
    assert_eq!(fixture.family, Family::Ipv4);
    Ipv4Key(fixture.family.parse(text) as u32)
}

fn key4(text: &str) -> Ipv4Key {
    Ipv4Key(Family::Ipv4.parse(text) as u32)
}

fn key6(text: &str) -> Ipv6Key {
    Ipv6Key::from_u128(Family::Ipv6.parse(text))
}

fn transaction_budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 32 * 1024 * 1024,
        max_private_pages: 200_000,
        max_file_growth_pages: 200_000,
        max_open_files: 2,
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
        Kind::Structured => ValueKind::Structured,
    }
}

fn structure_kind(fixture: &Fixture) -> StructureKind {
    match (fixture.kind, fixture.structure) {
        (Kind::Direct | Kind::Membership, None) => StructureKind::None,
        (Kind::Structured, Some(Structure::NetworkEnrichmentV1)) => {
            StructureKind::NetworkEnrichmentV1
        }
        _ => panic!("fixture value and structure kinds disagree"),
    }
}

fn publish(source: &Path, root: &Path, fixture: &Fixture, index: usize) {
    let target = root.join(&fixture.file);
    let parent = target.parent().unwrap();
    fs::create_dir_all(parent).unwrap();
    let replacement = parent.join(format!(
        ".{}-replacement-{}-{index}",
        target.file_name().unwrap().to_string_lossy(),
        std::process::id()
    ));
    fs::copy(source, &replacement).unwrap();
    OpenOptions::new()
        .read(true)
        .open(&replacement)
        .unwrap()
        .sync_all()
        .unwrap();
    #[cfg(windows)]
    if target.exists() {
        fs::remove_file(&target).unwrap();
    }
    fs::rename(&replacement, &target).unwrap();
    #[cfg(unix)]
    OpenOptions::new()
        .read(true)
        .open(parent)
        .unwrap()
        .sync_all()
        .unwrap();
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
            "iprange-v4-conformance-generate-{}-{unique}",
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
