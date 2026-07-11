//go:build linux

package iprangedb

import "syscall"

// getThreadID returns the OS thread ID via the gettid syscall (Linux only).
// This differentiates same-process readers in different goroutines/threads.
func getThreadID() uint32 {
	return uint32(syscall.Gettid())
}
