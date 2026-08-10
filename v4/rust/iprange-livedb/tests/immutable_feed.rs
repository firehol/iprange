#![cfg(any(unix, windows))]

use std::fs;
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_immutable_feed_v4, create_immutable_feed_v6, AddressRange, CancellationToken,
    Cardinality129, Error, ErrorCode, FeedName, ImmutableFeedBudget, ImmutableReader, Ipv4Key,
    Ipv6Key, PublicationPolicy, PublicationStatus, RangeDirection, RangeSource, SliceSource,
    ValueKind, ValueTag,
};

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
            "iprange-v4-immutable-feed-{label}-{}-{unique}",
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
        }
    }
}

fn budget() -> ImmutableFeedBudget {
    ImmutableFeedBudget::new(2 * 1024 * 1024, 10_000, 10_000, 3)
}

fn feed() -> FeedName {
    FeedName::new("source").unwrap()
}

#[test]
fn unordered_ipv4_input_is_normalized_in_one_published_file() {
    let mut files = Files::new();
    let path = files.path("v4");
    let ranges = [
        AddressRange {
            from: Ipv4Key(30),
            to: Ipv4Key(30),
        },
        AddressRange {
            from: Ipv4Key(10),
            to: Ipv4Key(20),
        },
        AddressRange {
            from: Ipv4Key(1),
            to: Ipv4Key(3),
        },
        AddressRange {
            from: Ipv4Key(2),
            to: Ipv4Key(12),
        },
    ];
    let metadata = br#"{"source":"test"}"#;
    let result = create_immutable_feed_v4(
        &path,
        ValueTag::new(b"feeds").unwrap(),
        feed(),
        Some(metadata),
        PublicationPolicy::FailIfExists,
        &mut SliceSource::new(&ranges),
        &budget(),
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(result.publication.publication, PublicationStatus::Published);
    assert_eq!(result.report.input_record_count, 4);
    assert_eq!(result.report.normalized_interval_count, 2);
    assert_eq!(result.report.addresses, Cardinality129::from_u64(21));

    let reader = ImmutableReader::open(&path).unwrap();
    let info = reader.info();
    assert_eq!(info.value_kind, ValueKind::Membership);
    assert_eq!(info.transaction_id, 1);
    assert_ne!(info.database_id, [0; 16]);
    assert_ne!(info.commit_nonce, [0; 16]);
    assert_eq!(info.active_feed_count, 1);
    assert_eq!(reader.lookup_feed("source").unwrap().unwrap().index, 0);
    assert_eq!(reader.metadata_json().unwrap().unwrap(), metadata);
    let mut cursor = reader
        .feed_range_cursor_v4("source", RangeDirection::Forward)
        .unwrap();
    assert_eq!(
        cursor.next_range().unwrap(),
        Some(AddressRange {
            from: Ipv4Key(1),
            to: Ipv4Key(20),
        })
    );
    assert_eq!(
        cursor.next_range().unwrap(),
        Some(AddressRange {
            from: Ipv4Key(30),
            to: Ipv4Key(30),
        })
    );
    assert_eq!(cursor.next_range().unwrap(), None);
    assert!(!sidecar(&path).exists());
}

#[test]
fn empty_feed_and_full_ipv6_space_are_exact() {
    let mut files = Files::new();
    let empty_path = files.path("empty");
    let full_path = files.path("full");
    let empty: [AddressRange<Ipv4Key>; 0] = [];
    let result = create_immutable_feed_v4(
        &empty_path,
        ValueTag::new(b"feeds").unwrap(),
        feed(),
        None,
        PublicationPolicy::FailIfExists,
        &mut SliceSource::new(&empty),
        &budget(),
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(result.report.normalized_interval_count, 0);
    assert_eq!(result.report.addresses, Cardinality129::ZERO);
    let reader = ImmutableReader::open(&empty_path).unwrap();
    assert!(reader.lookup_feed("source").unwrap().is_some());
    assert_eq!(reader.info().range_record_count, 0);

    let full = [AddressRange {
        from: Ipv6Key::MIN,
        to: Ipv6Key::MAX,
    }];
    let result = create_immutable_feed_v6(
        &full_path,
        ValueTag::new(b"feeds").unwrap(),
        feed(),
        None,
        PublicationPolicy::FailIfExists,
        &mut SliceSource::new(&full),
        &budget(),
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(result.report.addresses, Cardinality129::FULL_IPV6_SPACE);
    let reader = ImmutableReader::open(&full_path).unwrap();
    let mut cursor = reader
        .feed_range_cursor_v6("source", RangeDirection::Forward)
        .unwrap();
    assert_eq!(cursor.next_range().unwrap(), Some(full[0]));
    assert_eq!(cursor.next_range().unwrap(), None);
}

#[test]
fn source_and_workspace_failures_publish_nothing_and_clean_the_attempt() {
    let mut files = Files::new();
    let source_failure = files.path("source-failure");
    let workspace_failure = files.path("workspace-failure");
    let mut failing = FailingSource::new();
    let failure = create_immutable_feed_v4(
        &source_failure,
        ValueTag::new(b"feeds").unwrap(),
        feed(),
        None,
        PublicationPolicy::FailIfExists,
        &mut failing,
        &budget(),
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert_eq!(failure.cause.code, ErrorCode::InvalidArgument);
    assert_eq!(
        failure.cleanup_state(),
        iprange_livedb::publication::CleanupState::Clean
    );
    assert!(!source_failure.exists());

    let ranges = [AddressRange {
        from: Ipv4Key(1),
        to: Ipv4Key(1),
    }];
    let tiny = ImmutableFeedBudget::new(0, 20, 0, 3);
    let failure = create_immutable_feed_v4(
        &workspace_failure,
        ValueTag::new(b"feeds").unwrap(),
        feed(),
        None,
        PublicationPolicy::FailIfExists,
        &mut SliceSource::new(&ranges),
        &tiny,
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert_eq!(failure.cause.code, ErrorCode::InsufficientResourceBudget);
    assert_eq!(
        failure.cleanup_state(),
        iprange_livedb::publication::CleanupState::Clean
    );
    assert!(!workspace_failure.exists());
}

#[test]
fn existing_destination_is_rejected_before_the_source_is_drained() {
    let mut files = Files::new();
    let path = files.path("exists");
    let empty: [AddressRange<Ipv4Key>; 0] = [];
    create_immutable_feed_v4(
        &path,
        ValueTag::new(b"feeds").unwrap(),
        feed(),
        None,
        PublicationPolicy::FailIfExists,
        &mut SliceSource::new(&empty),
        &budget(),
        &CancellationToken::new(),
    )
    .unwrap();
    let mut source = CountingSource { calls: 0 };
    let failure = create_immutable_feed_v4(
        &path,
        ValueTag::new(b"feeds").unwrap(),
        feed(),
        None,
        PublicationPolicy::FailIfExists,
        &mut source,
        &budget(),
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert_eq!(failure.cause.code, ErrorCode::NameExists);
    assert_eq!(source.calls, 0);
    ImmutableReader::open(&path).unwrap();
}

fn sidecar(path: &Path) -> PathBuf {
    let mut name = path.file_name().unwrap().to_os_string();
    name.push(".readers");
    path.with_file_name(name)
}

struct FailingSource {
    batch: [AddressRange<Ipv4Key>; 1],
    state: u8,
}

impl FailingSource {
    fn new() -> Self {
        Self {
            batch: [AddressRange {
                from: Ipv4Key(1),
                to: Ipv4Key(1),
            }],
            state: 0,
        }
    }
}

impl RangeSource<AddressRange<Ipv4Key>> for FailingSource {
    fn next_batch(&mut self) -> Result<Option<&[AddressRange<Ipv4Key>]>, Error> {
        self.state += 1;
        match self.state {
            1 => Ok(Some(&self.batch)),
            _ => Err(Error::InvalidArgument("injected source failure")),
        }
    }
}

struct CountingSource {
    calls: u64,
}

impl RangeSource<AddressRange<Ipv4Key>> for CountingSource {
    fn next_batch(&mut self) -> Result<Option<&[AddressRange<Ipv4Key>]>, Error> {
        self.calls += 1;
        Ok(None)
    }
}
