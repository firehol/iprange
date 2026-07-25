use std::fs::{self, OpenOptions};
use std::io::{Read, Seek, SeekFrom, Write};
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live,
    validation::{
        validate, ValidationBudget, ValidationMode, ValidationReason, ValidationSinkControl,
    },
    AddressFamily, CancellationToken, CreationState, FeedName, Ipv4Key, LiveReader, LiveWriter,
    MembershipOperation, TransactionBudget, ValueKind, ValueTag,
};

struct Paths {
    live: PathBuf,
    snapshot: PathBuf,
}

impl Paths {
    fn new(label: &str) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let base = std::env::temp_dir().join(format!("iprange-v4-validation-{label}-{unique}"));
        Self {
            live: base.with_extension("live"),
            snapshot: base.with_extension("v4"),
        }
    }
}

impl Drop for Paths {
    fn drop(&mut self) {
        remove_pair(&self.live);
        remove_pair(&self.snapshot);
    }
}

#[test]
fn empty_immutable_database_validates_explicitly() {
    let paths = Paths::new("empty");
    let created = create_live(
        &paths.live,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::RETENTION,
        2,
    )
    .unwrap();
    assert_eq!(created.state, CreationState::Created);
    fs::copy(&paths.live, &paths.snapshot).unwrap();

    let mut findings = Vec::new();
    let result = validate(
        &paths.snapshot,
        ValidationMode::ImmutableCurrent,
        &ValidationBudget::heap_only(1024, 1),
        &CancellationToken::new(),
        &mut |finding: &iprange_livedb::validation::ValidationFinding| {
            findings.push(*finding);
            Ok(ValidationSinkControl::Continue)
        },
    )
    .unwrap();

    assert!(result.valid);
    assert!(findings.is_empty());
    assert_eq!(result.progress.checked_unique_pages, 0);
    assert_eq!(result.generation.unwrap().page_count, 2);
}

#[test]
fn populated_direct_database_validates_explicitly() {
    let paths = Paths::new("direct");
    create_live(
        &paths.live,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::RETENTION,
        2,
    )
    .unwrap();
    let mut writer = LiveWriter::open(&paths.live, transaction_budget()).unwrap();
    writer
        .assign_direct_v4(Ipv4Key(10), Ipv4Key(30), 7)
        .unwrap();
    writer.set_metadata_json(br#"{"source":"test"}"#).unwrap();
    writer.commit().unwrap();
    writer.close().unwrap();
    fs::copy(&paths.live, &paths.snapshot).unwrap();

    let result = validate_clean(&paths.snapshot);
    assert!(result.valid);
    assert!(result.progress.checked_unique_pages > 0);
}

#[test]
fn populated_membership_database_validates_all_indexes() {
    let paths = Paths::new("membership");
    create_live(
        &paths.live,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        ValueTag::new(b"membership").unwrap(),
        2,
    )
    .unwrap();
    let mut writer = LiveWriter::open(&paths.live, transaction_budget()).unwrap();
    let mut transaction = writer.begin_membership_transaction().unwrap();
    let alpha = transaction
        .ensure_feed(FeedName::new("alpha").unwrap())
        .unwrap();
    let beta = transaction
        .ensure_feed(FeedName::new("beta").unwrap())
        .unwrap();
    let empty = transaction.empty_membership().unwrap();
    let alpha_only = transaction.add_feed(empty, alpha).unwrap();
    let both = transaction.add_feed(alpha_only, beta).unwrap();
    transaction
        .apply_v4(
            Ipv4Key(10),
            Ipv4Key(19),
            alpha_only,
            MembershipOperation::Union,
        )
        .unwrap();
    transaction
        .apply_v4(Ipv4Key(15), Ipv4Key(24), both, MembershipOperation::Union)
        .unwrap();
    transaction
        .set_metadata_json(b"membership metadata")
        .unwrap();
    transaction.commit().unwrap();
    writer.close().unwrap();
    fs::copy(&paths.live, &paths.snapshot).unwrap();

    let result = validate_clean(&paths.snapshot);
    assert!(result.valid, "{:?}", result.progress);
    assert!(result.progress.checked_unique_pages >= 7);
}

#[test]
fn page_crc_damage_is_a_factual_invalid_report() {
    let paths = Paths::new("crc");
    create_live(
        &paths.live,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::RETENTION,
        2,
    )
    .unwrap();
    let mut writer = LiveWriter::open(&paths.live, transaction_budget()).unwrap();
    writer
        .assign_direct_v4(Ipv4Key(10), Ipv4Key(20), 3)
        .unwrap();
    writer.commit().unwrap();
    writer.close().unwrap();
    fs::copy(&paths.live, &paths.snapshot).unwrap();

    let root = selected_range_root(&paths.snapshot);
    let mut file = OpenOptions::new()
        .read(true)
        .write(true)
        .open(&paths.snapshot)
        .unwrap();
    file.seek(SeekFrom::Start(u64::from(root) * 4096 + 100))
        .unwrap();
    file.write_all(&[0x5a]).unwrap();
    file.sync_all().unwrap();

    let mut findings = Vec::new();
    let result = validate(
        &paths.snapshot,
        ValidationMode::ImmutableCurrent,
        &ValidationBudget::heap_only(1024 * 1024, 1),
        &CancellationToken::new(),
        &mut |finding: &iprange_livedb::validation::ValidationFinding| {
            findings.push(*finding);
            Ok(ValidationSinkControl::Continue)
        },
    )
    .unwrap();
    assert!(!result.valid);
    assert_eq!(
        result
            .progress
            .findings_for(ValidationReason::PageCrcMismatch),
        1
    );
    assert_eq!(findings[0].reason, ValidationReason::PageCrcMismatch);
}

#[test]
fn live_current_validation_pins_and_releases_its_reader_slot() {
    let paths = Paths::new("live");
    create_live(
        &paths.live,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::RETENTION,
        1,
    )
    .unwrap();
    let mut writer = LiveWriter::open(&paths.live, transaction_budget()).unwrap();
    writer
        .assign_direct_v4(Ipv4Key(100), Ipv4Key(200), 9)
        .unwrap();
    writer.commit().unwrap();

    let mut findings = Vec::new();
    let result = validate(
        &paths.live,
        ValidationMode::LiveCurrent,
        &ValidationBudget::heap_only(1024 * 1024, 2),
        &CancellationToken::new(),
        &mut |finding: &iprange_livedb::validation::ValidationFinding| {
            findings.push(*finding);
            Ok(ValidationSinkControl::Continue)
        },
    )
    .unwrap();
    assert!(result.valid);
    assert!(findings.is_empty());

    let reader = LiveReader::open(&paths.live).unwrap();
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(150)).unwrap(), Some(9));
    reader.close().unwrap();
    writer.close().unwrap();
}

#[test]
fn unselectable_bootstrap_is_a_completed_invalid_report() {
    let paths = Paths::new("bootstrap");
    let file = OpenOptions::new()
        .read(true)
        .write(true)
        .create_new(true)
        .open(&paths.snapshot)
        .unwrap();
    file.set_len(8192).unwrap();
    file.sync_all().unwrap();

    let mut findings = Vec::new();
    let result = validate(
        &paths.snapshot,
        ValidationMode::ImmutableCurrent,
        &ValidationBudget::heap_only(1024, 1),
        &CancellationToken::new(),
        &mut |finding: &iprange_livedb::validation::ValidationFinding| {
            findings.push(*finding);
            Ok(ValidationSinkControl::Continue)
        },
    )
    .unwrap();
    assert!(!result.valid);
    assert!(result.generation.is_none());
    assert!(result.progress.has_unbounded_unknown);
    assert_eq!(
        result
            .progress
            .findings_for(ValidationReason::MetaUnavailable),
        2
    );
    assert_eq!(findings.len(), 2);
}

#[test]
fn stopped_sink_returns_truthful_partial_progress() {
    let paths = Paths::new("stopped");
    let file = OpenOptions::new()
        .read(true)
        .write(true)
        .create_new(true)
        .open(&paths.snapshot)
        .unwrap();
    file.set_len(8192).unwrap();

    let failure = validate(
        &paths.snapshot,
        ValidationMode::ImmutableCurrent,
        &ValidationBudget::heap_only(1024, 1),
        &CancellationToken::new(),
        &mut |_: &iprange_livedb::validation::ValidationFinding| Ok(ValidationSinkControl::Stop),
    )
    .unwrap_err();
    assert!(matches!(
        failure.cause,
        iprange_livedb::Error::StoppedBySink
    ));
    assert_eq!(failure.progress.finding_count, 1);
}

#[test]
fn bound_live_database_can_report_an_unselectable_bootstrap() {
    let paths = Paths::new("live-bootstrap");
    create_live(
        &paths.live,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::RETENTION,
        1,
    )
    .unwrap();
    let mut file = OpenOptions::new()
        .read(true)
        .write(true)
        .open(&paths.live)
        .unwrap();
    file.seek(SeekFrom::Start(4096 + 200)).unwrap();
    file.write_all(&[1]).unwrap();
    file.sync_all().unwrap();

    let mut findings = Vec::new();
    let result = validate(
        &paths.live,
        ValidationMode::LiveCurrent,
        &ValidationBudget::heap_only(1024, 2),
        &CancellationToken::new(),
        &mut |finding: &iprange_livedb::validation::ValidationFinding| {
            findings.push(*finding);
            Ok(ValidationSinkControl::Continue)
        },
    )
    .unwrap();
    assert!(!result.valid);
    assert!(result.generation.is_none());
    assert!(result.progress.has_unbounded_unknown);
    assert_eq!(findings.len(), 1);
    assert_eq!(findings[0].reason, ValidationReason::MetaInvalid);
}

fn validate_clean(path: &Path) -> iprange_livedb::validation::ValidationResult {
    let mut findings = Vec::new();
    let result = validate(
        path,
        ValidationMode::ImmutableCurrent,
        &ValidationBudget::heap_only(1024 * 1024, 1),
        &CancellationToken::new(),
        &mut |finding: &iprange_livedb::validation::ValidationFinding| {
            findings.push(*finding);
            Ok(ValidationSinkControl::Continue)
        },
    )
    .unwrap();
    assert!(findings.is_empty(), "{findings:?}");
    result
}

fn transaction_budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 2 * 1024 * 1024,
        max_private_pages: 20_000,
        max_file_growth_pages: 20_000,
        max_open_files: 2,
    }
}

fn selected_range_root(path: &Path) -> u32 {
    let mut file = OpenOptions::new().read(true).open(path).unwrap();
    let mut metas = [[0u8; 4096]; 2];
    file.read_exact(&mut metas[0]).unwrap();
    file.read_exact(&mut metas[1]).unwrap();
    let transaction = |page: &[u8; 4096]| u64::from_le_bytes(page[48..56].try_into().unwrap());
    let selected = usize::from(transaction(&metas[1]) > transaction(&metas[0]));
    u32::from_le_bytes(metas[selected][144..148].try_into().unwrap())
}

fn remove_pair(path: &Path) {
    let _ = fs::remove_file(path);
    let mut sidecar = path.as_os_str().to_os_string();
    sidecar.push(".readers");
    let _ = fs::remove_file(sidecar);
}
