//go:build linux

package random

import (
	"errors"

	"golang.org/x/sys/unix"
)

// entropy fills b from getrandom(2) directly.
//
// crypto/rand.Read reaches the kernel through
// crypto/internal/sysrand.Read, which arms a one-shot warning timer on
// first use (crypto/internal/sysrand/rand.go:38-41). Any runtime timer
// arm initializes the network poller (runtime/time.go:455-461), and the
// poller's creation has no failure path under a low RLIMIT_NOFILE: the
// process would abort with "fatal error: runtime: eventfd failed"
// instead of answering (design section 2). getrandom(2) is the same
// kernel entropy source with neither a descriptor nor a timer. Flags
// are zero, so the call blocks until the pool is initialized exactly
// like the crypto/rand path it replaces; EINTR resumes the remaining
// bytes, matching the io.Reader contract crypto/rand offered.
func entropy(b []byte) error {
	for len(b) > 0 {
		n, err := unix.Getrandom(b, 0)
		if n > 0 {
			b = b[n:]
			continue
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}
