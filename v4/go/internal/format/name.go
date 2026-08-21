package format

// Name rules shared by the reader and the writer: the section-3 reserved
// basename prefix and the reader-coordination suffix. The reserved
// matches are byte-wise ASCII-case-insensitive (Rust
// eq_ignore_ascii_case); Unicode folding is not applied, so spellings
// Rust accepts (for example ".İPRANGE-" with the Turkish dotted I) are
// accepted here too.
const (
	// ReservedBasenamePrefix is the reserved publication prefix
	// (binary-format-v4.md section 3, Rust platform::destination_names).
	ReservedBasenamePrefix = ".iprange-"

	// CoordinationSuffix is the reader-coordination twin suffix
	// (binary-format-v4.md section 3, Rust platform::destination_names).
	CoordinationSuffix = ".readers"
)

// AsciiFoldLower folds one ASCII byte to lowercase (Rust
// eq_ignore_ascii_case).
func AsciiFoldLower(value byte) byte {
	if 'A' <= value && value <= 'Z' {
		return value + 'a' - 'A'
	}
	return value
}

// AsciiFoldHasPrefix reports an ASCII-case-insensitive prefix match.
func AsciiFoldHasPrefix(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for index := range len(prefix) {
		if AsciiFoldLower(s[index]) != prefix[index] {
			return false
		}
	}
	return true
}

// AsciiFoldHasSuffix reports an ASCII-case-insensitive suffix match.
func AsciiFoldHasSuffix(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	offset := len(s) - len(suffix)
	for index := range len(suffix) {
		if AsciiFoldLower(s[offset+index]) != suffix[index] {
			return false
		}
	}
	return true
}
