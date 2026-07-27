use std::fs;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};

use iprange_livedb::{SnapshotBudget, TransactionBudget};

static NEXT_DIRECTORY: AtomicU64 = AtomicU64::new(0);

#[derive(Debug)]
pub(crate) struct TestDatabase {
    directory: PathBuf,
    main: PathBuf,
}

impl TestDatabase {
    pub(crate) fn new(label: &str) -> Result<Self, String> {
        let ordinal = NEXT_DIRECTORY.fetch_add(1, Ordering::Relaxed);
        let directory = std::env::temp_dir().join(format!(
            "iprange-v4-bench-{label}-{}-{ordinal}",
            std::process::id()
        ));
        fs::create_dir(&directory).map_err(|error| error.to_string())?;
        Ok(Self {
            main: directory.join("live.v4"),
            directory,
        })
    }

    pub(crate) fn main(&self) -> &Path {
        &self.main
    }

    pub(crate) fn snapshot(&self) -> PathBuf {
        self.directory.join("snapshot.v4")
    }

    pub(crate) fn private_artifacts(&self) -> Result<u64, String> {
        let mut count = 0u64;
        for entry in fs::read_dir(&self.directory).map_err(|error| error.to_string())? {
            let name = entry
                .map_err(|error| error.to_string())?
                .file_name()
                .to_string_lossy()
                .into_owned();
            if name.starts_with(".iprange-") {
                count += 1;
            }
        }
        Ok(count)
    }
}

impl Drop for TestDatabase {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.directory);
    }
}

pub(crate) fn transaction_budget(records: usize, feeds: usize) -> TransactionBudget {
    let scale = records
        .saturating_mul(8)
        .saturating_add(feeds.saturating_mul(128))
        .max(20_000);
    TransactionBudget {
        max_heap_bytes: 64 * 1024 * 1024,
        max_private_pages: scale as u64,
        max_file_growth_pages: scale as u64,
        max_open_files: 2,
    }
}

pub(crate) fn snapshot_budget(records: usize) -> SnapshotBudget {
    SnapshotBudget::new(
        64 * 1024 * 1024,
        records.saturating_mul(16).max(20_000) as u64,
        3,
    )
}
