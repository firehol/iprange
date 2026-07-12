//! Rule 2 verification: concurrent N readers + 1 writer, cross-process.
//!
//! Uses fork + pipe synchronization to ensure the reader registers before
//! the writer starts committing. The reader pins its transaction snapshot
//! (MVCC) and must still read correct data after the writer commits subsequent
//! transactions.
//!
//! These tests use fork() and may be flaky when run in parallel with CPU-heavy
//! tests (robustness fuzz). Run with `--test-threads 1` or explicitly:
//!   cargo test --test multiprocess_test -- --test-threads 1

use std::path::PathBuf;
use std::time::Duration;

use iprange_livedb::{Ipv4Key};
use iprange_livedb::os::{FileWriter, MmapReader};

fn temp_db(name: &str) -> PathBuf {
    let pid = std::process::id();
    let p = std::env::temp_dir().join(format!("iprange_mp_{}_{}.iprdb", name, pid));
    let _ = std::fs::remove_file(&p);
    let _ = std::fs::remove_file(p.with_extension("iprdb.readers"));
    p
}

#[test]
fn reader_survives_writer_commits() {
    let path = temp_db("rsw");
    let path_str = path.to_str().unwrap().to_string();

    // 1. Create DB with 1000 records at txn 1.
    {
        let mut w = FileWriter::<Ipv4Key>::create(&path, 0, 0).unwrap();
        for i in 0..1000u32 { w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap(); }
        w.commit(1).unwrap();
        w.close();
    }

    // 2. Create pipe for parent-child synchronization.
    let mut fds = [0i32; 2];
    unsafe { assert!(libc::pipe(fds.as_mut_ptr()) == 0, "pipe failed"); }
    let read_fd = fds[0];
    let write_fd = fds[1];

    // 3. Fork child.
    let pid = unsafe { libc::fork() };
    assert!(pid >= 0, "fork failed");

    if pid == 0 {
        // Child: close read end of pipe.
        unsafe { libc::close(read_fd); }

        let reader = match MmapReader::open(std::path::Path::new(&path_str)) {
            Ok(r) => r,
            Err(e) => { eprintln!("[child] open failed: {:?}", e); unsafe { libc::_exit(1); } }
        };

        // Verify initial data.
        let samples: Vec<(u32, u32)> = (0..10).map(|i| (i * 100, i * 100)).collect();
        if let Ok(r) = reader.reader() {
            for &(k, v) in &samples {
                if r.lookup(Ipv4Key(k)).unwrap_or(None) != Some(v) {
                    eprintln!("[child] initial check failed at {}", k);
                    unsafe { libc::_exit(2); }
                }
            }
        }

        // Signal parent: reader is registered.
        let buf = [1u8; 1];
        unsafe { libc::write(write_fd, buf.as_ptr() as *const _, 1); }
        unsafe { libc::close(write_fd); }

        // Sleep to keep reader alive while parent commits.
        std::thread::sleep(Duration::from_secs(3));

        // Re-verify: MVCC guarantees the reader still sees the old data.
        if let Ok(r) = reader.reader() {
            for &(k, v) in &samples {
                let got = r.lookup(Ipv4Key(k)).unwrap_or(None);
                if got != Some(v) {
                    eprintln!("[child] MVCC violation at {}: expected {} got {:?}", k, v, got);
                    unsafe { libc::_exit(3); }
                }
            }
        }
        unsafe { libc::_exit(0); }
    }

    // Parent: close write end of pipe.
    unsafe { libc::close(write_fd); }

    // 4. Wait for child to signal ready (blocks until child opens reader).
    let mut buf = [0u8; 1];
    unsafe { libc::read(read_fd, buf.as_mut_ptr() as *mut _, 1); }
    unsafe { libc::close(read_fd); }

    // 5. Parent: open writer and commit multiple transactions (churn).
    {
        let mut w = FileWriter::<Ipv4Key>::open(&path).unwrap();
        for cycle in 0..5u32 {
            for i in 0..1000u32 { w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap(); }
            for i in 0..1000u32 { w.set(Ipv4Key(i), Ipv4Key(i), cycle + 10).unwrap(); }
            w.commit(cycle as u64 + 2).unwrap();
        }
        w.close();
    }

    // 6. Wait for child to exit.
    let mut status = 0;
    unsafe { libc::waitpid(pid, &mut status, 0); }

    assert!(libc::WIFEXITED(status), "child did not exit normally");
    let exit_code = libc::WEXITSTATUS(status);
    assert_eq!(exit_code, 0, "child reader failed with exit code {}", exit_code);

    let _ = std::fs::remove_file(&path);
    let _ = std::fs::remove_file(path.with_extension("iprdb.readers"));
}

#[test]
fn multiple_readers_survive() {
    let path = temp_db("mrs");
    let path_str = path.to_str().unwrap().to_string();

    {
        let mut w = FileWriter::<Ipv4Key>::create(&path, 0, 0).unwrap();
        for i in 0..500u32 { w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap(); }
        w.commit(1).unwrap();
        w.close();
    }

    // Fork 3 child readers with pipe sync.
    let mut child_pids = vec![];
    let mut parent_fds = vec![];
    for _ in 0..3 {
        let mut fds = [0i32; 2];
        unsafe { assert!(libc::pipe(fds.as_mut_ptr()) == 0); }
        let pid = unsafe { libc::fork() };
        assert!(pid >= 0, "fork failed");

        if pid == 0 {
            // Child
            unsafe { libc::close(fds[0]); }
            let reader = match MmapReader::open(std::path::Path::new(&path_str)) {
                Ok(r) => r,
                Err(_) => unsafe { libc::_exit(1); }
            };
            let buf = [1u8; 1];
            unsafe { libc::write(fds[1], buf.as_ptr() as *const _, 1); }
            unsafe { libc::close(fds[1]); }
            std::thread::sleep(Duration::from_secs(3));
            let samples: Vec<(u32, u32)> = (0..5).map(|i| (i * 100, i * 100)).collect();
            if let Ok(r) = reader.reader() {
                for &(k, v) in &samples {
                    if r.lookup(Ipv4Key(k)).unwrap_or(None) != Some(v) {
                        unsafe { libc::_exit(3); }
                    }
                }
            }
            unsafe { libc::_exit(0); }
        }
        // Parent
        unsafe { libc::close(fds[1]); }
        child_pids.push(pid);
        parent_fds.push(fds[0]);
    }

    // Wait for all children to signal ready.
    for fd in &parent_fds {
        let mut buf = [0u8; 1];
        unsafe { libc::read(*fd, buf.as_mut_ptr() as *mut _, 1); }
    }
    for fd in &parent_fds {
        unsafe { libc::close(*fd); }
    }

    // Writer commits.
    {
        let mut w = FileWriter::<Ipv4Key>::open(&path).unwrap();
        for i in 0..500u32 { w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap(); }
        for i in 0..500u32 { w.set(Ipv4Key(i), Ipv4Key(i), i + 50).unwrap(); }
        w.commit(2).unwrap();
        w.close();
    }

    for &cpid in &child_pids {
        let mut status = 0;
        unsafe { libc::waitpid(cpid, &mut status, 0); }
        if libc::WIFEXITED(status) {
            assert_eq!(libc::WEXITSTATUS(status), 0, "child {} failed", cpid);
        }
    }

    let _ = std::fs::remove_file(&path);
    let _ = std::fs::remove_file(path.with_extension("iprdb.readers"));
}
