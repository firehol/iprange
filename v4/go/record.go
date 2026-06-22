package iprangedb

// The fixed [from, to, scope] leaf record (§4).
//
// record_size = 2*key_width + scope_width (§4, D1) — scope_width is per-file (from the
// meta), so a record is handled as a borrowed record_size-byte slice, not a fixed-size
// struct. scope is opaque (D11) and always borrowed: the read path never copies it.

// recordRef is a zero-copy view over one record_size-byte record: from (K.width()) ·
// to (K.width()) · scope (the remainder, scope_width bytes). It enforces nothing about
// ordering/disjointness/to>=from — those cross-record invariants are the leaf walk's job
// (§9). It is purely a view.
type recordRef[K ipKey[K]] struct {
	rec []byte
}

func newRecordRef[K ipKey[K]](rec []byte) recordRef[K] { return recordRef[K]{rec: rec} }

// from returns the inclusive range start.
func (r recordRef[K]) from() K {
	var zero K
	w := zero.width()
	return zero.readLE(r.rec[0:w])
}

// to returns the inclusive range end.
func (r recordRef[K]) to() K {
	var zero K
	w := zero.width()
	return zero.readLE(r.rec[w : 2*w])
}

// scope returns the opaque scope bytes (borrowed; never interpreted — D11). Empty when
// scope_width == 0 (a presence map).
func (r recordRef[K]) scope() []byte {
	var zero K
	return r.rec[2*zero.width():]
}

// recordWrite writes a record [from, to, scope] into out, which MUST be exactly
// 2*K.width() + len(scope) bytes. The caller owns out.
func recordWrite[K ipKey[K]](out []byte, from, to K, scope []byte) {
	w := from.width()
	from.writeLE(out[0:w])
	to.writeLE(out[w : 2*w])
	copy(out[2*w:], scope)
}
