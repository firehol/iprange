// Metadata delivery encoding and bounded atomic file publication
// (Rust handlers/output.rs parity).

package handlers

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"runtime"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/fileio"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/pathname"

	"github.com/firehol/iprange/v4/go/internal/calleropen"
)

// Base64Padded is the standard padded base64 alphabet (wire encoding
// for metadata blobs).
func Base64Padded(input []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	output := make([]byte, 0, (len(input)+2)/3*4)
	for i := 0; i < len(input); i += 3 {
		var b0, b1, b2 byte
		b0 = input[i]
		if i+1 < len(input) {
			b1 = input[i+1]
		}
		if i+2 < len(input) {
			b2 = input[i+2]
		}
		word := uint32(b0)<<16 | uint32(b1)<<8 | uint32(b2)
		output = append(output, alphabet[word>>18], alphabet[(word>>12)&63])
		if i+1 < len(input) {
			output = append(output, alphabet[(word>>6)&63])
		} else {
			output = append(output, '=')
		}
		if i+2 < len(input) {
			output = append(output, alphabet[word&63])
		} else {
			output = append(output, '=')
		}
	}
	return string(output)
}

// MetadataOutput publishes one metadata blob under the requested
// policy and returns the generic OUTPUT_FACTS wire object. The blob
// has already been read when the budget is enforced: an over-limit
// refusal is an output-limit failure of a read-only operation.
func MetadataOutput(path string, bytes []byte, policy iprangedb.PublicationPolicy, maxOutputBytes uint64, maxOpenFiles uint32) (map[string]any, *rpc.HandlerError) {
	if maxOpenFiles < 1 {
		return nil, rpc.NewHandlerError("invalid_argument", "not_started",
			"metadata file delivery requires at least one open file")
	}
	if uint64(len(bytes)) > maxOutputBytes {
		return nil, rpc.NewHandlerError("output_limit", "read_only_failure",
			fmt.Sprintf("metadata output is %d bytes, limit is %d", len(bytes), maxOutputBytes))
	}
	sum := sha256.Sum256(bytes)
	sha := HexBytes(sum[:])
	if herr := publishMetadata(path, bytes, policy, sha); herr != nil {
		return nil, herr
	}
	return map[string]any{
		"path":   path,
		"sha256": sha,
		"bytes":  fmt.Sprintf("%d", len(bytes)),
		"rows":   "1",
	}, nil
}

// publishMetadata delivers one metadata blob through a private
// temporary and an atomic publication step. Failures before the
// destination name is visible are definite refusals; failures after it
// are unknown durability of a delivered file.
func publishMetadata(path string, bytes []byte, policy iprangedb.PublicationPolicy, sha string) *rpc.HandlerError {
	parent := live.FileParent(path)
	handle, herr := rpc.NewHandle()
	if herr != nil {
		return herr
	}
	temporary := pathname.Push(parent, "."+handle+".metadata.tmp")
	// The owner-side open keeps this create out of the runtime network
	// poller, whose initialization has no failure path under a low
	// RLIMIT_NOFILE (wave-19.25 design section 5).
	file, err := calleropen.Open(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL|calleropen.NonBlocking, 0o600)
	if err != nil {
		return outputFileError(err, "create metadata output")
	}
	if herr := writeAndPublishMetadata(file, temporary, path, bytes, policy, sha); herr != nil {
		return herr
	}
	// The destination name is visible with its complete content. Failing to
	// synchronize the directory now leaves the durability of that namespace
	// entry unproven, which is an unknown outcome and not a failure to have
	// delivered: the destination must never be removed on this path.
	if err := syncDirectoryRaw(parent); err != nil {
		return metadataPublicationFailure(err, "sync metadata output directory",
			path, bytes, policy, sha, true)
	}
	return nil
}

// writeAndPublishMetadata writes the private temporary, synchronizes it,
// and moves it onto the destination. Every failure before the destination
// name appears removes the private temporary and keeps the definite
// read-only refusal class; every failure after it can only be the unknown
// durability of a delivered file.
func writeAndPublishMetadata(file *os.File, temporary, destination string, bytes []byte, policy iprangedb.PublicationPolicy, sha string) *rpc.HandlerError {
	if _, err := file.Write(bytes); err != nil {
		_ = file.Close()
		_ = os.Remove(temporary)
		return outputFileError(err, "write metadata output")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(temporary)
		return outputFileError(err, "write metadata output")
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporary)
		return outputFileError(err, "write metadata output")
	}
	switch policy {
	case iprangedb.PolicyFailIfExists:
		// A hard-link publication is the portable no-replacement atom:
		// destination creation succeeds only while the name is absent.
		if err := os.Link(temporary, destination); err != nil {
			_ = os.Remove(temporary)
			return outputFileError(err, "publish metadata output")
		}
		// The destination is published. Removing the private name is
		// cleanup now, so its failure cannot be reported as a delivery
		// failure; retry once and report what the namespace holds.
		if err := os.Remove(temporary); err != nil {
			temporaryRemoved := os.Remove(temporary) == nil
			return metadataPublicationFailure(err, "remove metadata temporary",
				destination, bytes, policy, sha, temporaryRemoved)
		}
	case iprangedb.PolicyReplaceExisting, iprangedb.PolicyReplaceExistingNoRollback:
		// rename(2) and MoveFileExW(REPLACE_EXISTING) replace the
		// destination atomically on both supported families.
		if err := fileio.RenameReplace(temporary, destination); err != nil {
			_ = os.Remove(temporary)
			return outputFileError(err, "publish metadata output")
		}
	}
	return nil
}

// metadataPublicationFailure is the adapter-owned publication failure
// once the destination name is visible: the bytes are delivered and the
// durability of the namespace entry is unproven, so the outcome is
// `outcome_unknown` and the error carries the publication facts the
// caller needs to judge the destination (the same factual model as the
// export writer's publicationFailure).
func metadataPublicationFailure(err error, stage, destination string, bytes []byte, policy iprangedb.PublicationPolicy, sha string, temporaryRemoved bool) *rpc.HandlerError {
	return &rpc.HandlerError{
		Code:    "io",
		Outcome: "outcome_unknown",
		Message: fmt.Sprintf("%s: %v", stage, err),
		Details: map[string]any{
			"publication": map[string]any{
				"outcome":             "outcome_unknown",
				"publication_policy":  fileio.PolicyName(policy),
				"path":                destination,
				"stage":               stage,
				"destination_visible": true,
				"temporary_removed":   temporaryRemoved,
				"bytes":               fmt.Sprintf("%d", len(bytes)),
				"rows":                "1",
				"sha256":              sha,
			},
		},
	}
}

// syncDirectoryRaw synchronizes one output directory. The no-op on
// Windows is the platform's own durability rule for namespace entries;
// callers decide the error class, because a failure once the destination
// is visible is an unknown durability and not a definite refusal.
func syncDirectoryRaw(parent string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	// A directory handle goes through the same owner as every persistent
	// node: on Linux os.Open registers the descriptor with the runtime
	// network poller regardless of the node type, and that initialization
	// has no failure path under a low RLIMIT_NOFILE.
	dir, err := calleropen.Open(parent, os.O_RDONLY|calleropen.NonBlocking, 0)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func outputFileError(err error, operation string) *rpc.HandlerError {
	message := fmt.Sprintf("%s: %v", operation, err)
	// Metadata delivery is a read-only operation: every file-I/O
	// failure after the metadata read began reports
	// read_only_failure.
	if errors.Is(err, os.ErrExist) {
		return rpc.NewHandlerError("name_exists", "read_only_failure", message)
	}
	return rpc.NewHandlerError("io", "read_only_failure", message)
}
