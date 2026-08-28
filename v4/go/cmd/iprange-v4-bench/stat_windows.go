//go:build windows

package main

import "os"

// statBlocks reports zero on platforms without the unix st_blocks
// field (the physical size is unreported there, matching the Rust
// reported-size rules).
func statBlocks(_ os.FileInfo) int64 { return 0 }
