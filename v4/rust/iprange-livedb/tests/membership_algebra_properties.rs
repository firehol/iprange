#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

use std::fs;
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, AddressFamily, AddressRange, AlgebraOutputBudget, AlgebraOutputMode,
    AlgebraSetOperation, CancellationToken, FeedName, FeedSelection, FinishedWorkflow,
    ImmutableReader, Ipv4Key, LiveReader, LiveWriter, MembershipAlgebra, MembershipAlgebraBudget,
    MembershipQueryBudget, PublicationPolicy, TransactionBudget, ValueKind, ValueTag,
};

const SOURCES: usize = 5;
const FEEDS: usize = 6;
const DOMAIN: usize = 128;

struct Files(Vec<PathBuf>);

impl Files {
    fn new() -> Self {
        Self(Vec::new())
    }

    fn path(&mut self, label: &str) -> PathBuf {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "iprange-v4-algebra-property-{label}-{}-{unique}",
            std::process::id()
        ));
        self.0.push(path.clone());
        path
    }
}

impl Drop for Files {
    fn drop(&mut self) {
        for path in &self.0 {
            let _ = fs::remove_file(path);
            let mut sidecar = path.file_name().unwrap().to_os_string();
            sidecar.push(".readers");
            let _ = fs::remove_file(path.with_file_name(sidecar));
        }
    }
}

fn transaction_budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 2 * 1024 * 1024,
        max_private_pages: 20_000,
        max_file_growth_pages: 20_000,
        max_open_files: 2,
    }
}

fn output_budget() -> AlgebraOutputBudget {
    AlgebraOutputBudget {
        max_output_pages: 20_000,
        max_open_files: 3,
    }
}

#[test]
fn randomized_global_algebra_matches_a_scalar_address_model() {
    let mut state = 0xa183_f9de_36b4_7021u64;
    let mut source_model = vec![vec![[false; DOMAIN]; FEEDS]; SOURCES];
    for source in &mut source_model {
        for feed in source {
            for present in feed {
                *present = random(&mut state) % 7 < 2;
            }
        }
    }
    let mut global = vec![[false; DOMAIN]; FEEDS];
    for feed in 0..FEEDS {
        for address in 0..DOMAIN {
            global[feed][address] = source_model.iter().any(|source| source[feed][address]);
        }
    }

    let cancellation = CancellationToken::new();
    let mut files = Files::new();
    let source_paths: Vec<_> = (0..SOURCES)
        .map(|index| files.path(&format!("source-{index}")))
        .collect();
    for (source_index, path) in source_paths.iter().enumerate() {
        create_live(
            path,
            AddressFamily::Ipv4,
            ValueKind::Membership,
            ValueTag::new(b"feeds").unwrap(),
            1,
            &cancellation,
        )
        .unwrap();
        let mut writer = LiveWriter::open(path, transaction_budget(), &cancellation).unwrap();
        for (feed, coverage) in source_model[source_index].iter().enumerate() {
            add_feed(&mut writer, &format!("f{feed}"), coverage);
        }
        writer.close().unwrap();
    }

    let mut readers: Vec<_> = source_paths
        .iter()
        .map(|path| LiveReader::open(path, &cancellation).unwrap())
        .collect();
    let scopes: Vec<_> = readers
        .iter()
        .map(|reader| {
            reader
                .membership_query()
                .unwrap()
                .all_feeds(
                    MembershipQueryBudget {
                        max_heap_bytes: 2 * 1024 * 1024,
                    },
                    &cancellation,
                )
                .unwrap()
        })
        .collect();
    let scope_refs: Vec<_> = scopes.iter().collect();
    let algebra = MembershipAlgebra::new(
        &scope_refs,
        MembershipAlgebraBudget {
            max_heap_bytes: 8 * 1024 * 1024,
            max_sources: SOURCES as u32,
        },
        &cancellation,
    )
    .unwrap();

    let union_indexes = [0usize, 2, 4];
    let intersection_indexes = [1usize, 3, 5];
    let exclude_indexes = [0usize, 5];
    let union_names = names(&union_indexes);
    let intersection_names = names(&intersection_indexes);
    let exclude_names = names(&exclude_indexes);
    let expected_union = address_set(&global, &union_indexes, |present, wanted| {
        wanted.iter().any(|&feed| present[feed])
    });
    let expected_right = address_set(&global, &intersection_indexes, |present, wanted| {
        wanted.iter().any(|&feed| present[feed])
    });
    let expected_exclusion: [bool; DOMAIN] = std::array::from_fn(|address| {
        union_indexes.iter().any(|&feed| global[feed][address])
            && !exclude_indexes.iter().any(|&feed| global[feed][address])
    });

    assert_eq!(
        algebra
            .count(FeedSelection::Named(&union_names), &cancellation)
            .unwrap()
            .addresses
            .lo(),
        expected_union.iter().filter(|&&value| value).count() as u64
    );
    let comparison = algebra
        .compare(
            FeedSelection::Named(&union_names),
            FeedSelection::Named(&intersection_names),
            &cancellation,
        )
        .unwrap();
    let expected_overlap = (0..DOMAIN)
        .filter(|&address| expected_union[address] && expected_right[address])
        .count() as u64;
    assert_eq!(comparison.overlap_addresses.lo(), expected_overlap);
    assert_eq!(
        comparison.left_only_addresses.lo(),
        (0..DOMAIN)
            .filter(|&address| expected_union[address] && !expected_right[address])
            .count() as u64
    );
    assert_eq!(
        comparison.right_only_addresses.lo(),
        (0..DOMAIN)
            .filter(|&address| !expected_union[address] && expected_right[address])
            .count() as u64
    );

    let union_path = files.path("union");
    algebra
        .publish_set(
            &union_path,
            ValueTag::new(b"union").unwrap(),
            AlgebraSetOperation::Union(FeedSelection::Named(&union_names)),
            AlgebraOutputMode::PreserveFeeds,
            None,
            PublicationPolicy::FailIfExists,
            output_budget(),
            &cancellation,
        )
        .unwrap();
    assert_preserved(&union_path, &global, &union_indexes, |present, wanted| {
        wanted.iter().any(|&feed| present[feed])
    });

    let intersection_path = files.path("intersection");
    algebra
        .publish_set(
            &intersection_path,
            ValueTag::new(b"intersection").unwrap(),
            AlgebraSetOperation::Intersection(FeedSelection::Named(&intersection_names)),
            AlgebraOutputMode::PreserveFeeds,
            None,
            PublicationPolicy::FailIfExists,
            output_budget(),
            &cancellation,
        )
        .unwrap();
    assert_preserved(
        &intersection_path,
        &global,
        &intersection_indexes,
        |present, wanted| wanted.iter().all(|&feed| present[feed]),
    );

    let exclusion_path = files.path("exclusion");
    algebra
        .publish_set(
            &exclusion_path,
            ValueTag::new(b"exclusion").unwrap(),
            AlgebraSetOperation::Exclusion {
                included: FeedSelection::Named(&union_names),
                excluded: FeedSelection::Named(&exclude_names),
            },
            AlgebraOutputMode::PreserveFeeds,
            None,
            PublicationPolicy::FailIfExists,
            output_budget(),
            &cancellation,
        )
        .unwrap();
    let exclusion_reader = ImmutableReader::open(&exclusion_path).unwrap();
    for address in 0..DOMAIN {
        let expected: Vec<_> = if expected_exclusion[address] {
            union_indexes
                .iter()
                .copied()
                .filter(|&feed| global[feed][address])
                .map(|feed| format!("f{feed}"))
                .collect()
        } else {
            Vec::new()
        };
        assert_eq!(matching(&exclusion_reader, address), expected);
    }

    let flat_path = files.path("flat");
    algebra
        .publish_set(
            &flat_path,
            ValueTag::new(b"flat").unwrap(),
            AlgebraSetOperation::Union(FeedSelection::All),
            AlgebraOutputMode::Flat(FeedName::new("all").unwrap()),
            None,
            PublicationPolicy::FailIfExists,
            output_budget(),
            &cancellation,
        )
        .unwrap();
    let flat_reader = ImmutableReader::open(&flat_path).unwrap();
    for address in 0..DOMAIN {
        let any = global.iter().any(|feed| feed[address]);
        assert_eq!(
            matching(&flat_reader, address),
            if any {
                vec!["all".to_owned()]
            } else {
                Vec::new()
            }
        );
    }

    drop(algebra);
    drop(scopes);
    for reader in &mut readers {
        reader.close().unwrap();
    }
}

fn add_feed(writer: &mut LiveWriter, name: &str, values: &[bool; DOMAIN]) {
    let ranges: Vec<_> = boolean_ranges(values)
        .into_iter()
        .map(|(from, to)| AddressRange {
            from: Ipv4Key(from),
            to: Ipv4Key(to),
        })
        .collect();
    let cancellation = CancellationToken::new();
    let mut workflow = writer
        .begin_create_feed(FeedName::new(name).unwrap(), &cancellation)
        .unwrap();
    workflow.add_ranges_v4_slice(&ranges).unwrap();
    match workflow.finish_input().unwrap() {
        FinishedWorkflow::Changed(prepared) => {
            prepared.commit().unwrap();
        }
        FinishedWorkflow::NoChange(report) => panic!("expected catalog change: {report:?}"),
    }
}

fn assert_preserved(
    path: &Path,
    global: &[[bool; DOMAIN]],
    wanted: &[usize],
    predicate: impl Fn(&[bool], &[usize]) -> bool,
) {
    let reader = ImmutableReader::open(path).unwrap();
    for address in 0..DOMAIN {
        let present: Vec<_> = global.iter().map(|feed| feed[address]).collect();
        let expected: Vec<_> = if predicate(&present, wanted) {
            wanted
                .iter()
                .copied()
                .filter(|&feed| global[feed][address])
                .map(|feed| format!("f{feed}"))
                .collect()
        } else {
            Vec::new()
        };
        assert_eq!(matching(&reader, address), expected);
    }
}

fn matching(reader: &ImmutableReader, address: usize) -> Vec<String> {
    let mut names = Vec::new();
    reader
        .membership_query()
        .unwrap()
        .matching_feeds_v4(
            Ipv4Key(address as u32),
            &mut |name: FeedName| {
                names.push(name.as_str().to_owned());
                Ok(())
            },
            &CancellationToken::new(),
        )
        .unwrap();
    names
}

fn address_set(
    global: &[[bool; DOMAIN]],
    wanted: &[usize],
    predicate: impl Fn(&[bool], &[usize]) -> bool,
) -> [bool; DOMAIN] {
    std::array::from_fn(|address| {
        let present: Vec<_> = global.iter().map(|feed| feed[address]).collect();
        predicate(&present, wanted)
    })
}

fn names(indexes: &[usize]) -> Vec<FeedName> {
    indexes
        .iter()
        .map(|index| FeedName::new(&format!("f{index}")).unwrap())
        .collect()
}

fn boolean_ranges(values: &[bool]) -> Vec<(u32, u32)> {
    let mut output = Vec::new();
    let mut start = None;
    for (index, present) in values
        .iter()
        .copied()
        .chain(std::iter::once(false))
        .enumerate()
    {
        match (start, present) {
            (None, true) => start = Some(index as u32),
            (Some(from), false) => {
                output.push((from, index as u32 - 1));
                start = None;
            }
            _ => {}
        }
    }
    output
}

fn random(state: &mut u64) -> u64 {
    *state ^= *state << 13;
    *state ^= *state >> 7;
    *state ^= *state << 17;
    *state
}
