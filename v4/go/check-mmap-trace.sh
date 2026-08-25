#!/bin/sh
# check-mmap-trace.sh - runtime half of the mmap-only evidence.
#
# Traces real open+lookup, validation, and recovery sessions (the Rust
# conformance cross-open test, the public live and routed validation
# proofs, and the in-process and routed recovery constructions) and
# asserts that the database and worker-control descriptors are never
# read through file I/O: after openat + OFD lock, every byte of the
# fixtures must arrive through mmap. Any
# read/pread64/readv/write/lseek on an fd whose path is a v4 artifact
# or the worker control file (.iprange-v4-worker-*) fails the check.
# Legs 2, 5, and 6 route through the isolated worker process (strace
# -f follows the child), so those tests build the worker binary inside
# the traced test run (the facade harness).
#
# Usage: ./check-mmap-trace.sh   (run from the v4/go directory)
# The trace set and the violation pattern must stay in sync: every
# descriptor I/O syscall the pattern names has to be traced, or the
# "no file I/O" evidence silently misses it.
set -u

ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT" || exit 1
TRACE="$(mktemp /tmp/iprange-mmap-trace.XXXXXX)"
TESTLOG="$(mktemp /tmp/iprange-mmap-trace-test.XXXXXX)"
trap 'rm -f "$TRACE" "$TESTLOG"' EXIT

if ! command -v strace >/dev/null 2>&1; then
	echo "check-mmap-trace: strace not found; skipping (runtime evidence unavailable)" >&2
	exit 0
fi

# leg 1: the immutable conformance cross-open (Rust-written fixtures).
echo "check-mmap-trace: tracing TestConformanceRustFixtures (nice)"
nice -n 10 strace -f -y -e trace=openat,read,pread64,readv,write,writev,pwrite64,lseek,mmap,munmap,close,fcntl \
	-o "$TRACE" \
	"$(go env GOROOT)/bin/go" test -run '^TestConformanceRustFixtures$' -count=1 . >"$TESTLOG" 2>&1
TEST_RC=$?
if [ $TEST_RC -ne 0 ]; then
	echo "check-mmap-trace: test failed (rc=$TEST_RC); see $TESTLOG" >&2
	exit 1
fi

# leg 2: the live-current validation session over the public live
# writer (Rust mmap_runtime_tests live validation proof): the trace
# covers create + commit + validate + reader open, so the sidecar
# coordination and the validation sweep must stay mmap-only too.
TRACE2="$(mktemp /tmp/iprange-mmap-trace2.XXXXXX)"
TESTLOG2="$(mktemp /tmp/iprange-mmap-trace-test2.XXXXXX)"
trap 'rm -f "$TRACE" "$TESTLOG" "$TRACE2" "$TESTLOG2" "$TRACE3" "$TESTLOG3"' EXIT
echo "check-mmap-trace: tracing TestPublicValidateLiveCleanSweep (nice)"
nice -n 10 strace -f -y -e trace=openat,read,pread64,readv,write,writev,pwrite64,lseek,mmap,munmap,close,fcntl \
	-o "$TRACE2" \
	"$(go env GOROOT)/bin/go" test -run '^TestPublicValidateLiveCleanSweep$' -count=1 . >"$TESTLOG2" 2>&1
TEST_RC=$?
if [ $TEST_RC -ne 0 ]; then
	echo "check-mmap-trace: live validation test failed (rc=$TEST_RC); see $TESTLOG2" >&2
	exit 1
fi

# leg 3: the offline recovery-candidate session (Rust recovery
# inspection + validate_offline proof): create + commit + offline
# inspection + offline candidate validation. The offline source opens
# the main read-write, so this leg proves the writable descriptor is
# still never read or written through file I/O.
TRACE3="$(mktemp /tmp/iprange-mmap-trace3.XXXXXX)"
TESTLOG3="$(mktemp /tmp/iprange-mmap-trace-test3.XXXXXX)"
echo "check-mmap-trace: tracing TestValidateOfflineCandidateCommittedLiveGeneration (nice)"
nice -n 10 strace -f -y -e trace=openat,read,pread64,readv,write,writev,pwrite64,lseek,mmap,munmap,close,fcntl \
	-o "$TRACE3" \
	"$(go env GOROOT)/bin/go" test -run '^TestValidateOfflineCandidateCommittedLiveGeneration$' -count=1 ./internal/recovery/ >"$TESTLOG3" 2>&1
TEST_RC=$?
if [ $TEST_RC -ne 0 ]; then
	echo "check-mmap-trace: offline recovery test failed (rc=$TEST_RC); see $TESTLOG3" >&2
	exit 1
fi

# leg 4: the recovery construction session (Rust recovery api_tests
# proof over the incomplete direct source): recover_immutable opens
# the source read-only, builds the fresh fail-if-exists destination
# through the mapped writer, and publishes it. The leg proves both the
# source and the destination (private output and published main) are
# never read or written through file I/O.
TRACE4="$(mktemp /tmp/iprange-mmap-trace4.XXXXXX)"
TESTLOG4="$(mktemp /tmp/iprange-mmap-trace-test4.XXXXXX)"
trap 'rm -f "$TRACE" "$TESTLOG" "$TRACE2" "$TESTLOG2" "$TRACE3" "$TESTLOG3" "$TRACE4" "$TESTLOG4"' EXIT
echo "check-mmap-trace: tracing TestRecoverImmutableConstructsThePublishedRejectedRange (nice)"
nice -n 10 strace -f -y -e trace=openat,read,pread64,readv,write,writev,pwrite64,lseek,mmap,munmap,close,fcntl \
	-o "$TRACE4" \
	"$(go env GOROOT)/bin/go" test -run '^TestRecoverImmutableConstructsThePublishedRejectedRange$' -count=1 ./internal/recovery/ >"$TESTLOG4" 2>&1
TEST_RC=$?
if [ $TEST_RC -ne 0 ]; then
	echo "check-mmap-trace: recovery construction test failed (rc=$TEST_RC); see $TESTLOG4" >&2
	exit 1
fi

# leg 5: the routed recovery construction (slice 4-12B): the facade
# entry routes through the isolated worker process, which builds the
# fresh fail-if-exists destination through the mapped writer and
# publishes it. The leg proves the parent and the worker never read or
# write the source, the output, or the worker control file through
# file I/O (the control file stays a mapped coordination artifact).
TRACE5="$(mktemp /tmp/iprange-mmap-trace5.XXXXXX)"
TESTLOG5="$(mktemp /tmp/iprange-mmap-trace-test5.XXXXXX)"
trap 'rm -f "$TRACE" "$TESTLOG" "$TRACE2" "$TESTLOG2" "$TRACE3" "$TESTLOG3" "$TRACE4" "$TESTLOG4" "$TRACE5" "$TESTLOG5"' EXIT
echo "check-mmap-trace: tracing TestRoutedRecoverImmutablePublishesReadableOutput (nice)"
nice -n 10 strace -f -y -e trace=openat,read,pread64,readv,write,writev,pwrite64,lseek,mmap,munmap,close,fcntl \
	-o "$TRACE5" \
	"$(go env GOROOT)/bin/go" test -run '^TestRoutedRecoverImmutablePublishesReadableOutput$' -count=1 . >"$TESTLOG5" 2>&1
TEST_RC=$?
if [ $TEST_RC -ne 0 ]; then
	echo "check-mmap-trace: routed recovery test failed (rc=$TEST_RC); see $TESTLOG5" >&2
	exit 1
fi

# leg 6: the routed immutable validation (slice 4-12B): the facade
# entry routes through the isolated worker process, which maps the
# committed source extent and sweeps every page through the claims
# partition. The leg proves the parent and the worker never stream the
# source or the worker control file through file I/O.
TRACE6="$(mktemp /tmp/iprange-mmap-trace6.XXXXXX)"
TESTLOG6="$(mktemp /tmp/iprange-mmap-trace-test6.XXXXXX)"
trap 'rm -f "$TRACE" "$TESTLOG" "$TRACE2" "$TESTLOG2" "$TRACE3" "$TESTLOG3" "$TRACE4" "$TESTLOG4" "$TRACE5" "$TESTLOG5" "$TRACE6" "$TESTLOG6"' EXIT
echo "check-mmap-trace: tracing TestRoutedValidateMatchesInProcess (nice)"
nice -n 10 strace -f -y -e trace=openat,read,pread64,readv,write,writev,pwrite64,lseek,mmap,munmap,close,fcntl \
	-o "$TRACE6" \
	"$(go env GOROOT)/bin/go" test -run '^TestRoutedValidateMatchesInProcess$' -count=1 . >"$TESTLOG6" 2>&1
TEST_RC=$?
if [ $TEST_RC -ne 0 ]; then
	echo "check-mmap-trace: routed validation test failed (rc=$TEST_RC); see $TESTLOG6" >&2
	exit 1
fi

# Match only the fd path inside the strace -y header (read(3<path>, ...));
# the payload bytes after the header may legally contain ".iprdb" text.
# The worker control file is /tmp/.iprange-v4-worker-*.ctl: its marker
# sits mid-path, so the pattern accepts it anywhere in the fd path.
VIOLS="$TRACE.viol"
VIOLS2="$TRACE2.viol"
VIOLS3="$TRACE3.viol"
VIOLS4="$TRACE4.viol"
VIOLS5="$TRACE5.viol"
VIOLS6="$TRACE6.viol"
PATTERN='(read|pread64|readv|write|writev|pwrite64|lseek)\([0-9]+<[^>]*\.(iprdb|v4|iprange-v4-worker-)[^>]*>'
grep -E "$PATTERN" "$TRACE" > "$VIOLS" || true
grep -E "$PATTERN" "$TRACE2" > "$VIOLS2" || true
grep -E "$PATTERN" "$TRACE3" > "$VIOLS3" || true
grep -E "$PATTERN" "$TRACE4" > "$VIOLS4" || true
grep -E "$PATTERN" "$TRACE5" > "$VIOLS5" || true
grep -E "$PATTERN" "$TRACE6" > "$VIOLS6" || true
if [ -s "$VIOLS" ] || [ -s "$VIOLS2" ] || [ -s "$VIOLS3" ] || [ -s "$VIOLS4" ] || [ -s "$VIOLS5" ] || [ -s "$VIOLS6" ]; then
	echo "check-mmap-trace: FAIL - file I/O on a database or worker-control descriptor:"
	head -5 "$VIOLS"
	head -5 "$VIOLS2"
	head -5 "$VIOLS3"
	head -5 "$VIOLS4"
	head -5 "$VIOLS5"
	head -5 "$VIOLS6"
	exit 1
fi

OPEN_CNT=$(grep -c 'openat' "$TRACE" || true)
MMAP_CNT=$(grep -c 'mmap(' "$TRACE" || true)
OPEN_CNT2=$(grep -c 'openat' "$TRACE2" || true)
MMAP_CNT2=$(grep -c 'mmap(' "$TRACE2" || true)
OPEN_CNT3=$(grep -c 'openat' "$TRACE3" || true)
MMAP_CNT3=$(grep -c 'mmap(' "$TRACE3" || true)
OPEN_CNT4=$(grep -c 'openat' "$TRACE4" || true)
MMAP_CNT4=$(grep -c 'mmap(' "$TRACE4" || true)
OPEN_CNT5=$(grep -c 'openat' "$TRACE5" || true)
MMAP_CNT5=$(grep -c 'mmap(' "$TRACE5" || true)
OPEN_CNT6=$(grep -c 'openat' "$TRACE6" || true)
MMAP_CNT6=$(grep -c 'mmap(' "$TRACE6" || true)
echo "check-mmap-trace: PASS - no read/pread64/readv/write/writev/pwrite64/lseek on any v4 artifact or worker-control descriptor"
echo "check-mmap-trace: immutable conformance: openat=$OPEN_CNT mmap=$MMAP_CNT (fixtures mapped, never streamed)"
echo "check-mmap-trace: live validation (worker leg): openat=$OPEN_CNT2 mmap=$MMAP_CNT2 (pair mapped, control mapped, never streamed)"
echo "check-mmap-trace: offline recovery: openat=$OPEN_CNT3 mmap=$MMAP_CNT3 (writable descriptor mapped, never streamed)"
echo "check-mmap-trace: recovery construction: openat=$OPEN_CNT4 mmap=$MMAP_CNT4 (source and output mapped, never streamed)"
echo "check-mmap-trace: routed recovery (worker leg): openat=$OPEN_CNT5 mmap=$MMAP_CNT5 (source, output, control mapped, never streamed)"
echo "check-mmap-trace: routed validation (worker leg): openat=$OPEN_CNT6 mmap=$MMAP_CNT6 (source and control mapped, never streamed)"
exit 0
