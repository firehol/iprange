//go:build darwin

package live

// atNofollow is the fstatat AT_SYMLINK_NOFOLLOW flag (darwin
// sys/fcntl.h value 0x20; x/sys does not export the constant).
const atNofollow = 0x20
