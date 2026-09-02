package format

import "fmt"

// DecodeCanonicalBase64 decodes the strict canonical base64 form used
// by the v4 wire (Rust iprange-cli lifecycle.rs decode_base64): length
// a multiple of four, standard alphabet only, padding only in the
// final quartet, at most two pad bytes, and zero non-canonical
// trailing bits in the final quartet. This is the single authority for
// wire base64 in the Go peer; the CLI validators and the SDK
// publication-result decoder both delegate here.
func DecodeCanonicalBase64(text string) ([]byte, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	if len(text)%4 != 0 {
		return nil, fmt.Errorf("base64 length must be a multiple of four")
	}
	bytes := []byte(text)
	var output []byte
	chunkCount := len(bytes) / 4
	for index := 0; index < chunkCount; index++ {
		chunk := bytes[index*4 : index*4+4]
		last := index == chunkCount-1
		padding := 0
		for i := len(chunk) - 1; i >= 0 && chunk[i] == '='; i-- {
			padding++
		}
		if padding > 2 || (last && padding == 0 && len(bytes) == 0) {
			return nil, fmt.Errorf("base64 padding is invalid")
		}
		if !last && padding != 0 {
			return nil, fmt.Errorf("base64 padding is not at the end")
		}
		var word uint32
		for position, c := range chunk {
			var digit uint32
			if c == '=' {
				if !last || position < 4-padding {
					return nil, fmt.Errorf("base64 padding is invalid")
				}
				digit = 0
			} else {
				found := -1
				for i := 0; i < len(alphabet); i++ {
					if alphabet[i] == c {
						found = i
						break
					}
				}
				if found < 0 {
					return nil, fmt.Errorf("base64 uses the standard alphabet only")
				}
				digit = uint32(found)
			}
			word = word<<6 | digit
		}
		significant := 3 - padding
		decoded := [4]byte{0, byte(word >> 16), byte(word >> 8), byte(word)}
		output = append(output, decoded[1:1+significant]...)
		if padding > 0 {
			bits := padding * 8
			if word&((1<<bits)-1) != 0 {
				return nil, fmt.Errorf("base64 has non-canonical trailing bits")
			}
		}
	}
	return output, nil
}
