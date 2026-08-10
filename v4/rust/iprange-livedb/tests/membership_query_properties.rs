#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, AddressFamily, AddressRange, CancellationToken, FeedCardinality, FeedName,
    FeedOverlap, FinishedWorkflow, Ipv4Key, LiveReader, LiveWriter, MembershipAggregateSink,
    MembershipAggregationMode, MembershipQueryBudget, TransactionBudget, ValueKind, ValueTag,
};

struct File(PathBuf);

impl File {
    fn new(round: usize) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        Self(std::env::temp_dir().join(format!(
            "iprange-v4-query-property-{round}-{}-{unique}",
            std::process::id()
        )))
    }
}

impl Drop for File {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.0);
        let mut sidecar = self.0.file_name().unwrap().to_os_string();
        sidecar.push(".readers");
        let _ = fs::remove_file(self.0.with_file_name(sidecar));
    }
}

#[derive(Default)]
struct Output {
    feeds: Vec<FeedCardinality>,
    pairs: Vec<FeedOverlap>,
}

impl MembershipAggregateSink for Output {
    fn feed_cardinalities(&mut self, batch: &[FeedCardinality]) -> iprange_livedb::Result<()> {
        self.feeds.extend_from_slice(batch);
        Ok(())
    }

    fn feed_overlaps(&mut self, batch: &[FeedOverlap]) -> iprange_livedb::Result<()> {
        self.pairs.extend_from_slice(batch);
        Ok(())
    }
}

#[test]
fn randomized_point_and_pair_queries_match_a_scalar_model() {
    const ROUNDS: usize = 24;
    const ADDRESSES: usize = 96;
    const FEEDS: usize = 7;

    let cancellation = CancellationToken::new();
    let mut random_state = 0x51d2_09ba_a36e_c47fu64;
    for round in 0..ROUNDS {
        let mut model = [[false; ADDRESSES]; FEEDS];
        for feed in &mut model {
            for value in feed {
                *value = random(&mut random_state) % 7 < 2;
            }
        }
        let file = File::new(round);
        create_live(
            &file.0,
            AddressFamily::Ipv4,
            ValueKind::Membership,
            ValueTag::new(b"feeds").unwrap(),
            1,
            &cancellation,
        )
        .unwrap();
        let mut writer = LiveWriter::open(&file.0, transaction_budget(), &cancellation).unwrap();
        for (index, values) in model.iter().enumerate() {
            let mut operation = writer
                .begin_create_feed(FeedName::new(&format!("f{index}")).unwrap(), &cancellation)
                .unwrap();
            operation
                .add_ranges_v4_slice(&boolean_ranges(values))
                .unwrap();
            match operation.finish_input().unwrap() {
                FinishedWorkflow::Changed(prepared) => {
                    prepared.commit().unwrap();
                }
                FinishedWorkflow::NoChange(_) => panic!("new feed did not change catalog"),
            }
        }
        writer.close().unwrap();

        let mut reader = LiveReader::open(&file.0, &cancellation).unwrap();
        let query = reader.membership_query().unwrap();
        for address in 0..ADDRESSES {
            let mut actual = Vec::new();
            query
                .matching_feeds_v4(
                    Ipv4Key(address as u32),
                    &mut |name: FeedName| {
                        actual.push(name.as_str().to_owned());
                        Ok(())
                    },
                    &cancellation,
                )
                .unwrap();
            let expected = model
                .iter()
                .enumerate()
                .filter(|&(_, feed)| feed[address])
                .map(|(index, _)| format!("f{index}"))
                .collect::<Vec<_>>();
            assert_eq!(actual, expected, "round {round}, address {address}");
        }

        let scope = query
            .all_feeds(
                MembershipQueryBudget {
                    max_heap_bytes: 2 * 1024 * 1024,
                },
                &cancellation,
            )
            .unwrap();
        let mut output = Output::default();
        scope
            .aggregate(
                MembershipAggregationMode::AllPairs,
                &mut output,
                &cancellation,
            )
            .unwrap();
        for (index, feed) in model.iter().enumerate() {
            let expected = feed.iter().filter(|&&value| value).count() as u64;
            let actual = output
                .feeds
                .iter()
                .find(|cell| cell.feed.as_str() == format!("f{index}"))
                .unwrap()
                .addresses
                .lo();
            assert_eq!(actual, expected, "round {round}, feed {index}");
        }
        for left in 0..FEEDS {
            for right in left + 1..FEEDS {
                let expected = model[left]
                    .iter()
                    .zip(model[right])
                    .filter(|&(left, right)| *left && right)
                    .count() as u64;
                let actual = output
                    .pairs
                    .iter()
                    .find(|cell| {
                        cell.left.as_str() == format!("f{left}")
                            && cell.right.as_str() == format!("f{right}")
                    })
                    .unwrap()
                    .addresses
                    .lo();
                assert_eq!(actual, expected, "round {round}, pair {left}/{right}");
            }
        }
        drop(scope);
        reader.close().unwrap();
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

fn boolean_ranges(values: &[bool]) -> Vec<AddressRange<Ipv4Key>> {
    let mut ranges = Vec::new();
    let mut start = None;
    for (index, &present) in values.iter().chain(std::iter::once(&false)).enumerate() {
        match (start, present) {
            (None, true) => start = Some(index as u32),
            (Some(from), false) => {
                ranges.push(AddressRange {
                    from: Ipv4Key(from),
                    to: Ipv4Key(index as u32 - 1),
                });
                start = None;
            }
            _ => {}
        }
    }
    ranges
}

fn random(state: &mut u64) -> u64 {
    *state ^= *state << 13;
    *state ^= *state >> 7;
    *state ^= *state << 17;
    *state
}
