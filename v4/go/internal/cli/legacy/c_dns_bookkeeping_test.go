// DNS -v bookkeeping for resolved hostnames: the C resolver adds one ipset
// entry per reply ADDRESS, and that single fact drives every line pinned
// here. The Go twin of v4/rust/iprange-cli/tests/legacy_dns_bookkeeping.rs;
// the mechanism and the C source references are documented there and in
// internal/cli/legacy/dns.go.
//
// Summary of the C behaviour (each item is the released source, not a
// reconstruction):
//
//   * the worker stacks one reply node per address (`src/ipset_dns.c:253-255`,
//     `src/ipset6_dns.c:210-211`) and `dns_process_replies()` adds one range
//     per node with no deduplication (`src/ipset_dns.c:275-281`,
//     `src/ipset6_dns.c:223-233`), so `ips->lines` (`src/ipset.h:89`,
//     `src/ipset6.h:72`) and `totals: %zu lines read`
//     (`src/ipset_print.c:223`, `src/ipset6_print.c:210`) count entries, not
//     input records;
//   * an add that is not after the previous entry clears the optimized flag
//     and prints `NON-OPTIMIZED ...` (`src/ipset.h:100-121`,
//     `src/ipset6.h:96-105`), which drives `Loaded non-optimized NAME`
//     (`src/ipset_load.c:418`) and `Optimizing ...`
//     (`src/ipset_optimize.c:47`, `src/ipset6_optimize.c:24`);
//   * `DNS: '%s' = %s` is printed once per address while the worker walks the
//     answer list, IPv4 only (`src/ipset_dns.c:246-249`); the IPv6 pool prints
//     no per-address line, no `Creating new DNS thread` (`src/ipset_dns.c:92`)
//     and no summary at all (`dns6_done()` in `src/ipset6_dns.c`);
//   * `IPs got %lu` counts raw addresses, duplicates included
//     (`dns_request_done()` in `src/ipset_dns.c:118-128`);
//   * `dns_done()` prints its summary AFTER the last `dns_process_replies()`
//     call (`src/ipset_dns.c:363-375`).
//
// The numeric-form hostnames are answered by the short circuit inside
// `getaddrinfo(3)` (reproduced by lookupLegacyHost) and so need no resolver
// traffic and no host configuration. The names that are NOT numeric
// (`localhost`, `ip6-allnodes`) are answered by the host, and several cases
// pin bytes that depend on which addresses the host returns and in which
// order, because the order decides whether the loader's optimization trace
// appears. Those cases state the answer they were measured against through
// dnsCase.resolverNeeds and report themselves as scoped when this host
// answers differently; nothing is asserted about a name whose answer the
// platform owns.
//
// rc and stdout are always byte for byte. Stderr is
// byte for byte except for the two lines the C derives from its own clock: the
// exit-time timing line (`src/iprange.c:1216-1222`, maskWallclock) and
// `DNS: waiting %lu DNS resolutions to finish...` (`src/ipset_dns.c:348`,
// dropDNSWaiting). The waiting line is printed once per iteration of the
// loader's `while(pending) { ...; sleep(1); }` loop, so its count, how often it
// appears, and whether it appears at all follow how long the resolver took;
// measured on this host, the two-request case printed one waiting line in 11 of
// 12 consecutive C runs and none in the twelfth. Every waiting line that does
// appear is still required to have the exact C text (assertDNSWaitingShape).

package legacy

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type dnsCase struct {
	label  string
	argv   []string
	rc     int
	stdout string
	stderr string // masked: <WALLCLOCK>, and <N> for the DNS waiting count

	// resolverNeeds states the host-provided answer each pinned byte below
	// was measured against, in the order the reply list carries it. The
	// answer set and its order decide stdout, `totals: N lines read`, and
	// whether `NON-OPTIMIZED`/`Optimizing` appear, so a case with needs is
	// only comparable on a host that answers the same way; requireDNSAnswers
	// measures that first and reports the case as scoped otherwise.
	resolverNeeds []dnsAnswer

	// numericForms marks a case whose hostnames are answered only by the
	// glibc numeric short-circuit ("0x7f000001", "0x0A000001"). That answer
	// is a property of the platform's resolver, not of the engines: the
	// platforms whose libc getaddrinfo calls inet_aton() for AF_INET
	// (linux/glibc, freebsd, netbsd, dragonfly-by-lineage) answer those
	// forms and the Rust authority — which delegates to the platform
	// resolver — answers them there too, while Windows and OpenBSD
	// refuse them and darwin is an unverified conservative default (see
	// the two dns_numeric_*.go files), so the Go emulation is scoped to the answering set
	// (dns_numeric_answer.go / dns_numeric_refuse.go) and these cases'
	// pinned bytes exist only on a platform that answers them.
	// requireDNSAnswers reports such a case as scoped on every other
	// platform rather than silently comparing bytes no engine can
	// produce there.
	numericForms bool
}

// dnsAnswer is one name whose addresses a case's pinned bytes depend on.
type dnsAnswer struct {
	host  string
	addrs []string // in reply order, the order the loader adds them
}

// dnsFixtures recreates the directory the C was measured over. A numeric-form
// hostname holds exactly one address; localhost is used where the answer set
// is compared as a multiset instead of being pinned.
var dnsFixtures = map[string]string{
	"h1.txt":  "0x7f000001\n",
	"h2.txt":  "0x7f000001\n0x7f000001\n",
	"hA.txt":  "0x7f000001\n",
	"hB.txt":  "0x0A000001\n",
	"l1.txt":  "localhost\n",
	"l2.txt":  "localhost\nlocalhost\n",
	"lo4.txt": "localhost\n",
	"n1.txt":  "ip6-allnodes\n",
	"hn.txt":  "0x7f000001\n0x0A000001\n",
}

func writeDNSFixtures(t *testing.T, dir string) {
	t.Helper()
	for name, payload := range dnsFixtures {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(payload), 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}
}

// dnsBookkeepingByte: the bookkeeping family exactly. Each case is a hostname
// whose answer set is the same for every resolver on this class of host, so
// the C's own bytes are pinned and the C reference must reproduce them too.
//
// "dns same host twice v4" belongs here rather than in a line-multiset family:
// its two per-address lines are the same text, and the one line whose position
// could move - the `NON-OPTIMIZED` report of the second add - is printed by the
// drain that precedes the summary (src/ipset_dns.c:363-375), so the order is
// fixed. assertDNSLineOrder states that rule for the families that cannot pin
// it line for line. A file with more than one hostname is not here, because
// there the worker interleave decides the optimization trace itself; see
// dnsInterleaveDependent.
var dnsBookkeepingByte = []dnsCase{
	{
		label:        "dns one host v4",
		numericForms: true,
		argv:         []string{"-v", "h1.txt"},
		rc:           0,
		stdout:       "127.0.0.1\n",
		stderr:       "iprange: Loading from h1.txt\niprange: DNS resolution for hostname '0x7f000001' from line 1 of file h1.txt.\niprange: Creating new DNS thread\niprange: DNS: waiting <N> DNS resolutions to finish...\niprange: DNS: '0x7f000001' = 127.0.0.1\niprange: DNS: made 1 DNS requests, failed 0, retries: 0, IPs got 1, threads used 1 of 5\niprange: Loaded optimized h1.txt\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label:        "dns two files one host each",
		numericForms: true,
		argv:         []string{"-v", "hA.txt", "hB.txt"},
		rc:           0,
		stdout:       "10.0.0.1\n127.0.0.1\n",
		stderr:       "iprange: Loading from hA.txt\niprange: DNS resolution for hostname '0x7f000001' from line 1 of file hA.txt.\niprange: Creating new DNS thread\niprange: DNS: waiting <N> DNS resolutions to finish...\niprange: DNS: '0x7f000001' = 127.0.0.1\niprange: DNS: made 1 DNS requests, failed 0, retries: 0, IPs got 1, threads used 1 of 5\niprange: Loaded optimized hA.txt\niprange: Loading from hB.txt\niprange: DNS resolution for hostname '0x0A000001' from line 1 of file hB.txt.\niprange: DNS: waiting <N> DNS resolutions to finish...\niprange: DNS: '0x0A000001' = 10.0.0.1\niprange: DNS: made 1 DNS requests, failed 0, retries: 0, IPs got 1, threads used 1 of 5\niprange: Loaded optimized hB.txt\niprange: Merging hB.txt to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "dns one host v6 localhost",
		resolverNeeds: []dnsAnswer{{host: "localhost",
			addrs: []string{"::1", "::ffff:127.0.0.1"}}},
		argv:   []string{"-6", "-v", "l1.txt"},
		rc:     0,
		stdout: "::1\n::ffff:127.0.0.1\n",
		stderr: "iprange: Loading from l1.txt (IPv6 mode)\niprange: DNS resolution for hostname 'localhost' from line 1 of file l1.txt (IPv6 mode).\niprange: Printing combined ipset (IPv6) with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /128 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n",
	},
	{
		label: "dns one host v6 allnodes",
		resolverNeeds: []dnsAnswer{{host: "ip6-allnodes",
			addrs: []string{"ff02::1"}}},
		argv:   []string{"-6", "-v", "n1.txt"},
		rc:     0,
		stdout: "ff02::1\n",
		stderr: "iprange: Loading from n1.txt (IPv6 mode)\niprange: DNS resolution for hostname 'ip6-allnodes' from line 1 of file n1.txt (IPv6 mode).\niprange: Printing combined ipset (IPv6) with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /128 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n",
	},
	{
		label:        "dns one host v4 binary",
		numericForms: true,
		argv:         []string{"-v", "--print-binary", "h1.txt"},
		rc:           0,
		stdout:       "iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 1\nM<+\x1a\x01\x00\x00\x7f\x01\x00\x00\x7f",
		stderr:       "iprange: Loading from h1.txt\niprange: DNS resolution for hostname '0x7f000001' from line 1 of file h1.txt.\niprange: Creating new DNS thread\niprange: DNS: waiting <N> DNS resolutions to finish...\niprange: DNS: '0x7f000001' = 127.0.0.1\niprange: DNS: made 1 DNS requests, failed 0, retries: 0, IPs got 1, threads used 1 of 5\niprange: Loaded optimized h1.txt\n<WALLCLOCK>\n",
	},
	{
		label: "dns one host v6 binary header",
		resolverNeeds: []dnsAnswer{{host: "localhost",
			addrs: []string{"::1", "::ffff:127.0.0.1"}}},
		argv:   []string{"-6", "-v", "--print-binary", "l1.txt"},
		rc:     0,
		stdout: "iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 2\nbytes 68\nlines 2\nunique ips 2\nM<+\x1a\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01\x00\x00\x7f\xff\xff\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01\x00\x00\x7f\xff\xff\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00",
		stderr: "iprange: Loading from l1.txt (IPv6 mode)\niprange: DNS resolution for hostname 'localhost' from line 1 of file l1.txt (IPv6 mode).\n",
	},
	{
		label:        "dns same host twice v4",
		numericForms: true,
		argv:         []string{"-v", "h2.txt"},
		rc:           0,
		stdout:       "127.0.0.1\n",
		stderr:       "iprange: Loading from h2.txt\niprange: DNS resolution for hostname '0x7f000001' from line 1 of file h2.txt.\niprange: Creating new DNS thread\niprange: DNS resolution for hostname '0x7f000001' from line 2 of file h2.txt.\niprange: Creating new DNS thread\niprange: DNS: waiting <N> DNS resolutions to finish...\niprange: DNS: '0x7f000001' = 127.0.0.1\niprange: DNS: '0x7f000001' = 127.0.0.1\niprange: NON-OPTIMIZED h2.txt at line 2, entry 1, last was 127.0.0.1 (2130706433) - 127.0.0.1 (2130706433), new is 127.0.0.1 (2130706433) - 127.0.0.1 (2130706433)\niprange: DNS: made 2 DNS requests, failed 0, retries: 0, IPs got 2, threads used 2 of 5\niprange: Loaded non-optimized h2.txt\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n",
	},
}

// dnsInterleaveDependent holds the cases whose file carries more than one
// hostname, so more than one worker pushes onto the single shared reply stack
// (src/ipset_dns.c:253-255, src/ipset6_dns.c:210-211) that the adder then drains
// head-first (src/ipset_dns.c:275-281, src/ipset6_dns.c:223-233). The interleave
// decides how many `NON-OPTIMIZED` lines appear and what their `at line N, entry
// E` fields say, and with it whether the printed set needs optimizing at all, so
// those two line families are compared only by shape and by their relation to
// each other. Everything else - the `totals:` entry count in particular - is
// still compared as a multiset with exact counts against the pinned C text and
// against the installed C.
//
// The per-address lines of such a batch also come out in the reverse of load
// order in the C, because the request list is a stack too
// (src/ipset_dns.c:80-81, taken at :187) while both engines serve their job
// queue first-in-first-out; the multiset comparison absorbs that, and every
// count stays identical.
var dnsInterleaveDependent = []dnsCase{
	{
		label: "dns same host twice v6",
		resolverNeeds: []dnsAnswer{{host: "localhost",
			addrs: []string{"::1", "::ffff:127.0.0.1"}}},
		argv:   []string{"-6", "-v", "l2.txt"},
		rc:     0,
		stdout: "::1\n::ffff:127.0.0.1\n",
		stderr: "iprange: Loading from l2.txt (IPv6 mode)\niprange: DNS resolution for hostname 'localhost' from line 1 of file l2.txt (IPv6 mode).\niprange: DNS resolution for hostname 'localhost' from line 2 of file l2.txt (IPv6 mode).\niprange: NON-OPTIMIZED l2.txt at line 3, entry 2, last was ::ffff:127.0.0.1 - ::ffff:127.0.0.1, new is ::1 - ::1\niprange: Optimizing combined ipset (IPv6)\niprange: Printing combined ipset (IPv6) with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /128 counts 2 entries\n\ntotals: 4 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n",
	},
	{
		label:        "dns two names one thread",
		numericForms: true,
		argv:         []string{"-v", "--dns-threads", "1", "hn.txt"},
		rc:           0,
		stdout:       "10.0.0.1\n127.0.0.1\n",
		stderr:       "iprange: Loading from hn.txt\niprange: DNS resolution for hostname '0x7f000001' from line 1 of file hn.txt.\niprange: Creating new DNS thread\niprange: DNS resolution for hostname '0x0A000001' from line 2 of file hn.txt.\niprange: DNS: waiting <N> DNS resolutions to finish...\niprange: DNS: '0x0A000001' = 10.0.0.1\niprange: DNS: '0x7f000001' = 127.0.0.1\niprange: NON-OPTIMIZED hn.txt at line 2, entry 1, last was 127.0.0.1 (2130706433) - 127.0.0.1 (2130706433), new is 10.0.0.1 (167772161) - 10.0.0.1 (167772161)\niprange: DNS: made 2 DNS requests, failed 0, retries: 0, IPs got 2, threads used 1 of 1\niprange: Loaded non-optimized hn.txt\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n",
	},
}

// dnsThreadCreationLine is the C per-worker notification (src/ipset_dns.c:91-92).
const dnsThreadCreationLine = "iprange: Creating new DNS thread"

// stripDNSWorkerTrace removes every per-worker notification. dns_threads is
// incremented outside the requests lock and only ever grows
// (src/ipset_dns.c:88-110), and a worker starts only while more requests are pending
// than there are workers, so how many workers a batch reaches depends on whether an
// earlier request had already finished when the next was queued. Measured on this
// host: 1 of 29 consecutive C runs of the two-request case reached one worker
// instead of two.
func stripDNSWorkerTrace(out string) string {
	kept := make([]string, 0, strings.Count(out, "\n")+1)
	for _, line := range strings.Split(out, "\n") {
		if line == dnsThreadCreationLine {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// maskDNSThreadsUsed rewrites only the number of workers the summary reports using,
// keeping the configured maximum, so the rest of that line - the request count, the
// failures, the retries and the address count `IPs got` - stays pinned byte for byte.
func maskDNSThreadsUsed(out string) string {
	const head = "iprange: DNS: made "
	const tail = ", threads used "
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, head) {
			continue
		}
		at := strings.Index(line, tail)
		if at < 0 {
			continue
		}
		after := line[at+len(tail):]
		digits := 0
		for digits < len(after) && after[digits] >= '0' && after[digits] <= '9' {
			digits++
		}
		if digits == 0 || !strings.HasPrefix(after[digits:], " of ") {
			continue
		}
		lines[i] = line[:at+len(tail)] + "<T> of " + after[digits+4:]
	}
	return strings.Join(lines, "\n")
}

// canonicalDNSStderr applies the four stderr shapes this file retires because the C
// derives them from the clock and the worker pool rather than from the data: the
// exit-time timing line, the DNS waiting line, the per-worker notification, and the
// worker count of the summary. Everything else stays pinned, so this is a whitelist
// of specific lines, not a normalization of the output.
func canonicalDNSStderr(out string) string {
	return maskDNSThreadsUsed(stripDNSWorkerTrace(dropDNSWaiting(maskWallclock(out))))
}

// cutPrefix splits off the text before sep, reporting whether sep was present. It
// differs from strings.Cut only in requiring sep to appear, which is what makes a
// malformed summary line detectable.
func cutPrefix(s, sep string) (before, after string, found bool) {
	if i := strings.Index(s, sep); i >= 0 {
		return s[:i], s[i+len(sep):], true
	}
	return s, "", false
}

// assertDNSThreadAccounting checks what IS determined about the worker pool, on the
// unmasked text: every thread-creation line carries the exact C text, every line that
// reports workers is a well-formed summary, the worker count lies between one and the
// smaller of the request count and the configured maximum, and the number of
// thread-creation lines equals the worker count the summary reports - the C increments
// dns_threads exactly where it prints that line, so the two must agree.
func assertDNSThreadAccounting(t *testing.T, label, out string) {
	t.Helper()
	const head = "iprange: DNS: made "
	// dns_done() runs once per input file (src/ipset_load.c:386 resets the counters
	// via dns_reset_stats()), so a run has one summary per file. dns_threads is not
	// reset, so the worker count is monotonic across the whole run and a later file
	// can legitimately report more workers than it requested itself.
	created, summaries, totalRequests := 0, 0, int64(0)
	lastUsed, previousUsed := int64(-1), int64(0)
	for _, line := range strings.Split(out, "\n") {
		if line == dnsThreadCreationLine {
			created++
			continue
		}
		if strings.HasPrefix(line, dnsThreadCreationLine) {
			t.Errorf("%s: a DNS thread-creation line is not the C text: %q", label, line)
			continue
		}
		if !strings.HasPrefix(line, head) || !strings.Contains(line, ", threads used ") {
			continue
		}
		summaries++
		// The summary is a fixed sequence of separator-delimited numbers, so it is
		// parsed by walking the separators in order; anything else is a divergence.
		rest := strings.TrimPrefix(line, head)
		made, rest, ok := cutPrefix(rest, " DNS requests, failed ")
		if !ok {
			t.Errorf("%s: a DNS summary line is not the C text: %q", label, line)
			continue
		}
		failed, rest, ok := cutPrefix(rest, ", retries: ")
		if !ok {
			t.Errorf("%s: a DNS summary line is not the C text: %q", label, line)
			continue
		}
		retries, rest, ok := cutPrefix(rest, ", IPs got ")
		if !ok {
			t.Errorf("%s: a DNS summary line is not the C text: %q", label, line)
			continue
		}
		got, rest, ok := cutPrefix(rest, ", threads used ")
		if !ok {
			t.Errorf("%s: a DNS summary line is not the C text: %q", label, line)
			continue
		}
		u, m, ok := cutPrefix(rest, " of ")
		if !ok {
			t.Errorf("%s: a DNS summary line has an incomplete worker count: %q", label, line)
			continue
		}
		fields := []string{made, failed, retries, got, u, m}
		values := make([]int64, len(fields))
		bad := false
		for i, field := range fields {
			v, perr := strconv.ParseInt(field, 10, 64)
			if perr != nil {
				t.Errorf("%s: a DNS summary line is not the C text: %q", label, line)
				bad = true
				break
			}
			values[i] = v
		}
		if bad {
			continue
		}
		madeN, usedN, maxN := values[0], values[4], values[5]
		totalRequests += madeN
		if usedN < 1 || usedN > maxN {
			t.Errorf("%s: %d workers with a maximum of %d", label, usedN, maxN)
		}
		if usedN < previousUsed {
			t.Errorf("%s: the worker count fell from %d to %d, but dns_threads only grows", label, previousUsed, usedN)
		}
		previousUsed = usedN
		lastUsed = usedN
	}
	if summaries == 0 {
		if created != 0 {
			t.Errorf("%s: %d thread-creation lines with no DNS summary line", label, created)
		}
		return
	}
	if lastUsed > totalRequests {
		t.Errorf("%s: %d workers for %d requests in total", label, lastUsed, totalRequests)
	}
	if created != int(lastUsed) {
		t.Errorf("%s: %d thread-creation lines but the summary reports %d workers", label, created, lastUsed)
	}
}

// dnsWaitingPrefix is the C waiting line (src/ipset_dns.c:348).
const dnsWaitingPrefix = "iprange: DNS: waiting "

// dropDNSWaiting removes every waiting line, from the engine output and from
// the pinned C text alike. Its presence, its count of lines, and the in-flight
// value it prints all follow how long the resolver took, so no part of it is a
// pinned contract; assertDNSWaitingShape still checks the text of each one.
func dropDNSWaiting(out string) string {
	kept := make([]string, 0, strings.Count(out, "\n")+1)
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, dnsWaitingPrefix) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// assertDNSWaitingShape requires every waiting line present in raw stderr to
// carry the exact C text, so dropping the line cannot hide a malformed one.
// It reports through t.Errorf and returns the number of lines it saw.
func assertDNSWaitingShape(t *testing.T, label, out string) int {
	t.Helper()
	const suffix = " DNS resolutions to finish..."
	seen := 0
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(line, dnsWaitingPrefix)
		if !ok {
			continue
		}
		seen++
		digits := 0
		for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
			digits++
		}
		if digits == 0 || rest[digits:] != suffix {
			t.Errorf("%s: a DNS waiting line does not have the C shape: %q", label, line)
		}
	}
	return seen
}

// dnsLineCounts returns every stderr line with its exact count.
func dnsLineCounts(out string) map[string]int {
	counts := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		counts[line]++
	}
	return counts
}

func dnsLinesEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func sortedLines(out string) string {
	lines := strings.Split(out, "\n")
	sort.Strings(lines)
	return strings.Join(lines, " | ")
}

// numberAfter returns the unsigned integer after key in the first line that
// holds it, or -1.
func numberAfter(out, key string) int64 {
	for _, line := range strings.Split(out, "\n") {
		at := strings.Index(line, key)
		if at < 0 {
			continue
		}
		rest := line[at+len(key):]
		digits := 0
		for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
			digits++
		}
		if digits == 0 {
			continue
		}
		var value int64
		for _, d := range rest[:digits] {
			value = value*10 + int64(d-'0')
		}
		return value
	}
	return -1
}

// isPerAddressLine reports the debug line the C prints once per reply address
// (src/ipset_dns.c:248).
func isPerAddressLine(line string) bool {
	return strings.HasPrefix(line, "iprange: DNS: '") && strings.Contains(line, "' = ")
}

// assertDNSCounts asserts the invariants the reply-accounting family must
// hold, for either family:
//
//   - IPv4 debug prints one `DNS: '%s' = %s` line per reply address
//     (src/ipset_dns.c:246-249) and one summary per file whose `IPs got`
//     counts those addresses (src/ipset_dns.c:118-128, :375), so the two
//     totals must be equal;
//   - `totals: %zu lines read` counts added entries (src/ipset.h:89,
//     src/ipset6.h:72), so for a file that holds nothing but hostnames it
//     must equal the number of reply addresses;
//   - the IPv6 pool prints neither the per-address line nor the summary
//     (dns6_done() in src/ipset6_dns.c), so their absence is also asserted;
//   - `Loaded non-optimized` is printed per file whose flag was cleared by an
//     out-of-order add (src/ipset_load.c:418), so it can never outnumber the
//     NON-OPTIMIZED lines of that same family of adds.
//
// Every case here holds nothing but hostnames, which is what makes the second
// item an equality rather than a bound. A reply list that deduplicates the
// addresses breaks the first two items while leaving the diagnostics
// otherwise intact.
func assertDNSCounts(t *testing.T, label, masked string) {
	t.Helper()
	perAddress, summaries, loadedNon, nonOptimized := 0, 0, 0, 0
	var sumGot, read int64 = -1, -1
	for _, line := range strings.Split(masked, "\n") {
		switch {
		case isPerAddressLine(line):
			perAddress++
		case strings.HasPrefix(line, "iprange: DNS: made "):
			summaries++
			got := numberAfter(line, "IPs got ")
			if got < 0 {
				t.Fatalf("%s: summary line without `IPs got`: %q", label, line)
			}
			if sumGot < 0 {
				sumGot = 0
			}
			sumGot += got
		case strings.HasPrefix(line, "totals: "):
			if read >= 0 {
				t.Errorf("%s: more than one totals line in\n%s", label, masked)
			}
			read = numberAfter(line, "totals: ")
		case strings.HasPrefix(line, "iprange: NON-OPTIMIZED "):
			nonOptimized++
		case strings.HasPrefix(line, "iprange: Loaded non-optimized "):
			loadedNon++
		}
	}
	if summaries == 0 {
		// The IPv6 pool prints no summary and no per-address line.
		if perAddress != 0 {
			t.Errorf("%s: the IPv6 pool must print no per-address DNS line, got %d\n%s", label, perAddress, masked)
		}
	} else {
		if int64(perAddress) != sumGot {
			t.Errorf("%s: one debug line per reply address requires %d per-address lines, `IPs got` %d\n%s", label, perAddress, sumGot, masked)
		}
	}
	if read >= 0 && sumGot >= 0 && read != sumGot {
		t.Errorf("%s: a file of hostnames must count one `lines read` entry per reply address: lines read %d, reply addresses %d\n%s", label, read, sumGot, masked)
	}
	if loadedNon > nonOptimized {
		t.Errorf("%s: `Loaded non-optimized` (%d) cannot outnumber the out-of-order adds (%d)\n%s", label, loadedNon, nonOptimized, masked)
	}
}

// cOracleStatus returns why the released C tool cannot be consulted here,
// or the empty string when it can.
//
// The C CLI has no native Windows build, so on that host every comparison
// against the reference is unavailable rather than passed: the case states
// that explicitly and the engine's own pinned bytes still decide it. The
// status is a fact about the host, not a verdict, so it never converts a
// divergence into a pass.
func cOracleStatus() string {
	if _, err := os.Stat(cReference); err != nil {
		return fmt.Sprintf("at %s: this platform has no native build of the released tool, so the "+
			"comparison against C is unavailable and only the engine's own pinned bytes decided "+
			"this case (%v)", cReference, err)
	}
	return ""
}

// requireDNSAnswers measures the host answers a case's pinned bytes were
// built from and reports the case as scoped when this host differs.
//
// The measurement goes through the product's own resolver so the
// precondition checks the same code path the case will run, and the
// measured list is printed in the skip text: a reader can see which answer
// set the case is missing rather than being told a case did not run.
func requireDNSAnswers(t *testing.T, c dnsCase) {
	t.Helper()
	if c.numericForms && !numericFormsAnswerHere {
		t.Skipf("%s: scoped, not run: the pinned bytes require the glibc numeric "+
			"short-circuit, which this platform's resolver does not answer and the Rust "+
			"authority therefore does not answer either; the failure shape here is "+
			"pinned by TestDNSNumericFormsAreNotAnsweredHere",
			c.label)
	}
	if len(c.resolverNeeds) == 0 {
		return
	}
	family := V4
	for _, arg := range c.argv {
		if arg == "-6" {
			family = V6
		}
	}
	for _, need := range c.resolverNeeds {
		probe := NewResolver(1, true, false, family, false)
		addrs, err := probe.resolve(need.host)
		if err != nil {
			probe.Finish()
			t.Skipf("%s: scoped, not run: this host cannot answer %q (%v), which the pinned bytes of "+
				"this case require as %v", c.label, need.host, err, need.addrs)
		}
		render := &Options{Family: family}
		got := make([]string, 0, len(addrs))
		for _, a := range addrs {
			got = append(got, fmtAddr(render, a))
		}
		if strings.Join(got, ",") != strings.Join(need.addrs, ",") {
			t.Skipf("%s: scoped, not run: the pinned bytes were measured against %q answering %v in that "+
				"order; this host answers %v", c.label, need.host, need.addrs, got)
		}
	}
}

// runDNSCase runs the engine in the fixture directory and masks the two
// free shapes.
func runDNSCase(t *testing.T, dir string, c dnsCase) (int, string, string) {
	t.Helper()
	rc, out, errs := runLegacyChild(t, dir, c.argv)
	assertDNSWaitingShape(t, c.label+" (engine)", errs)
	assertDNSThreadAccounting(t, c.label+" (engine)", errs)
	masked := canonicalDNSStderr(errs)
	assertDNSLineOrder(t, c.label+" (engine)", masked)
	return rc, out, masked
}

func TestDNSBookkeepingMatchesCByteForByte(t *testing.T) {
	dir := t.TempDir()
	writeDNSFixtures(t, dir)
	for _, c := range dnsBookkeepingByte {
		c := c
		t.Run(c.label, func(t *testing.T) {
			requireDNSAnswers(t, c)
			rc, out, masked := runDNSCase(t, dir, c)
			if rc != c.rc || out != c.stdout || masked != canonicalDNSStderr(c.stderr) {
				t.Errorf("%s: engine rc %d out %q stderr %q, want rc %d out %q stderr %q", c.label, rc, out, masked, c.rc, c.stdout, canonicalDNSStderr(c.stderr))
			}
			assertDNSCounts(t, c.label, masked)
			if cOracleUnavailable(t) {
				return
			}
			crc, cout, cerrs := runCChild(t, dir, c.argv)
			assertDNSWaitingShape(t, c.label+" (the C reference)", cerrs)
			assertDNSThreadAccounting(t, c.label+" (the C reference)", cerrs)
			cerrs = canonicalDNSStderr(cerrs)
			assertDNSLineOrder(t, c.label+" (the C reference)", cerrs)
			if crc != c.rc || cout != c.stdout || cerrs != canonicalDNSStderr(c.stderr) {
				t.Errorf("%s: the pin drifted from %s: rc %d out %q stderr %q", c.label, cReference, crc, cout, cerrs)
			}
		})
	}
}

// stripInterleaveTrace removes the three line families whose presence and
// content the worker interleave decides, leaving a stable set to compare as a
// multiset: the out-of-order-add report, the `Loaded` line that reports the
// flag those adds left behind (src/ipset_load.c:418), and the optimize request
// that flag triggers.
func stripInterleaveTrace(out string) string {
	kept := make([]string, 0, strings.Count(out, "\n")+1)
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "iprange: NON-OPTIMIZED ") ||
			strings.HasPrefix(line, "iprange: Optimizing combined ipset") ||
			strings.HasPrefix(line, "iprange: Loaded optimized ") ||
			strings.HasPrefix(line, "iprange: Loaded non-optimized ") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// assertNonOptimizedShape requires every NON-OPTIMIZED line to carry the exact C
// text around the two numbers the interleave decides (src/ipset.h:117,
// src/ipset6.h:102): file, "at line N, entry E", "last was LO - HI", and
// "new is LO - HI". It returns how many such lines appeared.
func assertNonOptimizedShape(t *testing.T, label, out string) int {
	t.Helper()
	seen := 0
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(line, "iprange: NON-OPTIMIZED ")
		if !ok {
			continue
		}
		seen++
		file, after, ok := strings.Cut(rest, " at line ")
		if !ok || file == "" {
			t.Errorf("%s: NON-OPTIMIZED line without ' at line ': %q", label, line)
			continue
		}
		nums, tails, ok := strings.Cut(after, ", entry ")
		if !ok || !allDigits(nums) {
			t.Errorf("%s: NON-OPTIMIZED line without a line number: %q", label, line)
			continue
		}
		entry, tails, ok := strings.Cut(tails, ", last was ")
		if !ok || !allDigits(entry) {
			t.Errorf("%s: NON-OPTIMIZED line without an entry number: %q", label, line)
			continue
		}
		last, news, ok := strings.Cut(tails, ", new is ")
		if !ok {
			t.Errorf("%s: NON-OPTIMIZED line without 'new is': %q", label, line)
			continue
		}
		last = strings.TrimSuffix(last, ",")
		news = strings.TrimSuffix(news, ",")
		if !strings.Contains(last, " - ") || !strings.Contains(news, " - ") {
			t.Errorf("%s: NON-OPTIMIZED line without range pairs: %q", label, line)
		}
	}
	return seen
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// loadedRelationViolation checks the `Loaded <adj> <file>` line
// (src/ipset_load.c:418) against the out-of-order adds of the same run. That line
// reports the optimized flag of the file just loaded, and only an out-of-order add
// clears the flag (src/ipset.h:107, src/ipset6.h:98), so for a run loading one file
// it must read `non-optimized` exactly when at least one add was out of order. The
// gate is the one part that is not derivable from the adds: the line belongs to the
// IPv4 loader, and src/ipset6_load.c prints no `Loaded` line at all, so a v6 run
// legitimately reports out-of-order adds with no `Loaded` line to agree with them.
// It returns "" when the output is consistent.
func loadedRelationViolation(raw string, nonOptimized int) string {
	loaded, loadedNon := 0, 0
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, "iprange: Loaded ") {
			continue
		}
		loaded++
		if strings.HasPrefix(line, "iprange: Loaded non-optimized ") {
			loadedNon++
		}
	}
	if loaded == 0 {
		return ""
	}
	want := 0
	if nonOptimized > 0 {
		want = 1
	}
	if loadedNon == want {
		return ""
	}
	return fmt.Sprintf("%d `Loaded non-optimized` lines for %d out-of-order adds (want %d)",
		loadedNon, nonOptimized, want)
}

// flipOptimizationTrace builds the C's other optimization branch for the same
// input from the three sites that decide it: with the adds in order
// ipset_added_entry() returns early and never clears the flag (src/ipset.h:100-121),
// so src/ipset_load.c:418 prints `optimized` and ipset_print.c:142-143 does not call
// ipset_optimize() and so prints no `Optimizing` line. Every entry count is
// unchanged, because ips->lines is incremented before the order test (src/ipset.h:89).
func flipOptimizationTrace(t *testing.T, pin string) string {
	t.Helper()
	droppedNon, droppedOpt, rewrote := 0, 0, 0
	kept := make([]string, 0, strings.Count(pin, "\n")+1)
	for _, line := range strings.Split(pin, "\n") {
		switch {
		case strings.HasPrefix(line, "iprange: NON-OPTIMIZED "):
			droppedNon++
			continue
		case strings.HasPrefix(line, "iprange: Optimizing combined ipset"):
			droppedOpt++
			continue
		case strings.HasPrefix(line, "iprange: Loaded non-optimized "):
			rewrote++
			line = "iprange: Loaded optimized " + strings.TrimPrefix(line, "iprange: Loaded non-optimized ")
		}
		kept = append(kept, line)
	}
	// A stale pin must be loud, not silently turned into a different shape.
	if droppedNon != 1 || droppedOpt != 1 || rewrote != 1 {
		t.Fatalf("the pin no longer has the shape this flip was derived from: %d NON-OPTIMIZED, %d Optimizing, %d Loaded",
			droppedNon, droppedOpt, rewrote)
	}
	return strings.Join(kept, "\n")
}

// TestDNSBookkeepingOptimizationTraceFlipIsWhyTheFamilyExists shows why a file with
// more than one hostname cannot be compared line-for-line: the released C produces
// both optimization branches for the same input, and the two branches are different
// SETS of stderr lines, so a comparison that keeps every line rejects a correct run.
// Stripping the three trace lines makes the branches identical again while leaving
// the entry count - the thing these cases pin - fully intact.
func TestDNSBookkeepingOptimizationTraceFlipIsWhyTheFamilyExists(t *testing.T) {
	var pin string
	for _, c := range dnsInterleaveDependent {
		if c.label == "dns two names one thread" {
			pin = c.stderr
		}
	}
	if pin == "" {
		t.Fatal("`dns two names one thread` must live in dnsInterleaveDependent")
	}
	flipped := flipOptimizationTrace(t, pin)
	if dnsLinesEqual(dnsLineCounts(canonicalDNSStderr(pin)), dnsLineCounts(canonicalDNSStderr(flipped))) {
		t.Fatal("the C's two branches were multiset-identical, so the case needed no special family")
	}
	if !dnsLinesEqual(dnsLineCounts(stripInterleaveTrace(canonicalDNSStderr(pin))),
		dnsLineCounts(stripInterleaveTrace(canonicalDNSStderr(flipped)))) {
		t.Errorf("stripping the trace must make the C's two branches agree\n pin: %s\n flip: %s",
			sortedLines(stripInterleaveTrace(canonicalDNSStderr(pin))),
			sortedLines(stripInterleaveTrace(canonicalDNSStderr(flipped))))
	}
	stable := strings.Split(stripInterleaveTrace(canonicalDNSStderr(pin)), "\n")
	var sawTotals, sawLoaded bool
	for _, l := range stable {
		sawTotals = sawTotals || strings.HasPrefix(l, "totals: 2 lines read")
		sawLoaded = sawLoaded || strings.HasPrefix(l, "iprange: Loaded ")
	}
	if !sawTotals {
		t.Errorf("the entry count the case exists to pin must survive the strip\n%s", strings.Join(stable, "\n"))
	}
	if sawLoaded {
		t.Errorf("the strip must retire the `Loaded` line, whose text the same branch rewrites\n%s", strings.Join(stable, "\n"))
	}
}

// TestDNSBookkeepingLoadedRelationDetectsEveryMismatch keeps the `Loaded` relation
// able to fail, and pins that it stays silent for the IPv6 loader.
func TestDNSBookkeepingLoadedRelationDetectsEveryMismatch(t *testing.T) {
	outOfOrder := "iprange: NON-OPTIMIZED f at line 2, entry 1, last was 1 - 1, new is 0 - 0\niprange: Loaded non-optimized f\n"
	inOrder := "iprange: Loaded optimized f\n"
	v6OutOfOrder := "iprange: NON-OPTIMIZED f at line 3, entry 2, last was ::1 - ::1, new is ::2 - ::2\n"
	twoFilesNonOpt := "iprange: Loaded non-optimized a\niprange: Loaded non-optimized b\n"
	for _, tc := range []struct {
		name, raw, want string
		non             int
	}{
		{"consistent out-of-order", outOfOrder, "", 1},
		{"consistent in-order", inOrder, "", 0},
		{"v6 prints no Loaded line", v6OutOfOrder, "", 1},
		{"non-optimized without an out-of-order add", outOfOrder, "want 0", 0},
		{"optimized despite an out-of-order add", inOrder, "want 1", 1},
		{"two files for one out-of-order add", twoFilesNonOpt, "want 1", 1},
	} {
		if got := loadedRelationViolation(tc.raw, tc.non); !strings.Contains(got, tc.want) || (tc.want == "" && got != "") {
			t.Errorf("%s: loadedRelationViolation = %q, want a message containing %q", tc.name, got, tc.want)
		}
	}
}

// TestDNSBookkeepingInterleavedBatch asserts the multi-hostname-per-file cases:
// the optimization trace follows the worker interleave and is therefore checked
// only for shape and for the relation between the two families, while the entry
// count the same adds produce stays pinned as a multiset with exact counts.
func TestDNSBookkeepingInterleavedBatch(t *testing.T) {
	dir := t.TempDir()
	writeDNSFixtures(t, dir)
	for _, c := range dnsInterleaveDependent {
		c := c
		t.Run(c.label, func(t *testing.T) {
			requireDNSAnswers(t, c)
			rc, out, raw := runLegacyChild(t, dir, c.argv)
			if rc != c.rc || out != c.stdout {
				t.Fatalf("%s: rc %d out %q, want rc %d out %q", c.label, rc, out, c.rc, c.stdout)
			}
			assertDNSWaitingShape(t, c.label, raw)
			non := assertNonOptimizedShape(t, c.label, raw)
			opt := 0
			for _, line := range strings.Split(raw, "\n") {
				if strings.HasPrefix(line, "iprange: Optimizing combined ipset") {
					opt++
				}
			}
			want := 0
			if non > 0 {
				want = 1
			}
			if opt != want {
				t.Errorf("%s: %d Optimizing lines for %d out-of-order adds\n%s", c.label, opt, non, raw)
			}
			if why := loadedRelationViolation(raw, non); why != "" {
				t.Errorf("%s: %s\n%s", c.label, why, raw)
			}
			engine := dnsLineCounts(stripInterleaveTrace(canonicalDNSStderr(raw)))
			if !dnsLinesEqual(engine, dnsLineCounts(stripInterleaveTrace(canonicalDNSStderr(c.stderr)))) {
				t.Errorf("%s: stable stderr multiset differs from the pin\n engine: %s\n    pin: %s",
					c.label, sortedLines(stripInterleaveTrace(canonicalDNSStderr(c.stderr))), sortedLines(stripInterleaveTrace(canonicalDNSStderr(raw))))
			}
			if cOracleUnavailable(t) {
				return
			}
			_, _, craw := runCChild(t, dir, c.argv)
			assertDNSWaitingShape(t, c.label+" (the C reference)", craw)
			assertDNSThreadAccounting(t, c.label+" (the C reference)", craw)
			assertNonOptimizedShape(t, c.label+" (the C reference)", craw)
			cref := dnsLineCounts(stripInterleaveTrace(canonicalDNSStderr(craw)))
			if !dnsLinesEqual(engine, cref) {
				t.Errorf("%s: stable stderr multiset differs from %s\n engine: %s\n    C: %s",
					c.label, cReference, sortedLines(stripInterleaveTrace(canonicalDNSStderr(raw))), sortedLines(stripInterleaveTrace(canonicalDNSStderr(craw))))
			}
		})
	}
}

// TestDNSBookkeepingWaitingLineIsFree pins what remains true of the one stderr
// line the C does not reproduce. It is printed once per iteration of the
// loader's `while(pending) { ...; sleep(1); }` loop (`src/ipset_dns.c:337-348`),
// so its in-flight value, how many times it appears, and whether it appears at
// all are all timing. The pinned comparisons above drop the line; this test
// holds the line to its exact C text, bounds its frequency by the number of
// requests, and requires a summary whenever it appeared.
func TestDNSBookkeepingWaitingLineIsFree(t *testing.T) {
	dir := t.TempDir()
	writeDNSFixtures(t, dir)
	for _, c := range append([]dnsCase{}, dnsBookkeepingByte...) {
		c := c
		t.Run(c.label, func(t *testing.T) {
			_, _, raw := runLegacyChild(t, dir, c.argv)
			seen := assertDNSWaitingShape(t, c.label, raw)
			if seen == 0 {
				return
			}
			// One summary is printed per loaded file (dns_done() runs per ipset),
			// so the bound is the total number of requests across all of them.
			requests, summaries := 0, 0
			for _, line := range strings.Split(raw, "\n") {
				if strings.HasPrefix(line, "iprange: DNS: made ") {
					summaries++
					requests += int(numberAfter(line, "iprange: DNS: made "))
				}
			}
			if summaries == 0 {
				t.Errorf("%s: waiting lines with no DNS summary line", c.label)
				return
			}
			if seen > requests {
				t.Errorf("%s: %d waiting lines for %d requests across %d files", c.label, seen, requests, summaries)
			}
		})
	}
}

// TestDNSBookkeepingOneRecordPerAddress pins the drain contract that makes the
// C entry counting observable: every resolved address is handed out as its own
// reply record holding exactly one address, so the caller adds one ipset entry
// (and counts one `lines` unit) per address, as dns_process_replies() does
// (src/ipset_dns.c:275-281, src/ipset.h:89). A record that carries the whole
// address list hands the duplicates to the caller's per-record dedup and the
// count is lost.
func TestDNSBookkeepingOneRecordPerAddress(t *testing.T) {
	probe := NewResolver(2, true, false, V6, false)
	addrs, err := probe.resolve("localhost")
	if err != nil {
		t.Fatalf("localhost must resolve: %v", err)
	}
	if len(addrs) < 2 {
		t.Skipf("this host answers localhost with %d IPv6-family addresses; the case needs at least 2", len(addrs))
	}
	if probe.Finish() {
		// Finish reports whether the run must fail; IPv6 never fails
		// (dns6_done() in src/ipset6_dns.c always returns 0).
		t.Fatal("v6 finish must not report failure")
	}

	r := NewResolver(2, true, false, V6, false)
	for i := 0; i < 3; i++ {
		if err := r.Request("localhost"); err != nil {
			t.Fatalf("queue localhost: %v", err)
		}
	}
	records := r.Drain()
	if len(records) != 3*len(addrs) {
		t.Fatalf("3 requests x %d addresses must produce %d records, got %d", len(addrs), 3*len(addrs), len(records))
	}
	seen := map[int][]IP128{}
	for _, rec := range records {
		if rec.Err != nil {
			t.Fatalf("unexpected failed reply: %v", rec.Err)
		}
		if len(rec.Addrs) != 1 {
			t.Fatalf("one record per reply address requires exactly one address, got %v", rec.Addrs)
		}
		seen[rec.Seq] = append(seen[rec.Seq], rec.Addrs[0])
	}
	for seq := 0; seq < 3; seq++ {
		got := seen[seq]
		if len(got) != len(addrs) {
			t.Fatalf("request %d produced %d records, want %d", seq, len(got), len(addrs))
		}
		if !sameCount(toCounts(got), toCounts(addrs)) {
			t.Fatalf("request %d must hand out every resolved address: got %v want %v", seq, got, addrs)
		}
	}
	if r.Finish() {
		t.Fatal("v6 finish must not report failure")
	}
}

func toCounts(addrs []IP128) map[IP128]int {
	counts := map[IP128]int{}
	for _, a := range addrs {
		counts[a]++
	}
	return counts
}

// sameCount compares two per-address multiplicity maps.
func sameCount(a, b map[IP128]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestDNSBookkeepingSinkKeepsEveryAnswer pins the reply list itself: it keeps
// every address the resolver answered, duplicates included, because the C
// stacks one DNSREP node per address (src/ipset_dns.c:253-255) and the ipset
// counts one entry per node (src/ipset.h:89). The deduplicated list stays for
// the synchronous resolve() helper only.
func TestDNSBookkeepingSinkKeepsEveryAnswer(t *testing.T) {
	shared := testShared(V6, false)
	sink := newAddrSink(shared, "host")
	a := IP128{Hi: 0x2001_0DB8_0000_0000, Lo: 1}
	b := mapped6(0x0A00_0001)
	sink.push(a)
	sink.push(b)
	sink.push(a)
	if got, want := len(sink.full), 3; got != want {
		t.Fatalf("the reply list must keep every answer, duplicates included: got %d entries, want %d", got, want)
	}
	if got, want := sink.full, []IP128{a, b, a}; !sameCount(toCounts(got), toCounts(want)) || len(got) != len(want) {
		t.Fatalf("reply list %v, want %v", got, want)
	}
	if got, want := len(sink.addrs), 2; got != want {
		t.Fatalf("the deduplicated list must stay for resolve(): got %d, want %d", got, want)
	}
	if sink.raw != 3 {
		t.Fatalf("C `added` counts duplicates: raw=%d", sink.raw)
	}
}

// dnsWorkerLine reports a line the resolver worker prints while it serves one
// request: the per-address answer (src/ipset_dns.c:246-249), or a name that
// failed or is being retried (:161-175). Each of them is printed before the
// worker marks its request finished, so dns_done() can only report its summary
// after all of them.
func dnsWorkerLine(line string) bool {
	return strings.HasPrefix(line, "iprange: DNS: '")
}

// assertDNSLineOrder pins where one file's batch puts its summary line. C
// dns_done() drains the replies into the ipset and only then prints the
// summary (src/ipset_dns.c:363-375), so inside a file the summary comes after
// every worker line and after every `NON-OPTIMIZED` report those additions
// print; the loader prints `Loaded ...` only after dns_done() returned
// (src/ipset_load.c:401, :418), so the summary comes before that line. The
// batch is bounded by the `Loading from` line that opens the file, because
// dns_done() runs once per input file.
//
// The byte-exact table pins the same fact line for line; this check is what
// keeps the order pinned in the families that compare their lines as
// multisets, and it names the swapped pair when it breaks.
func assertDNSLineOrder(t *testing.T, label, masked string) {
	t.Helper()
	lines := strings.Split(masked, "\n")
	var starts []int
	for i, line := range lines {
		if strings.HasPrefix(line, "iprange: Loading from ") {
			starts = append(starts, i)
		}
	}
	for b, start := range starts {
		end := len(lines)
		if b+1 < len(starts) {
			end = starts[b+1]
		}
		summary := -1
		for i := start; i < end; i++ {
			if strings.HasPrefix(lines[i], "iprange: DNS: made ") {
				summary = i
				break
			}
		}
		if summary < 0 {
			// The IPv6 pool prints no summary at all (dns6_done(),
			// src/ipset6_dns.c:270), and the loader prints no `Loaded`
			// line either (src/ipset6_load.c).
			continue
		}
		for i := start; i < end; i++ {
			if i > summary && (dnsWorkerLine(lines[i]) ||
				strings.HasPrefix(lines[i], "iprange: NON-OPTIMIZED ")) {
				t.Errorf("%s: %q must be printed before the DNS summary line\n%s", label, lines[i], masked)
			}
			if i < summary && strings.HasPrefix(lines[i], "iprange: Loaded ") {
				t.Errorf("%s: %q must be printed after the DNS summary line\n%s", label, lines[i], masked)
			}
		}
	}
}

// TestDNSBookkeepingLocalhostReplyCountsAreSelfConsistent pins the entry
// counting for the host whose answer count differs between resolvers:
// under the canonical CGO_ENABLED=0 build the Go netgo resolver answers
// "localhost" with one IPv4 record, while C's glibc files+myhostname chain
// answers the same address twice. This count is resolver-determined and is
// a defined compatibility exception (SOW-0028, DNS parity scope): it
// changes the `-v` per-address lines, `IPs got`, `Loaded` optimization
// trace, `totals: N lines read`, and the `lines` field of the legacy
// binary v1/v2 header written by --print-binary (verified: C and Rust emit
// "lines 2" for an IPv4 localhost file, pure Go emits "lines 1"; the
// address content itself is identical). The absolute count therefore
// cannot be pinned against C here, but the relation between the
// per-address lines, `IPs got` and `lines read` can, and it is exactly the
// relation a deduplicated reply list breaks.
func TestDNSBookkeepingLocalhostReplyCountsAreSelfConsistent(t *testing.T) {
	dir := t.TempDir()
	writeDNSFixtures(t, dir)
	rc, out, masked := runDNSCase(t, dir, dnsCase{
		label: "localhost v4", argv: []string{"-v", "lo4.txt"}, rc: 0, stdout: "127.0.0.1\n",
	})
	if rc != 0 || out != "127.0.0.1\n" {
		t.Fatalf("localhost v4: rc %d out %q", rc, out)
	}
	assertDNSCounts(t, "localhost v4", masked)
}

// The pinned cases only mean something if the fixture directory they run in
// holds the measured bytes.
func TestDNSBookkeepingFixtureSetIsComplete(t *testing.T) {
	dir := t.TempDir()
	writeDNSFixtures(t, dir)
	for name, payload := range dnsFixtures {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("fixture %s: %v", name, err)
		}
		if string(raw) != payload {
			t.Errorf("fixture %s = %q, want %q", name, raw, payload)
		}
	}
	all := append(append([]dnsCase{}, dnsBookkeepingByte...), dnsInterleaveDependent...)
	for _, c := range all {
		for _, arg := range c.argv {
			if _, ok := dnsFixtures[arg]; !ok {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, arg)); err != nil {
				t.Errorf("%s references fixture %s that is not written: %v", c.label, arg, err)
			}
		}
	}
}
