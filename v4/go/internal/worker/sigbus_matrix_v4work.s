//go:build v4work && linux && amd64

#include "textflag.h"

// Naked previous-disposition handlers for the signal-chain subprocess
// matrix (Rust worker/posix.rs tests::signal_chain_subprocess_matrix).
// These are test-only symbols: production builds compile this file out
// via the v4work build tag. Each handler is entered by the kernel (or by
// the production handler's chain tail-jump) with the C ABI and never
// returns: it exits with the matrix code the Rust test asserts.

#define SYS_rt_sigprocmask 14
#define SYS_rt_sigaction 13
#define SYS_exit_group 231

#define SIGBUS 7
#define SIGUSR1_BIT 9
#define SIGBUS_BIT 6

// matrixExit exits with the code in DI.
TEXT ·matrixExit(SB), NOSPLIT|NOFRAME, $0-0
	MOVQ $SYS_exit_group, AX
	SYSCALL
	RET

// matrixOneArgument: 1-arg ABI handler (no SA_SIGINFO); exit 81 when the
// signal is SIGBUS, else 86 (Rust one_argument).
TEXT ·matrixOneArgument(SB), NOSPLIT|NOFRAME, $0-0
	MOVQ $SIGBUS, AX
	CMPQ DI, AX
	JE one_bus
	MOVQ $86, DI
	JMP ·matrixExit(SB)
one_bus:
	MOVQ $81, DI
	JMP ·matrixExit(SB)

// matrixSiginfo: 3-arg ABI handler; 86 when the signal is not SIGBUS or
// the info pointer is null, 83 for a synchronous kernel bus fault
// (si_addr non-null and si_code 1..=5), else 82 (Rust siginfo +
// synchronous_bus_fault).
TEXT ·matrixSiginfo(SB), NOSPLIT|NOFRAME, $0-0
	MOVQ $SIGBUS, AX
	CMPQ DI, AX
	JNE siginfo_86
	TESTQ SI, SI
	JZ siginfo_86
	MOVQ 16(SI), AX // si_addr
	TESTQ AX, AX
	JZ siginfo_82
	MOVL 8(SI), AX // si_code
	CMPL AX, $1
	JL siginfo_82
	CMPL AX, $5
	JG siginfo_82
	MOVQ $83, DI
	JMP ·matrixExit(SB)
siginfo_82:
	MOVQ $82, DI
	JMP ·matrixExit(SB)
siginfo_86:
	MOVQ $86, DI
	JMP ·matrixExit(SB)

// matrixMaskedSiginfo: exit 88 when the current mask keeps SIGUSR1 and
// SIGBUS blocked, else 86 (Rust masked_siginfo).
TEXT ·matrixMaskedSiginfo(SB), NOSPLIT|NOFRAME, $0-0
	SUBQ $64, SP
	MOVQ $SYS_rt_sigprocmask, AX
	MOVQ $2, DI // SIG_SETMASK with a null set queries the current mask
	MOVQ $0, SI
	MOVQ SP, DX
	MOVQ $8, R10
	SYSCALL
	CMPQ AX, $0
	JNE masked_86
	MOVQ (SP), AX
	MOVQ AX, BX
	ANDQ $1<<SIGUSR1_BIT, AX
	JZ masked_86
	ANDQ $1<<SIGBUS_BIT, BX
	JZ masked_86
	MOVQ $88, DI
	JMP ·matrixExit(SB)
masked_86:
	MOVQ $86, DI
	JMP ·matrixExit(SB)

// matrixNodeferSiginfo: exit 89 when SIGBUS is NOT in the current mask
// (SA_NODEFER honored), else 86 (Rust nodefer_siginfo).
TEXT ·matrixNodeferSiginfo(SB), NOSPLIT|NOFRAME, $0-0
	SUBQ $64, SP
	MOVQ $SYS_rt_sigprocmask, AX
	MOVQ $2, DI
	MOVQ $0, SI
	MOVQ SP, DX
	MOVQ $8, R10
	SYSCALL
	CMPQ AX, $0
	JNE nodefer_86
	MOVQ (SP), AX
	ANDQ $1<<SIGBUS_BIT, AX
	JNZ nodefer_86
	MOVQ $89, DI
	JMP ·matrixExit(SB)
nodefer_86:
	MOVQ $86, DI
	JMP ·matrixExit(SB)

// matrixResetSiginfo: exit 90 when the current SIGBUS disposition is
// SIG_DFL (SA_RESETHAND reset it before delivery), else 86 (Rust
// reset_siginfo).
TEXT ·matrixResetSiginfo(SB), NOSPLIT|NOFRAME, $0-0
	SUBQ $64, SP
	MOVQ $SYS_rt_sigaction, AX
	MOVQ $SIGBUS, DI
	MOVQ $0, SI
	MOVQ SP, DX
	MOVQ $8, R10
	SYSCALL
	CMPQ AX, $0
	JNE reset_86
	MOVQ (SP), AX // handler field at offset 0
	TESTQ AX, AX
	JNZ reset_86
	MOVQ $90, DI
	JMP ·matrixExit(SB)
reset_86:
	MOVQ $86, DI
	JMP ·matrixExit(SB)

// matrixReplacementSiginfo: exit 91 unconditionally (Rust
// replacement_siginfo).
TEXT ·matrixReplacementSiginfo(SB), NOSPLIT|NOFRAME, $0-0
	MOVQ $91, DI
	JMP ·matrixExit(SB)

// matrixCallChainNullInfo enters the production handler with the
// null-info shape (SIGBUS, null info, null context; Rust
// signal_chain_subprocess_matrix case "null-info"). The handler chains
// into the matrixSiginfo previous disposition, which exits 86; this
// wrapper never returns.
TEXT ·matrixCallChainNullInfo(SB), NOSPLIT, $0-0
	MOVQ $SIGBUS, DI
	MOVQ $0, SI
	MOVQ $0, DX
	JMP ·sigbusHandler(SB)

// Address getters for the naked matrix symbols.
TEXT ·matrixOneArgumentAddr(SB), NOSPLIT, $0-8
	LEAQ ·matrixOneArgument(SB), AX
	MOVQ AX, ret+0(FP)
	RET
TEXT ·matrixSiginfoAddr(SB), NOSPLIT, $0-8
	LEAQ ·matrixSiginfo(SB), AX
	MOVQ AX, ret+0(FP)
	RET
TEXT ·matrixMaskedSiginfoAddr(SB), NOSPLIT, $0-8
	LEAQ ·matrixMaskedSiginfo(SB), AX
	MOVQ AX, ret+0(FP)
	RET
TEXT ·matrixNodeferSiginfoAddr(SB), NOSPLIT, $0-8
	LEAQ ·matrixNodeferSiginfo(SB), AX
	MOVQ AX, ret+0(FP)
	RET
TEXT ·matrixResetSiginfoAddr(SB), NOSPLIT, $0-8
	LEAQ ·matrixResetSiginfo(SB), AX
	MOVQ AX, ret+0(FP)
	RET
TEXT ·matrixReplacementSiginfoAddr(SB), NOSPLIT, $0-8
	LEAQ ·matrixReplacementSiginfo(SB), AX
	MOVQ AX, ret+0(FP)
	RET
