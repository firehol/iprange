//go:build windows

package publication

import "github.com/firehol/iprange/v4/go/internal/live"

// basenameEncodingWindowsUtf16Le is the Windows UTF-16LE encoding
// (Rust BasenameEncoding::WindowsUtf16Le = 2).
const basenameEncodingWindowsUtf16Le basenameEncoding = live.BasenameEncoding(2)

// validateEncoding proves one WindowsUtf16Le payload binds (Rust
// validate_windows_utf16le); the authority lives in live.
func validateEncoding(encoding basenameEncoding, bytes []byte) error {
	return live.ValidateEncodingBinding(encoding, bytes)
}
