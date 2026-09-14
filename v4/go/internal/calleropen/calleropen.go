// Package calleropen owns the caller-side open of one persistent file.
//
// The engines must decide what a path names on the descriptor they
// actually opened, never on an earlier path stat: a node swapped in
// between the two would otherwise be judged by the stale answer. That
// requires O_NONBLOCK on the open (a blocking read-only open of a FIFO
// waits for a writer that may never arrive) and an fstat of the
// returned descriptor.
//
// Handing those flags to os.OpenFile is not an option. os.OpenFile
// derives the descriptor's pollability from the flags it was given
// (os/file_unix.go: newFile(r, name, kindOpenFile,
// unix.HasNonblockFlag(flag))), so every O_NONBLOCK open attaches the
// handle to the runtime netpoller, and the first attachment initializes
// the netpoller — one epoll fd plus one eventfd on Linux. That
// initialization has no failure path: under a low RLIMIT_NOFILE the
// runtime aborts the process with "fatal error: runtime: netpollinit
// failed" (exit status 2, no answer to the caller) instead of the io
// error the open should have reported. A file-only SDK must not depend
// on the network poller at all.
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
// The caller owns the flags, including O_NONBLOCK: passing it is what
// makes a FIFO or other slow-opening node answer immediately, and the
// returned descriptor has the flag cleared so the handle stays out of
// the runtime network poller. A failure is reported as an
// *os.PathError, exactly like os.OpenFile, so callers keep their own
// error classification.
func Open(path string, flags int, perm os.FileMode) (*os.File, error) {
	return openPath(path, flags, perm)
}

// Blocking wraps an already-opened descriptor as a blocking *os.File.
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
	return blockingFile(fd, name)
}

// HasDescriptorReserve reports whether the process can still claim
// count descriptors.
//
// The probe opens count handles on the platform null device and closes
// them again; it allocates nothing that outlives the call and asks no
// filesystem of the caller. Callers use it to refuse work they cannot
// resource before the first handler open, so the answer is the io class
// instead of an error surfaced from wherever the descriptor table
// happened to run out.
func HasDescriptorReserve(count int) bool {
	if count <= 0 {
		return true
	}
	files := make([]*os.File, 0, count)
	for range count {
		file, err := Open(os.DevNull, os.O_RDONLY, 0)
		if err != nil {
			for _, open := range files {
				_ = open.Close()
			}
			return false
		}
		files = append(files, file)
	}
	for _, open := range files {
		_ = open.Close()
	}
	return true
}
