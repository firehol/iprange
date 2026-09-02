// Metadata delivery encoding and bounded atomic file publication
// (Rust handlers/output.rs parity).

package handlers

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
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
	if herr := publishMetadata(path, bytes, policy); herr != nil {
		return nil, herr
	}
	return map[string]any{
		"path":   path,
		"sha256": sha,
		"bytes":  fmt.Sprintf("%d", len(bytes)),
		"rows":   "1",
	}, nil
}

func publishMetadata(path string, bytes []byte, policy iprangedb.PublicationPolicy) *rpc.HandlerError {
	parent := filepath.Dir(path)
	if parent == "" {
		parent = "."
	}
	handle, herr := rpc.NewHandle()
	if herr != nil {
		return herr
	}
	temporary := filepath.Join(parent, "."+handle+".metadata.tmp")
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return outputFileError(err, "create metadata output")
	}
	if herr := writeAndPublishMetadata(file, temporary, path, bytes, policy); herr != nil {
		_ = os.Remove(temporary)
		return herr
	}
	return syncOutputDirectory(parent)
}

func writeAndPublishMetadata(file *os.File, temporary, destination string, bytes []byte, policy iprangedb.PublicationPolicy) *rpc.HandlerError {
	if _, err := file.Write(bytes); err != nil {
		_ = file.Close()
		return outputFileError(err, "write metadata output")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return outputFileError(err, "write metadata output")
	}
	if err := file.Close(); err != nil {
		return outputFileError(err, "write metadata output")
	}
	switch policy {
	case iprangedb.PolicyFailIfExists:
		// A hard-link publication is the portable no-replacement atom:
		// destination creation succeeds only while the name is absent.
		if err := os.Link(temporary, destination); err != nil {
			return outputFileError(err, "publish metadata output")
		}
		if err := os.Remove(temporary); err != nil {
			return outputFileError(err, "remove metadata temporary")
		}
	case iprangedb.PolicyReplaceExisting, iprangedb.PolicyReplaceExistingNoRollback:
		// rename(2) and MoveFileExW(REPLACE_EXISTING) replace the
		// destination atomically on both supported families.
		if err := os.Rename(temporary, destination); err != nil {
			return outputFileError(err, "publish metadata output")
		}
	}
	return nil
}

func syncOutputDirectory(parent string) *rpc.HandlerError {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(parent)
	if err != nil {
		return outputFileError(err, "sync metadata output directory")
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return outputFileError(err, "sync metadata output directory")
	}
	return nil
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
