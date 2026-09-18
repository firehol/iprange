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
//! The reply path reproduces the C exactly.  The worker prints one
//! `DNS: '%s' = %s` debug line per returned address in resolver order
//! (`src/ipset_dns.c:246-249`), then pushes every address onto the
//! shared reply list (`src/ipset_dns.c:245-247`), and
//! `dns_process_replies()` drains that list head-first, adding one
//! ipset range per address (`src/ipset_dns.c:264-271`, IPv6 twin
//! `src/ipset6_dns.c:215-233`).  Nothing is deduplicated, so one
//! hostname with N answers becomes N entries and N units of the `-v`
//! `lines read` counter, which the C increments once per added entry
//! (`src/ipset.h:89`, `src/ipset6.h:72`).  The list is a stack, so the
//! insertion order is the reverse of the resolver's answer order, and
//! that order is observable: it decides whether `ipset_added_entry()`
//! sees an out-of-order entry and clears the optimized flag
//! (`src/ipset.h:100-121`, `src/ipset6.h:96-105`), which in turn
//! decides the `Loaded non-optimized` and `Optimizing` bookkeeping
//! (`src/ipset_load.c:418`, `src/ipset_optimize.c:47`).
//!
//! C model differences that are unobservable in the output:
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

/// One completed reply: exactly one address of one host, which is one
/// C `DNSREP` node (`src/ipset_dns.c`), or one failed request (the C
/// prints one failure line per request, not per address).
///
/// The caller adds one ipset entry per reply, so handing out one
/// record per address is what makes the C's per-reply
/// `ipset_add_ip_range()` and its `lines` increment observable
/// (`src/ipset_dns.c:264-271`, `src/ipset.h:89`, `src/ipset6.h:72`).
#[derive(Debug)]
pub struct ReplyRecord {
    /// Submission order of the host this reply belongs to (C load
    /// order). Several records share one `seq` when that host returned
    /// several addresses; their relative order is the C reply-stack
    /// order (reverse of the resolver's answer order).
    pub seq: usize,
    /// `Ok` with the single address to add, or the request's failure.
    pub result: Result<Vec<u128>, DnsError>,
}

/// The replies of one file batch and the `-v` line that C `dns_done()`
/// prints after its last `dns_process_replies()` call.
///
/// The caller (the parse worker) adds the replies to the ipset, and an
/// out-of-order add prints `NON-OPTIMIZED ...`
/// (`src/ipset.h:100-121`, `src/ipset6.h:96-105`).  C prints the
/// summary after those additions (`src/ipset_dns.c:368-375`), so the
/// summary is emitted when this batch is dropped rather than inside
/// [`Resolver::drain`].
#[must_use = "consume the batch: its replies are the addresses to add and dropping it prints the C summary"]
pub struct Batch {
    records: std::vec::IntoIter<ReplyRecord>,
    summary: Option<String>,
}

impl Iterator for Batch {
    type Item = ReplyRecord;

    fn next(&mut self) -> Option<ReplyRecord> {
        self.records.next()
    }
}

impl Drop for Batch {
    fn drop(&mut self) {
        if let Some(line) = self.summary.take() {
            eprintln!("{line}");
        }
    }
}

/// The C reply list of the current file batch, plus the number of
/// requests that terminated. The drain waits on `jobs_done` because
/// the number of reply addresses is not the number of requests; this is
/// C `dns_requests_pending` reaching zero (`src/ipset_dns.c:341-345`).
#[derive(Default)]
struct Pending {
    records: Vec<ReplyRecord>,
    jobs_done: usize,
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
    /// drain time) and the count of requests that terminated;
    /// workers update both under one lock and notify `replies_cond`
    /// so the drain can wait for the batch.
    pending: Mutex<Pending>,
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
            pending: Mutex::new(Pending::default()),
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

    /// Replies of one file batch as a plain vector, for the unit tests
    /// that assert batch behaviour rather than the streaming contract.
    #[cfg(test)]
    fn drain_records(&mut self) -> Vec<ReplyRecord> {
        self.drain().collect()
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
    fn submit(&mut self, _host: &str) -> Result<Receiver<Result<Vec<u128>, DnsError>>, DnsError> {
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
    pub fn drain(&mut self) -> Batch {
        let (shared, made, batch_jobs) = {
            let shared = self.shared.clone();
            let made = shared.stats.lock().unwrap().made;
            (shared, made, self.next_seq - self.batch_start)
        };
        if made == 0 || batch_jobs == 0 {
            self.batch_start = self.next_seq;
            return Batch {
                records: Vec::new().into_iter(),
                summary: None,
            };
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

        // Wait until every request of the batch terminated, which is
        // C `dns_done()`'s `while (pending)` condition. The count is
        // requests, not reply addresses, and both are updated under
        // one lock by the workers, so a partial host can never be
        // observed. Resetting the batch here (rather than indexing it
        // by absolute sequence numbers) is what makes consecutive
        // files independent, as each C file load calls dns_done().
        let threads_used = self.workers.len() as u32;
        let mut records = {
            let mut pending = shared.pending.lock().unwrap();
            while pending.jobs_done < batch_jobs {
                pending = shared.replies_cond.wait(pending).unwrap();
            }
            pending.jobs_done = 0;
            std::mem::take(&mut pending.records)
        };
        // Stable: records of one host keep the reply-stack order, and
        // the hosts keep the load order of the file.
        records.sort_by_key(|r| r.seq);

        let summary = {
            let stats = shared.stats.lock().unwrap();
            let (made, failed, retries, found) =
                (stats.made, stats.failed, stats.retries, stats.found);
            // C dns_done(): debug wins over the progress bar; the IPv6
            // pool prints no summary at all (src/ipset6_dns.c dns6_done).
            if shared.family == Family::V4 {
                if shared.debug {
                    Some(summary_line(
                        made,
                        failed,
                        retries,
                        found,
                        threads_used,
                        self.threads_max,
                    ))
                } else if shared.progress {
                    Some(progress_bar())
                } else {
                    None
                }
            } else {
                None
            }
            // C dns_reset_stats() runs after dns_done() printed; the
            // counters are reset with the batch, so the next file is
            // independent. The pool stays alive.
        };
        {
            let mut stats = shared.stats.lock().unwrap();
            *stats = Stats::default();
        }
        self.batch_start = self.next_seq;

        Batch {
            records: records.into_iter(),
            summary,
        }
    }

    /// Wait for all in-flight work and print the C summary/progress
    /// text; Err when the C resolver would exit non-zero.
    ///
    /// Run-end counterpart of C `dns_done()`: the per-file drain
    /// (called by the loader after every file) already collected the
    /// replies and texts; this final call only drains any remaining
    /// batch, closes the job queue, and joins the workers.
    pub fn finish(&mut self) -> Result<(), ()> {
        // C dns_done(): a failed IPv4 reply fails the run; the IPv6
        // side never fails. Consuming the batch adds nothing to the
        // ipset here (the loader already drained its own file) and
        // dropping it prints the summary line, exactly where C prints
        // it after the final reply drain.
        let records: Vec<ReplyRecord> = self.drain().collect();
        let any_failed = records.iter().any(|record| record.result.is_err());
        let failed = self.shared.family == Family::V4 && any_failed;
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
/// record one reply per returned address for the per-file drain, like
/// the C reply stack.
fn worker_loop(shared: Arc<Shared>) {
    while let Some(job) = next_job(&shared) {
        let result = resolve_host(&shared, &job.host);
        let _ = job.reply.send(result.clone());
        {
            let mut pending = shared.pending.lock().unwrap();
            match result {
                // C dns_thread_resolve(): one DNSREP per address, so
                // every address of one host is stacked together and
                // other hosts can only be interleaved between hosts.
                Ok(addrs) => {
                    for addr in addrs {
                        pending.records.push(ReplyRecord {
                            seq: job.seq,
                            result: Ok(vec![addr]),
                        });
                    }
                }
                // C dns_request_failed(): a failed request produces no
                // address and one diagnostic line.
                Err(error) => pending.records.push(ReplyRecord {
                    seq: job.seq,
                    result: Err(error),
                }),
            }
            pending.jobs_done += 1;
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
            let addrs = collect_addrs(shared, host, result);
            unsafe { libc::freeaddrinfo(result) };
            let mut stats = shared.stats.lock().unwrap();
            stats.finished += 1;
            // C dns_request_done(added): added is the number of reply
            // nodes the worker stacked, and zero added counts as a
            // failure even when getaddrinfo succeeded.
            if addrs.is_empty() {
                stats.failed += 1;
            } else {
                stats.found += addrs.len() as u64;
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
/// the sink (debug lines in answer order, the reply list). Returns the
/// reply list in C insertion order.
#[cfg(unix)]
fn collect_addrs(shared: &Shared, host: &str, result: *mut libc::addrinfo) -> Vec<u128> {
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
                let addrs = collect_socket_addrs(shared, host, iter);
                let mut stats = shared.stats.lock().unwrap();
                stats.finished += 1;
                if addrs.is_empty() {
                    stats.failed += 1;
                } else {
                    stats.found += addrs.len() as u64;
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
) -> Vec<u128> {
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

/// Address collection shared by both platforms: the C per-address
/// debug line (IPv4 only, printed while the worker walks the answer
/// list) and the reply list itself, which the C stacks and drains
/// head-first, so the addresses come back in the reverse of the
/// resolver's answer order and none of them is dropped.
struct AddrSink<'a> {
    shared: &'a Shared,
    host: &'a str,
    /// Answers in resolver order; `finish` reverses it into the C
    /// reply-stack (insertion) order.
    addrs: Vec<u128>,
}

impl<'a> AddrSink<'a> {
    fn new(shared: &'a Shared, host: &'a str) -> Self {
        AddrSink {
            shared,
            host,
            addrs: Vec::new(),
        }
    }

    /// One answer of the resolver, in answer order. Every answer is
    /// kept: the C adds a range per reply node and counts a `lines`
    /// unit per added entry, duplicates included.
    fn push(&mut self, value: u128) {
        if self.shared.debug && self.shared.family == Family::V4 {
            // C ipset_dns.c:246-249 (no IPv6 equivalent).
            eprintln!("iprange: DNS: '{}' = {}", self.host, fmt_v4(value as u32));
        }
        self.addrs.push(value);
    }

    /// The reply list in C insertion order (last answer first).
    fn finish(self) -> Vec<u128> {
        let mut addrs = self.addrs;
        addrs.reverse();
        addrs
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
            pending: Mutex::new(Pending::default()),
            replies_cond: Condvar::new(),
        }
    }

    /// `dns_process_replies()` adds a range per reply node and
    /// `ipset_added_entry()` counts a `lines` unit per added entry
    /// (`src/ipset.h:89`, `src/ipset6.h:72`), so the sink must keep
    /// every answer, in the C reply-stack order (the list is a stack,
    /// `src/ipset_dns.c:245-247`).
    #[test]
    fn sink_keeps_every_answer_in_reply_stack_order() {
        let shared = test_shared(Family::V6, false);
        let mut sink = AddrSink::new(&shared, "host");
        let a = 0x2001_0db8_0000_0000_0000_0000_0000_0001u128;
        let b = mapped6(0x0a00_0001);
        sink.push(a);
        sink.push(b);
        sink.push(a);
        sink.push(b);
        assert_eq!(
            sink.finish(),
            vec![b, a, b, a],
            "no dedup, and reversed into insertion order"
        );
    }

    #[test]
    fn sink_v4_keeps_duplicate_answers() {
        let shared = test_shared(Family::V4, false);
        let mut sink = AddrSink::new(&shared, "host");
        sink.push(0x7f00_0001);
        sink.push(0x7f00_0001);
        assert_eq!(
            sink.finish(),
            vec![0x7f00_0001, 0x7f00_0001],
            "the C adds both, which makes the set non-optimized"
        );
    }

    /// One reply record per address, and every address of the host is
    /// handed out: dropping either half loses the C `lines` accounting.
    #[test]
    fn drain_hands_out_one_record_per_reply_address() {
        let mut r = Resolver::new(2, true, false, Family::V4, false);
        let addrs = r.resolve("localhost").expect("localhost must resolve");
        assert!(!addrs.is_empty(), "the resolver must answer localhost");
        assert!(
            addrs.iter().all(|&a| a == 0x7f00_0001),
            "localhost v4 must answer 127.0.0.1 only, got {addrs:?}"
        );
        let records = r.drain_records();
        assert_eq!(
            records.len(),
            addrs.len(),
            "one reply per address, duplicates included"
        );
        for record in &records {
            let addrs = record
                .result
                .as_ref()
                .expect("every reply of a resolved host carries addresses");
            assert_eq!(
                addrs.len(),
                1,
                "one reply per address means one address per reply: {records:?}"
            );
        }
        let added: Vec<u128> = records
            .iter()
            .flat_map(|rec| rec.result.clone().unwrap_or_default())
            .collect();
        assert_eq!(added, addrs, "the drain must not drop or reorder replies");
        assert_eq!(r.finish(), Ok(()));
    }

    /// C calls `dns_done()` per file and resets the counters there, so
    /// each file's batch is independent. Indexing the reply list by
    /// absolute sequence numbers hangs the second DNS-using file.
    ///
    /// The host is the dotted-numeric loopback `127.0.0.1`, which every
    /// supported platform's `getaddrinfo` answers locally with exactly one
    /// address and no resolver traffic, so one request yields exactly one
    /// reply and the batch sizes are the point of the test rather than a
    /// property of the host. It deliberately avoids the glibc-only hex
    /// numeric forms (`0x7f000001`), which Windows answers as an unknown
    /// host: those forms are pinned by
    /// [`hex_numeric_hostnames_answer_locally_where_the_authority_answers`] on
    /// the answering platforms instead.
    #[test]
    fn multi_file_batches_drain_independently() {
        let mut r = Resolver::new(3, true, false, Family::V4, false);
        for _ in 0..4 {
            r.request("127.0.0.1").expect("queue file 1");
        }
        let first = r.drain_records();
        assert_eq!(first.len(), 4, "file 1: one reply per request");
        for (i, rec) in first.iter().enumerate() {
            assert_eq!(rec.seq, i, "file 1 reply {i} keeps the load order");
            assert_eq!(rec.result.as_deref().unwrap(), &[0x7f00_0001]);
        }
        for _ in 0..2 {
            r.request("127.0.0.1").expect("queue file 2");
        }
        let second = r.drain_records();
        assert_eq!(second.len(), 2, "file 2 must not wait for file 1");
        for (i, rec) in second.iter().enumerate() {
            assert_eq!(rec.seq, 4 + i, "file 2 reply {i} continues the load order");
            assert_eq!(rec.result.as_deref().unwrap(), &[0x7f00_0001]);
        }
        assert_eq!(r.finish(), Ok(()));
    }

    /// The unix resolvers this project qualifies — glibc, musl
    /// (`__inet_aton` parses with strtoul base 0), the KAME-lineage BSDs
    /// (freebsd/netbsd/dragonfly `explore_numeric` calls `inet_aton` for
    /// AF_INET) and Apple's Libinfo resolver (`_gai_numerichost` falls
    /// back to `_inet_aton_check`, 0x=hex; ios/tvos/watchos share the
    /// same system library and answer with macos) — accept the
    /// inet_at-style hex numeric forms and answer them locally, so
    /// `0x7f000001` resolves to 127.0.0.1 with no resolver traffic. This
    /// is the authority the Go side mirrors (dns_numeric_answer.go lists
    /// the same set). Windows has no such form and reports
    /// `WSAHOST_NOT_FOUND`; android (bionic) refuses too (its live
    /// AF_INET path is `inet_pton`; the `inet_aton` call sits inside
    /// `#if 0 /*X/Open spec*/` — dns_numeric_refuse.go cites the tag),
    /// so the compatibility is pinned on the answering set only; the
    /// platform-independent batching above runs everywhere. Sources read
    /// 2026-09-17/18; a native leg must still execute this pin wherever
    /// it runs.
    #[cfg(any(
        target_os = "linux",
        target_os = "macos",
        target_os = "ios",
        target_os = "tvos",
        target_os = "watchos",
        target_os = "freebsd",
        target_os = "netbsd",
        target_os = "dragonfly"
    ))]
    #[test]
    fn hex_numeric_hostnames_answer_locally_where_the_authority_answers() {
        let mut r = Resolver::new(2, true, false, Family::V4, false);
        for host in ["0x7f000001", "0x0A000001"] {
            r.request(host)
                .unwrap_or_else(|e| panic!("{host} must be answered: {e}"));
        }
        let replies = r.drain_records();
        assert_eq!(replies.len(), 2, "one reply per hex request");
        assert_eq!(replies[0].result.as_deref().unwrap(), &[0x7f00_0001]);
        assert_eq!(replies[1].result.as_deref().unwrap(), &[0x0a00_0001]);
        assert_eq!(r.finish(), Ok(()));
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
        // Uses only /etc/hosts and the myhostname NSS module (no DNS
        // needed). How many 127.0.0.1 answers the resolver returns is
        // the environment's own answer (glibc can return the same
        // address twice here); the port must hand every one of them to
        // the ipset, because the C counts a `lines` unit per added
        // entry. The reply count is pinned against the drain instead
        // of a hard-coded number.
        let mut r = Resolver::new(2, true, false, Family::V4, false);
        let addrs = r.resolve("localhost").expect("localhost must resolve");
        assert!(addrs.contains(&0x7f00_0001));
        assert_eq!(r.drain_records().len(), addrs.len());
        assert_eq!(r.finish(), Ok(()));
    }

    #[test]
    fn localhost_resolves_to_loopback_and_mapped_v4_in_v6() {
        let mut r = Resolver::new(2, true, false, Family::V6, false);
        let addrs = r.resolve("localhost").expect("localhost must resolve");
        assert!(addrs.contains(&1), "::1 missing");
        let mapped = mapped6(0x7f00_0001);
        assert!(addrs.contains(&mapped), "::ffff:127.0.0.1 missing");
        assert_eq!(r.drain_records().len(), addrs.len());
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
        let replies = r.drain_records();
        let mut seqs: Vec<usize> = replies.iter().map(|rec| rec.seq).collect();
        seqs.sort_unstable();
        seqs.dedup();
        assert_eq!(
            seqs.len(),
            DNS_POOL_HARD_MAX * 4,
            "every job must reply ({} replies for {} jobs)",
            replies.len(),
            DNS_POOL_HARD_MAX * 4
        );
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
