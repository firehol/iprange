// Empty transaction-1 database creation (Rust live_lifecycle/creation.rs
// create_live minus the sidecar, and database_file.rs EmptySpec::live +
// write_empty). The Go milestone has no sidecar, so the public create
// writes only the main file: an O_EXCL destination, the exclusive
// lifetime lock, a two-page extent with the identical txn-1 meta on both
// meta pages (proven-current), flush, sync, then close. reader_capacity,
// the cleanup IDs, the private-then-publish namespace stage, and the
// sidecar pair are milestone-4 gaps recorded in the SOW chunk-6 design
// record (D2).

package writer

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/fault"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// Created is the factual identity of one created database (Rust
// CreateResult minus the sidecar/namespace/cleanup surface).
type Created struct {
	DatabaseID  [16]byte
	CommitNonce [16]byte
}

// Create writes a brand-new empty transaction-1 database at path and
// mirrors Rust create_live's physical half: refuse an existing
// destination, validate the value-kind/structure-kind combination, draw
// the database id and commit nonce, write the identical txn-1 meta to
// both meta pages through the mapping, flush and sync, then close. The
// file is left committed and readable by both readers.
func Create(path string, family, kind, structure uint8, valueTag [16]byte, check func(clean string) error) (Created, error) {
	if !validKinds(kind, structure) {
		return Created{}, &format.Error{Code: format.CodeWrongStructureKind, Detail: "value kind and structure kind do not form a valid database"}
	}
	databaseID, err := randomNonce()
	if err != nil {
		return Created{}, err
	}
	nonce, err := randomNonce()
	if err != nil {
		return Created{}, err
	}
	m, err := mapping.Create(path, 2*format.PageSize, check)
	if err != nil {
		return Created{}, err
	}
	meta := emptyMeta(family, kind, structure, valueTag, databaseID, nonce)
	err = writeEmptyMeta(m, meta)
	if err != nil {
		// The destination is only a partial file: remove it so a retried
		// Create is not poisoned (Rust live_cleanup::remove parity for a
		// create that fails after the mapping opened). The removal is
		// identity-guarded like Rust remove_exact: a path that no longer
		// names the file we created is left untouched.
		if m.VerifyIdentity(path) == nil {
			_ = os.Remove(path)
		}
		closeErr := m.Close()
		if closeErr != nil {
			// Keep the primary failure reachable through Unwrap and
			// surface the cleanup failure too (Rust cleanup absorbs and
			// reports both sides of a failed create).
			return Created{}, joinError{text: err.Error() + "; close failed: " + closeErr.Error(), cause: err}
		}
		return Created{}, err
	}
	closeErr := m.Close()
	if closeErr != nil {
		return Created{}, closeErr
	}
	return Created{DatabaseID: databaseID, CommitNonce: nonce}, nil
}

// joinError preserves the primary cause while recording a failing
// cleanup, without formatting an interface-typed error through fmt (the
// gate treats an interface error value as a possible page carrier).
type joinError struct {
	text  string
	cause error
}

func (e joinError) Error() string { return e.text }

func (e joinError) Unwrap() error { return e.cause }

// writeEmptyMeta encodes the identical meta into both meta pages through
// the mapping and flushes and syncs the pair (Rust write_empty).
func writeEmptyMeta(m *mapping.Mapping, meta format.Meta) error {
	if err := fault.Fail("create.write_empty"); err != nil {
		return err
	}
	p0, err := m.Page(0)
	if err != nil {
		return err
	}
	p1, err := m.Page(1)
	if err != nil {
		return err
	}
	if err := meta.EncodeMapped(p0); err != nil {
		return err
	}
	if err := meta.EncodeMapped(p1); err != nil {
		return err
	}
	return flushAndSync(m)
}

// validKinds mirrors Rust creation.rs validate_kinds: direct and
// membership databases carry no structure; structured databases must name
// one.
func validKinds(kind, structure uint8) bool {
	switch kind {
	case format.ValueKindDirect, format.ValueKindMembership:
		return structure == format.StructureKindNone
	case format.ValueKindStructured:
		return structure != format.StructureKindNone
	}
	return false
}

// emptyMeta mirrors Rust database_file.rs empty_meta for EmptySpec::live:
// txn 1, two pages, all roots zero, the feed index limit zero, the
// membership id limit one for membership/structured kinds, and the
// structure id limit one for the structured kind.
func emptyMeta(family, kind, structure uint8, valueTag [16]byte, databaseID, nonce [16]byte) format.Meta {
	m := format.Meta{
		AddressFamily: family,
		ValueKind:     kind,
		StructureKind: structure,
		ValueTag:      valueTag,
		DatabaseID:    databaseID,
		TxnID:         1,
		CommitNonce:   nonce,
		PageCount:     2,
	}
	if kind == format.ValueKindMembership || kind == format.ValueKindStructured {
		m.MembershipIDLimit = 1
	}
	if kind == format.ValueKindStructured {
		m.StructureIDLimit = 1
	}
	return m
}

// flushAndSync mirror Rust write_empty's flush_range + sync_all so the
// created meta pair is durable before the mapping closes.
func flushAndSync(m *mapping.Mapping) error {
	if err := m.FlushRange(0, 2*format.PageSize); err != nil {
		return err
	}
	return m.SyncFile()
}
