//go:build unix && freebsd

package handlers

import "golang.org/x/sys/unix"

// installDescriptorLimit lowers RLIMIT_NOFILE to the requested ceiling,
// using the descriptor-count width this platform's rlimit fields carry.
func installDescriptorLimit(limit uint64) error {
	value := int64(limit)
	return unix.Setrlimit(unix.RLIMIT_NOFILE, &unix.Rlimit{Cur: value, Max: value})
}
