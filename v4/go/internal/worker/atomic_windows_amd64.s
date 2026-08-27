//go:build windows && amd64

// Mapped-control 32-bit atomics for amd64 (XNU/FreeBSD/Linux/Windows
// x86-64 raise the same aligned semantics; plain 32-bit load/store are
// atomic and the CAS carries LOCK.
#include "textflag.h"

TEXT ·mapAtomicLoad32(SB), NOSPLIT, $0-20
	MOVQ base+0(FP), AX
	MOVL off+8(FP), CX
	MOVL (AX)(CX*1), DX
	MOVL DX, ret+16(FP)
	RET

TEXT ·mapAtomicStore32(SB), NOSPLIT, $0-16
	MOVQ base+0(FP), AX
	MOVL off+8(FP), CX
	MOVL value+12(FP), DX
	MOVL DX, (AX)(CX*1)
	RET

TEXT ·mapAtomicCas32(SB), NOSPLIT, $0-28
	MOVQ base+0(FP), SI
	MOVL off+8(FP), CX
	MOVL old+12(FP), AX
	MOVL new+16(FP), DX
	LOCK
	CMPXCHGL DX, (SI)(CX*1)
	JE cas_ok
	MOVL $0, ret+24(FP)
	RET
cas_ok:
	MOVL $1, ret+24(FP)
	RET
