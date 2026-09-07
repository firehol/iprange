// Reconstruction of a locked secured output from exact persisted
// evidence (Rust output.rs resume_secured_output /
// resume_secured_output_for_cleanup / bind_secured_output). The
// cleanup and recovery machines rebuild the attempt from its portable
// facts without any path-based trust: the identity, basename, and
// creator commitment must all match the bound destination.

package publication

import (
	"bytes"
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// resumeSecuredOutput opens the secured private output named by one
// facts record (Rust output.rs resume_secured_output). A missing
// artifact reports Missing.
func resumeSecuredOutput(destinationPath string, facts *PrivateOutputAttempt) (outputAttempt, *os.File, error) {
	attempt, file, present, err := openSecuredOutput(destinationPath, facts)
	if err != nil {
		return outputAttempt{}, nil, err
	}
	if !present {
		return outputAttempt{}, nil, &live.NamespaceError{Kind: live.NamespaceMissing}
	}
	return attempt, file, nil
}

// resumeSecuredOutputForCleanup opens the secured private output for
// exact cleanup evidence; an absent artifact is proven absent after a
// durability sync and reports present=false (Rust
// output.rs resume_secured_output_for_cleanup).
func resumeSecuredOutputForCleanup(destinationPath string, facts *PrivateOutputAttempt) (outputAttempt, *os.File, bool, error) {
	attempt, file, present, err := openSecuredOutput(destinationPath, facts)
	if err != nil {
		return outputAttempt{}, nil, false, err
	}
	if present {
		return attempt, file, true, nil
	}
	d, name, _, err := bindSecuredOutput(destinationPath, facts)
	if err != nil {
		return outputAttempt{}, nil, false, err
	}
	if err := d.directory().Sync(); err != nil {
		return outputAttempt{}, nil, false, err
	}
	if err := d.directory().Verify(); err != nil {
		return outputAttempt{}, nil, false, err
	}
	if err := d.directory().RequireAbsent(name); err != nil {
		return outputAttempt{}, nil, false, err
	}
	return outputAttempt{}, nil, false, nil
}

// openSecuredOutput binds the facts and opens the artifact without
// following symlinks (Rust output.rs open_secured_output). present is
// false only when the artifact is absent.
func openSecuredOutput(destinationPath string, facts *PrivateOutputAttempt) (outputAttempt, *os.File, bool, error) {
	d, name, identity, err := bindSecuredOutput(destinationPath, facts)
	if err != nil {
		return outputAttempt{}, nil, false, err
	}
	regular, err := d.directory().OpenRegular(name, true)
	if err != nil {
		return outputAttempt{}, nil, false, err
	}
	if regular == nil {
		return outputAttempt{}, nil, false, nil
	}
	if regular.Identity != identity {
		regular.File.Close()
		return outputAttempt{}, nil, false, &live.NamespaceError{Kind: live.NamespaceIdentityChanged}
	}
	if err := d.directory().VerifyName(name, identity); err != nil {
		regular.File.Close()
		return outputAttempt{}, nil, false, err
	}
	if err := d.verifyCreated(regular.File); err != nil {
		regular.File.Close()
		return outputAttempt{}, nil, false, err
	}
	return outputAttempt{
		destination: d,
		attemptID:   facts.PublicationAttemptID,
		name:        name,
		identity:    identity,
	}, regular.File, true, nil
}

// bindSecuredOutput rebuilds the destination, private name, and
// identity from one facts record without touching the artifact (Rust
// output.rs bind_secured_output).
func bindSecuredOutput(destinationPath string, facts *PrivateOutputAttempt) (*destination, string, live.FileIdentity, error) {
	if facts.BasenameEncoding != basenameEncodingKind || facts.CreationSecurity.Kind != creationSecurityKind {
		return nil, "", live.FileIdentity{}, &format.Error{
			Code:   format.CodeInvalidArgument,
			Detail: "worker output facts use an unsupported encoding",
		}
	}
	var identity live.FileIdentity
	if !facts.IdentityPresent {
		return nil, "", live.FileIdentity{}, &format.Error{
			Code:   format.CodeInvalidArgument,
			Detail: "worker output identity is invalid",
		}
	}
	device, inode, ok := facts.Identity.DeviceInode()
	if !ok {
		return nil, "", live.FileIdentity{}, &format.Error{
			Code:   format.CodeInvalidArgument,
			Detail: "worker output identity is invalid",
		}
	}
	identity = live.IdentityFromDeviceInode(device, inode)
	d, err := bindDestination(destinationPath)
	if err != nil {
		return nil, "", live.FileIdentity{}, err
	}
	name, err := d.outputName(facts.PublicationAttemptID)
	if err != nil {
		return nil, "", live.FileIdentity{}, err
	}
	if directoryLocalIdentity(d) != facts.DirectoryIdentity ||
		!bytes.Equal(platformEncodedBytes(name), facts.Basename) ||
		d.securityCommitment() != facts.CreationSecurity.Commitment {
		return nil, "", live.FileIdentity{}, &live.NamespaceError{Kind: live.NamespaceIdentityChanged}
	}
	return d, name, identity, nil
}

// resumePreparedOutput reconstructs one prepared output from the
// exact inspected private artifact (Rust output_resume.rs
// PreparedOutput::resume): the inspected file, mapping, meta, byte
// length, and digest move into the prepared output unchanged. On
// error the inspected resources are closed (Rust drops the moved
// ResumedOutput on the error path).
func resumePreparedOutput(destination *destination, header reservationHeader, inspected *inspectedOutput) (*preparedOutput, error) {
	name, err := destination.outputName(header.attemptID)
	if err != nil {
		_ = inspected.Close()
		return nil, err
	}
	return &preparedOutput{
		attempt: outputAttempt{
			destination: destination,
			attemptID:   header.attemptID,
			name:        name,
			identity:    inspected.identity,
		},
		file:       inspected.file,
		mapping:    inspected.mapping,
		meta:       inspected.meta,
		byteLength: inspected.byteLength,
		sha512:     inspected.sha512,
		policy:     reservationPolicyFailIfExists,
		previous:   nil,
	}, nil
}

// resumePreparedOutputReplacement reconstructs one replacement
// prepared output from the exact inspected private artifact and the
// recorded previous main (Rust output_resume.rs
// PreparedOutput::resume_replacement): the inspected file, mapping,
// meta, byte length, and digest move into the prepared output with
// the replacement policy and the previous main. The previous main
// keeps its own mapping; preparedOutput.Close releases it together
// with the output, exactly like the Rust drop of PreparedOutput
// (which owns PreviousMain). On an error here the inspected artifact
// is closed and the previous main stays owned by the caller.
func resumePreparedOutputReplacement(destination *destination, header reservationHeader, inspected *inspectedReplacement, previous *previousMain) (*preparedOutput, error) {
	if !inspected.metaPresent || inspected.mapping == nil {
		_ = inspected.Close()
		return nil, conflictProblem("finished replacement output has no selected metadata")
	}
	name, err := destination.outputName(header.attemptID)
	if err != nil {
		_ = inspected.Close()
		return nil, err
	}
	return &preparedOutput{
		attempt: outputAttempt{
			destination: destination,
			attemptID:   header.attemptID,
			name:        name,
			identity:    inspected.identity,
		},
		file:       inspected.file,
		mapping:    inspected.mapping,
		meta:       inspected.meta,
		byteLength: inspected.byteLength,
		sha512:     inspected.sha512,
		policy:     header.policy,
		previous:   previous,
	}, nil
}

// ResumePublishAttempt opens the secured private output named by one
// facts record and wraps it as the buildable attempt of a resumed
// session (Rust output.rs resume_secured_output; the worker recovery
// machine consumes the parent-created attempt the request carried).
// The resumed attempt keeps the fail-if-exists policy of the parent
// creation, so the finish selects the same publication machine the
// in-process caller would have used.
func ResumePublishAttempt(destinationPath string, facts *PrivateOutputAttempt) (*PublishAttempt, error) {
	attempt, file, err := resumeSecuredOutput(destinationPath, facts)
	if err != nil {
		return nil, err
	}
	return &PublishAttempt{attempt: attempt, file: file, policy: reservationPolicyFailIfExists}, nil
}
