package recovery

// Fixed-size scratch access retained by the external sort and the
// file-backed page table (Rust recovery/scratch/fixed.rs): one
// detached shared file whose payload region starts after the
// ownership header and never moves past the retained length.

import "github.com/firehol/iprange/v4/go/internal/format"

// scratchFile is shared fixed-size access to one scratch file
// retained by its attempt (Rust ScratchFile).
type scratchFile struct {
	index  int
	shared *scratchSharedFile
}

// slot returns the retained slot of the detached file.
func (f scratchFile) slot() scratchSlot { return scratchSlot{index: f.index} }

// length is the current logical length of the shared file (Rust
// ScratchFile::length).
func (f scratchFile) length() uint64 { return f.shared.length.Load() }

// read copies fixed-region bytes of the file (Rust ScratchFile::read).
func (f scratchFile) read(offset uint64, bytes []byte) error {
	if err := requireFixedScratchIO(offset, len(bytes), f.length()); err != nil {
		return err
	}
	return f.shared.read(offset, bytes)
}

// write copies bytes into the fixed region (Rust ScratchFile::write).
func (f scratchFile) write(offset uint64, bytes []byte) error {
	if err := requireFixedScratchIO(offset, len(bytes), f.length()); err != nil {
		return err
	}
	return f.shared.write(offset, bytes)
}

// requireFixedScratchIO proves one fixed-region I/O stays inside the
// retained payload (Rust require_fixed_io: past the header and inside
// the retained length, otherwise the Corrupt class).
func requireFixedScratchIO(offset uint64, length int, retained uint64) error {
	end, ok := checkedAdd(offset, uint64(length))
	if !ok {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "fixed recovery scratch I/O"}
	}
	if offset < scratchHeaderSize || end > retained {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "scratch I/O exceeds its fixed region"}
	}
	return nil
}
