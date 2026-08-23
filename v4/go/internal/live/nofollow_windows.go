//go:build windows

package live

// isNofollowSymlink on Windows reports false: the no-follow
// final-symlink class is a POSIX openat concept (Rust
// namespace::is_nofollow_symlink non-unix arm), and the whole live
// surface refuses before any path access on Windows.
func isNofollowSymlink(error) bool { return false }
