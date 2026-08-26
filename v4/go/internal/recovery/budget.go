package recovery

// Recovery budget (Rust recovery/budget.rs RecoveryBudget): the
// maximum simultaneously retained resources of one recovery
// operation. Scratch limits are accepted and validated together with
// the scratch directory, and a supplied scratch budget enables the
// file-backed page table and the external sort.

import "github.com/firehol/iprange/v4/go/internal/format"

// RecoveryBudget bounds one recovery operation (Rust RecoveryBudget).
type RecoveryBudget struct {
	MaxHeapBytes     uint64
	MaxOutputPages   uint64
	MaxOpenFiles     uint32
	MaxScratchBytes  uint64
	MaxScratchFiles  uint32
	ScratchDirectory string
}

// HeapOnly builds a recovery budget which forbids external scratch
// files (Rust RecoveryBudget::heap_only).
func HeapOnly(maxHeapBytes uint64, maxOutputPages uint64, maxOpenFiles uint32) *RecoveryBudget {
	return &RecoveryBudget{
		MaxHeapBytes:   maxHeapBytes,
		MaxOutputPages: maxOutputPages,
		MaxOpenFiles:   maxOpenFiles,
	}
}

// validate checks the budget invariants (Rust RecoveryBudget::
// validate): source and output files, a minimum output extent, and
// scratch limits supplied together with the scratch directory.
func (b *RecoveryBudget) validate() error {
	if b.MaxOpenFiles < 2 {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "recovery requires source and output files"}
	}
	if b.MaxOutputPages < 2 {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "recovery output pages"}
	}
	scratchLimits := b.MaxScratchBytes != 0 && b.MaxScratchFiles != 0
	if scratchLimits != (b.ScratchDirectory != "") || (b.MaxScratchBytes == 0) != (b.MaxScratchFiles == 0) {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "recovery scratch path and limits must be supplied together"}
	}
	return nil
}
