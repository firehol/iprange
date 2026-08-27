//go:build linux && arm64

#include "textflag.h"

// Raw linux/arm64 syscall numbers (asm-generic table; the amd64 file
// uses the x86-64 table).
#define SYS_rt_sigaction 134
#define SYS_rt_sigprocmask 135
#define SYS_rt_sigreturn 139
#define SYS_rt_sigsuspend 133
#define SYS_sigaltstack 132
#define SYS_kill 129
#define SYS_getpid 172
#define SYS_exit_group 94

#define SIGBUS 7
#define SIG_DFL 0
#define SIG_IGN 1

// sa_flags bits (kernel values, identical on amd64 and arm64).
#define SA_SIGINFO 0x4
#define SA_NODEFER 0x40000000
#define SA_RESETHAND 0x80000000
#define SA_RESTORER 0x04000000
#define SA_ONSTACK 0x08000000
#define SIGBUS_BIT 6

// Control-page fault-subset offsets (worker/control.rs).
#define CTL_STATE 12
#define CTL_GENERATION 104
#define CTL_ROLE 112
#define CTL_ARMED 116
#define CTL_HANDLING 120
#define CTL_BASE 128
#define CTL_LEN 136
#define CTL_FAULT_GENERATION 144
#define CTL_FAULT_ROLE 152
#define CTL_FAULT_CODE 156
#define CTL_FAULT_RELATIVE 160
#define CTL_FAULT_ADDRESS 168
#define CTL_FAULT_MARKER 176

#define STATE_FAULT 8
#define FAULT_MARKER 0x42555346
#define OWNED_FAULT_EXIT 197
#define UNOWNED_REDISPATCH_FAILED 198

// The kernel enters sigbusHandler with the AArch64 C ABI: X0=signal,
// X1=siginfo, X2=ucontext, LR=our SA_RESTORER stub (installed by
// sigbus_linux.go), SP=the kernel signal frame on the alternate stack.
// It never calls Go. Register contract (the kernel preserves every
// register except X0 across SVC; our only BL is chain_mask, and it
// touches registers below X19): R19=signal, R20=info, R21=context,
// R29=entry LR (restored before the chain tail-jump). All gate and
// fault-record values live in caller-saved scratch registers because
// the owned path makes no intervening call or syscall. The fault
// record stores are plain STRs ordered by the final STLRW release
// store of the state word (the ARM64 equivalent of the amd64 TSO
// ordering; posix.rs state.store(Release)).
TEXT ·sigbusHandler(SB), NOSPLIT|NOFRAME, $0-0
	MOVD R0, R19
	MOVD R1, R20
	MOVD R2, R21
	MOVD R30, R26

	// owned_fault gate (posix.rs signal_handler + owned_fault): a kernel
	// bus code inside an armed registered region.
	MOVD ·activeControl(SB), R22
	CBZ R22, chain
	CMPW $SIGBUS, R19
	BNE chain
	CBZ R20, chain
	MOVW 8(R20), R23 // si_code
	CMPW $1, R23
	BLT chain
	CMPW $5, R23
	BGT chain
	ADD $CTL_ARMED, R22, R5
	LDARW (R5), R6
	CMPW $1, R6
	BNE chain
	ADD $CTL_GENERATION, R22, R5
	LDAR (R5), R9
	CBZ R9, chain
	ADD $CTL_ROLE, R22, R5
	LDARW (R5), R10
	CMPW $1, R10
	BLT chain
	CMPW $4, R10
	BGT chain
	ADD $CTL_LEN, R22, R5
	LDAR (R5), R11
	CBZ R11, chain
	MOVD 16(R20), R24 // si_addr (kernel siginfo, both arm64 and amd64)
	ADD $CTL_BASE, R22, R5
	LDAR (R5), R12
	CMP R12, R24
	BLO chain
	SUB R12, R24, R25 // relative
	CMP R11, R25
	BHS chain

	// Claim handling 0->1 (posix.rs compare_exchange AcqRel); a
	// concurrent owned fault keeps the chain path.
	ADD $CTL_HANDLING, R22, R5
	MOVW $1, R4
cas_loop:
	LDAXRW (R5), R6
	CBNZ R6, chain
	STLXRW R4, (R5), R7
	CBNZ R7, cas_loop

	// Write the fault record, then publish state=Fault with a release
	// store (posix.rs owned_fault tail): the parent sees the record
	// before the state word on both architectures.
	MOVD R9, CTL_FAULT_GENERATION(R22)
	MOVW R10, CTL_FAULT_ROLE(R22)
	MOVW R23, CTL_FAULT_CODE(R22)
	MOVD R25, CTL_FAULT_RELATIVE(R22)
	MOVD R24, CTL_FAULT_ADDRESS(R22)
	MOVW $FAULT_MARKER, R6
	MOVW R6, CTL_FAULT_MARKER(R22)
	ADD $CTL_STATE, R22, R5
	MOVW $STATE_FAULT, R6
	STLRW R6, (R5)

	MOVD $OWNED_FAULT_EXIT, R0
	MOVD $SYS_exit_group, R8
	SVC
	RET // unreachable

chain:
	// Unpublish before any further delivery (posix.rs chain).
	MOVD ZR, ·activeControl(SB)

	MOVD ·previousAction+0(SB), R11
	CMP $SIG_DFL, R11
	BEQ chain_dfl
	CMP $SIG_IGN, R11
	BEQ chain_ign

	MOVD ·previousAction+8(SB), R12 // flags
	AND $SA_RESETHAND, R12, R12
	CBNZ R12, chain_reset

	// Ordinary chain: restore the previous action, apply its kernel-
	// equivalent mask, then tail-jump to the previous handler with the
	// original C ABI registers and the entry LR (our SA_RESTORER stub);
	// the kernel signal frame stays intact below RSP.
	SUB $64, RSP
	MOVD R19, R0
	MOVD $·previousAction(SB), R1
	MOVD ZR, R2
	MOVD $8, R3
	MOVD $SYS_rt_sigaction, R8
	SVC
	CMP $0, R0
	BNE chain_fail_restore
	CALL ·chain_mask(SB)
	CMP $0, R0
	BNE chain_fail_restore
	ADD $64, RSP
	MOVD R19, R0
	MOVD R20, R1
	MOVD R21, R2
	MOVD R26, R30
	MOVD ·previousAction+0(SB), R16
	JMP (R16)

chain_reset:
	// SA_RESETHAND: the disposition is cleared to SIG_DFL before the
	// previous handler runs (posix.rs chain reset arm).
	SUB $64, RSP
	MOVD ZR, R0
	MOVD R0, 0(RSP)
	MOVD R0, 8(RSP)
	MOVD R0, 16(RSP)
	MOVD R0, 24(RSP)
	MOVD R0, 32(RSP)
	MOVD R0, 40(RSP)
	MOVD R0, 48(RSP)
	MOVD R0, 56(RSP)
	MOVD R19, R0
	MOVD RSP, R1
	MOVD ZR, R2
	MOVD $8, R3
	MOVD $SYS_rt_sigaction, R8
	SVC
	CMP $0, R0
	BNE chain_fail_restore
	CALL ·chain_mask(SB)
	CMP $0, R0
	BNE chain_fail_restore
	ADD $64, RSP
	MOVD R19, R0
	MOVD R20, R1
	MOVD R21, R2
	MOVD R26, R30
	MOVD ·previousAction+0(SB), R16
	JMP (R16)

chain_dfl:
	// Restore SIG_DFL. A synchronous kernel bus fault re-executes the
	// faulting instruction on return and dies with SIGBUS; asynchronous
	// deliveries redispatch through kill + sigsuspend (posix.rs chain
	// SIG_DFL arm + redispatch_default).
	MOVD R19, R0
	MOVD $·previousAction(SB), R1
	MOVD ZR, R2
	MOVD $8, R3
	MOVD $SYS_rt_sigaction, R8
	SVC
	CMP $0, R0
	BNE chain_fail
	CBZ R20, chain_redispatch
	MOVD 16(R20), R9 // si_addr
	CBZ R9, chain_redispatch
	MOVW 8(R20), R9 // si_code
	CMPW $1, R9
	BLT chain_redispatch
	CMPW $5, R9
	BGT chain_redispatch
	RET // re-executed instruction faults under SIG_DFL

chain_ign:
	// Restore SIG_IGN and return (posix.rs chain SIG_IGN arm).
	MOVD R19, R0
	MOVD $·previousAction(SB), R1
	MOVD ZR, R2
	MOVD $8, R3
	MOVD $SYS_rt_sigaction, R8
	SVC
	CMP $0, R0
	BNE chain_fail
	RET

chain_redispatch:
	// kill(getpid(), SIGBUS), then wait with SIGBUS unblocked: the
	// default disposition kills the process (posix.rs
	// redispatch_default).
	SUB $64, RSP
	MOVD $SYS_getpid, R8
	SVC
	MOVD $SIGBUS, R1
	MOVD $SYS_kill, R8
	SVC
	CMP $0, R0
	BNE chain_fail_restore
	MOVD $2, R0 // SIG_SETMASK with a null set queries the current mask
	MOVD ZR, R1
	ADD $32, RSP, R2
	MOVD $8, R3
	MOVD $SYS_rt_sigprocmask, R8
	SVC
	CMP $0, R0
	BNE chain_fail_restore
	MOVD $64, R9 // 1<<SIGBUS_BIT
	ADD $32, RSP, R10
	MOVD (R10), R11
	BIC R9, R11, R11
	MOVD R11, (R10)
chain_suspend:
	ADD $32, RSP, R0
	MOVD $8, R1
	MOVD $SYS_rt_sigsuspend, R8
	SVC
	JMP chain_suspend

chain_fail_restore:
	ADD $64, RSP
chain_fail:
	MOVD $UNOWNED_REDISPATCH_FAILED, R0
	MOVD $SYS_exit_group, R8
	SVC
	RET // unreachable

// chain_mask builds the kernel-equivalent blocked mask of the previous
// action into the 8-byte slot at 32(RSP) (posix.rs apply_mask): the
// current mask OR the previous mask, then SIGBUS re-added unless
// SA_NODEFER clears it. Returns 0 on success, errno otherwise. The
// caller allocates the 64-byte scratch below RSP; on arm64 BL does not
// push, so the slot is at the same 32(RSP) the caller uses. Touches
// only caller-saved registers, keeping R19/R20/R21/R26 live for the
// chain tail-jump.
TEXT ·chain_mask(SB), NOSPLIT|NOFRAME, $0-0
	MOVD $2, R0
	MOVD ZR, R1
	ADD $32, RSP, R2
	MOVD $8, R3
	MOVD $SYS_rt_sigprocmask, R8
	SVC
	CMP $0, R0
	BNE mask_fail
	MOVD ·previousAction+24(SB), R9 // previous mask (kernel 64-bit set)
	ADD $32, RSP, R10
	MOVD (R10), R11
	ORR R9, R11, R11
	MOVD R11, (R10)
	MOVD ·previousAction+8(SB), R12 // flags
	AND $SA_NODEFER, R12, R12
	MOVD $64, R9 // 1<<SIGBUS_BIT
	CBNZ R12, mask_nodefer
	MOVD (R10), R11
	ORR R9, R11, R11
	MOVD R11, (R10)
	JMP mask_write
mask_nodefer:
	MOVD (R10), R11
	BIC R9, R11, R11
	MOVD R11, (R10)
mask_write:
	MOVD $2, R0
	ADD $32, RSP, R1
	MOVD ZR, R2
	MOVD $8, R3
	MOVD $SYS_rt_sigprocmask, R8
	SVC
	CMP $0, R0
	BNE mask_fail
	MOVD ZR, R0
	RET
mask_fail:
	NEG R0, R0
	RET

// rtSigreturnStub is the kernel return path for any handler that
// returns: the interrupted context lives in the kernel signal frame and
// LR points here (SA_RESTORER). syscall 139 restores it.
TEXT ·rtSigreturnStub(SB), NOSPLIT|NOFRAME, $0-0
	MOVD $SYS_rt_sigreturn, R8
	SVC
	RET // unreachable

// Go-callable syscall wrappers. Each returns errno (0 = ok).
TEXT ·sigaltstackSet(SB), NOSPLIT, $0-20
	MOVD ss+0(FP), R0
	MOVD old+8(FP), R1
	MOVD $SYS_sigaltstack, R8
	SVC
	CMP $0, R0
	BGE sigaltstackSet_ret
	NEG R0, R0
sigaltstackSet_ret:
	MOVW R0, ret+16(FP)
	RET
TEXT ·sigaltstackQuery(SB), NOSPLIT, $0-12
	MOVD ZR, R0
	MOVD old+0(FP), R1
	MOVD $SYS_sigaltstack, R8
	SVC
	CMP $0, R0
	BGE sigaltstackQuery_ret
	NEG R0, R0
sigaltstackQuery_ret:
	MOVW R0, ret+8(FP)
	RET
TEXT ·rtSigactionSet(SB), NOSPLIT, $0-28
	MOVW sig+0(FP), R0
	MOVD act+8(FP), R1
	MOVD old+16(FP), R2
	MOVD $8, R3
	MOVD $SYS_rt_sigaction, R8
	SVC
	CMP $0, R0
	BGE rtSigactionSet_ret
	NEG R0, R0
rtSigactionSet_ret:
	MOVW R0, ret+24(FP)
	RET
TEXT ·rtSigactionQuery(SB), NOSPLIT, $0-20
	MOVW sig+0(FP), R0
	MOVD ZR, R1
	MOVD old+8(FP), R2
	MOVD $8, R3
	MOVD $SYS_rt_sigaction, R8
	SVC
	CMP $0, R0
	BGE rtSigactionQuery_ret
	NEG R0, R0
rtSigactionQuery_ret:
	MOVW R0, ret+16(FP)
	RET

// Address getters for the naked symbols.
TEXT ·sigbusHandlerAddr(SB), NOSPLIT, $0-8
	MOVD $·sigbusHandler(SB), R0
	MOVD R0, ret+0(FP)
	RET
TEXT ·rtSigreturnStubAddr(SB), NOSPLIT, $0-8
	MOVD $·rtSigreturnStub(SB), R0
	MOVD R0, ret+0(FP)
	RET
