package writer

// Test-only file helper split from the Rust-mirror tests so the writer
// package keeps one small os surface.

import "os"

func osWriteFile(path string, raw []byte, perm os.FileMode) error {
	return os.WriteFile(path, raw, perm)
}
