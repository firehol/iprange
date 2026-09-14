//go:build unix && (linux || darwin)

package handlers

import "golang.org/x/sys/unix"

// installDescriptorLimit lowers RLIMIT_NOFILE to the requested ceiling,
// using the descriptor-count width this platform's rlimit fields carry.
func installDescriptorLimit(limit uint64) error {
	return unix.Setrlimit(unix.RLIMIT_NOFILE, &unix.Rlimit{Cur: limit, Max: limit})
}
