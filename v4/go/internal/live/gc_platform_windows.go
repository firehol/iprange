//go:build windows

package live

import "os"

// gcBasenameEncodingValue is the Windows UTF-16LE tag (Rust
// namespace::BASENAME_ENCODING_KIND on windows).
func gcBasenameEncodingValue() BasenameEncoding { return basenameEncodingWindowsUtf16Le }

// gcCreationSecurityKind is the Windows creator-only kind (Rust
// namespace::CREATION_SECURITY_KIND on windows).
func gcCreationSecurityKind() uint16 { return 2 }

// gcNameBytesPlatform encodes one name as UTF-16LE code units (Rust
// Name::bytes on windows); the shared utf16LEBytes helper is tested
// on every platform.
func gcNameBytesPlatform(name string) []byte {
	return utf16LEBytes(name)
}

// gcFileSize reports one retained file's size from its handle
// information (Rust File::metadata().len()).
func gcFileSize(file *os.File) (uint64, error) {
	info, err := handleInfo(file)
	if err != nil {
		return 0, err
	}
	return uint64(info.FileSizeHigh)<<32 | uint64(info.FileSizeLow), nil
}

// gcSourceEncodedMatches compares one stored UTF-16LE source against
// an ASCII name (Rust encoded-byte equality).
func gcSourceEncodedMatches(encoded []byte, name string) bool {
	return string(encoded) == string(gcNameBytesPlatform(name))
}

// gcDecodeNameBytes projects one stored UTF-16LE source back to ASCII
// when every unit is a plain ASCII character (Rust ascii projection of
// the encoded source; non-ASCII names cannot bind in the Go surface).
func gcDecodeNameBytes(encoded []byte) (string, bool) {
	if len(encoded)%2 != 0 {
		return "", false
	}
	out := make([]byte, 0, len(encoded)/2)
	for i := 0; i+1 < len(encoded); i += 2 {
		if encoded[i+1] != 0 || encoded[i] > 0x7F {
			return "", false
		}
		out = append(out, encoded[i])
	}
	return string(out), true
}
