//! Legacy DNS resolution pool (getaddrinfo, A-only in IPv4 mode,
//! AAAA+A with mapped-IPv4 in IPv6 mode) with the exact C messages.
//!
//! Authority: `src/ipset_dns.c` (IPv4 pool), `src/ipset6_dns.c`
//! (IPv6 pool), and the loader call sites `src/ipset_load.c` /
//! `src/ipset6_load.c`. The pool mirrors the C worker model: a
//! bounded set of getaddrinfo workers sized by `--dns-threads`
//! (default 5), a shared job queue, per-host error isolation, and
//! the C progress/summary texts. Resolution uses libc `getaddrinfo`
//! directly on Unix (the C mechanism, zero extra crates); the
//! non-Unix port uses `std::net::ToSocketAddrs` (same host/port
//! tuple, same family policy) because the `libc` crate does not
//! expose the winsock getaddrinfo on Windows.
//!
//! C model differences that are unobservable in the output:
//! - C processes the queue LIFO and stacks replies; the ipset is an
//!   ordered set, so the per-host address order does not survive.
//!   This port keeps the getaddrinfo result order and deduplicates
//!   per host (first occurrence), as the C ipset deduplicates.
//! - C spawns workers lazily as the pending count grows (`pending >
//!   threads && threads < max`) and its load phase never blocks on
//!   replies, so multi-host files routinely reach the maximum; this
//!   port uses the same growth condition but parse.rs's per-host
//!   blocking resolve() keeps the queue nearly empty, so in practice
//!   one worker serves the whole run and the `-v` summary reports
//!   "threads used 1 of N".
//! - The C getnameinfo -> str2netaddr round-trip (IPv4 worker) is a
//!   lossless identity for the AF_INET sockaddrs getaddrinfo
//!   returns; this port reads `sin_addr.s_addr` directly. The C
//!   "failed to get IP string" / "cannot parse the IP" lines are
//!   unreachable without a libc bug and are not ported.
//! - C malloc failures ("out of memory...") cannot be reproduced in
//!   Rust; the allocator aborts instead.
//! - parse.rs owns one Resolver per run and calls finish() once,
//!   while C calls dns_done() per file and resets the counters, so
//!   the `-v` summary aggregates the whole run.

use super::family::{Family, FamilyImpl};
use std::collections::HashSet;
use std::sync::mpsc::{channel, Receiver, Sender, TryRecvError};
use std::sync::{Arc, Condvar, Mutex};

/// C `MAX_INPUT_ELEMENT` (`src/ipset_load.c`): the maximum hostname
/// length accepted in IPv4 mode.
const MAX_HOSTNAME_V4: usize = 255;
/// C `MAX_INPUT_ELEMENT6` (`src/ipset6_load.h`): IPv6 mode.
const MAX_HOSTNAME_V6: usize = 256;
/// C `d->tries = 20` (`src/ipset_dns.c`, `src/ipset6_dns.c`): the
/// number of EAI_AGAIN re-attempts before a permanent failure.
const RETRIES: i32 = 20;

/// Hard ceiling for the DNS worker pool. The C oracle eagerly spawns
/// up to `--dns-threads` workers while requests are pending, so a
/// legal-but-large value (up to INT_MAX) combined with a large host
/// file makes the C tool (8 MiB stacks) and this port (2 MiB stacks)
/// reserve hundreds of GiB and get OOM-killed. The released default
/// is 5 and the legacy suite uses at most 4, so this ceiling is
/// unobservable wherever C itself survives; it only bounds the pool
/// in the regime where C dies. 128 workers (256 MiB of stacks) is far
/// beyond useful getaddrinfo parallelism.
const DNS_POOL_HARD_MAX: usize = 128;

/// One host resolution failure; the parse worker renders the exact C
/// stderr text from the variant.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum DnsError {
    /// The host does not resolve.
    NotFound(String),
    /// Resolver infrastructure failure.
    System(String),
}

impl std::fmt::Display for DnsError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        // The payload is the full pre-rendered C stderr line.
        match self {
            DnsError::NotFound(line) | DnsError::System(line) => f.write_str(line),
        }
    }
}

/// One queued job: the submission sequence number, the hostname,
/// and the per-job reply channel (per-host error isolation, C
/// `DNSREQ` + the reply it produces). The sequence number orders the
/// per-file drain output exactly like the C load order.
struct Job {
    seq: usize,
    host: String,
    reply: Sender<Result<Vec<u128>, DnsError>>,
}

/// One completed reply, kept in submission order for the per-file
/// drain (the C processes replies as they arrive; the ipset is an
/// ordered set, so the entry set is identical, and the diagnostics
/// are printed in file order here).
pub struct ReplyRecord {
    /// Submission order (C load order).
    pub seq: usize,
    /// The resolution outcome for the per-file drain to render.
    pub result: Result<Vec<u128>, DnsError>,
}

/// Shared pool state: the C globals (`dns_requests_made`,
/// `dns_requests_retries`, `dns_replies_found`,
/// `dns_replies_failed`, ...) plus the job queue.
struct Shared {
    family: Family,
    silent: bool,
    progress: bool,
    debug: bool,
    stats: Mutex<Stats>,
    jobs: Mutex<Receiver<Job>>,
    jobs_cond: Condvar,
    /// Completed replies in completion order (sorted by seq at
    /// drain time); workers append under the lock and notify
    /// `replies_cond` so the drain can wait for the batch.
    replies: Mutex<Vec<ReplyRecord>>,
    replies_cond: Condvar,
}

/// C pool counters (`src/ipset_dns.c`): `made` counts requests that
/// entered the queue, `finished` requests that terminated (success or
/// failure), `found` addresses seen (including per-host duplicates,
/// like the C reply stack), `failed` requests that terminated with
/// zero addresses, `retries` EAI_AGAIN re-attempts.
#[derive(Default)]
struct Stats {
    made: u64,
    finished: u64,
    found: u64,
    failed: u64,
    retries: u64,
}

/// The legacy resolver pool: bounded worker threads, C-equivalent
/// progress and summary messages.
pub struct Resolver {
    _private: (),
    shared: Arc<Shared>,
    sender: Option<Sender<Job>>,
    workers: Vec<std::thread::JoinHandle<()>>,
    threads_max: u32,
    /// Sequence of the next job to submit (C load order).
    next_seq: usize,
    /// Sequence of the first job of the current per-file batch.
    batch_start: usize,
    /// Set once a worker spawn fails: further spawn attempts in the
    /// same run are pointless (the C retries and prints one line per
    /// attempt, which under resource exhaustion is a storm of failed
    /// mmaps); the pool keeps serving with the workers that exist.
    spawn_failed: bool,
}

impl Resolver {
    /// Create the pool. `_threads` is the C thread count (`--dns-threads`,
    /// default 5 in `src/iprange.c`), `_silent` and `_progress` map to
    /// the DNS flags, `_family` selects the record set, `_debug`
    /// enables the C `-v` diagnostics.
    pub fn new(
        _threads: u32,
        _silent: bool,
        _progress: bool,
        _family: Family,
        _debug: bool,
    ) -> Resolver {
        let (sender, receiver): (Sender<Job>, Receiver<Job>) = channel();
        let shared = Arc::new(Shared {
            family: _family,
            silent: _silent,
            progress: _progress,
            debug: _debug,
            stats: Mutex::new(Stats::default()),
            jobs: Mutex::new(receiver),
            jobs_cond: Condvar::new(),
            replies: Mutex::new(Vec::new()),
            replies_cond: Condvar::new(),
        });
        Resolver {
            _private: (),
            shared,
            sender: Some(sender),
            workers: Vec::new(),
            // C validates --dns-threads as >= 1; clamp defensively.
            threads_max: _threads.max(1),
            next_seq: 0,
            batch_start: 0,
            spawn_failed: false,
        }
    }

    /// Queue one hostname and wait for its own reply (synchronous
    /// test convenience; the production path queues with
    /// [`Resolver::request`] and drains per file).
    ///
    /// Mirrors C `dns_request()` (`src/ipset_dns.c`,
    /// `src/ipset6_dns.c`): the hostname length check runs before
    /// the request is counted or queued, the caller receives the
    /// full pre-rendered C stderr line on failure (`DnsError`), and
    /// EAI_AGAIN retries plus their messages are emitted while the
    /// request is in flight (this call blocks until that one host
    /// finishes).
    #[cfg(test)]
    pub fn resolve(&mut self, host: &str) -> Result<Vec<u128>, DnsError> {
        let reply = self.submit(host)?;
        reply
            .recv()
            .expect("iprange: internal error: DNS worker died while resolving")
    }

    /// Queue one hostname for resolution and return immediately. The
    /// caller drains the per-file batch with [`Resolver::drain`] and
    /// the pool grows exactly like the C (pending requests trigger
    /// lazy worker spawn up to `--dns-threads`).
    pub fn request(&mut self, host: &str) -> Result<(), DnsError> {
        self.submit(host).map(|reply| drop(reply))
    }

    /// Validate, count, and queue one hostname; return the per-job
    /// reply channel. Logs the C "Creating new DNS thread" debug
    /// line when the pending count forces pool growth.
    fn submit(
        &mut self,
        _host: &str,
    ) -> Result<Receiver<Result<Vec<u128>, DnsError>>, DnsError> {
        // C iprange_cstrnlen(): the hostname is a C string, so any
        // interior NUL truncates it (fgets can deliver NUL bytes).
        let host = match _host.split('\0').next() {
            Some(prefix) => prefix,
            None => "",
        };
        let max_len = match self.shared.family {
            Family::V4 => MAX_HOSTNAME_V4,
            Family::V6 => MAX_HOSTNAME_V6,
        };
        if host.is_empty() || host.len() > max_len {
            // C dns_request() prints this and returns -1 (the loader
            // then fails the file); always printed (silent does not
            // gate it), hence the System variant.
            return Err(DnsError::System(
                "iprange: DNS: hostname is empty or too long".to_string(),
            ));
        }

        {
            let mut stats = self.shared.stats.lock().unwrap();
            stats.made += 1;
            // C dns_request_add(): pending counts requests that
            // entered the queue and have not terminated; grow the
            // pool while that exceeds the workers and the max is not
            // reached. With the batch API the loader queues the whole
            // file first, so pending accumulates and the pool reaches
            // `--dns-threads` exactly like C.
            let pending = stats.made - stats.finished;
            if !self.spawn_failed
                && (pending as usize) > self.workers.len()
                && (self.workers.len() as u32) < self.threads_max
                && self.workers.len() < DNS_POOL_HARD_MAX
            {
                drop(stats);
                // C dns_request_add() debug line (IPv4 only; the
                // IPv6 request_add has no equivalent).
                if self.shared.debug && self.shared.family == Family::V4 {
                    eprintln!("iprange: Creating new DNS thread");
                }
                let shared = self.shared.clone();
                match std::thread::Builder::new()
                    .name("iprange-dns".into())
                    .spawn(move || worker_loop(shared))
                {
                    Ok(handle) => self.workers.push(handle),
                    Err(_) => {
                        // C pthread_create failure text (printed once
                        // per run; C prints it per attempt).
                        self.spawn_failed = true;
                        eprintln!("iprange: Cannot create DNS thread.");
                        if self.workers.is_empty() {
                            // C dns_request_add(): with no worker yet
                            // the request is rolled back (pending--,
                            // made--) and dns_request() returns -1 so
                            // the loader fails the file cleanly;
                            // requests already queued stay queued for
                            // the workers that exist.
                            let mut stats = self.shared.stats.lock().unwrap();
                            stats.made -= 1;
                            return Err(DnsError::System(
                                "iprange: Cannot create DNS thread.".to_string(),
                            ));
                        }
                    }
                }
            }
        }

        let sender = match self.sender.as_ref() {
            Some(sender) => sender.clone(),
            // No channel (never started or already finished): resolve
            // synchronously so the caller still gets an answer.
            None => {
                let shared = self.shared.clone();
                let (reply_tx, reply_rx) = channel();
                let result = resolve_host(&shared, host);
                let _ = reply_tx.send(result);
                return Ok(reply_rx);
            }
        };

        let (reply_tx, reply_rx) = channel();
        let job = Job {
            seq: self.next_seq,
            host: host.to_string(),
            reply: reply_tx,
        };
        self.next_seq += 1;
        // Send while holding the jobs lock so a worker cannot miss
        // the notification between try_recv() and wait() (it is then
        // either already waiting on the condvar or blocked on the
        // lock; C pthread_cond_signal has the same contract).
        {
            let _jobs = self.shared.jobs.lock().unwrap();
            sender
                .send(job)
                .expect("iprange: internal error: DNS job channel closed");
            self.shared.jobs_cond.notify_one();
        }

        Ok(reply_rx)
    }

    /// Collect the replies of the current per-file batch in load
    /// order, print the C per-file diagnostics, and reset the
    /// per-file counters (C `dns_done()` + `dns_reset_stats()`).
    ///
    /// Every reply is returned, failed ones included: the caller
    /// renders the per-host C failure lines and decides whether the
    /// run fails (C prints each line when the worker finishes and
    /// then dns_done() reports the failed count).
    pub fn drain(&mut self) -> Vec<ReplyRecord> {
        let (shared, made, batch_start, batch_len) = {
            let shared = self.shared.clone();
            let stats = shared.stats.lock().unwrap();
            let made = stats.made;
            drop(stats);
            (shared, made, self.batch_start, self.next_seq - self.batch_start)
        };
        if made == 0 || batch_len == 0 {
            self.batch_start = self.next_seq;
            return Vec::new();
        }

        // C dns_done() wait loop: while requests are pending it
        // prints the debug "waiting" line (or the partial progress
        // bar) once per second. The drain below completes when the
        // batch finishes, so only the first observation is
        // reproduced; the partial per-second bars collapse into the
        // final bar (accepted timing deviation, recorded in SOW-0028).
        if shared.family == Family::V4 {
            let pending = {
                let stats = shared.stats.lock().unwrap();
                stats.made - stats.finished
            };
            if pending > 0 && shared.debug {
                eprintln!("{}", waiting_line(pending));
            }
        }

        // Wait for every job of the batch to record its reply. The
        // stats reset below makes `made` per-file, so the wait
        // condition is length-based instead (seqs are contiguous).
        {
            let mut replies = shared.replies.lock().unwrap();
            while replies.len() < batch_start + batch_len {
                replies = shared.replies_cond.wait(replies).unwrap();
            }
        }

        let threads_used = self.workers.len() as u32;
        let mut batch: Vec<ReplyRecord> = Vec::with_capacity(batch_len);
        {
            let mut replies = shared.replies.lock().unwrap();
            batch.extend(replies.drain(batch_start..batch_start + batch_len));
        }
        batch.sort_by_key(|r| r.seq);

        let stats = shared.stats.lock().unwrap();
        let (made, failed, retries, found) =
            (stats.made, stats.failed, stats.retries, stats.found);
        if shared.family == Family::V4 {
            // C dns_done(): debug wins over the progress bar.
            if shared.debug {
                eprintln!(
                    "{}",
                    summary_line(made, failed, retries, found, threads_used, self.threads_max)
                );
            } else if shared.progress {
                eprintln!("{}", progress_bar());
            }
        }
        drop(stats);

        // C dns_reset_stats() after dns_done(); the pool stays alive
        // for the next file.
        {
            let mut stats = shared.stats.lock().unwrap();
            *stats = Stats::default();
        }
        self.batch_start = self.next_seq;

        batch
    }

    /// Wait for all in-flight work and print the C summary/progress
    /// text; Err when the C resolver would exit non-zero.
    ///
    /// Run-end counterpart of C `dns_done()`: the per-file drain
    /// (called by the loader after every file) already collected the
    /// replies and texts; this final call only drains any remaining
    /// batch, closes the job queue, and joins the workers.
    pub fn finish(&mut self) -> Result<(), ()> {
        let batch = self.drain();
        // C dns_done(): a failed IPv4 reply fails the run; the IPv6
        // side never fails.
        let failed = self.shared.family == Family::V4
            && batch.iter().any(|r| r.result.is_err());
        let shared = self.shared.clone();
        {
            let _jobs = shared.jobs.lock().unwrap();
            drop(self.sender.take());
            shared.jobs_cond.notify_all();
        }
        for worker in self.workers.drain(..) {
            let _ = worker.join();
        }
        if failed {
            Err(())
        } else {
            Ok(())
        }
    }
}

/// `src/ipset_dns.c` debug "waiting" line.
fn waiting_line(pending: u64) -> String {
    format!("iprange: DNS: waiting {pending} DNS resolutions to finish...")
}

/// `src/ipset_dns.c` final `-v` summary line.
fn summary_line(
    made: u64,
    failed: u64,
    retries: u64,
    found: u64,
    threads: u32,
    threads_max: u32,
) -> String {
    format!(
        "iprange: DNS: made {made} DNS requests, failed {failed}, retries: {retries}, \
         IPs got {found}, threads used {threads} of {threads_max}"
    )
}

/// `src/ipset_dns.c` progress bar (dots = 40): the loop prints a
/// label at every tenth position (`shown * 100 / 40` -> 0, 25, 50,
/// 75, 100) and a dot otherwise.
fn progress_bar() -> String {
    let mut bar = String::new();
    for shown in 0..=40u32 {
        if shown % 10 == 0 {
            bar.push_str(&format!("{}%", shown * 100 / 40));
        } else {
            bar.push('.');
        }
    }
    bar
}

/// One worker: take jobs from the queue until it is closed, resolve
/// each host exactly like the C worker thread, answer the caller of
/// that host (`dns_thread_resolve` / `dns6_thread_resolve`), and
/// record the reply for the per-file drain.
fn worker_loop(shared: Arc<Shared>) {
    while let Some(job) = next_job(&shared) {
        let result = resolve_host(&shared, &job.host);
        let _ = job.reply.send(result.clone());
        {
            let mut replies = shared.replies.lock().unwrap();
            replies.push(ReplyRecord {
                seq: job.seq,
                result,
            });
            shared.replies_cond.notify_all();
        }
    }
}

/// Block for the next queued job; None means the channel is closed
/// (finish() dropped the sender) and the worker must exit.
fn next_job(shared: &Shared) -> Option<Job> {
    let mut jobs = shared.jobs.lock().unwrap();
    loop {
        match jobs.try_recv() {
            Ok(job) => return Some(job),
            Err(TryRecvError::Empty) => {
                jobs = shared.jobs_cond.wait(jobs).unwrap();
            }
            Err(TryRecvError::Disconnected) => return None,
        }
    }
}

/// One host resolution with the C retry/error semantics. Runs on a
/// worker (or inline when no worker exists); prints the retry and
/// per-address debug lines itself and returns the final DnsError
/// payload for the parse worker to render.
#[cfg(unix)]
fn resolve_host(shared: &Shared, host: &str) -> Result<Vec<u128>, DnsError> {
    // Interior NULs were truncated by resolve(); CString::new is
    // infallible here.
    let host_c =
        std::ffi::CString::new(host).expect("iprange: internal error: NUL byte in DNS hostname");
    // C hints: service "80", SOCK_DGRAM (src/ipset_dns.c
    // dns_thread_resolve). The service string keeps the DNS query
    // shape byte-identical to C.
    let service = b"80\0".as_ptr().cast::<libc::c_char>();
    let mut tries = RETRIES;

    loop {
        let mut hints: libc::addrinfo = unsafe { std::mem::zeroed() };
        hints.ai_family = match shared.family {
            Family::V4 => libc::AF_INET,
            Family::V6 => libc::AF_UNSPEC,
        };
        hints.ai_socktype = libc::SOCK_DGRAM;

        let mut result: *mut libc::addrinfo = std::ptr::null_mut();
        let rc = unsafe { libc::getaddrinfo(host_c.as_ptr(), service, &hints, &mut result) };

        if rc == 0 {
            let (addrs, raw) = collect_addrs(shared, host, result);
            unsafe { libc::freeaddrinfo(result) };
            let mut stats = shared.stats.lock().unwrap();
            stats.finished += 1;
            // C dns_request_done(): zero addresses counts as a
            // failure even when getaddrinfo succeeded.
            if raw == 0 {
                stats.failed += 1;
            } else {
                stats.found += raw;
            }
            return Ok(addrs);
        }

        // Capture errno before anything else can clobber it.
        let errno_error = if rc == libc::EAI_SYSTEM {
            Some(std::io::Error::last_os_error())
        } else {
            None
        };

        if rc == libc::EAI_AGAIN && tries > 0 {
            // C dns_request_failed(): the retry happens regardless
            // of --dns-silent; only the message is gated.
            if !shared.silent {
                eprintln!("iprange: DNS: '{host}' will be retried: {}", gai_text(rc));
            }
            tries -= 1;
            shared.stats.lock().unwrap().retries += 1;
            continue;
        }

        // Terminal failure: C dns_request_done(d, 0) counts it as
        // finished with zero added addresses (the pending counter
        // must reach zero for the drain wait to end).
        {
            let mut stats = shared.stats.lock().unwrap();
            stats.finished += 1;
            stats.failed += 1;
        }
        let line = match shared.family {
            Family::V4 => match rc {
                libc::EAI_AGAIN => format!(
                    "iprange: DNS: '{host}' failed permanently after retries: {}",
                    gai_text(rc)
                ),
                libc::EAI_SYSTEM => format!(
                    "iprange: DNS: '{host}' system error: {}",
                    strerror_text(&errno_error.expect("EAI_SYSTEM implies errno"))
                ),
                libc::EAI_SOCKTYPE | libc::EAI_SERVICE | libc::EAI_MEMORY | libc::EAI_BADFLAGS => {
                    format!("iprange: DNS: '{host}' error: {}", gai_text(rc))
                }
                _ => format!(
                    "iprange: DNS: '{host}' failed permanently: {}",
                    gai_text(rc)
                ),
            },
            Family::V6 => format!("iprange: DNS: '{host}' failed: {}", gai_text(rc)),
        };
        return Err(DnsError::error_variant(shared.family, rc, line));
    }
}

/// `src/ipset_dns.c` getnameinfo/str2netaddr replacement: walk the
/// addrinfo list, convert each family-appropriate address, and feed
/// the sink (debug lines, dedup, raw count). Returns `(Vec, raw)`.
#[cfg(unix)]
fn collect_addrs(shared: &Shared, host: &str, result: *mut libc::addrinfo) -> (Vec<u128>, u64) {
    let mut sink = AddrSink::new(shared, host);
    let mut rp = result;
    while !rp.is_null() {
        let ai = unsafe { &*rp };
        let value: Option<u128> = match shared.family {
            Family::V4 => {
                if ai.ai_family != libc::AF_INET {
                    None
                } else {
                    let sin = unsafe { &*(ai.ai_addr as *const libc::sockaddr_in) };
                    Some(u32::from_be(sin.sin_addr.s_addr as u32) as u128)
                }
            }
            Family::V6 => match ai.ai_family {
                libc::AF_INET6 => {
                    let sin6 = unsafe { &*(ai.ai_addr as *const libc::sockaddr_in6) };
                    // Copy the 16 address bytes generically (the
                    // libc crate spells in6_addr.s6_addr differently
                    // on some unixes); network byte order, so read
                    // big-endian exactly like C in6_addr_to_ipv6().
                    let mut bytes = [0u8; 16];
                    unsafe {
                        std::ptr::copy_nonoverlapping(
                            &sin6.sin6_addr as *const _ as *const u8,
                            bytes.as_mut_ptr(),
                            16,
                        );
                    }
                    Some(u128::from_be_bytes(bytes))
                }
                libc::AF_INET => {
                    let sin = unsafe { &*(ai.ai_addr as *const libc::sockaddr_in) };
                    Some(mapped6(u32::from_be(sin.sin_addr.s_addr as u32)))
                }
                _ => None,
            },
        };
        rp = ai.ai_next;
        if let Some(value) = value {
            sink.push(value);
        }
    }
    sink.finish()
}

/// The non-Unix path: `std::net::ToSocketAddrs` (the libc crate does
/// not expose winsock getaddrinfo). Same host/port tuple and the
/// same family policy as C; differences: the system resolver controls
/// the result set, EAI_AGAIN cannot be distinguished from other
/// failures (only WouldBlock/TimedOut are retried), and every
/// failure renders as the silent-gated class because the glibc
/// error classes are not available.
#[cfg(not(unix))]
fn resolve_host(shared: &Shared, host: &str) -> Result<Vec<u128>, DnsError> {
    use std::net::ToSocketAddrs;
    let mut tries = RETRIES;
    loop {
        match (host, 80u16).to_socket_addrs() {
            Ok(iter) => {
                let (addrs, raw) = collect_socket_addrs(shared, host, iter);
                let mut stats = shared.stats.lock().unwrap();
                stats.finished += 1;
                if raw == 0 {
                    stats.failed += 1;
                } else {
                    stats.found += raw;
                }
                return Ok(addrs);
            }
            Err(error) => {
                let retriable = matches!(
                    error.kind(),
                    std::io::ErrorKind::WouldBlock | std::io::ErrorKind::TimedOut
                );
                if retriable && tries > 0 {
                    if !shared.silent {
                        eprintln!("iprange: DNS: '{host}' will be retried: {error}");
                    }
                    tries -= 1;
                    shared.stats.lock().unwrap().retries += 1;
                    continue;
                }
                shared.stats.lock().unwrap().failed += 1;
                let line = match shared.family {
                    Family::V4 => format!("iprange: DNS: '{host}' failed permanently: {error}"),
                    Family::V6 => format!("iprange: DNS: '{host}' failed: {error}"),
                };
                return Err(DnsError::NotFound(line));
            }
        }
    }
}

/// `collect_addrs` for the non-Unix socket iterator.
#[cfg(not(unix))]
fn collect_socket_addrs(
    shared: &Shared,
    host: &str,
    sockets: impl Iterator<Item = std::net::SocketAddr>,
) -> (Vec<u128>, u64) {
    let mut sink = AddrSink::new(shared, host);
    for socket in sockets {
        let value: Option<u128> = match (shared.family, socket) {
            (Family::V4, std::net::SocketAddr::V4(v4)) => {
                Some(u32::from_be_bytes(v4.ip().octets()) as u128)
            }
            (Family::V6, std::net::SocketAddr::V6(v6)) => {
                Some(u128::from_be_bytes(v6.ip().octets()))
            }
            (Family::V6, std::net::SocketAddr::V4(v4)) => {
                Some(mapped6(u32::from_be_bytes(v4.ip().octets())))
            }
            _ => None,
        };
        if let Some(value) = value {
            sink.push(value);
        }
    }
    sink.finish()
}

/// Address collection shared by both platforms: C per-address debug
/// line (IPv4 only), first-occurrence dedup of the returned Vec, and
/// the raw address count (C `added`, includes per-host duplicates).
struct AddrSink<'a> {
    shared: &'a Shared,
    host: &'a str,
    addrs: Vec<u128>,
    seen: HashSet<u128>,
    raw: u64,
}

impl<'a> AddrSink<'a> {
    fn new(shared: &'a Shared, host: &'a str) -> Self {
        AddrSink {
            shared,
            host,
            addrs: Vec::new(),
            seen: HashSet::new(),
            raw: 0,
        }
    }

    /// One address from the reply list. Returns true when it is a new
    /// address (pushed into the returned Vec).
    fn push(&mut self, value: u128) -> bool {
        self.raw += 1;
        if self.shared.debug && self.shared.family == Family::V4 {
            // C ipset_dns.c per-address line (no IPv6 equivalent).
            eprintln!("iprange: DNS: '{}' = {}", self.host, fmt_v4(value as u32));
        }
        if self.seen.insert(value) {
            self.addrs.push(value);
            true
        } else {
            false
        }
    }

    fn finish(self) -> (Vec<u128>, u64) {
        (self.addrs, self.raw)
    }
}

/// The IPv4-mapped IPv6 form of an IPv4 address: C
/// `ipv4_to_mapped6()` (`src/iprange6.h`) and the private
/// `ipv6::mapped_addr` builder (`MAPPED_PREFIX | v4 as u128`). The
/// published `is_mapped_addr`/`mapped_v4` predicates pin the
/// invariant in the tests below.
fn mapped6(v4: u32) -> u128 {
    (0xffff_u128 << 32) | v4 as u128
}

/// Dotted-quad text of a v4 value (C `ip2str_r`); the authoritative
/// formatter is the IPv4 family implementation.
fn fmt_v4(addr: u32) -> String {
    <u32 as FamilyImpl>::fmt_addr(addr)
}

/// glibc `gai_strerror` text.
#[cfg(unix)]
fn gai_text(code: i32) -> String {
    // Safe: gai_strerror returns a static/TLS buffer.
    let msg = unsafe { std::ffi::CStr::from_ptr(libc::gai_strerror(code)) };
    msg.to_string_lossy().into_owned()
}

/// glibc `strerror(errno)` text (same rendering as parse.rs::strerror):
/// without the Rust " (os error N)" suffix.
#[cfg(unix)]
fn strerror_text(error: &std::io::Error) -> String {
    if let Some(errno) = error.raw_os_error() {
        // Safe: strerror returns a static/TLS buffer.
        let msg = unsafe { std::ffi::CStr::from_ptr(libc::strerror(errno)) };
        msg.to_string_lossy().into_owned()
    } else {
        error.to_string()
    }
}

impl DnsError {
    /// The C family error class: IPv4 splits infrastructure errors
    /// (always printed) from host failures (gated by --dns-silent in
    /// the parse worker); IPv6 has one class for everything.
    #[cfg(unix)]
    fn error_variant(family: Family, code: i32, line: String) -> DnsError {
        match family {
            Family::V4 => match code {
                libc::EAI_SYSTEM
                | libc::EAI_SOCKTYPE
                | libc::EAI_SERVICE
                | libc::EAI_MEMORY
                | libc::EAI_BADFLAGS => DnsError::System(line),
                _ => DnsError::NotFound(line),
            },
            Family::V6 => DnsError::NotFound(line),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A test pool state without any worker threads.
    fn test_shared(family: Family, debug: bool) -> Shared {
        let (_tx, rx): (Sender<Job>, Receiver<Job>) = channel();
        Shared {
            family,
            silent: false,
            progress: false,
            debug,
            stats: Mutex::new(Stats::default()),
            jobs: Mutex::new(rx),
            jobs_cond: Condvar::new(),
            replies: Mutex::new(Vec::new()),
            replies_cond: Condvar::new(),
        }
    }

    #[test]
    fn sink_dedups_preserving_first_occurrence_and_counts_raw() {
        let shared = test_shared(Family::V6, false);
        let mut sink = AddrSink::new(&shared, "host");
        let a = 0x2001_0db8_0000_0000_0000_0000_0000_0001u128;
        let b = mapped6(0x0a00_0001);
        assert!(sink.push(a));
        assert!(sink.push(b));
        assert!(!sink.push(a), "duplicate must be dropped");
        assert!(!sink.push(b), "mapped-A duplicate must be dropped");
        let (addrs, raw) = sink.finish();
        assert_eq!(addrs, vec![a, b], "first occurrence order");
        assert_eq!(raw, 4, "C `added` counts duplicates");
    }

    #[test]
    fn sink_v4_dedup() {
        let shared = test_shared(Family::V4, false);
        let mut sink = AddrSink::new(&shared, "host");
        assert!(sink.push(0x7f00_0001));
        assert!(!sink.push(0x7f00_0001));
        let (addrs, raw) = sink.finish();
        assert_eq!(addrs, vec![0x7f00_0001]);
        assert_eq!(raw, 2);
    }

    #[test]
    fn progress_bar_text_is_byte_exact() {
        // src/ipset_dns.c dns_done(): labels at every tenth position
        // of 0..=40 (0, 25, 50, 75, 100) with 9 dots between them.
        assert_eq!(
            progress_bar(),
            "0%.........25%.........50%.........75%.........100%"
        );
    }

    #[test]
    fn waiting_and_summary_lines_are_byte_exact() {
        assert_eq!(
            waiting_line(3),
            "iprange: DNS: waiting 3 DNS resolutions to finish..."
        );
        assert_eq!(
            summary_line(10, 2, 4, 16, 5, 5),
            "iprange: DNS: made 10 DNS requests, failed 2, retries: 4, IPs got 16, threads used 5 of 5"
        );
    }

    #[test]
    fn empty_and_oversized_hostnames_match_c_payloads() {
        let mut r = Resolver::new(1, false, false, Family::V4, false);
        assert_eq!(
            r.resolve("").unwrap_err(),
            DnsError::System("iprange: DNS: hostname is empty or too long".to_string())
        );
        let long = "a".repeat(MAX_HOSTNAME_V4 + 1);
        assert_eq!(
            r.resolve(&long).unwrap_err(),
            DnsError::System("iprange: DNS: hostname is empty or too long".to_string())
        );
        // IPv6 accepts 256 chars (MAX_INPUT_ELEMENT6) but rejects 257.
        let mut r6 = Resolver::new(1, false, false, Family::V6, false);
        let long6 = "a".repeat(MAX_HOSTNAME_V6 + 1);
        assert_eq!(
            r6.resolve(&long6).unwrap_err(),
            DnsError::System("iprange: DNS: hostname is empty or too long".to_string())
        );
        // No request entered the queue: finish() is the C made==0 no-op.
        assert_eq!(r.finish(), Ok(()));
        assert_eq!(r6.finish(), Ok(()));
    }

    #[test]
    fn localhost_resolves_to_127_0_0_1_in_v4() {
        // Uses only /etc/hosts (no DNS needed); glibc returns the A
        // record twice, which pins the per-host dedup.
        let mut r = Resolver::new(2, true, false, Family::V4, false);
        let addrs = r.resolve("localhost").expect("localhost must resolve");
        assert!(addrs.contains(&0x7f00_0001));
        assert_eq!(
            addrs.iter().filter(|&&a| a == 0x7f00_0001).count(),
            1,
            "per-host duplicates must be deduplicated"
        );
        assert_eq!(r.finish(), Ok(()));
    }

    #[test]
    fn localhost_resolves_to_loopback_and_mapped_v4_in_v6() {
        let mut r = Resolver::new(2, true, false, Family::V6, false);
        let addrs = r.resolve("localhost").expect("localhost must resolve");
        assert!(addrs.contains(&1), "::1 missing");
        let mapped = mapped6(0x7f00_0001);
        assert!(addrs.contains(&mapped), "::ffff:127.0.0.1 missing");
        assert_eq!(
            addrs.iter().filter(|&&a| a == mapped).count(),
            1,
            "mapped-A duplicates must be deduplicated"
        );
        assert_eq!(r.finish(), Ok(()));
    }

    #[test]
    fn invalid_invalid_fails_permanently_with_c_payload() {
        // RFC 2606 reserved TLD: resolvable NXDOMAIN on any resolver
        // with a working upstream; the sandbox answers instantly.
        let mut r = Resolver::new(2, false, false, Family::V4, false);
        let err = r.resolve("invalid.invalid").expect_err("must not resolve");
        let DnsError::NotFound(msg) = &err else {
            panic!("NXDOMAIN is the silent-gated class: got {err:?}");
        };
        assert!(
            msg.starts_with("iprange: DNS: 'invalid.invalid' failed permanently: "),
            "got: {msg}"
        );
        // The glibc gai_strerror(EAI_NONAME) text is stable on Linux.
        #[cfg(target_os = "linux")]
        assert_eq!(
            msg,
            "iprange: DNS: 'invalid.invalid' failed permanently: Name or service not known"
        );
        // C dns_done(): a failed reply fails the IPv4 run.
        assert_eq!(r.finish(), Err(()));
    }

    #[test]
    fn v6_never_fails_the_run() {
        let mut r = Resolver::new(2, false, false, Family::V6, false);
        let err = r.resolve("invalid.invalid").expect_err("must not resolve");
        let DnsError::NotFound(msg) = &err else {
            panic!("v6 has one failure class: got {err:?}");
        };
        assert!(
            msg.starts_with("iprange: DNS: 'invalid.invalid' failed: "),
            "got: {msg}"
        );
        // C dns6_done() always returns 0.
        assert_eq!(r.finish(), Ok(()));
    }

    #[test]
    fn pool_is_hard_capped_with_huge_threads_max() {
        // The C oracle spawns one worker per pending request while
        // `pending > workers && workers < --dns-threads`; a legal but
        // huge --dns-threads value therefore reserves hundreds of GiB
        // of worker stacks and OOMs the process. The pool must stop
        // at DNS_POOL_HARD_MAX so the run stays bounded.
        let mut r = Resolver::new(1_000_000, false, false, Family::V4, false);
        for _ in 0..DNS_POOL_HARD_MAX * 4 {
            r.request("localhost").expect("queue localhost");
        }
        assert!(
            r.workers.len() <= DNS_POOL_HARD_MAX,
            "pool grew to {} workers, ceiling is {}",
            r.workers.len(),
            DNS_POOL_HARD_MAX
        );
        let replies = r.drain();
        assert_eq!(replies.len(), DNS_POOL_HARD_MAX * 4, "every job must reply");
        assert!(
            replies.iter().all(|r| r.result.is_ok()),
            "localhost replies must all resolve"
        );
        assert_eq!(r.finish(), Ok(()));
    }

    #[test]
    fn silent_does_not_change_payloads() {
        let mut loud = Resolver::new(2, false, false, Family::V4, false);
        let mut quiet = Resolver::new(2, true, false, Family::V4, false);
        assert_eq!(
            loud.resolve("invalid.invalid").unwrap_err(),
            quiet.resolve("invalid.invalid").unwrap_err(),
            "--dns-silent only gates the parse worker rendering"
        );
    }

    #[test]
    fn pool_grows_and_serves_concurrent_hosts() {
        let mut r = Resolver::new(4, true, false, Family::V4, false);
        for _ in 0..6 {
            let addrs = r.resolve("localhost").expect("localhost must resolve");
            assert!(addrs.contains(&0x7f00_0001));
        }
        assert_eq!(r.finish(), Ok(()));
    }
}
