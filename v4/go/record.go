package iprangedb

// Record size for key width K: 2*K + 4.
func recordSizeBytes(keyWidth int) int {
	return 2*keyWidth + ScopeIDSize
}

// recordWrite writes [from, to, scope_id] into out (must be 2*kw+4 bytes).
func recordWrite(out []byte, fromLE, toLE []byte, scopeID uint32, keyWidth int) {
	copy(out[0:keyWidth], fromLE)
	copy(out[keyWidth:2*keyWidth], toLE)
	putU32(out, 2*keyWidth, scopeID)
}
