use std::hint::black_box;

use iprange_livedb::{
    create_live, AddressFamily, CancellationToken, FeedName, ImmutableReader, Ipv4Key, LiveReader,
    LiveWriter, MembershipOperation, NetworkEnrichmentV1, NetworkEnrichmentV1Location,
    RangeDirection, StructureKind, ValueKind, ValueTag,
};

use crate::measure::{self, FileSize};
use crate::model::{transaction_budget, TestDatabase};
use crate::scenarios::direct::seeded_direct;
use crate::scenarios::{
    close_reader, close_writer, immutable_snapshot, random_points, reader_work, require_committed,
    require_count, result, validate_output, ScenarioResult,
};

const PROFILE_LIMIT: usize = 65_536;
const LOOKUP_WORK_UNITS: usize = 10_000_000;

pub(super) fn build_random(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let database = create_structured("structured-build-random")?;
    let points = random_points(size)?;
    let (operation, measured) =
        measure::operation(|| populate_structured(&database, size, feeds, Some(points.as_slice())));
    operation?;
    result(
        "structured-build-random",
        size,
        feeds,
        size as u64,
        &database,
        measured,
        database.main(),
    )
}

pub(super) fn intern_profiles(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let database = create_structured("structured-intern")?;
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(
        database.main(),
        transaction_budget(size, feeds),
        &cancellation,
    )
    .map_err(display)?;
    let mut transaction = writer
        .begin_structured_transaction(&cancellation)
        .map_err(display)?;
    let threat = prepare_threat_membership(&mut transaction, feeds)?;
    let (operation, measured) = measure::operation(|| -> Result<u64, String> {
        let mut count = 0u64;
        for index in 0..size {
            let reference = transaction
                .intern_network_enrichment_v1(profile(index), (index % 2 == 0).then_some(threat))
                .map_err(display)?;
            black_box(reference);
            count += 1;
        }
        Ok(count)
    });
    require_count("structure interning", operation?, size as u64, "profiles")?;
    transaction.abort().map_err(display)?;
    close_writer(&mut writer)?;
    result(
        "structured-intern",
        size,
        feeds,
        size as u64,
        &database,
        measured,
        database.main(),
    )
}

pub(super) fn assign_random(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let database = create_structured("structured-assign-random")?;
    let points = random_points(size)?;
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(
        database.main(),
        transaction_budget(size, feeds),
        &cancellation,
    )
    .map_err(display)?;
    let mut transaction = writer
        .begin_structured_transaction(&cancellation)
        .map_err(display)?;
    let profiles = prepare_profiles(&mut transaction, size, feeds)?;
    let (operation, measured) = measure::operation(|| -> Result<u64, String> {
        let mut count = 0u64;
        for &point in &points {
            let index = point.0 as usize / 4;
            assign(&mut transaction, index, profiles[index % profiles.len()])?;
            count += 1;
        }
        Ok(count)
    });
    require_count(
        "structured range assignment",
        operation?,
        size as u64,
        "ranges",
    )?;
    require_committed(transaction.commit().map_err(display)?)?;
    close_writer(&mut writer)?;
    result(
        "structured-assign-random",
        size,
        feeds,
        size as u64,
        &database,
        measured,
        database.main(),
    )
}

pub(super) fn commit(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let database = create_structured("structured-commit")?;
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(
        database.main(),
        transaction_budget(size, feeds),
        &cancellation,
    )
    .map_err(display)?;
    let mut transaction = writer
        .begin_structured_transaction(&cancellation)
        .map_err(display)?;
    let profiles = prepare_profiles(&mut transaction, size, feeds)?;
    for index in 0..size {
        assign(&mut transaction, index, profiles[index % profiles.len()])?;
    }
    let (operation, measured) = measure::operation(|| transaction.commit());
    require_committed(operation.map_err(display)?)?;
    close_writer(&mut writer)?;
    result(
        "structured-commit",
        size,
        feeds,
        size as u64,
        &database,
        measured,
        database.main(),
    )
}

pub(super) fn live_scalar_random_lookup(
    size: usize,
    feeds: usize,
) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let database = populated_structured("live-structured-scalar-random-lookup", size, feeds)?;
    let points = random_points(size)?;
    let mut reader =
        LiveReader::open(database.main(), &CancellationToken::new()).map_err(display)?;
    let (repetitions, work_units) = structured_reader_work(size)?;
    let (operation, measured) = measure::operation(|| {
        count_scalar_points(&points, repetitions, |address| {
            reader.lookup_network_enrichment_v1_v4(address)
        })
    });
    require_count(
        "live structured scalar lookup",
        operation?,
        work_units,
        "addresses",
    )?;
    close_reader(&mut reader)?;
    result(
        "live-structured-scalar-random-lookup",
        size,
        feeds,
        work_units,
        &database,
        measured,
        database.main(),
    )
}

pub(super) fn immutable_scalar_random_lookup(
    size: usize,
    feeds: usize,
) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let database = populated_structured("immutable-structured-scalar-random-lookup", size, feeds)?;
    let snapshot = immutable_snapshot(&database, size)?;
    let points = random_points(size)?;
    let reader = ImmutableReader::open(&snapshot).map_err(display)?;
    let (repetitions, work_units) = structured_reader_work(size)?;
    let (operation, measured) = measure::operation(|| {
        count_scalar_points(&points, repetitions, |address| {
            reader.lookup_network_enrichment_v1_v4(address)
        })
    });
    require_count(
        "immutable structured scalar lookup",
        operation?,
        work_units,
        "addresses",
    )?;
    drop(reader);
    result(
        "immutable-structured-scalar-random-lookup",
        size,
        feeds,
        work_units,
        &database,
        measured,
        &snapshot,
    )
}

pub(super) fn live_threat_random_lookup(
    size: usize,
    feeds: usize,
) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let database = populated_structured("live-structured-threat-random-lookup", size, feeds)?;
    let points = random_points(size)?;
    let mut reader =
        LiveReader::open(database.main(), &CancellationToken::new()).map_err(display)?;
    let target = target_feed_index(|name| reader.lookup_feed(name), feeds)?;
    let (repetitions, work_units) = structured_reader_work(size)?;
    let expected = expected_threat_hits(size, repetitions)?;
    let (operation, measured) = measure::operation(|| {
        count_threat_points(&points, repetitions, target, |address| {
            reader.lookup_network_enrichment_v1_v4(address)
        })
    });
    require_count(
        "live structured threat lookup",
        operation?,
        expected,
        "matches",
    )?;
    close_reader(&mut reader)?;
    result(
        "live-structured-threat-random-lookup",
        size,
        feeds,
        work_units,
        &database,
        measured,
        database.main(),
    )
}

pub(super) fn immutable_threat_random_lookup(
    size: usize,
    feeds: usize,
) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let database = populated_structured("immutable-structured-threat-random-lookup", size, feeds)?;
    let snapshot = immutable_snapshot(&database, size)?;
    let points = random_points(size)?;
    let reader = ImmutableReader::open(&snapshot).map_err(display)?;
    let target = target_feed_index(|name| reader.lookup_feed(name), feeds)?;
    let (repetitions, work_units) = structured_reader_work(size)?;
    let expected = expected_threat_hits(size, repetitions)?;
    let (operation, measured) = measure::operation(|| {
        count_threat_points(&points, repetitions, target, |address| {
            reader.lookup_network_enrichment_v1_v4(address)
        })
    });
    require_count(
        "immutable structured threat lookup",
        operation?,
        expected,
        "matches",
    )?;
    drop(reader);
    result(
        "immutable-structured-threat-random-lookup",
        size,
        feeds,
        work_units,
        &database,
        measured,
        &snapshot,
    )
}

pub(super) fn live_scalar_scan(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let database = populated_structured("live-structured-scalar-scan", size, feeds)?;
    let mut reader =
        LiveReader::open(database.main(), &CancellationToken::new()).map_err(display)?;
    let (repetitions, work_units) = reader_work(size)?;
    let (operation, measured) = measure::operation(|| -> Result<u64, String> {
        let mut records = 0u64;
        let mut checksum = 0u64;
        for _ in 0..repetitions {
            let mut cursor = reader
                .network_enrichment_v1_cursor_v4(RangeDirection::Forward)
                .map_err(display)?;
            while let Some(range) = cursor.next_range().map_err(display)? {
                checksum = checksum.wrapping_add(u64::from(range.value.value().asn));
                records += 1;
            }
        }
        black_box(checksum);
        Ok(records)
    });
    require_count(
        "live structured scalar scan",
        operation?,
        work_units,
        "ranges",
    )?;
    close_reader(&mut reader)?;
    result(
        "live-structured-scalar-scan",
        size,
        feeds,
        work_units,
        &database,
        measured,
        database.main(),
    )
}

pub(super) fn immutable_scalar_scan(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let database = populated_structured("immutable-structured-scalar-scan", size, feeds)?;
    let snapshot = immutable_snapshot(&database, size)?;
    let reader = ImmutableReader::open(&snapshot).map_err(display)?;
    let (repetitions, work_units) = reader_work(size)?;
    let (operation, measured) = measure::operation(|| -> Result<u64, String> {
        let mut records = 0u64;
        let mut checksum = 0u64;
        for _ in 0..repetitions {
            let mut cursor = reader
                .network_enrichment_v1_cursor_v4(RangeDirection::Forward)
                .map_err(display)?;
            while let Some(range) = cursor.next_range().map_err(display)? {
                checksum = checksum.wrapping_add(u64::from(range.value.value().asn));
                records += 1;
            }
        }
        black_box(checksum);
        Ok(records)
    });
    require_count(
        "immutable structured scalar scan",
        operation?,
        work_units,
        "ranges",
    )?;
    drop(reader);
    result(
        "immutable-structured-scalar-scan",
        size,
        feeds,
        work_units,
        &database,
        measured,
        &snapshot,
    )
}

pub(super) fn immutable_separate_random_lookup(
    size: usize,
    feeds: usize,
) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let asn = seeded_direct("separate-enrichment-asn", size, 1)?;
    let geo = seeded_direct("separate-enrichment-geo", size, 1)?;
    let threat = populated_threat("separate-enrichment-threat", size, feeds)?;
    let asn_snapshot = immutable_snapshot(&asn, size)?;
    let geo_snapshot = immutable_snapshot(&geo, size)?;
    let threat_snapshot = immutable_snapshot(&threat, size)?;
    let points = random_points(size)?;
    let asn_reader = ImmutableReader::open(&asn_snapshot).map_err(display)?;
    let geo_reader = ImmutableReader::open(&geo_snapshot).map_err(display)?;
    let threat_reader = ImmutableReader::open(&threat_snapshot).map_err(display)?;
    let target = target_feed_index(|name| threat_reader.lookup_feed(name), feeds)?;
    let (repetitions, work_units) = structured_reader_work(size)?;

    let (operation, measured) = measure::operation(|| -> Result<u64, String> {
        let mut checksum = 0u64;
        for _ in 0..repetitions {
            for &address in &points {
                let asn = asn_reader
                    .lookup_direct_v4(address)
                    .map_err(display)?
                    .ok_or_else(|| "separate ASN lookup missed an address".to_owned())?;
                let geo = geo_reader
                    .lookup_direct_v4(address)
                    .map_err(display)?
                    .ok_or_else(|| "separate Geo lookup missed an address".to_owned())?;
                let matched = threat_reader
                    .lookup_membership_v4(address)
                    .map_err(display)?
                    .map(|membership| membership.contains_index(target))
                    .transpose()
                    .map_err(display)?
                    .unwrap_or(false);
                checksum = checksum
                    .wrapping_add(u64::from(asn))
                    .wrapping_add(u64::from(geo).rotate_left(17))
                    .wrapping_add(u64::from(matched));
            }
        }
        Ok(black_box(checksum))
    });
    if operation? == 0 {
        return Err("separate enrichment lookup produced an empty checksum".to_owned());
    }
    drop((asn_reader, geo_reader, threat_reader));

    for snapshot in [&asn_snapshot, &geo_snapshot, &threat_snapshot] {
        validate_output(snapshot, false)?;
    }
    let file = aggregate_file_size([&asn_snapshot, &geo_snapshot, &threat_snapshot])?;
    let asn_artifacts = asn.private_artifacts()?;
    let geo_artifacts = geo.private_artifacts()?;
    let threat_artifacts = threat.private_artifacts()?;
    let private_artifacts = asn_artifacts
        .checked_add(geo_artifacts)
        .and_then(|count| count.checked_add(threat_artifacts))
        .ok_or_else(|| "private artifact count overflow".to_owned())?;
    if private_artifacts != 0 {
        return Err(format!(
            "separate enrichment lookup left {private_artifacts} private artifacts"
        ));
    }
    Ok(ScenarioResult {
        name: "immutable-separate-enrichment-random-lookup",
        size,
        auxiliary: feeds,
        work_units,
        emitted_units: 0,
        range_records: size as u64 * 2 + size.div_ceil(2) as u64,
        feeds: feeds as u64,
        measurement: measured,
        file,
        private_artifacts,
    })
}

fn populated_structured(label: &str, size: usize, feeds: usize) -> Result<TestDatabase, String> {
    let database = create_structured(label)?;
    populate_structured(&database, size, feeds, None)?;
    Ok(database)
}

fn create_structured(label: &str) -> Result<TestDatabase, String> {
    let database = TestDatabase::new(label)?;
    create_live(
        database.main(),
        AddressFamily::Ipv4,
        ValueKind::Structured,
        StructureKind::NetworkEnrichmentV1,
        ValueTag::new(b"enrichment").ok_or_else(|| "invalid enrichment tag".to_owned())?,
        1,
        &CancellationToken::new(),
    )
    .map_err(display)?;
    Ok(database)
}

fn populate_structured(
    database: &TestDatabase,
    size: usize,
    feeds: usize,
    random_order: Option<&[Ipv4Key]>,
) -> Result<(), String> {
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(
        database.main(),
        transaction_budget(size, feeds),
        &cancellation,
    )
    .map_err(display)?;
    let mut transaction = writer
        .begin_structured_transaction(&cancellation)
        .map_err(display)?;
    let profiles = prepare_profiles(&mut transaction, size, feeds)?;

    if let Some(points) = random_order {
        for &point in points {
            let index = point.0 as usize / 4;
            assign(&mut transaction, index, profiles[index % profiles.len()])?;
        }
    } else {
        for index in 0..size {
            assign(&mut transaction, index, profiles[index % profiles.len()])?;
        }
    }
    require_committed(transaction.commit().map_err(display)?)?;
    close_writer(&mut writer)
}

fn populated_threat(label: &str, size: usize, feeds: usize) -> Result<TestDatabase, String> {
    let database = TestDatabase::new(label)?;
    create_live(
        database.main(),
        AddressFamily::Ipv4,
        ValueKind::Membership,
        StructureKind::None,
        ValueTag::new(b"threat").ok_or_else(|| "invalid threat tag".to_owned())?,
        1,
        &CancellationToken::new(),
    )
    .map_err(display)?;
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(
        database.main(),
        transaction_budget(size, feeds),
        &cancellation,
    )
    .map_err(display)?;
    let mut transaction = writer
        .begin_membership_transaction(&cancellation)
        .map_err(display)?;
    let mut target = None;
    for index in 0..feeds {
        let feed = transaction
            .ensure_feed(feed_name(index)?)
            .map_err(display)?;
        if index + 1 == feeds {
            target = Some(feed);
        }
    }
    let empty = transaction.empty_membership().map_err(display)?;
    let threat = transaction
        .add_feed(
            empty,
            target.ok_or_else(|| "threat benchmark has no target feed".to_owned())?,
        )
        .map_err(display)?;
    for index in (0..size).step_by(2) {
        let start = range_start(index)?;
        transaction
            .apply_v4(
                Ipv4Key(start),
                Ipv4Key(start + 1),
                threat,
                MembershipOperation::Replace,
            )
            .map_err(display)?;
    }
    require_committed(transaction.commit().map_err(display)?)?;
    close_writer(&mut writer)?;
    Ok(database)
}

fn prepare_profiles(
    transaction: &mut iprange_livedb::StructuredTransaction<'_>,
    size: usize,
    feeds: usize,
) -> Result<Vec<iprange_livedb::StructureRef>, String> {
    let threat = prepare_threat_membership(transaction, feeds)?;
    let profile_count = size.min(PROFILE_LIMIT);
    let mut profiles = Vec::new();
    profiles
        .try_reserve_exact(profile_count)
        .map_err(|_| "structured profile-reference allocation failed".to_owned())?;
    for index in 0..profile_count {
        profiles.push(
            transaction
                .intern_network_enrichment_v1(profile(index), (index % 2 == 0).then_some(threat))
                .map_err(display)?,
        );
    }
    Ok(profiles)
}

fn prepare_threat_membership(
    transaction: &mut iprange_livedb::StructuredTransaction<'_>,
    feeds: usize,
) -> Result<iprange_livedb::MembershipRef, String> {
    let mut target = None;
    for index in 0..feeds {
        let feed = transaction
            .ensure_feed(feed_name(index)?)
            .map_err(display)?;
        if index + 1 == feeds {
            target = Some(feed);
        }
    }
    let empty = transaction.empty_membership().map_err(display)?;
    transaction
        .add_feed(
            empty,
            target.ok_or_else(|| "structured benchmark has no target feed".to_owned())?,
        )
        .map_err(display)
}

fn assign(
    transaction: &mut iprange_livedb::StructuredTransaction<'_>,
    index: usize,
    profile: iprange_livedb::StructureRef,
) -> Result<(), String> {
    let start = range_start(index)?;
    transaction
        .assign_v4(Ipv4Key(start), Ipv4Key(start + 1), profile)
        .map_err(display)?;
    Ok(())
}

fn profile(index: usize) -> NetworkEnrichmentV1 {
    NetworkEnrichmentV1 {
        asn: index as u32 + 1,
        country_id: index as u32 % 251 + 1,
        state_id: index as u32 % 4093 + 1,
        city_id: index as u32 + 1,
        location: Some(NetworkEnrichmentV1Location {
            latitude_microdegrees: index as i32 % 180_000_001 - 90_000_000,
            longitude_microdegrees: (index as i32 * 17) % 360_000_001 - 180_000_000,
        }),
    }
}

fn count_scalar_points<'a>(
    points: &[Ipv4Key],
    repetitions: usize,
    mut lookup: impl FnMut(
        Ipv4Key,
    ) -> iprange_livedb::Result<
        Option<iprange_livedb::NetworkEnrichmentV1View<'a>>,
    >,
) -> Result<u64, String> {
    let mut hits = 0u64;
    for _ in 0..repetitions {
        for &address in points {
            let view = lookup(address)
                .map_err(display)?
                .ok_or_else(|| "structured scalar lookup missed an address".to_owned())?;
            hits += u64::from(black_box(view.value().asn) != 0);
        }
    }
    Ok(black_box(hits))
}

fn count_threat_points<'a>(
    points: &[Ipv4Key],
    repetitions: usize,
    target: u32,
    mut lookup: impl FnMut(
        Ipv4Key,
    ) -> iprange_livedb::Result<
        Option<iprange_livedb::NetworkEnrichmentV1View<'a>>,
    >,
) -> Result<u64, String> {
    let mut hits = 0u64;
    for _ in 0..repetitions {
        for &address in points {
            let view = lookup(address)
                .map_err(display)?
                .ok_or_else(|| "structured threat lookup missed an address".to_owned())?;
            let matched = view
                .threat_membership()
                .map_err(display)?
                .map(|membership| membership.contains_index(target))
                .transpose()
                .map_err(display)?
                .unwrap_or(false);
            hits += u64::from(matched);
        }
    }
    Ok(black_box(hits))
}

fn expected_threat_hits(size: usize, repetitions: usize) -> Result<u64, String> {
    u64::try_from(size.div_ceil(2))
        .ok()
        .and_then(|hits| hits.checked_mul(repetitions as u64))
        .ok_or_else(|| "structured threat match count overflow".to_owned())
}

fn structured_reader_work(size: usize) -> Result<(usize, u64), String> {
    let (minimum_repetitions, _) = reader_work(size)?;
    let repetitions = minimum_repetitions.max(LOOKUP_WORK_UNITS.div_ceil(size));
    let work_units = size
        .checked_mul(repetitions)
        .and_then(|units| u64::try_from(units).ok())
        .ok_or_else(|| "structured reader work count overflow".to_owned())?;
    Ok((repetitions, work_units))
}

fn target_feed_index(
    lookup: impl FnOnce(&str) -> iprange_livedb::Result<Option<iprange_livedb::FeedEntry>>,
    feeds: usize,
) -> Result<u32, String> {
    lookup(feed_name(feeds - 1)?.as_str())
        .map_err(display)?
        .map(|feed| feed.index)
        .ok_or_else(|| "target threat feed is absent".to_owned())
}

fn feed_name(index: usize) -> Result<FeedName, String> {
    FeedName::new(&format!("feed-{index:06}")).map_err(display)
}

fn range_start(index: usize) -> Result<u32, String> {
    u32::try_from(index)
        .ok()
        .and_then(|index| index.checked_mul(4))
        .ok_or_else(|| "structured benchmark exceeds the IPv4 workload space".to_owned())
}

fn aggregate_file_size<const N: usize>(paths: [&std::path::Path; N]) -> Result<FileSize, String> {
    let mut result = FileSize {
        logical: 0,
        physical: Some(0),
    };
    for path in paths {
        let current = measure::file_size(path).map_err(|error| error.to_string())?;
        result.logical = result
            .logical
            .checked_add(current.logical)
            .ok_or_else(|| "aggregate logical file size overflow".to_owned())?;
        result.physical = match (result.physical, current.physical) {
            (Some(left), Some(right)) => left.checked_add(right),
            _ => None,
        };
    }
    Ok(result)
}

fn display(error: impl std::fmt::Display) -> String {
    error.to_string()
}
