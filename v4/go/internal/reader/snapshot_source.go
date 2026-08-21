package reader

// Snapshot source surface (Rust GenerationReader::metadata_json_len and
// recovery/source_guard/basic.rs BasicSource::final_check parity). The
// compact-snapshot builder consumes these members between open and
// publish: the metadata length sizes the budgeted copy, the identity
// captures the opened inode for the publish-time compare, and
// ConfirmUnchanged proves the source generation and namespace entry
// survived the build before the publish rename.

import (
	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/format"
)

// MetadataJSONLen returns the uncompressed metadata byte length; ok
// reports whether the generation carries metadata. It mirrors Rust
// metadata_json_len: (meta.metadata_root != 0).then_some(meta.
// metadata_uncompressed_len) — a present root means the exact declared
// uncompressed length, without walking the chain.
func (r *ImmutableReader) MetadataJSONLen() (uint64, bool) {
	if r.meta.MetadataRoot == 0 {
		return 0, false
	}
	return r.meta.MetadataUncompressed, true
}

// FileIdentity returns the device and inode of the mapped file (Rust
// identity_any_link over the held descriptor in
// recovery/source_guard/basic.rs). The snapshot builder captures it at
// open and compares it after the build so a namespace replacement never
// publishes a snapshot of a swapped-in inode. The mapping retains no
// heap page, and fstat reads descriptor metadata only.
func (r *ImmutableReader) FileIdentity() (device uint64, inode uint64, err error) {
	device, inode, err = r.m.FileIdentity()
	if err != nil {
		// The mapping reports raw fstat errors; the reader surfaces
		// them in the IO class like every other descriptor failure
		// (Rust NamespaceError::Io -> Error::Io).
		return 0, 0, &format.Error{Code: format.CodeIO, Detail: err.Error()}
	}
	return device, inode, nil
}

// ConfirmUnchanged re-verifies that path still names the opened database
// and that the committed generation is still the one this reader captured
// (Rust BasicSource::final_check over the Current selection, whose
// bind_current re-runs verify_path -> bootstrap -> verify_path). The
// snapshot builder calls it between builder finish and publish: a
// replaced path or a republished generation during the build refuses the
// publication with the RecoveryCandidateChanged class (code 51), the
// exact class bind_current's candidate_changed wrapping produces for
// identity failures.
func (r *ImmutableReader) ConfirmUnchanged(path string) error {
	// bind_current's first verify_path: the path must still name the
	// opened inode; every identity or namespace failure collapses to
	// RecoveryCandidateChanged (Rust candidate_changed(_) => Error::
	// RecoveryCandidateChanged).
	if err := r.m.VerifyIdentity(path); err != nil {
		return &format.Error{Code: format.CodeRecoveryCandidateChanged, Detail: "the selected recovery candidate changed"}
	}
	// bind_current's bootstrap_file: re-select the committed generation
	// over the mapped meta pair and prove it is unchanged. Bootstrap
	// failures keep their own class (Rust propagates bootstrap errors
	// unwrapped); only a different selection is the changed-candidate
	// class, exactly like final_check's `selected != used`.
	meta, err := r.reselectMeta()
	if err != nil {
		return err
	}
	if meta != r.meta {
		return &format.Error{Code: format.CodeRecoveryCandidateChanged, Detail: "the selected recovery candidate changed"}
	}
	// bind_current's second verify_path: catch a namespace replacement
	// during the re-read window.
	if err := r.m.VerifyIdentity(path); err != nil {
		return &format.Error{Code: format.CodeRecoveryCandidateChanged, Detail: "the selected recovery candidate changed"}
	}
	return nil
}

// reselectMeta re-runs the two-meta bootstrap selection over the mapped
// pages without mutating the reader (Rust bind_current's bootstrap_file).
// The immutable mapping is pinned to the committed extent, so the
// selection observes exactly the same physical length as the open did.
func (r *ImmutableReader) reselectMeta() (format.Meta, error) {
	p0, err := r.m.Page(0)
	if err != nil {
		return format.Meta{}, &format.Error{Code: format.CodeFormatInvalid, Detail: err.Error()}
	}
	p1, err := r.m.Page(1)
	if err != nil {
		return format.Meta{}, &format.Error{Code: format.CodeFormatInvalid, Detail: err.Error()}
	}
	res, err := bootstrap.Open(p0, p1, r.m.PhysicalSize(), bootstrap.ModeImmutableReader)
	if err != nil {
		return format.Meta{}, err
	}
	return res.Meta, nil
}
