package format

import "fmt"

// ParseCanonicalUint64 parses the wire's canonical unsigned decimal
// string form (Rust u64_string): "0" or digits without a leading
// zero, within [0, 2^64-1]. This is the single authority for canonical
// u64 parsing in the Go peer; the CLI validators and the SDK
// publication-result decoder both delegate here.
func ParseCanonicalUint64(text string) (uint64, error) {
	if text == "0" {
		return 0, nil
	}
	if text == "" || text[0] == '0' {
		return 0, fmt.Errorf("not a canonical unsigned decimal string")
	}
	var value uint64
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a canonical unsigned decimal string")
		}
		digit := uint64(c - '0')
		if value > (^uint64(0)-digit)/10 {
			return 0, fmt.Errorf("not a canonical unsigned decimal string")
		}
		value = value*10 + digit
	}
	return value, nil
}
