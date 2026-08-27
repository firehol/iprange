//go:build freebsd && arm64

// Mapped-control 32-bit atomics for arm64 (acquire load / release
// store, the ARM equivalent of the x86-64 plain aligned access with
// the Rust volatile/atomic ordering). LDAR/STLR take the base
// register only, so the effective address is computed first.
#include "textflag.h"

TEXT ·mapAtomicLoad32(SB), NOSPLIT, $0-20
	MOVD base+0(FP), R0
	MOVW off+8(FP), R1
	ADD R1, R0, R0
	LDARW (R0), R2
	MOVW R2, ret+16(FP)
	RET

TEXT ·mapAtomicStore32(SB), NOSPLIT, $0-16
	MOVD base+0(FP), R0
	MOVW off+8(FP), R1
	MOVW value+12(FP), R2
	ADD R1, R0, R0
	STLRW R2, (R0)
	RET

TEXT ·mapAtomicCas32(SB), NOSPLIT, $0-28
	MOVD base+0(FP), R0
	MOVW off+8(FP), R1
	MOVW old+12(FP), R2
	MOVW new+16(FP), R3
	ADD R1, R0, R0
cas_loop:
	LDAXRW (R0), R4
	CMPW R2, R4
	BNE cas_fail
	STLXRW R3, (R0), R5
	CBNZ R5, cas_loop
	MOVW $1, R6
	MOVW R6, ret+24(FP)
	RET
cas_fail:
	MOVW $0, R6
	MOVW R6, ret+24(FP)
	RET
