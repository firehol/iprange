//go:build windows

package live

// basenameEncodingWindowsUtf16Le is the Windows UTF-16LE encoding
// (Rust BasenameEncoding::WindowsUtf16Le = 2).
const basenameEncodingWindowsUtf16Le BasenameEncoding = 2

// validateEncodingBinding proves one WindowsUtf16Le payload is well
// formed (Rust validate_windows_utf16le): even byte length, no zero,
// slash, or backslash units, and no unpaired or malformed surrogate
// pairs. Names Go produces are ASCII components, so no output ever
// carries a surrogate; the full walk still rejects hostile kind-2
// payloads exactly like the Rust arm instead of failing open.
func validateEncodingBinding(encoding BasenameEncoding, bytes []byte) error {
	if encoding != basenameEncodingWindowsUtf16Le {
		return nsInvalidNameError()
	}
	if len(bytes)%2 != 0 {
		return nsInvalidNameError()
	}
	for i := 0; i+1 < len(bytes); i += 2 {
		unit := uint16(bytes[i]) | uint16(bytes[i+1])<<8
		if unit == 0 || unit == '/' || unit == '\\' {
			return nsInvalidNameError()
		}
		switch {
		case unit >= 0xd800 && unit <= 0xdbff:
			// A high surrogate must be followed by a low surrogate
			// (Rust unit[..] walk).
			if i+3 >= len(bytes) {
				return nsInvalidNameError()
			}
			low := uint16(bytes[i+2]) | uint16(bytes[i+3])<<8
			if low < 0xdc00 || low > 0xdfff {
				return nsInvalidNameError()
			}
			i += 2
		case unit >= 0xdc00 && unit <= 0xdfff:
			return nsInvalidNameError()
		}
	}
	return nil
}
