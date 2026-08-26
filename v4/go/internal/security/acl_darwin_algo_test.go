// Darwin creator-only probe classification tests (Rust publication/
// security/apple.rs parity matrix): the pure classification runs on
// every host, so the arm's outcome mapping is pinned without a Darwin
// host. The syscall glue (numbers, sentinels, kernel fill semantics)
// is cross-compiled and natively exercised in the authorized macOS
// round.

package security

import (
	"errors"
	"syscall"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

func TestDarwinACLAppliedClassification(t *testing.T) {
	if err := darwinACLAppliedAt("remove inherited access ACL", nil); err != nil {
		t.Fatalf("clean apply = %v, want nil", err)
	}
	err := darwinACLAppliedAt("remove inherited access ACL", syscall.EACCES)
	var fe *format.Error
	if !errors.As(err, &fe) || fe.Code != format.CodeIO || fe.Detail != "remove inherited access ACL: "+syscall.EACCES.Error() {
		t.Fatalf("failed apply = %v, want IO with the operation label", err)
	}
}

func TestDarwinTrivialProbeClassification(t *testing.T) {
	cases := []struct {
		name     string
		errno    error
		aclBytes uintptr
		wantCode format.ErrorCode // 0 = nil outcome
	}{
		{"no acl zero size", nil, 0, 0},
		{"enoent", syscall.ENOENT, 0, 0},
		{"acl present", nil, 1, format.CodeAccessPolicyUnsupported},
		{"unsupported filesystem", syscall.EOPNOTSUPP, 0, format.CodeDurabilityUnsupported},
		{"io failure", syscall.EIO, 0, format.CodeIO},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := darwinTrivialProbe(tc.errno, tc.aclBytes)
			if tc.wantCode == 0 {
				if err != nil {
					t.Fatalf("probe = %v, want nil", err)
				}
				return
			}
			var fe *format.Error
			if !errors.As(err, &fe) || fe.Code != tc.wantCode {
				t.Fatalf("probe = %v, want code %d", err, tc.wantCode)
			}
		})
	}
}

// TestDarwinProbeBufferGrowth pins the libc statx1 growth contract the
// arm implements: an undersized buffer reports the needed size, and
// the probe classifies after growing.
func TestDarwinProbeBufferGrowth(t *testing.T) {
	if got := kauthFilesecSize(16); got != 44+16*24 {
		t.Fatalf("kauth_filesec size for 16 entries = %d, want %d", got, 44+16*24)
	}
	if got := kauthFilesecSize(0); got != 44 {
		t.Fatalf("kauth_filesec size for 0 entries = %d, want 44", got)
	}
}
