// Package calleropen owns the caller-side open of one persistent file.
//
// The engines must decide what a path names on the descriptor they
// actually opened, never on an earlier path stat: a node swapped in
// between the two would otherwise be judged by the stale answer. That
// requires O_NONBLOCK on the open (a blocking read-only open of a FIFO
// waits for a writer that may never arrive) and an fstat of the
// returned descriptor.
//
// Handing those flags to os.OpenFile is not an option either. On Linux
// the open kind alone decides pollability (os/file_unix.go: pollable :=
// kind == kindOpenFile || ...); the regular-file and directory
// carve-outs that could clear it are compiled only for the Apple and BSD
// platforms (os/file_unix.go:165-192), so every os.Open, os.OpenFile and
// os.Create registers the descriptor with the runtime netpoller
// regardless of O_NONBLOCK, and os.NewFile on a descriptor whose F_GETFL
// reports O_NONBLOCK does the same. The first registration initializes
// the netpoller - one epoll fd plus one eventfd on Linux
// (runtime/netpoll_epoll.go:21-31). That initialization has no failure
// path: under a low RLIMIT_NOFILE the runtime aborts the process with
// "fatal error: runtime: netpollinit failed" (exit status 2, no answer
// to the caller) instead of the io error the open should have reported.
// A file-only SDK must not depend on the network poller at all.
//
// Open therefore issues open(2) on a bare descriptor and clears
// O_NONBLOCK before wrapping the handle in an *os.File. The FIFO
// refusal keeps working, because promptness is a property of the open
// itself and every caller judges regularity by fstat, never by reading
// the descriptor before that judgement. Clearing the flag is
// semantically neutral for the nodes this package is used on: regular
// files ignore O_NONBLOCK, and non-regular nodes are refused before any
// read.
package calleropen

import "os"

// Open issues open(2) on path with the supplied flags and permission,
// and returns the opened node as a blocking *os.File.
//
// Every call is offered to the armed caller-open witness (see
// WatchOpensForTest), which is how the committed pins prove a caller
// reached this owner instead of opening the path itself.
//
// The caller owns the flags, including O_NONBLOCK: passing it is what
// makes a FIFO or other slow-opening node answer immediately, and the
// returned descriptor has the flag cleared so the handle stays out of
// the runtime network poller. A failure is reported as an
// *os.PathError, exactly like os.OpenFile, so callers keep their own
// error classification.
func Open(path string, flags int, perm os.FileMode) (*os.File, error) {
	noteOpen(openOpCall, path, flags)
	return openPath(path, flags, perm)
}

// Blocking wraps an already-opened descriptor as a blocking *os.File.
//
// Every call is offered to the armed caller-open witness under the name
// the descriptor carries, separately from Open, so a caller cannot satisfy
// an open pin by wrapping a descriptor it opened itself.
//
// Callers that must issue open(2)/openat(2) themselves (a relative open
// under a bound directory descriptor, for example) still need the same
// protection: os.NewFile re-reads the descriptor flags with F_GETFL and
// attaches a non-blocking handle to the runtime netpoller, whose
// initialization aborts the process under a low RLIMIT_NOFILE. Blocking
// clears O_NONBLOCK when present and then wraps, so the handle stays out
// of the poller. The descriptor is owned by the returned file, which is
// closed when the file is closed; a failure closes it here.
func Blocking(fd int, name string) (*os.File, error) {
	noteOpen(openOpWrap, name, 0)
	return blockingFile(fd, name)
}
