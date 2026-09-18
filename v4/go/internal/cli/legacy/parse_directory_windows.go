//go:build windows

package legacy

import "syscall"

// errorInvalidFunction is Win32 ERROR_INVALID_FUNCTION, which ReadFile
// returns for a handle opened on a directory: a directory handle does not
// support the read operation, and there is no dedicated "is a directory"
// error in the Windows error space.
const errorInvalidFunction = syscall.Errno(0x00000001)

// directoryReadErrno is the code this platform reports for reading an
// already-opened directory handle.
//
// Windows numbers are shared with the CRT errnos Go declares (EPERM is
// also 1), so this constant is read as a Win32 error code and not as a
// POSIX errno: a read failure that is a permission or sharing problem
// arrives as ERROR_ACCESS_DENIED (5) or ERROR_SHARING_VIOLATION (32) and
// is therefore never classified here, and readLegacyInput additionally
// requires a "read" op error, so an open the platform refused cannot reach
// this comparison at all.
const directoryReadErrno = errorInvalidFunction
