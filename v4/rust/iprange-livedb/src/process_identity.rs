//! Fork-safe ownership checks without Linux hot-path syscalls.

#[cfg(target_os = "linux")]
mod platform {
    use std::ptr::{self, NonNull};
    use std::sync::atomic::{AtomicPtr, AtomicU32, AtomicU8, Ordering};

    const FORKED: u8 = 0;
    const READY: u8 = 1;
    const ADVANCING: u8 = 2;

    static MARKER: AtomicPtr<AtomicU8> = AtomicPtr::new(ptr::null_mut());
    static GENERATION: AtomicU32 = AtomicU32::new(1);

    pub(super) fn current() -> u32 {
        loop {
            let marker = MARKER.load(Ordering::Acquire);
            if marker.is_null() {
                install_marker();
                continue;
            }
            if marker == unsupported_marker() {
                return super::pid();
            }

            // SAFETY: Published markers are process-lifetime anonymous mappings.
            let state = unsafe { &*marker }.load(Ordering::Acquire);
            match state {
                READY => return GENERATION.load(Ordering::Acquire),
                FORKED => {
                    // SAFETY: See the load above. Only one child thread advances.
                    if unsafe { &*marker }
                        .compare_exchange(FORKED, ADVANCING, Ordering::AcqRel, Ordering::Acquire)
                        .is_ok()
                    {
                        let generation = advance_generation();
                        // Publish the generation before allowing another check.
                        unsafe { &*marker }.store(READY, Ordering::Release);
                        return generation;
                    }
                }
                ADVANCING => std::hint::spin_loop(),
                _ => unreachable!("private fork marker has an invalid state"),
            }
        }
    }

    fn install_marker() {
        let Some(candidate) = Candidate::new() else {
            let _ = MARKER.compare_exchange(
                ptr::null_mut(),
                unsupported_marker(),
                Ordering::AcqRel,
                Ordering::Acquire,
            );
            return;
        };
        if MARKER
            .compare_exchange(
                ptr::null_mut(),
                candidate.marker,
                Ordering::AcqRel,
                Ordering::Acquire,
            )
            .is_ok()
        {
            candidate.publish();
        }
    }

    fn advance_generation() -> u32 {
        let previous = GENERATION.fetch_add(1, Ordering::AcqRel);
        let next = previous.wrapping_add(1);
        if next != 0 {
            next
        } else {
            GENERATION.store(1, Ordering::Release);
            1
        }
    }

    fn unsupported_marker() -> *mut AtomicU8 {
        NonNull::<AtomicU8>::dangling().as_ptr()
    }

    struct Candidate {
        marker: *mut AtomicU8,
        len: usize,
        published: bool,
    }

    impl Candidate {
        fn new() -> Option<Self> {
            // SAFETY: `sysconf` has no memory contract.
            let page_size = unsafe { libc::sysconf(libc::_SC_PAGESIZE) };
            let len = usize::try_from(page_size).ok().filter(|size| *size != 0)?;
            // SAFETY: Requests a new private anonymous mapping of `len` bytes.
            let address = unsafe {
                libc::mmap(
                    ptr::null_mut(),
                    len,
                    libc::PROT_READ | libc::PROT_WRITE,
                    libc::MAP_PRIVATE | libc::MAP_ANONYMOUS,
                    -1,
                    0,
                )
            };
            if address == libc::MAP_FAILED {
                return None;
            }

            // Reject emulators that claim to accept every advice value.
            // SAFETY: `address..address + len` names the mapping above.
            let rejects_invalid = unsafe { libc::madvise(address, len, -1) } != 0;
            // SAFETY: Same live mapping; the advice changes only fork inheritance.
            let wipes_on_fork = rejects_invalid
                && unsafe { libc::madvise(address, len, libc::MADV_WIPEONFORK) } == 0;
            if !wipes_on_fork {
                // SAFETY: Releases exactly the mapping created above.
                let _ = unsafe { libc::munmap(address, len) };
                return None;
            }

            let marker = address.cast::<AtomicU8>();
            // SAFETY: AtomicU8 has byte alignment and fits in the fresh mapping.
            unsafe { marker.write(AtomicU8::new(READY)) };
            Some(Self {
                marker,
                len,
                published: false,
            })
        }

        fn publish(mut self) {
            self.published = true;
        }
    }

    impl Drop for Candidate {
        fn drop(&mut self) {
            if !self.published {
                // SAFETY: Unpublished candidates remain exclusively owned here.
                let _ = unsafe { libc::munmap(self.marker.cast(), self.len) };
            }
        }
    }

    #[cfg(test)]
    pub(super) fn fast_path_enabled() -> bool {
        let _ = current();
        let marker = MARKER.load(Ordering::Acquire);
        !marker.is_null() && marker != unsupported_marker()
    }
}

#[cfg(not(target_os = "linux"))]
mod platform {
    #[inline]
    pub(super) fn current() -> u32 {
        super::pid()
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct ProcessIdentity(u32);

impl ProcessIdentity {
    #[inline]
    pub(crate) fn capture() -> Self {
        Self(platform::current())
    }

    #[inline]
    pub(crate) fn is_current(self) -> bool {
        self == Self::capture()
    }

    #[cfg(test)]
    pub(crate) fn foreign() -> Self {
        let current = Self::capture().0;
        Self(if current == u32::MAX { 1 } else { current + 1 })
    }
}

#[inline]
fn pid() -> u32 {
    #[cfg(all(test, target_os = "linux"))]
    PID_CALLS.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
    std::process::id()
}

#[cfg(all(test, target_os = "linux"))]
static PID_CALLS: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);

#[cfg(all(test, target_os = "linux"))]
fn pid_calls() -> u64 {
    PID_CALLS.load(std::sync::atomic::Ordering::Relaxed)
}

#[cfg(all(test, target_os = "linux"))]
pub(crate) fn fast_path_enabled() -> bool {
    platform::fast_path_enabled()
}

#[cfg(all(test, target_os = "linux"))]
mod tests {
    use std::process::Command;

    use super::{fast_path_enabled, pid_calls, ProcessIdentity};

    const CHILD: &str = "process_identity::tests::fork_child";

    #[test]
    fn identity_is_stable_across_threads() {
        let identity = ProcessIdentity::capture();
        let threads: Vec<_> = (0..8)
            .map(|_| std::thread::spawn(ProcessIdentity::capture))
            .collect();
        for thread in threads {
            assert_eq!(thread.join().unwrap(), identity);
        }
        assert!(identity.is_current());
        let _supported_on_this_kernel = fast_path_enabled();
    }

    #[test]
    fn supported_fast_path_does_not_query_the_pid() {
        if !fast_path_enabled() {
            return;
        }
        let identity = ProcessIdentity::capture();
        let before = pid_calls();
        for _ in 0..100_000 {
            assert!(identity.is_current());
        }
        assert_eq!(pid_calls(), before);
    }

    #[test]
    fn identity_changes_after_fork() {
        let status = Command::new(std::env::current_exe().unwrap())
            .args(["--ignored", "--exact", CHILD, "--test-threads=1"])
            .status()
            .unwrap();
        assert!(status.success());
    }

    #[test]
    #[ignore = "single-threaded fork subprocess entry point"]
    fn fork_child() {
        let before = ProcessIdentity::capture();
        // SAFETY: This ignored entry point runs alone in its subprocess. The
        // child performs only atomic ownership checks before `_exit`.
        let child = unsafe { libc::fork() };
        assert!(child >= 0);
        if child == 0 {
            let changed = ProcessIdentity::capture() != before && !before.is_current();
            // SAFETY: Avoids all inherited Rust destructors after fork.
            unsafe { libc::_exit(i32::from(!changed)) }
        }
        let mut status = 0;
        // SAFETY: `child` is the exact positive PID returned above.
        assert_eq!(unsafe { libc::waitpid(child, &mut status, 0) }, child);
        assert!(libc::WIFEXITED(status));
        assert_eq!(libc::WEXITSTATUS(status), 0);
        assert!(before.is_current());
    }
}
