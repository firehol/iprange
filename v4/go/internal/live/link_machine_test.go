//go:build freebsd || v4work

// FreeBSD no-replace transition machine facts (Rust
// namespace_tests.rs freebsd arms). The machine compiles under the
// v4work tag on every host exactly like Rust's `test` cfg, so the
// crash-safe linkat transition is exercised on the linux/darwin test
// hosts and in the freebsd native build.

package live

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestFreebsdTransitionFinishesOnlyTheExactPair(t *testing.T) {
	dir := t.TempDir()
	d, err := OpenDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	source := ".iprange-publish-04040404040404040404040404040404.tmp"
	sourceFile, err := d.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := RegularIdentityAnyLink(sourceFile, d.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(dir, source), filepath.Join(dir, "output.v4")); err != nil {
		t.Fatal(err)
	}
	retained, err := d.OpenRegularAnyLink("output.v4", false)
	if err != nil {
		t.Fatal(err)
	}
	if retained == nil {
		t.Fatal("open_regular_any_link reported the linked destination absent")
	}
	if retained.Identity != identity {
		t.Fatalf("retained identity = %+v, want %+v", retained.Identity, identity)
	}
	if err := d.finishNoreplaceTransition(source, "output.v4", identity); err != nil {
		t.Fatal(err)
	}
	if err := d.VerifyName("output.v4", identity); err != nil {
		t.Fatal(err)
	}
	if err := d.RequireAbsent(source); err != nil {
		t.Fatal(err)
	}
	// A completed transition is idempotent (Rust repeats the call).
	if err := d.finishNoreplaceTransition(source, "output.v4", identity); err != nil {
		t.Fatal(err)
	}
}

func TestFreebsdTransitionRejectsExtraOrForeignLinks(t *testing.T) {
	dir := t.TempDir()
	d, err := OpenDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	source := ".iprange-publish-05050505050505050505050505050505.tmp"
	sourceFile, err := d.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := RegularIdentityAnyLink(sourceFile, d.id)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"output.v4", "third"} {
		if err := os.Link(filepath.Join(dir, source), filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	err = d.finishNoreplaceTransition(source, "output.v4", identity)
	nerr, ok := AsNamespaceError(err)
	if !ok || nerr.Kind != NamespaceLinkCount || nerr.Links != 3 {
		t.Fatalf("transition error = %v, want LinkCount(3)", err)
	}
	for _, name := range []string{source, "output.v4", "third"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s disappeared after the rejected transition: %v", name, err)
		}
	}
}

func TestLinkNoReplaceMachinePublishesAndResumes(t *testing.T) {
	// Happy path: linkat, transition, private alias unlinked, single
	// destination link proven.
	t.Run("publish", func(t *testing.T) {
		dir := t.TempDir()
		d, err := OpenDirectory(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		source := ".iprange-publish-06060606060606060606060606060606.tmp"
		sourceFile, err := d.Create(source)
		if err != nil {
			t.Fatal(err)
		}
		identity, err := RegularIdentityAnyLink(sourceFile, d.id)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.linkNoReplace(source, sourceFile, "output.v4"); err != nil {
			t.Fatal(err)
		}
		if err := d.VerifyName("output.v4", identity); err != nil {
			t.Fatal(err)
		}
		if err := d.RequireAbsent(source); err != nil {
			t.Fatal(err)
		}
	})
	// Already-linked resume state: the linkat completed but the
	// process died before the alias unlink, so the source still names
	// the identity with two links. The machine refuses a fresh
	// linkNoReplace at require_source with the exact LinkCount class
	// (Rust link_noreplace require_source); the caller resumes through
	// finishNoreplaceTransition instead, exactly like Rust
	// file_inspection.rs.
	t.Run("linked pair refuses fresh link", func(t *testing.T) {
		dir := t.TempDir()
		d, err := OpenDirectory(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		source := ".iprange-publish-07070707070707070707070707070707.tmp"
		sourceFile, err := d.Create(source)
		if err != nil {
			t.Fatal(err)
		}
		identity, err := RegularIdentityAnyLink(sourceFile, d.id)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Link(filepath.Join(dir, source), filepath.Join(dir, "output.v4")); err != nil {
			t.Fatal(err)
		}
		err = d.linkNoReplace(source, sourceFile, "output.v4")
		nerr, ok := AsNamespaceError(err)
		if !ok || nerr.Kind != NamespaceLinkCount || nerr.Links != 2 {
			t.Fatalf("fresh link on linked pair = %v, want LinkCount(2)", err)
		}
		if err := d.finishNoreplaceTransition(source, "output.v4", identity); err != nil {
			t.Fatal(err)
		}
		if err := d.VerifyName("output.v4", identity); err != nil {
			t.Fatal(err)
		}
	})
}

func (d *Directory) entryIdentity(t *testing.T, name string) FileIdentity {
	t.Helper()
	entry, present, err := d.Entry(name)
	if err != nil || !present {
		t.Fatalf("entry(%s) = (%v, %v, %v)", name, entry, present, err)
	}
	return entry.Identity
}

// TestFreebsdSymlinkOpenFailsClosed pins the any-link open on a
// final-symlink name: the no-follow open reports NotRegular exactly
// like the Rust open_regular_any_link.
func TestFreebsdSymlinkOpenFailsClosed(t *testing.T) {
	dir := t.TempDir()
	d, err := OpenDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := os.WriteFile(filepath.Join(dir, "target"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.OpenRegularAnyLink("link", false); !errorsIsNamespace(err, NamespaceNotRegular) {
		t.Fatalf("open_regular_any_link(link) = %v, want NotRegular", err)
	}
	_ = unix.ELOOP // keep x/sys imported for the nofollow classification
}

func errorsIsNamespace(err error, kind NamespaceErrorKind) bool {
	nerr, ok := AsNamespaceError(err)
	return ok && nerr.Kind == kind
}
