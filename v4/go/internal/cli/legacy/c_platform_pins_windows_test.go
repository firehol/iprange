//go:build windows

package legacy

import (
	"strings"
	"syscall"
)

// errorInvalidName is Win32 ERROR_INVALID_NAME. Opening a path the
// platform will not name fails with this code rather than with a
// "name too long" errno, because the Windows check is on the syntax of the
// whole path, not on a length limit shared with POSIX.
const errorInvalidName = syscall.Errno(123)

// rawBytePin renders a file name the way the platform stores it. Windows
// names are UTF-16, so a byte that has no representation there arrives as
// U+FFFD: the released tool's byte-oriented name echo cannot survive the
// path layer, and the difference is a property of the platform rather than
// of the engine. It is recorded here as a scoped difference, and every
// other byte of the same line stays pinned.
func rawBytePin(s string) string { return strings.ReplaceAll(s, "\xff", "\ufffd") }

// overCapPin renders the message half of the over-long-open diagnostic
// with the text this platform formats for the code its open actually
// failed with. The error meaning (an open refused for the name), the exit
// status, and the "iprange: NAME - MESSAGE" line shape stay contractual
// and are still compared; only the OS-supplied text moves, and it moves to
// whatever the platform's own message-format contract emits for that code,
// which is locale-dependent. This is never "any error": the code is pinned
// by errorInvalidName and TestPlatformPinRenderingsAreEffective requires
// the substitution to have happened.
func overCapPin(s string) string {
	return strings.ReplaceAll(s, posixLineCapMessage, errorInvalidName.Error())
}
