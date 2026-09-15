//go:build unix

package calleropen

import (
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Every descriptor-counting or descriptor-capping child in this package
// measures a table the case itself establishes: stdio, then the held
// descriptors a case claims with raw syscalls, then the one measured call.
// That is only true if the child starts from stdio alone, and Go's exec path
// does not guarantee it: on Linux there is no descriptor sweep in the child
// at all (syscall carries no CloseFds arm any more, and the child's table is
// simply whatever survived execve), so a descriptor an outer launcher holds
// without FD_CLOEXEC arrives in every process below it.
//
// The wave battery is exactly such a launcher. `exec 3>&1 4>&2`
// (v4/cli/battery-w1925.sh) keeps the battery's own stdout and stderr
// duplicated at slots 3 and 4 for its whole run, so both descriptors land in
// each re-exec'd child here. Under `go test ./...` alone they do not, which
// is why the readiness pins passed standalone and failed in the battery: five
// descriptors where a case had established three, and RLIMIT_NOFILE 5 with two
// of its five numbers already spent.
//
// closeInheritedDescriptors makes the child own its starting table instead of
// inheriting one. It is deliberately conservative, because closing the wrong
// descriptor here would replace a host-state defect with a broken runtime:
//
//   - the three standard streams are never touched: the child reports through
//     them, and os/exec hands them over as the child's own 0, 1 and 2;
//   - a descriptor whose kernel link target names an inode family the Go
//     runtime itself uses is left open and reported rather than closed. The
//     runtime creates nothing above the standard streams before TestMain
//     serves a child role on the platforms this suite is measured on, so a
//     kept descriptor of that kind means the poller (or an equivalent) already
//     predates the measured call: the case must then fail loudly, because
//     quietly closing it would let the case pass without measuring anything;
//   - a descriptor that cannot be classified, because the platform exposes no
//     /proc/self/fd to read its target from, is likewise kept and reported;
//   - everything else (pipes, regular files, sockets: the shapes an outer
//     shell or harness holds) is closed.
//
// Discovery uses fcntl(F_GETFD) rather than a directory walk so the sweep
// itself takes no descriptor and registers nothing with the poller, for the
// same reason the table reads in this package do.
//
// internal/cli/legacy/poller_free_test.go carries the same sweep for its own
// child. It cannot be shared: an unexported test helper cannot cross a package
// boundary, and the alternatives are worse — exporting it from this package
// would add a descriptor-destroying call to the product surface, and a new
// internal test-support package would change the module's package inventory
// that the committed coverage and kind-coverage reports pin.

// childDescriptorsAreClassifiable reports whether this platform lets the sweep
// identify an open descriptor by its kernel link target. Linux exposes
// /proc/self/fd; the kqueue platforms (darwin, the BSDs) do not, and design
// section 13.5 records them as unmeasured by this wave. The answer is asked of
// the kernel at runtime rather than read off a build tag, because a Linux host
// with no /proc mounted has the same problem the tag would only describe.
//
// Where descriptors cannot be classified, a case that caps its table cannot
// distinguish "the product registered the poller" from "something the launcher
// handed over is still open", and its child would die in the kernel and be
// reported as a product regression. The cases therefore skip on the stated
// reason instead of running a measurement whose premise is unavailable.
func childDescriptorsAreClassifiable() bool {
	var buffer [1]byte
	length, err := unix.Readlink("/proc/self/fd/0", buffer[:])
	return err == nil && length > 0
}

func closeInheritedDescriptors() (report string, ok bool) {
	window := uint64(maxScanDescriptors)
	// Rlimit.Max is uint64 on Linux, OpenBSD, NetBSD and Darwin but int64 on
	// FreeBSD and DragonFly, so it is normalized before the comparison.
	var rlimit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &rlimit); err == nil {
		if hard := uint64(rlimit.Max); hard > 0 && hard < window {
			window = hard
		}
	}
	var closed, kept []string
	for fd := childStandardDescriptors; fd < int(window); fd++ {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil {
			continue // not an open descriptor
		}
		var buffer [256]byte
		length, linkErr := unix.Readlink("/proc/self/fd/"+strconv.Itoa(fd), buffer[:])
		number := strconv.Itoa(fd)
		switch {
		case linkErr != nil:
			// Unclassifiable: keep it. The count assertions in the cases
			// decide, and they decide against a child that cannot prove its
			// own starting table.
			kept = append(kept, number+"=unclassifiable:"+linkErr.Error())
		case childRuntimeOwnedLink(string(buffer[:length])):
			kept = append(kept, number+"=runtime:"+shortChildLink(string(buffer[:length])))
		default:
			if err := unix.Close(fd); err != nil {
				kept = append(kept, number+"=close-failed:"+err.Error())
				continue
			}
			closed = append(closed, number+"="+shortChildLink(string(buffer[:length])))
		}
	}
	if len(closed) == 0 && len(kept) == 0 {
		return "none", true
	}
	return "closed[" + strings.Join(closed, ",") + "] kept[" + strings.Join(kept, ",") + "]", true
}

// childStandardDescriptors is the number of descriptors a child always keeps:
// the standard streams os/exec handed over as the child's own 0, 1 and 2.
const childStandardDescriptors = 3

// childRuntimeOwnedLink reports whether one link target names an inode family
// the Go runtime can own. The poller's descriptors live under anon_inode:, and
// the runtime's signal queue under signalfd:; closing either in a child that
// has not created them is not this sweep's business.
func childRuntimeOwnedLink(target string) bool {
	for _, prefix := range [...]string{"anon_inode:", "signalfd:", "pidfd:"} {
		if len(target) >= len(prefix) && target[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// shortChildLink keeps one reported descriptor recognizable without putting a
// whole path in every failure line.
func shortChildLink(target string) string {
	switch target {
	case "/dev/null":
		return "devnull"
	case "anon_inode:[eventpoll]":
		return "eventpoll"
	case "anon_inode:[eventfd]":
		return "eventfd"
	}
	if i := lastChildSlash(target); i >= 0 {
		return target[i+1:]
	}
	return target
}

func lastChildSlash(text string) int {
	for i := len(text) - 1; i >= 0; i-- {
		if text[i] == '/' {
			return i
		}
	}
	return -1
}
