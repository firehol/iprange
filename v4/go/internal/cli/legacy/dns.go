// Legacy DNS resolution pool: a bounded set of resolver workers sized
// by --dns-threads (default 5), a shared job queue, per-host error
// isolation, and the exact C diagnostics (src/ipset_dns.c IPv4 pool,
// src/ipset6_dns.c IPv6 pool, loader call sites src/ipset_load.c /
// src/ipset6_load.c). Go has no getaddrinfo: net.DefaultResolver
// LookupIP applies the same family policy (v4: A records only via
// "ip4"; v6: AF_UNSPEC via "ip", AAAA+A with IPv4 mapped). The Rust
// port (v4/rust/iprange-cli/src/legacy/dns.rs) is the structural
// reference; the recorded drain-shape deviation below fixes a
// multi-file hang that the Rust arithmetic has (the C oracle drains
// per file and never hangs).

package legacy

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
)

// C MAX_INPUT_ELEMENT (src/ipset_load.c): the maximum hostname length
// accepted in IPv4 mode.
const maxHostnameV4 = 255

// C MAX_INPUT_ELEMENT6 (src/ipset6_load.h): IPv6 mode.
const maxHostnameV6 = 256

// C d->tries = 20 (src/ipset_dns.c, src/ipset6_dns.c): the number of
// retriable-error re-attempts before a permanent failure.
const dnsRetries = 20

// dnsPoolHardMax is the hard ceiling for the DNS worker pool. The C
// oracle eagerly spawns up to --dns-threads workers while requests
// are pending, so a legal-but-large value (up to INT_MAX) combined
// with a large host file makes the C tool (8 MiB stacks) and the Rust
// port (2 MiB stacks) reserve hundreds of GiB and get OOM-killed.
// The released default is 5 and the legacy suite uses at most 4, so
// this ceiling is unobservable wherever C itself survives; it only
// bounds the pool in the regime where C dies. 128 workers is far
// beyond useful resolver parallelism (the OOM fix, SOW-0028).
const dnsPoolHardMax = 128

// DnsErrorClass selects the C dns_request_failed() error class, which
// decides whether the parse worker may silence the line.
type DnsErrorClass uint8

const (
	// DnsErrorNotFound is the host-failure class (EAI_NONAME and
	// friends): the C line is gated by --dns-silent.
	DnsErrorNotFound DnsErrorClass = iota
	// DnsErrorSystem is the resolver-infrastructure class (EAI_SYSTEM,
	// EAI_MEMORY, ...): always printed.
	DnsErrorSystem
)

// DnsError is one host resolution failure; the parse worker renders
// Line and decides the file outcome. Line is the full pre-rendered C
// stderr line.
type DnsError struct {
	Class DnsErrorClass
	Line  string
}

func (e *DnsError) Error() string { return e.Line }

// silentGated reports whether --dns-silent may suppress the
// pre-rendered line: the not-found class (C EAI_NONAME/EAI_FAIL/
// EAI_FAMILY and the EAI_AGAIN permanent class) is gated, the
// system class always prints (parse.go's dnsSilentGated contract).
func (e *DnsError) silentGated() bool { return e.Class == DnsErrorNotFound }

// ReplyRecord is one completed reply, kept in submission order for
// the per-file drain; the parse worker renders the failure lines and
// decides file failure exactly like the C reply processing.
type ReplyRecord struct {
	Seq   int
	Addrs []IP128
	Err   error // *DnsError when the host failed
}

// dnsStats mirrors the C pool counters (src/ipset_dns.c): made counts
// requests that entered the queue, finished requests that terminated,
// found addresses seen (including per-host duplicates, like the C
// reply stack), failed requests that terminated with zero addresses,
// retries retriable-error re-attempts.
type dnsStats struct {
	made     uint64
	finished uint64
	found    uint64
	failed   uint64
	retries  uint64
}

// dnsJob is one queued hostname: the submission sequence number (C
// load order) and the per-job reply channel (per-host error
// isolation).
type dnsJob struct {
	seq   int
	host  string
	reply chan dnsOutcome
}

// dnsOutcome is the resolution result of one host.
type dnsOutcome struct {
	addrs []IP128
	err   error
}

// dnsShared is the pool state shared with the workers: the C globals,
// the unbounded job queue, and the completed replies in completion
// order (sorted by seq at drain time).
type dnsShared struct {
	family   Family
	silent   bool
	progress bool
	debug    bool

	statsMu sync.Mutex
	stats   dnsStats

	jobsMu   sync.Mutex
	jobsCond *sync.Cond
	queue    []dnsJob
	closed   bool

	repliesMu   sync.Mutex
	repliesCond *sync.Cond
	replies     []ReplyRecord
}

// dnsWorker is one worker goroutine; done is closed when the worker
// exits (the C pthread join).
type dnsWorker struct {
	done chan struct{}
}

// Resolver is the legacy DNS pool: bounded worker goroutines,
// C-equivalent progress and summary messages.
type Resolver struct {
	shared     *dnsShared
	workers    []*dnsWorker
	threadsMax uint32
	// nextSeq is the sequence of the next job to submit (C load
	// order); batchStart is the first sequence of the current
	// per-file batch.
	nextSeq    int
	batchStart int
	// spawnFailed is set once a worker spawn fails: further spawn
	// attempts in the same run are pointless (C retries and prints
	// one line per attempt, a storm under resource exhaustion); the
	// pool keeps serving with the workers that exist.
	spawnFailed bool
}

// NewResolver creates the pool: threads is the C thread count
// (--dns-threads, default 5), silent and progress map to the DNS
// flags, fam selects the record set, debug enables the C -v
// diagnostics.
func NewResolver(threads uint32, silent, progress bool, fam Family, debug bool) *Resolver {
	shared := &dnsShared{
		family:   fam,
		silent:   silent,
		progress: progress,
		debug:    debug,
	}
	shared.jobsCond = sync.NewCond(&shared.jobsMu)
	shared.repliesCond = sync.NewCond(&shared.repliesMu)
	// C validates --dns-threads as >= 1; clamp defensively.
	if threads == 0 {
		threads = 1
	}
	return &Resolver{shared: shared, threadsMax: threads}
}

// Request queues one hostname for resolution and returns immediately;
// the caller drains the per-file batch with Drain. The pool grows
// exactly like the C (pending requests trigger lazy worker spawn up
// to --dns-threads, hard-capped at dnsPoolHardMax).
func (r *Resolver) Request(host string) error {
	if _, err := r.submit(host); err != nil {
		return err
	}
	return nil
}

// submit validates, counts, and queues one hostname; it returns the
// per-job reply channel. The C diagnostic lines that happen while the
// request is in flight (retry lines, per-address debug lines) print
// from inside resolution.
func (r *Resolver) submit(host string) (<-chan dnsOutcome, error) {
	// C iprange_cstrnlen(): the hostname is a C string, so any
	// interior NUL truncates it (fgets can deliver NUL bytes).
	if i := strings.IndexByte(host, 0); i >= 0 {
		host = host[:i]
	}
	maxLen := maxHostnameV4
	if r.shared.family == V6 {
		maxLen = maxHostnameV6
	}
	if host == "" || len(host) > maxLen {
		// C dns_request() prints this and returns -1 (the loader
		// then fails the file); always printed (silent does not
		// gate it), hence the System class.
		return nil, &DnsError{Class: DnsErrorSystem, Line: "iprange: DNS: hostname is empty or too long"}
	}

	r.shared.statsMu.Lock()
	r.shared.stats.made++
	// C dns_request_add(): pending counts requests that entered the
	// queue and have not terminated; grow the pool while that
	// exceeds the workers and the max is not reached. With the
	// batch API the loader queues the whole file first, so pending
	// accumulates and the pool reaches --dns-threads exactly like C.
	pending := r.shared.stats.made - r.shared.stats.finished
	grow := !r.spawnFailed &&
		pending > uint64(len(r.workers)) &&
		len(r.workers) < int(r.threadsMax) &&
		len(r.workers) < dnsPoolHardMax
	r.shared.statsMu.Unlock()
	if grow {
		// C dns_request_add() debug line (IPv4 only; the IPv6
		// request_add has no equivalent).
		if r.shared.debug && r.shared.family == V4 {
			fmt.Fprintln(os.Stderr, "iprange: Creating new DNS thread")
		}
		if err := r.spawnWorker(); err != nil {
			// C pthread_create failure text (printed once per run;
			// C prints it per attempt).
			r.spawnFailed = true
			fmt.Fprintln(os.Stderr, "iprange: Cannot create DNS thread.")
			if len(r.workers) == 0 {
				// C dns_request_add(): with no worker yet the
				// request is rolled back (pending--, made--) and
				// dns_request() returns -1 so the loader fails the
				// file cleanly; requests already queued stay queued
				// for the workers that exist. Go cannot fail to
				// start a goroutine, so this is unreachable; it is
				// kept for the exact C contract.
				r.shared.statsMu.Lock()
				r.shared.stats.made--
				r.shared.statsMu.Unlock()
				return nil, &DnsError{Class: DnsErrorSystem, Line: "iprange: Cannot create DNS thread."}
			}
		}
	}

	r.shared.jobsMu.Lock()
	closed := r.shared.closed
	r.shared.jobsMu.Unlock()
	if closed {
		// The queue is closed (Finish ran): resolve synchronously so
		// the caller still gets an answer.
		res := resolveHost(r.shared, host)
		reply := make(chan dnsOutcome, 1)
		reply <- res
		return reply, nil
	}

	reply := make(chan dnsOutcome, 1)
	job := dnsJob{seq: r.nextSeq, host: host, reply: reply}
	r.nextSeq++
	// Enqueue while holding the jobs lock so a worker cannot miss
	// the notification between checking the queue and waiting on
	// the condvar (C pthread_cond_signal has the same contract).
	r.shared.jobsMu.Lock()
	r.shared.queue = append(r.shared.queue, job)
	r.shared.jobsCond.Broadcast()
	r.shared.jobsMu.Unlock()
	return reply, nil
}

// Drain collects the replies of the current per-file batch in load
// order, prints the C per-file diagnostics, and resets the per-file
// counters (C dns_done() + dns_reset_stats()). Every reply is
// returned, failed ones included: the caller renders the per-host C
// failure lines and decides whether the run fails.
func (r *Resolver) Drain() []ReplyRecord {
	shared := r.shared
	r.shared.statsMu.Lock()
	made := r.shared.stats.made
	r.shared.statsMu.Unlock()
	batchLen := r.nextSeq - r.batchStart
	if made == 0 || batchLen == 0 {
		r.batchStart = r.nextSeq
		return nil
	}

	// C dns_done() wait loop: while requests are pending it prints
	// the debug "waiting" line (or the partial progress bar) once
	// per second. The drain below completes when the batch
	// finishes, so only the first observation is reproduced; the
	// partial per-second bars collapse into the final bar
	// (accepted timing deviation, recorded in SOW-0028).
	if shared.family == V4 && shared.debug {
		r.shared.statsMu.Lock()
		pending := r.shared.stats.made - r.shared.stats.finished
		r.shared.statsMu.Unlock()
		if pending > 0 {
			fmt.Fprintln(os.Stderr, waitingLine(pending))
		}
	}

	// Wait for every job of the batch to record its reply. Every
	// previous drain removed its batch, so the pending records are
	// exactly this batch, in completion order (sorted below). The
	// Rust reference waits on batch_start+batch_len absolute
	// indices instead and deadlocks on the second DNS-using file
	// (reproduced: rust iprange hangs, rc=124); the C oracle drains
	// per file and resets the counters, so the length-based wait is
	// the C behavior.
	shared.repliesMu.Lock()
	for len(shared.replies) < batchLen {
		shared.repliesCond.Wait()
	}
	batch := make([]ReplyRecord, batchLen)
	copy(batch, shared.replies[:batchLen])
	shared.replies = shared.replies[:0]
	shared.repliesMu.Unlock()
	sort.Slice(batch, func(i, j int) bool { return batch[i].Seq < batch[j].Seq })

	threadsUsed := uint32(len(r.workers))
	r.shared.statsMu.Lock()
	made, failed, retries, found := r.shared.stats.made, r.shared.stats.failed, r.shared.stats.retries, r.shared.stats.found
	r.shared.statsMu.Unlock()
	if shared.family == V4 {
		// C dns_done(): debug wins over the progress bar.
		if shared.debug {
			fmt.Fprintln(os.Stderr, summaryLine(made, failed, retries, found, threadsUsed, r.threadsMax))
		} else if shared.progress {
			fmt.Fprintln(os.Stderr, progressBar())
		}
	}

	// C dns_reset_stats() after dns_done(); the pool stays alive
	// for the next file.
	r.shared.statsMu.Lock()
	r.shared.stats = dnsStats{}
	r.shared.statsMu.Unlock()
	r.batchStart = r.nextSeq
	return batch
}

// Finish waits for all in-flight work and prints the C summary/
// progress text; it reports true when the IPv4 run must fail (any
// failed reply); the IPv6 run never fails (C dns6_done() returns 0).
// It drains any remaining batch, closes the job queue, and joins the
// workers.
func (r *Resolver) Finish() bool {
	batch := r.Drain()
	failed := false
	if r.shared.family == V4 {
		// C dns_done(): a failed IPv4 reply fails the run.
		for _, rec := range batch {
			if rec.Err != nil {
				failed = true
				break
			}
		}
	}
	r.shared.jobsMu.Lock()
	r.shared.closed = true
	r.shared.jobsCond.Broadcast()
	r.shared.jobsMu.Unlock()
	for _, w := range r.workers {
		<-w.done
	}
	r.workers = nil
	return failed
}

// spawnWorker starts one worker goroutine (C pthread_create +
// pthread_detach, src/ipset_dns.c dns_request_add). Go cannot fail
// to start a goroutine, so the error return is always nil; the
// caller keeps the C spawn-failure handling for the exact contract.
func (r *Resolver) spawnWorker() error {
	w := &dnsWorker{done: make(chan struct{})}
	go func() {
		defer close(w.done)
		workerLoop(r.shared)
	}()
	r.workers = append(r.workers, w)
	return nil
}

// workerLoop takes jobs from the queue until it is closed, resolves
// each host exactly like the C worker thread, answers the caller of
// that host, and records the reply for the per-file drain.
func workerLoop(shared *dnsShared) {
	for {
		job, ok := nextJob(shared)
		if !ok {
			return
		}
		res := resolveHost(shared, job.host)
		job.reply <- res
		shared.repliesMu.Lock()
		shared.replies = append(shared.replies, ReplyRecord{Seq: job.seq, Addrs: res.addrs, Err: res.err})
		shared.repliesCond.Broadcast()
		shared.repliesMu.Unlock()
	}
}

// nextJob blocks for the next queued job; ok is false when the queue
// is closed and empty (Finish ran) and the worker must exit.
func nextJob(shared *dnsShared) (dnsJob, bool) {
	shared.jobsMu.Lock()
	defer shared.jobsMu.Unlock()
	for {
		if len(shared.queue) > 0 {
			job := shared.queue[0]
			shared.queue = shared.queue[1:]
			return job, true
		}
		if shared.closed {
			return dnsJob{}, false
		}
		shared.jobsCond.Wait()
	}
}

// resolveHost resolves one host with the C retry/error semantics.
// It runs on a worker (or inline when the queue is closed); it
// prints the retry and per-address debug lines itself and returns the
// final DnsError payload for the parse worker to render.
func resolveHost(shared *dnsShared, host string) dnsOutcome {
	// C hints: AF_INET (v4) / AF_UNSPEC (v6), SOCK_DGRAM, service
	// "80" (src/ipset_dns.c dns_thread_resolve). Go has no
	// getaddrinfo; LookupIP applies the same family policy and the
	// service port is irrelevant to the record set.
	network := "ip4"
	if shared.family == V6 {
		network = "ip"
	}
	tries := dnsRetries
	for {
		ips, err := net.DefaultResolver.LookupIP(context.Background(), network, host)
		if err == nil {
			addrs, raw := collectAddrs(shared, host, ips)
			shared.statsMu.Lock()
			shared.stats.finished++
			// C dns_request_done(): zero addresses counts as a
			// failure even when the lookup succeeded.
			if raw == 0 {
				shared.stats.failed++
			} else {
				shared.stats.found += uint64(raw)
			}
			shared.statsMu.Unlock()
			return dnsOutcome{addrs: addrs}
		}

		if retriable(err) && tries > 0 {
			// C dns_request_failed(): the retry happens regardless
			// of --dns-silent; only the message is gated.
			if !shared.silent {
				fmt.Fprintf(os.Stderr, "iprange: DNS: '%s' will be retried: %s\n", host, gaiText(err))
			}
			tries--
			shared.statsMu.Lock()
			shared.stats.retries++
			shared.statsMu.Unlock()
			continue
		}

		// Terminal failure: C dns_request_done(d, 0) counts it as
		// finished with zero added addresses (the pending counter
		// must reach zero for the drain wait to end).
		shared.statsMu.Lock()
		shared.stats.finished++
		shared.stats.failed++
		shared.statsMu.Unlock()
		line, class := failureLine(shared.family, host, err, tries == 0)
		return dnsOutcome{err: &DnsError{Class: class, Line: line}}
	}
}

// retriable reports whether a Go resolver error corresponds to the C
// EAI_AGAIN class (temporary or timed-out resolution; the pure-Go
// and cgo resolvers both surface these on DNSError).
func retriable(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && (dnsErr.IsTimeout || dnsErr.IsTemporary) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// gaiText maps a Go resolver error to the closest glibc
// gai_strerror text: NXDOMAIN pins EAI_NONAME ("Name or service not
// known", the text the legacy suite pins for invalid.invalid) and
// timeout/temporary errors pin EAI_AGAIN ("Temporary failure in name
// resolution"). Everything else renders as the Go error.
func gaiText(err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		switch {
		case dnsErr.IsNotFound:
			return "Name or service not known"
		case dnsErr.IsTimeout || dnsErr.IsTemporary:
			return "Temporary failure in name resolution"
		}
	}
	return err.Error()
}

// failureLine renders the C terminal-failure line and its class:
// EAI_AGAIN exhausted (v4 silent-gated), NXDOMAIN and other DNS
// failures (v4 silent-gated, "failed permanently"), resolver
// infrastructure errors (v4 always-printed, "system error"), and the
// single IPv6 class ("failed", silent-gated).
func failureLine(fam Family, host string, err error, retriesExhausted bool) (string, DnsErrorClass) {
	if fam == V6 {
		return fmt.Sprintf("iprange: DNS: '%s' failed: %s", host, gaiText(err)), DnsErrorNotFound
	}
	if retriesExhausted {
		return fmt.Sprintf("iprange: DNS: '%s' failed permanently after retries: %s", host, gaiText(err)), DnsErrorNotFound
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return fmt.Sprintf("iprange: DNS: '%s' failed permanently: %s", host, gaiText(err)), DnsErrorNotFound
	}
	// EAI_SYSTEM analog: the resolver machinery failed (always
	// printed, like the C strerror(errno) line).
	return fmt.Sprintf("iprange: DNS: '%s' system error: %s", host, gaiText(err)), DnsErrorSystem
}

// collectAddrs converts each lookup result with the C family policy
// (v4: AF_INET only; v6: AAAA raw and A mapped to ::ffff:a.b.c.d)
// and feeds the sink (debug lines, dedup, raw count).
func collectAddrs(shared *dnsShared, host string, ips []net.IP) ([]IP128, int) {
	sink := newAddrSink(shared, host)
	for _, ip := range ips {
		var value IP128
		switch shared.family {
		case V4:
			v4 := ip.To4()
			if v4 == nil {
				continue
			}
			// Network byte order, read big-endian exactly like C
			// u32::from_be(sin_addr.s_addr).
			value = IP128{Lo: uint64(binary.BigEndian.Uint32(v4))}
		default:
			if v4 := ip.To4(); v4 != nil {
				value = mapped6(binary.BigEndian.Uint32(v4))
			} else {
				b := ip.To16()
				value = IP128{
					Hi: binary.BigEndian.Uint64(b[:8]),
					Lo: binary.BigEndian.Uint64(b[8:]),
				}
			}
		}
		sink.push(value)
	}
	return sink.addrs, sink.raw
}

// addrSink collects one reply's addresses: the C per-address debug
// line (IPv4 only), first-occurrence dedup of the returned slice
// (the C ipset dedup), and the raw address count (C added, includes
// per-host duplicates).
type addrSink struct {
	shared *dnsShared
	host   string
	addrs  []IP128
	seen   map[IP128]struct{}
	raw    int
}

func newAddrSink(shared *dnsShared, host string) *addrSink {
	return &addrSink{shared: shared, host: host, seen: make(map[IP128]struct{})}
}

// push accepts one address from the reply list; it reports whether
// the address is new (inserted into the returned slice).
func (s *addrSink) push(value IP128) bool {
	s.raw++
	if s.shared.debug && s.shared.family == V4 {
		// C ipset_dns.c per-address line (no IPv6 equivalent).
		fmt.Fprintf(os.Stderr, "iprange: DNS: '%s' = %s\n", s.host, FmtAddrV4(IP128{Lo: value.Lo}))
	}
	if _, dup := s.seen[value]; dup {
		return false
	}
	s.seen[value] = struct{}{}
	s.addrs = append(s.addrs, value)
	return true
}

// mapped6 is the IPv4-mapped IPv6 form of a v4 address (C
// ipv4_to_mapped6(), src/iprange6.h).
func mapped6(v4 uint32) IP128 {
	return IP128{Lo: 0x0000_FFFF_0000_0000 | uint64(v4)}
}

// waitingLine is the src/ipset_dns.c debug "waiting" line.
func waitingLine(pending uint64) string {
	return fmt.Sprintf("iprange: DNS: waiting %d DNS resolutions to finish...", pending)
}

// summaryLine is the src/ipset_dns.c final -v summary line.
func summaryLine(made, failed, retries, found uint64, threads, threadsMax uint32) string {
	return fmt.Sprintf(
		"iprange: DNS: made %d DNS requests, failed %d, retries: %d, IPs got %d, threads used %d of %d",
		made, failed, retries, found, threads, threadsMax)
}

// progressBar is the src/ipset_dns.c progress bar (dots = 40): a
// label at every tenth position (0, 25, 50, 75, 100) and a dot
// otherwise.
func progressBar() string {
	var b strings.Builder
	for shown := 0; shown <= 40; shown++ {
		if shown%10 == 0 {
			fmt.Fprintf(&b, "%d%%", shown*100/40)
		} else {
			b.WriteByte('.')
		}
	}
	return b.String()
}
