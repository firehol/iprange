#![cfg(target_os = "linux")]

use std::fs;
use std::path::PathBuf;
use std::process::Command;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, AddressFamily, CancellationToken, Error, FeedName, Ipv4Key, LiveReader,
    LiveWriter, MembershipOperation, RangeDirection, TransactionBudget, ValueKind, ValueTag,
};

const CHILD: &str = "inherited_live_handles_are_rejected_child";

struct TestFiles {
    main: PathBuf,
}

impl TestFiles {
    fn new() -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        Self {
            main: std::env::temp_dir().join(format!(
                "iprange-v4-fork-ownership-{}-{unique}",
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

impl Drop for TestFiles {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.main);
        let _ = fs::remove_file(self.sidecar());
    }
}

fn budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 2 * 1024 * 1024,
        max_private_pages: 10_000,
        max_file_growth_pages: 10_000,
        max_open_files: 2,
    }
}

#[test]
fn inherited_live_handles_are_rejected() {
    let status = Command::new(std::env::current_exe().unwrap())
        .args(["--ignored", "--exact", CHILD, "--test-threads=1"])
        .status()
        .unwrap();
    assert!(status.success());
}

#[test]
#[ignore = "single-threaded fork subprocess entry point"]
fn inherited_live_handles_are_rejected_child() {
    let files = TestFiles::new();
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        iprange_livedb::StructureKind::None,
        ValueTag::new(b"membership").unwrap(),
        2,
        &CancellationToken::new(),
    )
    .unwrap();

    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    {
        let mut transaction = writer
            .begin_membership_transaction(&CancellationToken::new())
            .unwrap();
        let feed = transaction
            .ensure_feed(FeedName::new("alpha").unwrap())
            .unwrap();
        let empty = transaction.empty_membership().unwrap();
        let member = transaction.add_feed(empty, feed).unwrap();
        transaction
            .apply_v4(Ipv4Key(10), Ipv4Key(20), member, MembershipOperation::Union)
            .unwrap();
        transaction.commit().unwrap();
    }

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    {
        let alpha = reader.lookup_feed("alpha").unwrap().unwrap();
        let view = reader.lookup_membership_v4(Ipv4Key(15)).unwrap().unwrap();
        let mut feed_cursor = reader.feed_cursor().unwrap();
        let mut range_cursor = reader
            .feed_range_cursor_v4("alpha", RangeDirection::Forward)
            .unwrap();

        // SAFETY: This ignored entry point runs alone in its subprocess. The child
        // performs only ownership checks and exits without running inherited drops.
        let child = unsafe { libc::fork() };
        assert!(child >= 0);
        if child == 0 {
            let rejected = matches!(reader.info(), Err(Error::ForkedHandle))
                && matches!(writer.metadata_json_len(), Err(Error::ForkedHandle))
                && matches!(feed_cursor.next_feed(), Err(Error::ForkedHandle))
                && matches!(range_cursor.next_range(), Err(Error::ForkedHandle))
                && matches!(view.word_count(), Err(Error::ForkedHandle))
                && matches!(view.contains_index(alpha.index), Err(Error::ForkedHandle));
            // SAFETY: Avoids all inherited Rust destructors after fork.
            unsafe { libc::_exit(i32::from(!rejected)) }
        }

        let mut status = 0;
        // SAFETY: `child` is the exact positive PID returned above.
        assert_eq!(unsafe { libc::waitpid(child, &mut status, 0) }, child);
        assert!(libc::WIFEXITED(status));
        assert_eq!(libc::WEXITSTATUS(status), 0);

        assert!(reader.info().is_ok());
        assert!(writer.metadata_json_len().is_ok());
        assert!(feed_cursor.next_feed().unwrap().is_some());
        assert!(range_cursor.next_range().unwrap().is_some());
        assert!(view.contains_index(alpha.index).unwrap());
    }
    reader.close().unwrap();
    writer.close().unwrap();
}
