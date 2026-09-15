package calleropen

// Poller-readiness table read. The design (SOW-0028 wave-19.25 section 6)
// names /proc/self/fd on Linux with the fcntl(F_GETFD) scan as the
// portable fallback. The getdents walk over the process's own descriptor
// directory was tried first and removed before shipping: in a pressured
// child the record walk did not terminate (SIGQUIT caught the session
// spinning inside the parser with only the table descriptor at slot 3),
// and the hang hazard is not worth a microsecond of scan time. The scan
// (wired through the linux build too) reads the kernel's per-descriptor
// state directly, never opens a filesystem path, and gives every platform
// the same decision boundary.
