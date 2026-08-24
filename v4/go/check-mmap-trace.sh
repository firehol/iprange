#!/bin/sh
# check-mmap-trace.sh - runtime half of the mmap-only evidence.
#
# Traces real open+lookup and live-validation sessions (the Rust
# conformance cross-open test and the public LiveCurrent validation
# proof) and asserts that the database descriptors are never read
# through file I/O: after openat + OFD lock, every byte of the
# fixtures must arrive through mmap. Any
# read/pread64/readv/write/lseek on an fd whose path is a v4 artifact
# fails the check.
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

# Match only the fd path inside the strace -y header (read(3<path>, ...));
# the payload bytes after the header may legally contain ".iprdb" text.
VIOLS="$TRACE.viol"
VIOLS2="$TRACE2.viol"
VIOLS3="$TRACE3.viol"
grep -E '(read|pread64|readv|write|writev|pwrite64|lseek)\([0-9]+<[^>]*\.iprdb>' "$TRACE" > "$VIOLS" || true
grep -E '(read|pread64|readv|write|writev|pwrite64|lseek)\([0-9]+<[^>]*\.iprdb>' "$TRACE2" > "$VIOLS2" || true
grep -E '(read|pread64|readv|write|writev|pwrite64|lseek)\([0-9]+<[^>]*\.iprdb>' "$TRACE3" > "$VIOLS3" || true
if [ -s "$VIOLS" ] || [ -s "$VIOLS2" ] || [ -s "$VIOLS3" ]; then
	echo "check-mmap-trace: FAIL - file I/O on a database descriptor:"
	head -5 "$VIOLS"
	head -5 "$VIOLS2"
	head -5 "$VIOLS3"
	exit 1
fi

OPEN_CNT=$(grep -c 'openat' "$TRACE" || true)
MMAP_CNT=$(grep -c 'mmap(' "$TRACE" || true)
OPEN_CNT2=$(grep -c 'openat' "$TRACE2" || true)
MMAP_CNT2=$(grep -c 'mmap(' "$TRACE2" || true)
OPEN_CNT3=$(grep -c 'openat' "$TRACE3" || true)
MMAP_CNT3=$(grep -c 'mmap(' "$TRACE3" || true)
echo "check-mmap-trace: PASS - no read/pread64/readv/write/writev/pwrite64/lseek on any .iprdb descriptor"
echo "check-mmap-trace: immutable: openat=$OPEN_CNT mmap=$MMAP_CNT (fixtures mapped, never streamed)"
echo "check-mmap-trace: live validation: openat=$OPEN_CNT2 mmap=$MMAP_CNT2 (pair mapped, never streamed)"
echo "check-mmap-trace: offline recovery: openat=$OPEN_CNT3 mmap=$MMAP_CNT3 (writable descriptor mapped, never streamed)"
exit 0
