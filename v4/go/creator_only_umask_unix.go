//go:build !windows

package iprangedb

import "golang.org/x/sys/unix"

func setUmask(mask int) int {
	return unix.Umask(mask)
}
