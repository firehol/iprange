// DNS pool unit tests, ported from the Rust reference
// (v4/rust/iprange-cli/src/legacy/dns.rs test module) with the same
// pins: sink dedup, byte-exact diagnostics, C payloads for empty/
// oversized hostnames, localhost resolution per family, the pinned
// invalid.invalid text, the hard pool cap, and the pool growth
// behavior.

package legacy

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// resolve queues one hostname and waits for its own reply (the
// synchronous test convenience; the Rust reference keeps the same
// helper cfg(test)-only, the Go test file is its equivalent).
func (r *Resolver) resolve(host string) ([]IP128, error) {
	reply, err := r.submit(host)
	if err != nil {
		return nil, err
	}
	res := <-reply
	return res.addrs, res.err
}

// testShared builds a pool state without any worker goroutines, for
// the sink unit tests.
func testShared(family Family, debug bool) *dnsShared {
	s := &dnsShared{family: family, debug: debug}
	s.jobsCond = sync.NewCond(&s.jobsMu)
	s.repliesCond = sync.NewCond(&s.repliesMu)
	return s
}

func asDnsError(t *testing.T, err error) *DnsError {
	t.Helper()
	var de *DnsError
	if !errors.As(err, &de) {
		t.Fatalf("expected *DnsError, got %T: %v", err, err)
	}
	return de
}

func TestDNSSinkDedupsPreservingFirstOccurrenceAndCountsRaw(t *testing.T) {
	shared := testShared(V6, false)
	sink := newAddrSink(shared, "host")
	a := IP128{Hi: 0x2001_0DB8_0000_0000, Lo: 1}
	b := mapped6(0x0A00_0001)
	if !sink.push(a) {
		t.Fatal("first push of a must be new")
	}
	if !sink.push(b) {
		t.Fatal("first push of b must be new")
	}
	if sink.push(a) {
		t.Fatal("duplicate a must be dropped")
	}
	if sink.push(b) {
		t.Fatal("mapped-A duplicate must be dropped")
	}
	if len(sink.addrs) != 2 || sink.addrs[0] != a || sink.addrs[1] != b {
		t.Fatalf("first occurrence order: got %v", sink.addrs)
	}
	if sink.raw != 4 {
		t.Fatalf("C `added` counts duplicates: raw=%d", sink.raw)
	}
}

func TestDNSSinkV4Dedup(t *testing.T) {
	shared := testShared(V4, false)
	sink := newAddrSink(shared, "host")
	v := IP128{Lo: 0x7F00_0001}
	if !sink.push(v) {
		t.Fatal("first push must be new")
	}
	if sink.push(v) {
		t.Fatal("duplicate must be dropped")
	}
	if len(sink.addrs) != 1 || sink.addrs[0] != v {
		t.Fatalf("addrs: %v", sink.addrs)
	}
	if sink.raw != 2 {
		t.Fatalf("raw=%d", sink.raw)
	}
}

func TestDNSProgressBarTextIsByteExact(t *testing.T) {
	// src/ipset_dns.c dns_done(): labels at every tenth position of
	// 0..=40 (0, 25, 50, 75, 100) with 9 dots between them.
	if got := progressBar(); got != "0%.........25%.........50%.........75%.........100%" {
		t.Fatalf("progress bar mismatch: %q", got)
	}
}

func TestDNSWaitingAndSummaryLinesAreByteExact(t *testing.T) {
	if got := waitingLine(3); got != "iprange: DNS: waiting 3 DNS resolutions to finish..." {
		t.Fatalf("waiting line mismatch: %q", got)
	}
	const want = "iprange: DNS: made 10 DNS requests, failed 2, retries: 4, IPs got 16, threads used 5 of 5"
	if got := summaryLine(10, 2, 4, 16, 5, 5); got != want {
		t.Fatalf("summary line mismatch: %q", got)
	}
}

func TestDNSEmptyAndOversizedHostnamesMatchCPayloads(t *testing.T) {
	const want = "iprange: DNS: hostname is empty or too long"
	r := NewResolver(1, false, false, V4, false)
	if _, err := r.resolve(""); err == nil || asDnsError(t, err).Line != want {
		t.Fatalf("empty hostname: %v", err)
	}
	if _, err := r.resolve(strings.Repeat("a", maxHostnameV4+1)); err == nil || asDnsError(t, err).Line != want {
		t.Fatalf("oversized v4 hostname: %v", err)
	}
	// IPv6 accepts 256 chars (MAX_INPUT_ELEMENT6) but rejects 257.
	r6 := NewResolver(1, false, false, V6, false)
	if _, err := r6.resolve(strings.Repeat("a", maxHostnameV6+1)); err == nil || asDnsError(t, err).Line != want {
		t.Fatalf("oversized v6 hostname: %v", err)
	}
	// No request entered the queue: Finish is the C made==0 no-op.
	if r.Finish() {
		t.Fatal("v4 finish with no requests must not fail")
	}
	if r6.Finish() {
		t.Fatal("v6 finish with no requests must not fail")
	}
}

func TestDNSLocalhostResolvesTo127_0_0_1InV4(t *testing.T) {
	// Uses only /etc/hosts (no DNS needed); the resolver returns the
	// A record once (glibc twice), which pins the per-host dedup.
	r := NewResolver(2, true, false, V4, false)
	addrs, err := r.resolve("localhost")
	if err != nil {
		t.Fatalf("localhost must resolve: %v", err)
	}
	v := IP128{Lo: 0x7F00_0001}
	seen := false
	for _, a := range addrs {
		if a == v {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("127.0.0.1 missing from %v", addrs)
	}
	if n := countAddr(addrs, v); n != 1 {
		t.Fatalf("per-host duplicates must be deduplicated: %d", n)
	}
	if r.Finish() {
		t.Fatal("localhost v4 finish must not fail")
	}
}

func TestDNSLocalhostResolvesToLoopbackAndMappedV4InV6(t *testing.T) {
	r := NewResolver(2, true, false, V6, false)
	addrs, err := r.resolve("localhost")
	if err != nil {
		t.Fatalf("localhost must resolve: %v", err)
	}
	loop := IP128{Lo: 1}
	if !containsAddr(addrs, loop) {
		t.Fatalf("::1 missing from %v", addrs)
	}
	mapped := mapped6(0x7F00_0001)
	if !containsAddr(addrs, mapped) {
		t.Fatalf("::ffff:127.0.0.1 missing from %v", addrs)
	}
	if n := countAddr(addrs, mapped); n != 1 {
		t.Fatalf("mapped-A duplicates must be deduplicated: %d", n)
	}
	if r.Finish() {
		t.Fatal("localhost v6 finish must not fail")
	}
}

func TestDNSInvalidInvalidFailsPermanentlyWithCPayload(t *testing.T) {
	// RFC 2606 reserved TLD: NXDOMAIN on any resolver with a working
	// upstream; the sandbox answers instantly.
	r := NewResolver(2, false, false, V4, false)
	_, err := r.resolve("invalid.invalid")
	if err == nil {
		t.Fatal("invalid.invalid must not resolve")
	}
	de := asDnsError(t, err)
	if de.Class != DnsErrorNotFound {
		t.Fatalf("NXDOMAIN is the silent-gated class: %v", de)
	}
	// The gai_strerror(EAI_NONAME) text is stable on Linux and pins
	// the Go NXDOMAIN -> glibc text mapping.
	const want = "iprange: DNS: 'invalid.invalid' failed permanently: Name or service not known"
	if de.Line != want {
		t.Fatalf("payload mismatch:\n got %q\nwant %q", de.Line, want)
	}
	// C dns_done(): a failed reply fails the IPv4 run.
	if !r.Finish() {
		t.Fatal("v4 finish with a failed reply must fail the run")
	}
}

func TestDNSV6NeverFailsTheRun(t *testing.T) {
	r := NewResolver(2, false, false, V6, false)
	_, err := r.resolve("invalid.invalid")
	if err == nil {
		t.Fatal("invalid.invalid must not resolve")
	}
	de := asDnsError(t, err)
	if de.Class != DnsErrorNotFound {
		t.Fatalf("v6 has one failure class: %v", de)
	}
	if !strings.HasPrefix(de.Line, "iprange: DNS: 'invalid.invalid' failed: ") {
		t.Fatalf("payload mismatch: %q", de.Line)
	}
	// C dns6_done() always returns 0.
	if r.Finish() {
		t.Fatal("v6 finish must never fail the run")
	}
}

func TestDNSPoolIsHardCappedWithHugeThreadsMax(t *testing.T) {
	// The C oracle spawns one worker per pending request while
	// pending > workers && workers < --dns-threads; a legal but huge
	// --dns-threads value therefore reserves hundreds of GiB of
	// worker stacks and OOMs the process (the 13:57 OOM regression).
	// The pool must stop at dnsPoolHardMax so the run stays bounded.
	r := NewResolver(1_000_000, false, false, V4, false)
	for i := 0; i < dnsPoolHardMax*4; i++ {
		if err := r.Request("localhost"); err != nil {
			t.Fatalf("queue localhost: %v", err)
		}
	}
	if len(r.workers) > dnsPoolHardMax {
		t.Fatalf("pool grew to %d workers, ceiling is %d", len(r.workers), dnsPoolHardMax)
	}
	replies := r.Drain()
	if len(replies) != dnsPoolHardMax*4 {
		t.Fatalf("every job must reply: got %d", len(replies))
	}
	for _, rec := range replies {
		if rec.Err != nil {
			t.Fatalf("localhost replies must all resolve: %v", rec.Err)
		}
		if rec.Seq < 0 || rec.Seq >= dnsPoolHardMax*4 {
			t.Fatalf("seq out of range: %d", rec.Seq)
		}
	}
	if r.Finish() {
		t.Fatal("localhost finish must not fail")
	}
}

func TestDNSSilentDoesNotChangePayloads(t *testing.T) {
	loud := NewResolver(2, false, false, V4, false)
	quiet := NewResolver(2, true, false, V4, false)
	_, errLoud := loud.resolve("invalid.invalid")
	_, errQuiet := quiet.resolve("invalid.invalid")
	if errLoud == nil || errQuiet == nil {
		t.Fatal("invalid.invalid must fail in both")
	}
	// --dns-silent only gates the parse worker rendering.
	if errLoud.Error() != errQuiet.Error() {
		t.Fatalf("payloads differ: %q vs %q", errLoud, errQuiet)
	}
	loud.Finish()
	quiet.Finish()
}

func TestDNSPoolGrowsAndServesConcurrentHosts(t *testing.T) {
	r := NewResolver(4, true, false, V4, false)
	for i := 0; i < 6; i++ {
		addrs, err := r.resolve("localhost")
		if err != nil {
			t.Fatalf("localhost must resolve: %v", err)
		}
		if !containsAddr(addrs, IP128{Lo: 0x7F00_0001}) {
			t.Fatalf("127.0.0.1 missing from %v", addrs)
		}
	}
	if r.Finish() {
		t.Fatal("localhost finish must not fail")
	}
}

func TestDNSMultiFileBatchesDrainIndependently(t *testing.T) {
	// C processes one file at a time: the loader queues a file's
	// hosts, drains them (per-file summary, stats reset), then moves
	// to the next file. The Rust reference deadlocks on the second
	// DNS-using file (its drain waits on absolute sequence indices
	// although the reply list was drained); this pins the C behavior
	// in the Go port.
	r := NewResolver(3, true, false, V4, false)
	for i := 0; i < 4; i++ {
		if err := r.Request("localhost"); err != nil {
			t.Fatalf("queue file1: %v", err)
		}
	}
	first := r.Drain()
	if len(first) != 4 {
		t.Fatalf("file1 batch: got %d replies", len(first))
	}
	for i, rec := range first {
		if rec.Seq != i || rec.Err != nil {
			t.Fatalf("file1 reply %d: seq=%d err=%v", i, rec.Seq, rec.Err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := r.Request("localhost"); err != nil {
			t.Fatalf("queue file2: %v", err)
		}
	}
	second := r.Drain()
	if len(second) != 2 {
		t.Fatalf("file2 batch: got %d replies", len(second))
	}
	for i, rec := range second {
		if rec.Seq != 4+i || rec.Err != nil {
			t.Fatalf("file2 reply %d: seq=%d err=%v", i, rec.Seq, rec.Err)
		}
	}
	if r.Finish() {
		t.Fatal("finish must not fail")
	}
}

func containsAddr(addrs []IP128, v IP128) bool {
	return countAddr(addrs, v) > 0
}

func countAddr(addrs []IP128, v IP128) int {
	n := 0
	for _, a := range addrs {
		if a == v {
			n++
		}
	}
	return n
}
