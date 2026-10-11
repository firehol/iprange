package fileio

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/security"
)

// sol round-6 P1 (Rust twin: export_writer.rs
// protected_output_removes_inherited_default_acl_entries): a default
// ACL on the parent directory leaves named entries on a newly created
// output that a chmod cannot remove — chmod changes the mask, not the
// entry list. The protected contract is a proven trivial ACL, so the
// creator routes through the security authority; this detector
// observes the artifact, never repairs it, so a creator regression
// fails the proof here.
func TestProtectedOutputRemovesInheritedDefaultACLEntries(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("the default-ACL inheritance shape is linux-specific: %s", runtime.GOOS)
	}
	dir := t.TempDir()
	if err := exec.Command("setfacl", "-d", "-m", "u:65534:---", dir).Run(); err != nil {
		t.Skipf("setfacl unavailable: %v", err)
	}
	path := filepath.Join(dir, "acl.out")
	file, err := CreateOutputFile(path, true)
	if err != nil {
		t.Fatalf("creator-only output under a default ACL: %v", err)
	}
	defer file.Close()
	defer os.Remove(path)
	if _, err := security.CreatorOnlyCommitment(file); err != nil {
		t.Fatalf("creator-only output created under a default ACL must prove the protected contract (no inherited named entries, exact 0600): %v", err)
	}
}
