//go:build !windows

package live

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// bindPath must derive the parent and the final component with Rust
// live_namespace::bind_path semantics on the raw path: the parent is
// path.parent(), the name is path.file_name(), so a trailing ".."
// component is refused with the no-file-name class instead of being
// resolved onto an ancestor, and a trailing "." is normalized away
// (accepted). These are the round-5 sibling-class pins at the
// namespace layer.
func TestBindPathSiblingClassParity(t *testing.T) {
	// Rejections that happen before any filesystem access.
	for _, path := range []string{
		".", "/", "//", "a/b/..", "/a/b/..", "x/y/../..", "a/..",
	} {
		dir, name, err := bindPath(path)
		if dir != nil {
			dir.Close()
		}
		if err == nil {
			t.Errorf("bindPath(%q) = (%q) accepted, want rejection", path, name)
			continue
		}
		fe, ok := err.(*format.Error)
		if !ok || fe.Code != format.CodeInvalidArgument {
			t.Errorf("bindPath(%q) error = %v, want InvalidArgument", path, err)
		}
	}
}

// bindPath("x") and bindPath("x/.") bind the same parent+name pair
// (Rust bind_path: file_name("x/.") is Some("x") and the empty parent
// open fails as Missing because a bare relative name has no parent
// directory to open).
func TestBindPathBareRelativeName(t *testing.T) {
	// A bare relative name has no parent to open; use an absolute
	// temp-dir path so the open reaches a real directory.
	dir := t.TempDir()
	probe := filepath.Join(dir, "name")
	bound, name, err := bindPath(probe)
	if bound != nil {
		defer bound.Close()
	}
	if err != nil {
		t.Fatalf("bindPath(%q) rejected: %v", probe, err)
	}
	if name != "name" {
		t.Fatalf("bindPath(%q) name = %q, want name", probe, name)
	}
	bound, name, err = bindPath(probe + "//.")
	if bound != nil {
		defer bound.Close()
	}
	if err != nil {
		t.Fatalf("bindPath(%q) rejected: %v", probe+"/.", err)
	}
	if name != "name" {
		t.Fatalf("bindPath(%q) name = %q, want name", probe+"/.", name)
	}
}

// bindPair checks the raw parents for equality before binding (Rust
// live_namespace::bind_pair: private.parent() != canonical.parent()),
// so a trailing-parent private path fails its own bind after the
// parent comparison instead of being resolved.
func TestBindPairParentParity(t *testing.T) {
	// Parents differ on the raw path: refuse before any open.
	if _, _, _, err := bindPair("a/b/..", "a/c/.."); err == nil {
		t.Error("bindPair(a/b/.., a/c/..) accepted, want share-directory rejection")
	}
	// Equal raw parents with a trailing-parent private: bind_path
	// refuses the private with the no-file-name class.
	if _, _, _, err := bindPair("a/b/..", "a/b/x"); err == nil {
		t.Error("bindPair(a/b/.., a/b/x) accepted, want no-file-name rejection")
	}
}

// A trailing-parent path must not be re-resolved to an ancestor by
// the verify path (Rust verify_path_any_link calls bind_path raw).
// The probe is built by raw concatenation: filepath.Join cleans the
// trailing ".." onto the target itself, and the handler would then
// verify the cleaned target instead of rejecting the raw spelling.
func TestVerifyRejectsTrailingParent(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// The cleaned form of the probe is exactly target, so a
	// regression that lexically re-resolves the trailing ".." would
	// succeed (target is the regular file whose identity is
	// expected) and the assertion below would fail.
	target := filepath.Join(sub, "main.iprange")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := pathIdentity(target)
	if err != nil {
		t.Fatal(err)
	}
	probe := target + string(filepath.Separator) + "x" + string(filepath.Separator) + ".."
	err = verifyPath(probe, *identity)
	fe, ok := err.(*format.Error)
	if err == nil || !ok || fe.Code != format.CodeInvalidArgument {
		t.Fatalf("verifyPath(%q) error = %v, want InvalidArgument no-file-name class", probe, err)
	}
}
