//go:build windows

package publication

// basenameEncodingWindowsUtf16Le is the Windows UTF-16LE encoding
// (Rust BasenameEncoding::WindowsUtf16Le = 2).
const basenameEncodingWindowsUtf16Le basenameEncoding = 2

// validateEncoding proves one WindowsUtf16Le payload is well formed
// (Rust validate_windows_utf16le): even byte length and no zero, slash,
// or backslash units. Go publication names are ASCII components, so
// the surrogate-pair walk of the Rust arm cannot fire; the unit checks
// stay for defense in depth.
func validateEncoding(encoding basenameEncoding, bytes []byte) error {
	if encoding != basenameEncodingWindowsUtf16Le {
		return &nameBindingError{}
	}
	if len(bytes)%2 != 0 {
		return &nameBindingError{}
	}
	for i := 0; i+1 < len(bytes); i += 2 {
		unit := uint16(bytes[i]) | uint16(bytes[i+1])<<8
		if unit == 0 || unit == '/' || unit == '\\' {
			return &nameBindingError{}
		}
	}
	return nil
}
