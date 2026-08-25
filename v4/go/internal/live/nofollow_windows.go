//go:build windows

package live

// isNofollowSymlink on Windows reports false: the no-follow
// final-symlink class is a POSIX openat concept (Rust
// namespace::is_nofollow_symlink non-unix arm). Windows opens refuse
// reparse points natively through FILE_FLAG_OPEN_REPARSE_POINT plus
// the attribute check, so no error-class fold exists here.
func isNofollowSymlink(error) bool { return false }
