//go:build unix

package legacy

import "syscall"

// directoryReadErrno is the code this platform reports for reading an
// already-opened directory handle. glibc's fgets() on a directory
// returned by fopen() surfaces the read(2) failure, so the released tool
// sees EISDIR here (src/ipset_load.c:270-280). The BSDs and macOS report
// the same errno for the same call.
//
// The companion case --- the open itself failing --- reports EACCES,
// ENOENT, or another errno and is never the directory case;
// readLegacyInput requires a "read" op error before it consults this
// code, so that distinction is enforced by shape and not by this value
// alone.
const directoryReadErrno = syscall.EISDIR
