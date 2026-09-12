//go:build linux

package legacy

import "syscall"

// errnoText maps syscall errnos to the glibc strerror(3) texts the
// released C iprange CLI prints, so the linux diagnostics are
// byte-exact (generated from the oracle libc). Non-linux targets use
// parse_strerror_other.go; errnos missing from a target's table fall
// back to Errno.Error() in strerror.
var errnoText = map[syscall.Errno]string{
	syscall.EPERM:           "Operation not permitted",
	syscall.ENOENT:          "No such file or directory",
	syscall.ESRCH:           "No such process",
	syscall.EINTR:           "Interrupted system call",
	syscall.EIO:             "Input/output error",
	syscall.ENXIO:           "No such device or address",
	syscall.E2BIG:           "Argument list too long",
	syscall.ENOEXEC:         "Exec format error",
	syscall.EBADF:           "Bad file descriptor",
	syscall.ECHILD:          "No child processes",
	syscall.EAGAIN:          "Resource temporarily unavailable",
	syscall.ENOMEM:          "Cannot allocate memory",
	syscall.EACCES:          "Permission denied",
	syscall.EFAULT:          "Bad address",
	syscall.ENOTBLK:         "Block device required",
	syscall.EBUSY:           "Device or resource busy",
	syscall.EEXIST:          "File exists",
	syscall.EXDEV:           "Invalid cross-device link",
	syscall.ENODEV:          "No such device",
	syscall.ENOTDIR:         "Not a directory",
	syscall.EISDIR:          "Is a directory",
	syscall.EINVAL:          "Invalid argument",
	syscall.ENFILE:          "Too many open files in system",
	syscall.EMFILE:          "Too many open files",
	syscall.ENOTTY:          "Inappropriate ioctl for device",
	syscall.ETXTBSY:         "Text file busy",
	syscall.EFBIG:           "File too large",
	syscall.ENOSPC:          "No space left on device",
	syscall.ESPIPE:          "Illegal seek",
	syscall.EROFS:           "Read-only file system",
	syscall.EMLINK:          "Too many links",
	syscall.EPIPE:           "Broken pipe",
	syscall.EDOM:            "Numerical argument out of domain",
	syscall.ERANGE:          "Numerical result out of range",
	syscall.EDEADLK:         "Resource deadlock avoided",
	syscall.ENAMETOOLONG:    "File name too long",
	syscall.ENOLCK:          "No locks available",
	syscall.ENOSYS:          "Function not implemented",
	syscall.ENOTEMPTY:       "Directory not empty",
	syscall.ELOOP:           "Too many levels of symbolic links",
	syscall.ENODATA:         "No data available",
	syscall.ETIME:           "Timer expired",
	syscall.EOVERFLOW:       "Value too large for defined data type",
	syscall.ESTALE:          "Stale file handle",
	syscall.EDQUOT:          "Disk quota exceeded",
	syscall.EOWNERDEAD:      "Owner died",
	syscall.ENOTRECOVERABLE: "State not recoverable",
}
