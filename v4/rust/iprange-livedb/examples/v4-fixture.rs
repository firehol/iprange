//! Development-only deterministic v4 fixture generator.
//!
//! Usage: v4-fixture direct-v4|membership-v4|structured-v4 OUTPUT
//!
//! The semantic ranges, values, feed names, metadata, and SDK workflow order
//! are fixed. SDK identity fields remain randomly generated as required by the
//! v4 format; fixture assertions therefore use public semantics, not file IDs.

use std::path::{Path, PathBuf};

use iprange_livedb::{
    create_immutable_feed_v4, create_live, snapshot_to, AddressFamily, AddressRange,
    CancellationToken, DirectTransaction, FeedName, FinishedWorkflow, ImmutableFeedBudget,
    ImmutableReader, Ipv4Key, LiveWriter, MembershipImportSource, MembershipOperation,
    NetworkEnrichmentV1, NetworkEnrichmentV1Location, PublicationPolicy, SliceSource,
    SnapshotBudget, SnapshotPublicationPolicy, SnapshotSourceMode, StructureKind,
    TransactionBudget, ValueKind, ValueTag,
};

fn main() {
    let mut args = std::env::args().skip(1);
    let Some(kind) = args.next() else {
        fail("usage: v4-fixture KIND OUTPUT");
    };
    let Some(output) = args.next() else {
        fail("usage: v4-fixture KIND OUTPUT");
    };
    if args.next().is_some() {
        fail("usage: v4-fixture KIND OUTPUT");
    }
    let output = PathBuf::from(output);
    let live = output.with_file_name(format!(
        ".{}.live",
        output
            .file_name()
            .and_then(|name| name.to_str())
            .unwrap_or_default()
    ));
    let immutable_source = live.with_file_name(format!(
        ".{}.source",
        output
            .file_name()
            .and_then(|name| name.to_str())
            .unwrap_or_default()
    ));
    remove_temporaries(&live);
    if let Err(error) = run(&kind, &output, &live, &immutable_source) {
        let _ = std::fs::remove_file(&live);
        let _ = std::fs::remove_file(sidecar(&live));
        let _ = std::fs::remove_file(&immutable_source);
        eprintln!("v4-fixture: {kind} failed: {error}");
        std::process::exit(1);
    }
}

fn run(kind: &str, output: &Path, live: &Path, immutable_source: &Path) -> Result<(), String> {
    match kind {
        "direct-v4" => direct_v4(live, output),
        "membership-v4" => membership_v4(live, immutable_source, output),
        "structured-v4" => structured_v4(live, output),
        _ => Err(format!(
            "unknown kind {kind:?}; expected direct-v4, membership-v4, or structured-v4"
        )),
    }
}

fn direct_v4(live: &Path, output: &Path) -> Result<(), String> {
    create(
        live,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        StructureKind::None,
        b"direct",
        "direct-v4",
    )?;
    let cancellation = CancellationToken::new();
    let mut writer = open_writer(live)?;
    let mut transaction = writer
        .begin_direct_transaction(&cancellation)
        .map_err(show)?;
    assign_direct(&mut transaction, 0xc000_020a, 0xc000_0214, 10)?;
    assign_direct(&mut transaction, 0xc000_020f, 0xc000_0219, 15)?;
    assign_direct(&mut transaction, 0xc633_641e, 0xc633_6427, 30)?;
    transaction
        .set_metadata_json(br#"{"fixture":"direct-v4"}"#)
        .map_err(show)?;
    transaction.commit().map_err(show)?;
    snapshot(live, output)
}

fn assign_direct(
    transaction: &mut DirectTransaction<'_>,
    from: u32,
    to: u32,
    value: u32,
) -> Result<(), String> {
    transaction
        .assign_v4(Ipv4Key(from), Ipv4Key(to), value)
        .map(|_| ())
        .map_err(show)
}

fn membership_v4(live: &Path, immutable_source: &Path, output: &Path) -> Result<(), String> {
    // Build one source feed through the public immutable-feed publisher, then
    // import it into the final two-feed fixture through the public SDK import.
    let ranges = [
        AddressRange {
            from: Ipv4Key(0xc000_0200),
            to: Ipv4Key(0xc000_0214),
        },
        AddressRange {
            from: Ipv4Key(0xc633_641e),
            to: Ipv4Key(0xc633_6427),
        },
    ];
    let mut source = SliceSource::new(&ranges);
    create_immutable_feed_v4(
        immutable_source,
        ValueTag::new(b"membership").expect("valid fixed tag"),
        FeedName::new("alpha").expect("valid fixed feed"),
        None,
        PublicationPolicy::FailIfExists,
        &mut source,
        &feed_budget(),
        &CancellationToken::new(),
    )
    .map_err(|failure| format!("{failure:?}"))?;
    let imported = ImmutableReader::open(immutable_source).map_err(show)?;

    create(
        live,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        StructureKind::None,
        b"membership",
        "membership-v4",
    )?;
    let cancellation = CancellationToken::new();
    let mut writer = open_writer(live)?;
    let import = writer
        .begin_membership_import(MembershipImportSource::Immutable(&imported), &cancellation)
        .map_err(show)?;
    match import.finish_input().map_err(show)? {
        FinishedWorkflow::Changed(prepared) => prepared.commit().map(|_| ()).map_err(show)?,
        FinishedWorkflow::NoChange(_) => {
            return Err("membership import unexpectedly made no change".into())
        }
    }
    drop(imported);
    let _ = std::fs::remove_file(immutable_source);

    let mut transaction = writer
        .begin_membership_transaction(&cancellation)
        .map_err(show)?;
    let alpha = transaction
        .lookup_feed(FeedName::new("alpha").expect("valid fixed feed"))
        .map_err(show)?
        .ok_or("imported alpha feed is absent")?;
    let beta = transaction
        .ensure_feed(FeedName::new("beta").expect("valid fixed feed"))
        .map_err(show)?;
    let empty = transaction.empty_membership().map_err(show)?;
    let beta_only = transaction.add_feed(empty, beta).map_err(show)?;
    let alpha_beta = transaction.add_feed(beta_only, alpha).map_err(show)?;
    transaction
        .apply_v4(
            Ipv4Key(0xc000_020a),
            Ipv4Key(0xc000_0214),
            alpha_beta,
            MembershipOperation::Replace,
        )
        .map(|_| ())
        .map_err(show)?;
    transaction
        .apply_v4(
            Ipv4Key(0xc633_640a),
            Ipv4Key(0xc633_6413),
            beta_only,
            MembershipOperation::Replace,
        )
        .map(|_| ())
        .map_err(show)?;
    transaction
        .set_metadata_json(br#"{"fixture":"membership-v4"}"#)
        .map_err(show)?;
    transaction.commit().map_err(show)?;
    writer.close().map(|_| ()).map_err(show)?;
    snapshot(live, output)
}

fn structured_v4(live: &Path, output: &Path) -> Result<(), String> {
    create(
        live,
        AddressFamily::Ipv4,
        ValueKind::Structured,
        StructureKind::NetworkEnrichmentV1,
        b"enrichment",
        "structured-v4",
    )?;
    let cancellation = CancellationToken::new();
    let mut writer = open_writer(live)?;
    let mut transaction = writer
        .begin_structured_transaction(&cancellation)
        .map_err(show)?;
    let threat = transaction
        .ensure_feed(FeedName::new("threat-a").expect("valid fixed feed"))
        .map_err(show)?;
    let empty = transaction.empty_membership().map_err(show)?;
    let membership = transaction.add_feed(empty, threat).map_err(show)?;
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
            Some(membership),
        )
        .map_err(show)?;
    let narrow = transaction
        .intern_network_enrichment_v1(
            NetworkEnrichmentV1 {
                asn: 64513,
                country_id: 4,
                state_id: 5,
                city_id: 6,
                location: None,
            },
            None,
        )
        .map_err(show)?;
    transaction
        .assign_v4(Ipv4Key(0xc000_0200), Ipv4Key(0xc000_0264), broad)
        .map(|_| ())
        .map_err(show)?;
    transaction
        .assign_v4(Ipv4Key(0xc000_0214), Ipv4Key(0xc000_021e), narrow)
        .map(|_| ())
        .map_err(show)?;
    transaction
        .clear_v4(Ipv4Key(0xc000_0228), Ipv4Key(0xc000_0232))
        .map(|_| ())
        .map_err(show)?;
    transaction
        .set_metadata_json(br#"{"fixture":"structured-v4"}"#)
        .map_err(show)?;
    transaction.commit().map_err(show)?;
    writer.close().map(|_| ()).map_err(show)?;
    snapshot(live, output)
}

fn create(
    path: &Path,
    family: AddressFamily,
    kind: ValueKind,
    structure: StructureKind,
    tag: &[u8],
    label: &str,
) -> Result<(), String> {
    create_live(
        path,
        family,
        kind,
        structure,
        ValueTag::new(tag).ok_or("fixed fixture tag is invalid")?,
        8,
        &CancellationToken::new(),
    )
    .map(|_| ())
    .map_err(|error| format!("create {label}: {error}"))
}

fn open_writer(path: &Path) -> Result<LiveWriter, String> {
    LiveWriter::open(path, transaction_budget(), &CancellationToken::new()).map_err(show)
}

fn transaction_budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 16 * 1024 * 1024,
        max_private_pages: 20_000,
        max_file_growth_pages: 20_000,
        max_open_files: 2,
    }
}

fn feed_budget() -> ImmutableFeedBudget {
    ImmutableFeedBudget::new(16 * 1024 * 1024, 20_000, 20_000, 3)
}

fn snapshot(live: &Path, output: &Path) -> Result<(), String> {
    let result = snapshot_to(
        live,
        SnapshotSourceMode::Live,
        output,
        SnapshotPublicationPolicy::FailIfExists,
        &SnapshotBudget::new(32 * 1024 * 1024, 20_000, 3),
        &CancellationToken::new(),
    )
    .map_err(|failure| format!("{failure:?}"))?;
    let _ = result;
    remove_temporaries(live);
    Ok(())
}

fn remove_temporaries(live: &Path) {
    let _ = std::fs::remove_file(live);
    let _ = std::fs::remove_file(sidecar(live));
}

fn sidecar(path: &Path) -> PathBuf {
    let mut name = path.file_name().unwrap_or_default().to_os_string();
    name.push(".readers");
    path.with_file_name(name)
}

fn show(error: iprange_livedb::Error) -> String {
    error.to_string()
}

fn fail(message: &str) -> ! {
    eprintln!("v4-fixture: {message}");
    std::process::exit(2);
}
