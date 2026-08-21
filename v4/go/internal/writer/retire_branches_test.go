package writer

// Direct-call pins for the exchange retirement classification branches
// (Rust main_file.rs unlink_previous + verify_private_or_retired +
// unlink_exact). The crash suite covers the happy path through the real
// publish; these cover every refusal branch deterministically by
// constructing the namespace state before the call.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// retireBranchState builds the namespace for one retirement branch: a
// destination file (the bound previous inode), an attempt owning the
// private name, and the bound previousCustody the real custody flow
// would have captured.
func retireBranchState(t *testing.T) (*OutputAttempt, *previousCustody, string, string) {
	t.Helper()
	dir := t.TempDir()
	dest := filepath.Join(dir, "output.v4")
	payload := []byte("previous bytes")
	if err := os.WriteFile(dest, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	device, inode, err := mapping.StatIdentity(dest)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	attempt, err := CreateAttempt(dest, PolicyReplaceExisting)
	if err != nil {
		t.Fatal(err)
	}
	previous := &previousCustody{device: device, inode: inode, byteLength: uint64(len(payload)), file: file}
	return attempt, previous, dest, filepath.Join(dir, attempt.Name())
}

// TestRetireExchangedPreviousBranchClassification pins every refusal
// branch against the Rust classification: Clean when the zero-link
// inode already retired with the private name absent, NameExists when a
// foreign name occupies the retired slot, NameNotFound when the single
// linked name vanished, NotRegular for a symlink or directory at the
// private name, IdentityChanged for a foreign inode or a changed byte
// length, LinkCount for a second hard link, and Clean when the private
// name is the last link of the bound inode and the unlink drops it.
// The post-unlink CleanupConflict proof is a race-window guarantee (a
// foreign link must appear between the pre-check fstat and the unlink);
// it has no deterministic construction without a blocking checkpoint,
// mirroring the Rust checkpoint tests.
func TestRetireExchangedPreviousBranchClassification(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(*testing.T, *OutputAttempt, *previousCustody, string, string)
		code    format.ErrorCode
		detail  string
		wantNil bool
	}{
		{
			name: "zero-links-and-name-absent-is-clean",
			setup: func(t *testing.T, _ *OutputAttempt, _ *previousCustody, dest, _ string) {
				if err := os.Remove(dest); err != nil {
					t.Fatal(err)
				}
			},
			wantNil: true,
		},
		{
			name: "zero-links-with-foreign-name-is-name-exists",
			setup: func(t *testing.T, attempt *OutputAttempt, _ *previousCustody, dest, private string) {
				if err := os.Remove(dest); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(private, []byte("foreign"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			code:   format.CodeNameExists,
			detail: "publication name already exists",
		},
		{
			name: "changed-byte-length-is-identity-changed",
			setup: func(t *testing.T, _ *OutputAttempt, previous *previousCustody, _ string, _ string) {
				previous.byteLength++
			},
			code:   format.CodeConflict,
			detail: "publication inode identity changed",
		},
		{
			name: "multi-link-is-link-count-changed",
			setup: func(t *testing.T, _ *OutputAttempt, _ *previousCustody, dest string, _ string) {
				if err := os.Link(dest, dest+".extra"); err != nil {
					t.Skipf("filesystem refuses hard links: %v", err)
				}
			},
			code:   format.CodeConflict,
			detail: "publication inode link count changed",
		},
		{
			name:   "one-link-name-absent-is-name-not-found",
			setup:  func(t *testing.T, _ *OutputAttempt, _ *previousCustody, _ string, _ string) {},
			code:   format.CodeNameNotFound,
			detail: "publication name is missing",
		},
		{
			name: "symlink-at-private-name-is-not-regular",
			setup: func(t *testing.T, _ *OutputAttempt, _ *previousCustody, _ string, private string) {
				if err := os.Symlink("elsewhere", private); err != nil {
					t.Skipf("filesystem refuses symlinks: %v", err)
				}
			},
			code:   format.CodeConflict,
			detail: "publication name is not a regular file",
		},
		{
			name: "directory-at-private-name-is-not-regular",
			setup: func(t *testing.T, _ *OutputAttempt, _ *previousCustody, _ string, private string) {
				if err := os.Mkdir(private, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			code:   format.CodeConflict,
			detail: "publication name is not a regular file",
		},
		{
			name: "foreign-inode-at-private-name-is-identity-changed",
			setup: func(t *testing.T, _ *OutputAttempt, _ *previousCustody, _ string, private string) {
				if err := os.WriteFile(private, []byte("foreign"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			code:   format.CodeConflict,
			detail: "publication inode identity changed",
		},
		{
			name: "last-link-at-private-name-is-clean",
			setup: func(t *testing.T, _ *OutputAttempt, _ *previousCustody, dest string, private string) {
				// The exchange leaves the bound inode with exactly one
				// link, at the private name (the destination name now
				// holds the attempt); mirror that state by moving the
				// file.
				if err := os.Rename(dest, private); err != nil {
					t.Fatal(err)
				}
			},
			wantNil: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attempt, previous, dest, private := retireBranchState(t)
			tc.setup(t, attempt, previous, dest, private)
			err := retireExchangedPrevious(attempt, previous)
			if tc.wantNil {
				if err != nil {
					t.Fatalf("retire = %v, want nil", err)
				}
				return
			}
			var fe *format.Error
			if !errors.As(err, &fe) || fe.Code != tc.code {
				t.Fatalf("retire err = %v, want code %d", err, tc.code)
			}
			if tc.detail != "" && fe.Detail != tc.detail {
				t.Fatalf("retire detail = %q, want %q", fe.Detail, tc.detail)
			}
		})
	}
}

// TestRetireExchangedPreviousNilPreviousIsClean pins the no-rollback
// guard: policies that never exchange have no bound custody and the
// retirement is a no-op.
func TestRetireExchangedPreviousNilPreviousIsClean(t *testing.T) {
	if err := retireExchangedPrevious(nil, nil); err != nil {
		t.Fatalf("nil retirement = %v, want nil", err)
	}
}
