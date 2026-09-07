package live

import "unicode/utf16"

// utf16LEBytes returns one name's UTF-16LE code-unit bytes (Rust
// Name::bytes on windows); gcNameBytesPlatform delegates here so the
// encoding is testable on every platform.
func utf16LEBytes(name string) []byte {
	units := utf16.Encode([]rune(name))
	encoded := make([]byte, 0, len(units)*2)
	for _, unit := range units {
		encoded = append(encoded, byte(unit), byte(unit>>8))
	}
	return encoded
}
