// Atomic, durable, budget-bounded export writers (SOW-0028, Rust
// io/export_writer.rs parity).
//
// Every export destination is published through one same-directory
// private temporary file, flushed with fsync, linked or renamed into
// place under the requested publication policy, and followed by a
// directory sync. Rows stream in bounded batches through a fixed
// output buffer; no export materializes its address set, and both the
// row and byte budgets are checked before the next row is written.
//
// The format encoders here are deliberately independent of the SDK:
// they format canonical numeric addresses and leave source iteration
// to the JSON-RPC handler.

package fileio

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"runtime"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// ExportBudget carries the caller-supplied export limits
// (`result_budget`).
type ExportBudget struct {
	MaxRows        uint64
	MaxOutputBytes uint64
	MaxOpenFiles   uint32
}

// ExportFacts is the complete factual result of one published export
// file.
type ExportFacts struct {
	Path      string
	SHA256    string
	Rows      uint64
	Addresses iprangedb.Cardinality129
	Bytes     uint64
}

// AddressCount is the address-count argument a writer row accepts: a
// plain uint64 count or an exact Cardinality129 (a full IPv6 space is
// 2^128 and needs bit 128).
type AddressCount interface {
	count() iprangedb.Cardinality129
}

type u64Count uint64

func (c u64Count) count() iprangedb.Cardinality129 {
	return iprangedb.CardinalityFromUint64(uint64(c))
}

type c129Count iprangedb.Cardinality129

func (c c129Count) count() iprangedb.Cardinality129 { return iprangedb.Cardinality129(c) }

// C129 wraps an exact cardinality as an AddressCount.
func C129(value iprangedb.Cardinality129) AddressCount { return c129Count(value) }

// U64 wraps a plain count as an AddressCount.
func U64(value uint64) AddressCount { return u64Count(value) }

// ExportWriter is the buffered writer behind one atomically published
// export file. Abort removes the unpublished temporary, so every error
// path (including budget refusal) leaves the destination namespace
// clean.
type ExportWriter struct {
	file        *bufio.Writer
	raw         *os.File
	temporary   string
	destination string
	policy      iprangedb.PublicationPolicy
	budget      ExportBudget
	rows        uint64
	bytes       uint64
	addresses   iprangedb.Cardinality129
	digest      hash.Hash // running SHA-256 over the emitted bytes
	published   bool      // destination name visible
}

// NewExportWriter creates the private temporary and the buffered
// writer. The caller must call Finish or Abort exactly once.
func NewExportWriter(destination string, policy iprangedb.PublicationPolicy, budget ExportBudget) (*ExportWriter, *rpc.HandlerError) {
	if budget.MaxOpenFiles == 0 {
		return nil, rpc.NewHandlerError("invalid_argument", "not_started",
			"export requires at least one open file")
	}
	parent := filepath.Dir(destination)
	if parent == "" {
		parent = "."
	}
	handle, herr := rpc.NewHandle()
	if herr != nil {
		return nil, herr
	}
	temporary := filepath.Join(parent, "."+handle+".export.tmp")
	raw, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return nil, fileError(err, "create export output")
	}
	return &ExportWriter{
		file:        bufio.NewWriterSize(raw, 64*1024),
		raw:         raw,
		temporary:   temporary,
		destination: destination,
		policy:      policy,
		budget:      budget,
		digest:      sha256.New(),
	}, nil
}

// WriteLine appends one LF-terminated row. The address delta is the
// exact number of addresses the row represents, not its text length.
func (w *ExportWriter) WriteLine(line []byte, addresses AddressCount) *rpc.HandlerError {
	if err := w.reserve(1, uint64(len(line)+1)); err != nil {
		return err
	}
	if err := w.emit(line, addresses.count()); err != nil {
		return err
	}
	return w.emit([]byte("\n"), iprangedb.CardinalityZero())
}

// WriteChunk appends raw format bytes (CSV header or a legacy binary
// record). rows counts rows represented by the chunk, not byte length.
func (w *ExportWriter) WriteChunk(bytes []byte, rows uint64, addresses AddressCount) *rpc.HandlerError {
	if err := w.reserve(rows, uint64(len(bytes))); err != nil {
		return err
	}
	return w.emit(bytes, addresses.count())
}

// Finish flushes, syncs, atomically publishes, and syncs the
// directory. The temporary is always removed; a failure reports the
// factual publication state in the error details.
func (w *ExportWriter) Finish() (*ExportFacts, *rpc.HandlerError) {
	facts, herr := w.publish()
	// Always remove the temporary when publication did not consume it.
	_ = os.Remove(w.temporary)
	return facts, herr
}

// Abort removes the unpublished temporary file (every error path leaves
// the destination namespace clean).
func (w *ExportWriter) Abort() {
	if !w.published {
		_ = os.Remove(w.temporary)
	}
}

func (w *ExportWriter) reserve(rows, byteLen uint64) *rpc.HandlerError {
	nextRows := w.rows + rows
	if nextRows < w.rows || nextRows > w.budget.MaxRows {
		return budgetError(fmt.Sprintf("row %d exceeds max_rows", nextRows), w.budget.MaxRows)
	}
	nextBytes := w.bytes + byteLen
	if nextBytes < w.bytes || nextBytes > w.budget.MaxOutputBytes {
		return budgetError(fmt.Sprintf("byte %d exceeds max_output_bytes", nextBytes), w.budget.MaxOutputBytes)
	}
	w.rows = nextRows
	w.bytes = nextBytes
	return nil
}

func (w *ExportWriter) emit(bytes []byte, addresses iprangedb.Cardinality129) *rpc.HandlerError {
	// Exact 129-bit accumulation: one single-family export never
	// exceeds 2^128 (the full IPv6 space), so overflow is a counter
	// invariant violation, never a legitimate output.
	next, err := w.addresses.Add(addresses)
	if err != nil {
		return rpc.NewHandlerError("output_limit", "not_started",
			"export address cardinality exceeded the exact 129-bit counter")
	}
	w.addresses = next
	if _, err := w.digest.Write(bytes); err != nil {
		return rpc.NewHandlerError("io", "not_started", "export digest update failed: "+err.Error())
	}
	if _, err := w.file.Write(bytes); err != nil {
		return fileError(err, "write export output")
	}
	return nil
}

func (w *ExportWriter) publish() (*ExportFacts, *rpc.HandlerError) {
	if err := w.file.Flush(); err != nil {
		return nil, fileError(err, "sync export output")
	}
	if err := w.raw.Sync(); err != nil {
		return nil, fileError(err, "sync export output")
	}
	if w.policy == iprangedb.PolicyFailIfExists {
		// Hard-link publication is the portable no-replacement atom:
		// the destination name appears only when complete. Remove the
		// private temporary before the directory sync so a crash
		// cannot leave the temporary name durable (same order as the
		// metadata publication path).
		if err := os.Link(w.temporary, w.destination); err != nil {
			return nil, fileError(err, "publish export output")
		}
		w.published = true
		// The destination name now exists with the complete content; a
		// failure to remove the private temporary no longer changes
		// that, so the durable state is unknown.
		if err := os.Remove(w.temporary); err != nil {
			return nil, w.publicationFailure(err, "remove export temporary", false)
		}
	} else {
		// rename(2) and MoveFileExW(REPLACE_EXISTING) replace the
		// destination atomically on both supported families.
		if err := os.Rename(w.temporary, w.destination); err != nil {
			return nil, fileError(err, "publish export output")
		}
		w.published = true
	}
	parent := filepath.Dir(w.destination)
	if parent == "" {
		parent = "."
	}
	// The destination is visible; a directory-sync failure means the
	// namespace entry's durability is unproven (outcome_unknown).
	if err := syncDirectory(parent); err != nil {
		return nil, w.publicationFailure(err, "sync export output directory", true)
	}
	return &ExportFacts{
		Path:      w.destination,
		SHA256:    HexDigest(w.digest.Sum(nil)),
		Rows:      w.rows,
		Addresses: w.addresses,
		Bytes:     w.bytes,
	}, nil
}

// publicationFailure maps a failure once the destination name is
// visible: the bytes are published but the durable state is unknown,
// so the outcome is `outcome_unknown` and the error carries the
// adapter-owned publication evidence.
func (w *ExportWriter) publicationFailure(err error, stage string, temporaryRemoved bool) *rpc.HandlerError {
	sha := HexDigest(w.digest.Sum(nil))
	return &rpc.HandlerError{
		Code:    "io",
		Outcome: "outcome_unknown",
		Message: fmt.Sprintf("%s: %v", stage, err),
		Details: map[string]any{
			"publication": map[string]any{
				"outcome":             "outcome_unknown",
				"publication_policy":  PolicyName(w.policy),
				"path":                w.destination,
				"stage":               stage,
				"destination_visible": true,
				"temporary_removed":   temporaryRemoved,
				"rows":                fmt.Sprintf("%d", w.rows),
				"bytes":               fmt.Sprintf("%d", w.bytes),
				"addresses":           w.addresses.String(),
				"sha256":              sha,
			},
		},
	}
}

// PolicyName maps one publication policy to its wire name.
func PolicyName(policy iprangedb.PublicationPolicy) string {
	switch policy {
	case iprangedb.PolicyFailIfExists:
		return "fail_if_exists"
	case iprangedb.PolicyReplaceExisting:
		return "replace_existing"
	case iprangedb.PolicyReplaceExistingNoRollback:
		return "replace_existing_no_rollback"
	}
	return "unknown"
}

// budgetError is the budget-refusal failure: it happens before the
// next row is written, so no destination or partial file is visible.
func budgetError(detail string, limit uint64) *rpc.HandlerError {
	return rpc.NewHandlerError("output_limit", "not_started",
		fmt.Sprintf("export refused before exceeding budget: %s (limit %d)", detail, limit))
}

func fileError(err error, operation string) *rpc.HandlerError {
	message := fmt.Sprintf("%s: %v", operation, err)
	if errors.Is(err, os.ErrExist) {
		return rpc.NewHandlerError("name_exists", "not_started", message)
	}
	return rpc.NewHandlerError("io", "not_started", message)
}

func syncDirectory(parent string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// HexDigest renders bytes as lowercase hex (wire digest form).
func HexDigest(bytes []byte) string {
	return hex.EncodeToString(bytes)
}
