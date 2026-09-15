//go:build !linux

package random

import "crypto/rand"

// entropy is the platform fallback: the Linux network-poller hazard the
// getrandom(2) owner exists to avoid (epoll fd plus eventfd, with no
// failure path) does not exist on these platforms: no epoll/eventfd
// allocation on the kqueue pollers, and on Windows the I/O completion
// port allocates no descriptor at all. crypto/rand stays authoritative
// there until the design section 6 platform clause brings each kqueue
// host its own measurement.
func entropy(b []byte) error {
	_, err := rand.Read(b)
	return err
}
