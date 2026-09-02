package format

import "testing"

// TestDecodeCanonicalBase64 pins the exact Rust decode_base64 rules:
// padding placement, alphabet, length, and the non-canonical
// trailing-bit rejection (lifecycle.rs decode_base64).
func TestDecodeCanonicalBase64(t *testing.T) {
	valid := map[string]string{
		"":             "",
		"Zg==":         "f",
		"Zm8=":         "fo",
		"Zm9v":         "foo",
		"Zm9vLm5ldHM=": "foo.nets",
	}
	for input, want := range valid {
		got, err := DecodeCanonicalBase64(input)
		if err != nil {
			t.Fatalf("%q: unexpected error %v", input, err)
		}
		if string(got) != want {
			t.Fatalf("%q: decoded %q, want %q", input, got, want)
		}
	}
	invalid := []string{
		"Zg",           // length not multiple of four
		"Zm9vLm5ldHM!", // non-alphabet
		"Zg===",        // three pad bytes
		"Zg=A",         // pad not at end
		"=g==",         // pad inside
		"AB==",         // non-canonical trailing bits (word 0x001000)
		"Zh==",         // non-canonical trailing bits
		"Zx==",         // non-canonical trailing bits
		"Zm9v====",     // double quartet with pads in non-final chunk
	}
	for _, input := range invalid {
		if _, err := DecodeCanonicalBase64(input); err == nil {
			t.Fatalf("%q: must fail", input)
		}
	}
}
