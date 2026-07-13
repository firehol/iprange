//! Rule 2 verification: concurrent N readers + 1 writer, cross-process.
//!
//! Uses fork + pipe synchronization. The reader pins its transaction snapshot
//! (MVCC) and must still read correct data after the writer commits 2+ more
//! transactions. Two commits are needed because the off-by-one in reclaimable()
//! only manifests on the second commit (first commit tags pages, second commit
//! would reclaim them).
//!
//! Run with `--test-threads 1` (fork-based tests are flaky in parallel).

use std::path::PathBuf;
use std::time::Duration;

use iprange_livedb::os::{FileWriter, MmapReader};
use iprange_livedb::Ipv4Key;

fn temp_db(name: &str) -> PathBuf {
    let pid = std::process::id();
    let p = std::env::temp_dir().join(format!("iprange_mp_{}_{}.iprdb", name, pid));
    let _ = std::fs::remove_file(&p);
    let _ = std::fs::remove_file(p.with_extension("iprdb.readers"));
    p
}

/// Child reader: opens at txn T, verifies data, signals ready, sleeps while
/// writer commits T+1 and T+2, then re-verifies. Uses catch_unwind to detect
/// panics (MVCC violations cause slice out-of-bounds panics in lookup).
unsafe fn run_child(path: &str, samples: &[(u32, u32)]) -> ! {
    let reader = match MmapReader::open(std::path::Path::new(path)) {
        Ok(r) => r,
        Err(e) => {
            eprintln!("[child] open failed: {:?}", e);
            libc::_exit(1);
        }
    };

    // Initial verification.
    let initial_ok = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        let r = reader.reader().unwrap();
        for &(k, v) in samples {
            assert_eq!(
                r.lookup(Ipv4Key(k)).unwrap_or(None),
                Some(v),
                "initial lookup({}) failed",
                k
            );
        }
    }))
    .is_ok();
    if !initial_ok {
        eprintln!("[child] initial verification panicked");
        libc::_exit(2);
    }

    // Signal parent: reader is registered at txn T.
    let buf = [1u8; 1];
    unsafe {
        libc::write(1, buf.as_ptr() as *const _, 1);
    }

    // Sleep while writer commits T+1 and T+2.
    std::thread::sleep(Duration::from_secs(3));

    // Re-verify: MVCC must hold after 2+ commits.
    let reverify_ok = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        let r = reader.reader().unwrap();
        for &(k, v) in samples {
            assert_eq!(
                r.lookup(Ipv4Key(k)).unwrap_or(None),
                Some(v),
                "MVCC violation: lookup({}) expected {} got {:?}",
                k,
                v,
                r.lookup(Ipv4Key(k)).unwrap_or(None)
            );
        }
    }))
    .is_ok();
    if !reverify_ok {
        eprintln!("[child] MVCC violation after 2 commits");
        libc::_exit(3);
    }
    libc::_exit(0);
}

#[test]
fn reader_survives_two_commits() {
    let path = temp_db("rsw");
    let path_str = path.to_str().unwrap().to_string();

    // 1. Create DB with 1000 records at txn 1.
    {
        let mut w = FileWriter::<Ipv4Key>::create(&path, 0, 0).unwrap();
        for i in 0..1000u32 {
            w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap();
        }
        w.commit(1).unwrap();
        w.close();
    }

    // 2. Fork child with pipe sync via stdout (fd 1 is inherited).
    let mut pipe_fds = [0i32; 2];
    unsafe {
        assert!(libc::pipe(pipe_fds.as_mut_ptr()) == 0);
    }
    let read_fd = pipe_fds[0];
    let write_fd = pipe_fds[1];

    let pid = unsafe { libc::fork() };
    assert!(pid >= 0, "fork failed");

    if pid == 0 {
        // Child: close read end, redirect write to pipe.
        unsafe {
            libc::close(read_fd);
        }
        // Use dup2 to redirect stdout to the pipe write fd.
        unsafe {
            libc::dup2(write_fd, 1);
        }
        unsafe {
            libc::close(write_fd);
        }
        let samples: Vec<(u32, u32)> = (0..10).map(|i| (i * 100, i * 100)).collect();
        unsafe {
            run_child(&path_str, &samples);
        }
    }

    // Parent: close write end.
    unsafe {
        libc::close(write_fd);
    }

    // 3. Wait for child to signal ready.
    let mut buf = [0u8; 1];
    unsafe {
        let n = libc::read(read_fd, buf.as_mut_ptr() as *mut _, 1);
        assert!(n == 1, "child did not signal ready (read returned {})", n);
        libc::close(read_fd);
    }

    // 4. Writer commits txn 2 AND txn 3 (two commits — the off-by-one
    //    in reclaimable only manifests on the second commit).
    {
        let mut w = FileWriter::<Ipv4Key>::open(&path).unwrap();
        // Commit 2: churn all records.
        for i in 0..1000u32 {
            w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap();
        }
        for i in 0..1000u32 {
            w.set(Ipv4Key(i), Ipv4Key(i), i + 50).unwrap();
        }
        w.commit(2).unwrap();
        // Commit 3: churn again (this is where stale pages get reclaimed).
        for i in 0..1000u32 {
            w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap();
        }
        for i in 0..1000u32 {
            w.set(Ipv4Key(i), Ipv4Key(i), i + 100).unwrap();
        }
        w.commit(3).unwrap();
        w.close();
    }

    // 5. Wait for child.
    let mut status = 0;
    unsafe {
        libc::waitpid(pid, &mut status, 0);
    }

    assert!(libc::WIFEXITED(status), "child did not exit normally");
    let exit_code = libc::WEXITSTATUS(status);
    assert_eq!(
        exit_code, 0,
        "child reader failed with exit code {} \
        (1=open fail, 2=initial fail, 3=MVCC violation after 2 commits)",
        exit_code
    );

    let _ = std::fs::remove_file(&path);
    let _ = std::fs::remove_file(path.with_extension("iprdb.readers"));
}
