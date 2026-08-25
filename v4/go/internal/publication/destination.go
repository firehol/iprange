// Destination binding for namespace publication (Rust
// publication/namespace.rs Destination + namespace/unix.rs
// destination_names). bind validates the main name with the SDK path
// rules, derives the reader-coordination twin (<main>.readers),
// opens the retained parent directory, proves the name_max bounds,
// and captures the basename and creator-only security commitments.
// The destination is the single authority for the private attempt and
// reservation names of one publication.

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// destination is one bound publication destination (Rust Destination).
type destination struct {
	dir                *live.Directory
	main               string
	coordination       string
	basenameCommitment [32]byte
	security           security.Profile
}

// bindDestination binds one destination path (Rust Destination::bind):
// the final component must satisfy the SDK main-name rules
// (validate_main_name; violations are the InvalidName class), the
// coordination twin derives from it, the parent directory must open
// as a retained directory, both names must fit the name_max proof,
// and the basename commitment must encode.
func bindDestination(path string) (*destination, error) {
	component, ok := mainComponent(path)
	if !ok {
		return nil, &live.NamespaceError{Kind: live.NamespaceInvalidName}
	}
	if invalidMainName(component) {
		return nil, &live.NamespaceError{Kind: live.NamespaceInvalidName}
	}
	main, coordination := destinationNames(component)
	dir, err := live.OpenDirectory(parentOfPath(path))
	if err != nil {
		return nil, err
	}
	if err := dir.RequireNameLengths(main, coordination); err != nil {
		dir.Close()
		return nil, err
	}
	commitment, err := basenameCommitment(destinationBasenameEncoding(), destinationEncodedBytes(main))
	if err != nil {
		dir.Close()
		return nil, &live.NamespaceError{Kind: live.NamespaceInvalidName}
	}
	profile, err := security.Capture()
	if err != nil {
		dir.Close()
		return nil, err
	}
	return &destination{
		dir:                dir,
		main:               main,
		coordination:       coordination,
		basenameCommitment: commitment,
		security:           profile,
	}, nil
}

// destinationNames derives the main and coordination names of one
// destination component (Rust destination_names: main unchanged, the
// coordination twin appends the ".readers" suffix).
func destinationNames(component string) (main, coordination string) {
	return component, component + ".readers"
}

// destinationBasenameEncoding returns the platform basename encoding
// of the destination names (Rust destination_names encoding tag).
func destinationBasenameEncoding() basenameEncoding {
	return platformBasenameEncoding()
}

// destinationEncodedBytes encodes one ASCII name for the basename
// commitment: raw bytes on unix, UTF-16LE units on Windows (Rust
// Name::bytes).
func destinationEncodedBytes(name string) []byte {
	return platformEncodedBytes(name)
}

// mainName returns the publication main name.
func (d *destination) mainName() string { return d.main }

// coordinationName returns the reader-coordination twin name.
func (d *destination) coordinationName() string { return d.coordination }

// basenameCommitmentValue returns the captured basename commitment.
func (d *destination) basenameCommitmentValue() [32]byte { return d.basenameCommitment }

// securityCommitment returns the captured creator-only security
// commitment (Rust Destination::security_commitment, stored in the
// reservation record).
func (d *destination) securityCommitment() [32]byte { return d.security.Commitment() }

// directory returns the retained parent directory.
func (d *destination) directory() *live.Directory { return d.dir }

// create creates one private name in the retained directory with the
// creator-only policy (Rust Destination::create; the platform arm
// selects the unprotected 0600 create plus post-proof or the protected
// descriptor create).
func (d *destination) create(name string) (*os.File, error) {
	return destinationCreate(d.dir, name, d.security)
}

// secureCreated applies the creator-only policy to one created
// artifact (Rust Destination::secure_created).
func (d *destination) secureCreated(f *os.File) error {
	if err := security.SecureCreatorOnly(f, d.security); err != nil {
		return securityNamespaceError(err)
	}
	return nil
}

// verifyCreated proves one created artifact still carries the
// destination creator commitment (Rust Destination::verify_created).
func (d *destination) verifyCreated(f *os.File) error {
	commitment, err := security.CreatorOnlyCommitment(f)
	if err != nil {
		return securityNamespaceError(err)
	}
	if commitment != d.security.Commitment() {
		return &live.NamespaceError{Kind: live.NamespaceAccessPolicy}
	}
	return nil
}

// requireFailIfExistsAvailable proves the main and coordination names
// are both absent (Rust Destination::require_fail_if_exists_available).
func (d *destination) requireFailIfExistsAvailable() error {
	if err := d.dir.RequireAbsent(d.main); err != nil {
		return err
	}
	return d.dir.RequireAbsent(d.coordination)
}

// outputName builds the private publication-output name of one attempt
// (Rust Destination::output_name).
func (d *destination) outputName(attempt [16]byte) (string, error) {
	return d.attemptName(outputPrefix, attempt)
}

// reservationName builds the private reservation name of one attempt
// (Rust Destination::reservation_name).
func (d *destination) reservationName(attempt [16]byte) (string, error) {
	return d.attemptName(reservationPrefix, attempt)
}

// attemptName builds one private attempt name and proves its length
// (Rust Destination::attempt_name).
func (d *destination) attemptName(prefix string, attempt [16]byte) (string, error) {
	name, err := privateName(prefix, attempt)
	if err != nil {
		return "", err
	}
	if err := d.dir.RequireNameLengths(name); err != nil {
		return "", err
	}
	return name, nil
}
