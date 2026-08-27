//go:build darwin && amd64

#include "textflag.h"

// Raw macOS x86-64 syscall numbers (sys/syscall.h; verified on the
// macOS 26 SDK; the SYSCALL instruction takes the plain number).
#define SYS_sigaction 46
#define SYS_sigprocmask 48
#define SYS_sigsuspend 111
#define SYS_sigaltstack 53
#define SYS_getpid 20
#define SYS_kill 37
#define SYS_exit 1

#define SIGBUS 10
#define SIG_DFL 0
#define SIG_IGN 1

// sa_flags bits (macOS values).
#define SA_SIGINFO 0x40
#define SA_NODEFER 0x10
#define SA_RESETHAND 0x4
#define SA_ONSTACK 0x1

// sigset_t is one 32-bit word on macOS; SIGBUS=10 is bit 9.
#define SIGBUS_BIT 9
#define SI_ADDR_OFF 24 // siginfo si_addr on LP64 macOS
#define MASK_BYTES 4

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

// darwinSigaction offsets (handler@0, mask@8, flags@12).
#define SACT_MASK 8
#define SACT_FLAGS 12

// kernelStack offsets (sp@0, size@8, flags@16).
#define STACK_FLAGS 16

#define STATE_FAULT 8
#define FAULT_MARKER 0x42555346
#define OWNED_FAULT_EXIT 197
#define UNOWNED_REDISPATCH_FAILED 198

// The kernel enters sigbusHandler with the C ABI: DI=signal, SI=siginfo,
// DX=ucontext. It never calls Go. Register contract (macOS preserves
// every register except RCX and R11 across SYSCALL): R12=signal,
// R13=info, R14=context (preserved for the chain tail-jump), R8=control
// base, R9=gate generation (reused for the fault record), R11=gate role
// (reused), SI=gate si_code (reused), R15=fault address, R10=relative
// offset, CX=gate len (reused), BX/AX scratch. There is no SA_RESTORER:
// the kernel frames every return through the thread sigtramp, so the
// chain paths return or tail-jump with the frame's return address
// intact.
TEXT ·sigbusHandler(SB), NOSPLIT|NOFRAME, $0-0
	MOVQ DI, R12
	MOVQ SI, R13
	MOVQ DX, R14

	// owned_fault gate (posix.rs signal_handler + owned_fault): a kernel
	// bus code inside an armed registered region.
	MOVQ ·activeControl(SB), R8
	TESTQ R8, R8
	JZ chain
	CMPL R12, $SIGBUS
	JNE chain
	TESTQ R13, R13
	JZ chain
	MOVL 8(R13), SI // si_code, kept for the fault record
	CMPL SI, $1
	JL chain
	CMPL SI, $3
	JG chain
	CMPL CTL_ARMED(R8), $1
	JNE chain
	MOVQ CTL_GENERATION(R8), R9
	TESTQ R9, R9
	JZ chain
	MOVL CTL_ROLE(R8), R11
	CMPL R11, $1
	JL chain
	CMPL R11, $4
	JG chain
	MOVQ CTL_LEN(R8), CX
	TESTQ CX, CX
	JZ chain
	MOVQ SI_ADDR_OFF(R13), R15 // si_addr (offset 24 on macOS)
	MOVQ R15, AX
	SUBQ CTL_BASE(R8), AX
	JC chain
	CMPQ AX, CX
	JAE chain
	MOVQ AX, R10 // relative

	// Claim handling 0->1; a concurrent owned fault keeps the chain path.
	MOVL $0, AX
	MOVL $1, BX
	LOCK
	CMPXCHGL BX, CTL_HANDLING(R8)
	JNE chain

	// Write the fault record, then publish state=Fault (posix.rs
	// owned_fault tail). Plain stores are visible to the parent through
	// the MAP_SHARED control mapping; x86-64 TSO orders them before the
	// state store.
	MOVQ R9, CTL_FAULT_GENERATION(R8)
	MOVL R11, CTL_FAULT_ROLE(R8)
	MOVL SI, CTL_FAULT_CODE(R8)
	MOVQ R10, CTL_FAULT_RELATIVE(R8)
	MOVQ R15, CTL_FAULT_ADDRESS(R8)
	MOVL $FAULT_MARKER, AX
	MOVL AX, CTL_FAULT_MARKER(R8)
	MOVL $STATE_FAULT, AX
	MOVL AX, CTL_STATE(R8)

	MOVQ $SYS_exit, AX
	MOVQ $OWNED_FAULT_EXIT, DI
	SYSCALL
	RET // unreachable

chain:
	// Unpublish before any further delivery (posix.rs chain).
	MOVQ $0, ·activeControl(SB)

	MOVQ ·previousAction+0(SB), AX
	CMPQ AX, $SIG_DFL
	JE chain_dfl
	CMPQ AX, $SIG_IGN
	JE chain_ign

	MOVQ ·previousAction+SACT_FLAGS(SB), BX // flags
	TESTQ $SA_RESETHAND, BX
	JNZ chain_reset

	// Ordinary chain: restore the previous action, apply its kernel-
	// equivalent mask, then TAIL-JUMP to the previous handler with the
	// original C ABI registers. The kernel frame stays intact: the
	// chained handler returns to the thread sigtramp and the kernel
	// sigreturn resumes the interrupted context.
	SUBQ $64, SP
	MOVQ $SYS_sigaction, AX
	MOVQ $SIGBUS, DI
	LEAQ ·previousAction(SB), SI
	MOVQ $0, DX
	SYSCALL
	CMPQ AX, $0
	JNE chain_fail_restore
	CALL ·chain_mask(SB)
	CMPQ AX, $0
	JNE chain_fail_restore
	ADDQ $64, SP
	MOVQ R12, DI
	MOVQ R13, SI
	MOVQ R14, DX
	MOVQ ·previousAction+0(SB), BX
	JMP BX

chain_reset:
	// SA_RESETHAND: the disposition is cleared to SIG_DFL before the
	// previous handler runs (posix.rs chain reset arm).
	SUBQ $64, SP
	MOVQ $0, AX
	MOVQ AX, 0(SP)
	MOVQ AX, 8(SP)
	MOVQ AX, 16(SP)
	MOVQ AX, 24(SP)
	MOVQ AX, 32(SP)
	MOVQ AX, 40(SP)
	MOVQ AX, 48(SP)
	MOVQ AX, 56(SP)
	MOVQ $SYS_sigaction, AX
	MOVQ $SIGBUS, DI
	MOVQ SP, SI
	MOVQ $0, DX
	SYSCALL
	CMPQ AX, $0
	JNE chain_fail_restore
	CALL ·chain_mask(SB)
	CMPQ AX, $0
	JNE chain_fail_restore
	ADDQ $64, SP
	MOVQ R12, DI
	MOVQ R13, SI
	MOVQ R14, DX
	MOVQ ·previousAction+0(SB), BX
	JMP BX

chain_dfl:
	// Restore SIG_DFL. A synchronous kernel bus fault re-executes the
	// faulting instruction on return and dies with SIGBUS; asynchronous
	// deliveries redispatch through kill + sigsuspend (posix.rs chain
	// SIG_DFL arm + redispatch_default).
	MOVQ $SYS_sigaction, AX
	MOVQ $SIGBUS, DI
	LEAQ ·previousAction(SB), SI
	MOVQ $0, DX
	SYSCALL
	CMPQ AX, $0
	JNE chain_fail
	TESTQ R13, R13
	JZ chain_redispatch
	MOVQ SI_ADDR_OFF(R13), AX // si_addr
	TESTQ AX, AX
	JZ chain_redispatch
	MOVL 8(R13), AX // si_code
	CMPL AX, $1
	JL chain_redispatch
	CMPL AX, $3
	JG chain_redispatch
	RET // re-executed instruction faults under SIG_DFL

chain_ign:
	// Restore SIG_IGN and return (posix.rs chain SIG_IGN arm).
	MOVQ $SYS_sigaction, AX
	MOVQ $SIGBUS, DI
	LEAQ ·previousAction(SB), SI
	MOVQ $0, DX
	SYSCALL
	CMPQ AX, $0
	JNE chain_fail
	RET

chain_redispatch:
	// kill(getpid(), SIGBUS), then wait with SIGBUS unblocked: the default
	// disposition kills the process (posix.rs redispatch_default).
	SUBQ $64, SP
	MOVQ $SYS_getpid, AX
	SYSCALL
	MOVQ AX, DI
	MOVQ $SIGBUS, SI
	MOVQ $SYS_kill, AX
	SYSCALL
	CMPQ AX, $0
	JNE chain_fail_restore
	MOVQ $SYS_sigprocmask, AX
	MOVQ $2, DI // SIG_SETMASK with a null set queries the current mask
	MOVQ $0, SI
	LEAQ 32(SP), DX
	SYSCALL
	CMPQ AX, $0
	JNE chain_fail_restore
	BTRQ $SIGBUS_BIT, 32(SP)
chain_suspend:
	MOVQ $SYS_sigsuspend, AX
	LEAQ 32(SP), DI
	SYSCALL
	JMP chain_suspend

chain_fail_restore:
	ADDQ $64, SP
chain_fail:
	MOVQ $SYS_exit, AX
	MOVQ $UNOWNED_REDISPATCH_FAILED, DI
	SYSCALL
	RET // unreachable

// chain_mask builds the kernel-equivalent blocked mask of the previous
// action into the 4-byte slot at 32(SP) (posix.rs apply_mask): the
// current mask OR the previous mask's signals 1..=32 (macOS sigset_t is
// one 32-bit word), then SIGBUS re-added unless SA_NODEFER clears it.
// Returns 0 on success, errno otherwise. The caller allocates the
// 64-byte scratch below SP.
TEXT ·chain_mask(SB), NOSPLIT|NOFRAME, $0-0
	MOVQ $SYS_sigprocmask, AX
	MOVQ $2, DI
	MOVQ $0, SI
	LEAQ 32(SP), DX
	SYSCALL
	CMPQ AX, $0
	JNE mask_ret
	MOVL ·previousAction+SACT_MASK(SB), BX // previous mask
	ORL BX, 32(SP)
	MOVQ ·previousAction+SACT_FLAGS(SB), BX // flags
	TESTQ $SA_NODEFER, BX
	JNZ mask_nodefer
	BTSQ $SIGBUS_BIT, 32(SP)
	JMP mask_write
mask_nodefer:
	BTRQ $SIGBUS_BIT, 32(SP)
mask_write:
	MOVQ $SYS_sigprocmask, AX
	MOVQ $2, DI
	LEAQ 32(SP), SI
	MOVQ $0, DX
	SYSCALL
mask_ret:
	RET

// Go-callable syscall wrappers. Each returns errno (0 = ok).
TEXT ·sigaltstackSet(SB), NOSPLIT, $0-20
	MOVQ ss+0(FP), DI
	MOVQ old+8(FP), SI
	MOVQ $SYS_sigaltstack, AX
	SYSCALL
	CMPQ AX, $0
	JGE sigaltstackSet_ret
	NEGQ AX
sigaltstackSet_ret:
	MOVL AX, ret+16(FP)
	RET
TEXT ·sigaltstackQuery(SB), NOSPLIT, $0-12
	MOVQ $0, DI
	MOVQ old+0(FP), SI
	MOVQ $SYS_sigaltstack, AX
	SYSCALL
	CMPQ AX, $0
	JGE sigaltstackQuery_ret
	NEGQ AX
sigaltstackQuery_ret:
	MOVL AX, ret+8(FP)
	RET
TEXT ·sigactionSet(SB), NOSPLIT, $0-28
	MOVL sig+0(FP), DI
	MOVQ act+8(FP), SI
	MOVQ old+16(FP), DX
	MOVQ $SYS_sigaction, AX
	SYSCALL
	CMPQ AX, $0
	JGE sigactionSet_ret
	NEGQ AX
sigactionSet_ret:
	MOVL AX, ret+24(FP)
	RET
TEXT ·sigactionQuery(SB), NOSPLIT, $0-20
	MOVL sig+0(FP), DI
	MOVQ $0, SI
	MOVQ old+8(FP), DX
	MOVQ $SYS_sigaction, AX
	SYSCALL
	CMPQ AX, $0
	JGE sigactionQuery_ret
	NEGQ AX
sigactionQuery_ret:
	MOVL AX, ret+16(FP)
	RET

// Address getters for the naked symbols.
TEXT ·sigbusHandlerAddr(SB), NOSPLIT, $0-8
	LEAQ ·sigbusHandler(SB), AX
	MOVQ AX, ret+0(FP)
	RET
