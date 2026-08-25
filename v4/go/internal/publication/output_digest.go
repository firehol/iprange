// Fixed-memory SHA-512 over exact publication bytes (Rust
// publication/output_digest.rs). The digest visits every mapped byte
// exactly once through a bounded stack buffer, with an optional
// cancellation checkpoint per chunk; the finished output never exists
// in owned memory.

package publication

import (
	"crypto/sha512"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// digestBufferSize is the fixed chunk of one digest pass (Rust
// output_digest::DIGEST_BUFFER_SIZE).
const digestBufferSize = 1024

// digest hashes byteLength mapped bytes (Rust output_digest::digest).
func digest(m *mapping.Mapping, byteLength uint64) ([64]byte, error) {
	return digestWith(byteLength, func(offset uint64, output []byte) error {
		view, err := m.View(offset, uint64(len(output)))
		if err != nil {
			return err
		}
		copy(output, view)
		return nil
	})
}

// digestCancellable hashes byteLength mapped bytes with one checkpoint
// per chunk and a final checkpoint (Rust output_digest::digest_
// cancellable).
func digestCancellable(m *mapping.Mapping, byteLength uint64, check func() error) ([64]byte, error) {
	result, err := digestWith(byteLength, func(offset uint64, output []byte) error {
		if err := live.Checkpoint(check); err != nil {
			return err
		}
		view, err := m.View(offset, uint64(len(output)))
		if err != nil {
			return err
		}
		copy(output, view)
		return nil
	})
	if err := live.Checkpoint(check); err != nil {
		return [64]byte{}, err
	}
	return result, err
}

// digestWith runs one fixed-memory SHA-512 pass over byteLength bytes
// supplied by read in bounded chunks, in order, exactly once each
// (Rust output_digest::digest_with).
func digestWith(byteLength uint64, read func(offset uint64, output []byte) error) ([64]byte, error) {
	hasher := sha512.New()
	var buffer [digestBufferSize]byte
	var offset uint64
	for offset < byteLength {
		remaining := byteLength - offset
		length := remaining
		if length > digestBufferSize {
			length = digestBufferSize
		}
		if err := read(offset, buffer[:int(length)]); err != nil {
			return [64]byte{}, err
		}
		_, _ = hasher.Write(buffer[:int(length)])
		offset += length
	}
	var out [64]byte
	hasher.Sum(out[:0])
	return out, nil
}

// finishedLengthChanged is the fixed length-changed class of the
// finished-output machine (Rust output::Error::FinishedLengthChanged,
// mapped to Problem::output by problem.go).
func finishedLengthChanged() error {
	return &format.Error{Code: format.CodeConflict, Detail: "finished output length changed"}
}

// finishedMetaChanged is the fixed metadata-changed class of the
// finished-output machine (Rust output::Error::FinishedMetaChanged,
// mapped to Problem::output by problem.go).
func finishedMetaChanged() error {
	return &format.Error{Code: format.CodeConflict, Detail: "finished output metadata changed"}
}

// outputBootstrapError is the bare FormatInvalid class that
// problem.go maps to the fixed "output metadata is malformed" detail
// (Rust output::Error::Bootstrap).
func outputBootstrapError() error {
	return &format.Error{Code: format.CodeFormatInvalid}
}
