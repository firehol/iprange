package handlers

// utf8LossyFirstContinuationOK reports whether the first
// continuation byte of one multi-byte sequence keeps the completed
// value inside the well-formed ranges: E0 must be followed by
// A0..BF (no overlong), ED by 80..9F (no surrogates), F0 by
// 90..BF (no overlong), and F4 by 80..8F (no value above
// U+10FFFF).  Every later continuation byte is unconstrained.
func utf8LossyFirstContinuationOK(lead, continuation byte) bool {
	switch {
	case lead == 0xe0 && continuation < 0xa0:
		return false
	case lead == 0xed && continuation > 0x9f:
		return false
	case lead == 0xf0 && continuation < 0x90:
		return false
	case lead == 0xf4 && continuation > 0x8f:
		return false
	}
	return true
}

// utf8Lossy decodes one byte string as UTF-8 with the same lossy
// replacement policy as Rust String::from_utf8_lossy (the reference
// implementation, WHATWG maximal-subpart rule): every ill-formed
// subsequence is replaced by the longest prefix that is itself a
// prefix of some well-formed sequence — an incomplete tail or a
// broken continuation replaces the consumed lead-plus-continuations
// run, while an out-of-range first continuation or a lone lead
// replaces a single byte and re-scans what follows.  Go's
// strings.ToValidUTF8 instead replaces maximal invalid runs, which
// diverges for these classes; artifactBasename must emit the exact
// wire text both products produce.
func utf8Lossy(bytes []byte) string {
	decoded := make([]byte, 0, len(bytes))
	i := 0
	for i < len(bytes) {
		lead := bytes[i]
		var need int
		switch {
		case lead < 0x80:
			decoded = append(decoded, lead)
			i++
			continue
		case lead >= 0xc2 && lead <= 0xdf:
			need = 2
		case lead >= 0xe0 && lead <= 0xef:
			need = 3
		case lead >= 0xf0 && lead <= 0xf4:
			need = 4
		default:
			// Lone continuation, overlong lead C0/C1, or lead
			// above U+10FFFF: no well-formed sequence can start
			// here; one replacement character, one byte.
			decoded = append(decoded, 0xef, 0xbf, 0xbd)
			i++
			continue
		}
		j := i + 1
		for j < len(bytes) && j < i+need && bytes[j]&0xc0 == 0x80 {
			if j == i+1 && !utf8LossyFirstContinuationOK(lead, bytes[j]) {
				break
			}
			j++
		}
		if j < i+need {
			// The walk stopped at an out-of-range first
			// continuation or a non-continuation byte: the
			// consumed lead-plus-continuations are the maximal
			// prefix of a well-formed sequence (for an
			// out-of-range first continuation that prefix is the
			// lead alone); one replacement for the run, and the
			// remainder re-scans (Rust from_utf8_lossy parity).
			decoded = append(decoded, 0xef, 0xbf, 0xbd)
			i = j
			continue
		}
		// Every continuation (including the lead range constraint)
		// was valid, so the completed sequence is well formed.
		decoded = append(decoded, bytes[i:i+need]...)
		i += need
	}
	return string(decoded)
}
