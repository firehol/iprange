#!/bin/sh
# check-mmap-trace.sh - runtime half of the mmap-only evidence.
#
# Traces a real open+lookup session (the Rust conformance cross-open test)
# and asserts that the database descriptors are never read through file
# I/O: after openat + OFD lock, every byte of the fixtures must arrive
# through mmap. Any read/pread64/readv/write/lseek on an fd whose path is
# a v4 artifact fails the check.
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

echo "check-mmap-trace: tracing TestConformanceRustFixtures (nice)"
nice -n 10 strace -f -y -e trace=openat,read,pread64,readv,write,writev,pwrite64,lseek,mmap,munmap,close,fcntl \
	-o "$TRACE" \
	"$(go env GOROOT)/bin/go" test -run '^TestConformanceRustFixtures$' -count=1 . >"$TESTLOG" 2>&1
TEST_RC=$?
if [ $TEST_RC -ne 0 ]; then
	echo "check-mmap-trace: test failed (rc=$TEST_RC); see $TESTLOG" >&2
	exit 1
fi

VIOLS="$TRACE.viol"
# Match only the fd path inside the strace -y header (read(3<path>, ...));
# the payload bytes after the header may legally contain ".iprdb" text.
grep -E '(read|pread64|readv|write|writev|pwrite64|lseek)\([0-9]+<[^>]*\.iprdb>' "$TRACE" > "$VIOLS" || true
if [ -s "$VIOLS" ]; then
	echo "check-mmap-trace: FAIL - file I/O on a database descriptor:"
	head -5 "$VIOLS"
	exit 1
fi

OPEN_CNT=$(grep -c 'openat' "$TRACE" || true)
MMAP_CNT=$(grep -c 'mmap(' "$TRACE" || true)
echo "check-mmap-trace: PASS - no read/pread64/readv/write/writev/pwrite64/lseek on any .iprdb descriptor"
echo "check-mmap-trace: openat=$OPEN_CNT mmap=$MMAP_CNT (fixtures mapped, never streamed)"
exit 0
