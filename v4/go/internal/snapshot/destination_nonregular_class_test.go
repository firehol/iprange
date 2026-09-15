//go:build unix

package snapshot

// Refusal-class pins for the destination probe of the live snapshot
// self-replacement check (Rust publication::namespace::unix
// Directory::open_regular through Destination::bind). The destination
// is classified by the open of the name, never by a path stat, and the
// order is what decides the class: a node the open itself refuses
// (AF_UNIX socket, ENXIO) is a filesystem failure, while a node the open
// returns and the descriptor then fails (directory, FIFO, symlink under
// O_NOFOLLOW) is the namespace non-regular class. A pre-open stat would
// report every one of them as the Conflict class and lose the io answer
// the reference implementation gives for the socket.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/fslocal"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// nonRegularDestination runs the destination probe against one name the
// test materializes, returning the class it reported.
func nonRegularDestination(t *testing.T, materialize func(t *testing.T, path string)) *format.Error {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "archive.iprange")
	materialize(t, target)
	err := rejectLiveSelf(probeSource{device: 1, inode: 1}, SourceLive, target,
		publication.PolicyReplaceExisting)
	if err == nil {
		t.Fatalf("%s destination was accepted", filepath.Base(target))
	}
	var fe *format.Error
	if !errors.As(err, &fe) {
		t.Fatalf("%v is not a typed v4 error", err)
	}
	return fe
}

// An AF_UNIX socket destination is refused by the open itself, so the
// answer is the io class (Rust NamespaceError::IoAt of
// open_regular_with_links, folded by Problem::namespace).
func TestRejectLiveSelfSocketDestinationIsIo(t *testing.T) {
	fe := nonRegularDestination(t, bindUnixSocket)
	if fe.Code != format.CodeIO {
		t.Fatalf("code = %v (%v), want %v", fe.Code, fe.Detail, format.CodeIO)
	}
}

// The genuine name-collision refusals must stay the Conflict class: a
// directory or FIFO opens and the descriptor is then refused as not a
// regular file, and a symlink is refused for the link under O_NOFOLLOW
// (Rust is_nofollow_symlink folds it into the same NotRegular arm).
func TestRejectLiveSelfCollisionDestinationsStayConflict(t *testing.T) {
	materializeDirectory := func(t *testing.T, path string) {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	materializeFifo := func(t *testing.T, path string) {
		if err := syscallMkfifo(path); err != nil {
			t.Skipf("FIFOs are unavailable: %v", err)
		}
	}
	materializeSymlink := func(t *testing.T, path string) {
		real := filepath.Join(filepath.Dir(path), "elsewhere.iprange")
		if err := os.WriteFile(real, make([]byte, 512), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, path); err != nil {
			t.Skipf("symlinks are unavailable: %v", err)
		}
	}
	for name, materialize := range map[string]func(t *testing.T, path string){
		"directory": materializeDirectory,
		"fifo":      materializeFifo,
		"symlink":   materializeSymlink,
	} {
		fe := nonRegularDestination(t, materialize)
		if fe.Code != format.CodeConflict {
			t.Errorf("%s destination: code = %v (%v), want %v", name, fe.Code, fe.Detail, format.CodeConflict)
		}
	}
}

// A missing destination is not a rejection of this probe at all: the
// attempt creation reports it with the exact publication class (Rust
// Directory::open_regular reports Ok(None)).
func TestRejectLiveSelfMissingDestinationIsNotARejection(t *testing.T) {
	dir := t.TempDir()
	if err := rejectLiveSelf(probeSource{device: 1, inode: 1}, SourceLive,
		filepath.Join(dir, "absent.iprange"), publication.PolicyReplaceExisting); err != nil {
		t.Fatalf("absent destination reported %v, want acceptance", err)
	}
}

// ---------------------------------------------------------------------------
// Destination parent filesystem.
//
// The destination probe must prove the durability contract of the
// destination's own parent before it classifies the node sitting at the
// destination name, because Rust Destination::bind takes the parent
// identity through Directory::open, which runs require_local_filesystem
// inside that open (publication::namespace::unix). The order is the whole
// contract: a node the open can classify would otherwise answer the
// namespace collision class (directory, FIFO, symlink, character device)
// or the io class of a socket open on a filesystem the replacement
// exchange can never be durable on, downgrading the refusal that
// binary-format-v4.md mandates for replacement publication ("a platform
// or filesystem without exchange returns DurabilityUnsupported before
// output construction and never downgrades").
//
// A character device cannot be created by an unprivileged test user
// (mknod needs CAP_MKNOD), so this file pins that class with the device
// nodes every Linux system already carries: /dev/null and /dev/zero sit
// under devtmpfs, which the durability proof refuses, which is exactly
// the pair the ordering decides.
// ---------------------------------------------------------------------------

// destinationShape is one node the destination probe can reach, with how
// to materialize it. The absent name and a plain regular file are
// included because the durability refusal must win over them too: they
// are the shapes the probe accepts on a qualified filesystem.
type destinationShape struct {
	name        string
	materialize func(t *testing.T, path string)
}

func destinationShapes() []destinationShape {
	return []destinationShape{
		{"absent", func(*testing.T, string) {}},
		{"regular", writeDestinationFile},
		{"directory", func(t *testing.T, path string) { mustMakeNode(t, os.Mkdir(path, 0o700)) }},
		{"fifo", func(t *testing.T, path string) { mustMakeNode(t, syscallMkfifo(path)) }},
		{"socket", bindUnixSocket},
		{"symlink", makeSymlinkDestination},
		{"chardev", func(t *testing.T, path string) { mustMakeNode(t, syscallMknodChardev(path)) }},
	}
}

// existingUnqualifiedNodes are namespace nodes the test cannot create but
// can name: each one's parent filesystem is outside the durability
// whitelist on every Linux host.
func existingUnqualifiedNodes() []string {
	return []string{"/dev/null", "/dev/zero"}
}

func writeDestinationFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, 512), 0o600); err != nil {
		t.Fatal(err)
	}
}

func makeSymlinkDestination(t *testing.T, path string) {
	t.Helper()
	real := filepath.Join(filepath.Dir(path), "elsewhere.iprange")
	if err := os.WriteFile(real, make([]byte, 512), 0o600); err != nil {
		t.Fatal(err)
	}
	mustMakeNode(t, os.Symlink(real, path))
}

// mustMakeNode materializes one shape, skipping that cell when the host
// refuses to create it: an unprivileged user has no CAP_MKNOD, and some
// configurations refuse FIFOs or AF_UNIX nodes.
func mustMakeNode(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Skipf("cannot materialize the destination shape: %v", err)
	}
}

// directoryIsQualified asks the engine's own durability predicate about
// one directory, so this file decides its parent by the same rule the
// product applies instead of by a hardcoded filesystem name.
func directoryIsQualified(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	err = fslocal.RequireLocal(f)
	if errors.Is(err, fslocal.ErrNotLocal) {
		return false
	}
	if err != nil {
		t.Fatalf("durability probe of %s: %v", path, err)
	}
	return true
}

// qualifiedParent returns a directory the durability proof accepts, so
// the per-node class pins measure the node and not the filesystem. It
// fails when the host offers none: without that control the durability
// cells above could pass by refusing every destination.
func qualifiedParent(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if !directoryIsQualified(t, dir) {
		t.Fatal("the test filesystem is refused the durability contract, so the destination class pins cannot distinguish the node from the filesystem")
	}
	return dir
}

// unqualifiedParent returns a fresh writable directory the durability
// proof refuses, or "" when the host offers none. The name is kept short
// because an AF_UNIX socket address cannot exceed the sun_path limit.
func unqualifiedParent(t *testing.T) string {
	t.Helper()
	for _, base := range []string{"/dev/shm", os.TempDir()} {
		dir := filepath.Join(base, "ipr-w1925-d")
		if err := os.RemoveAll(dir); err != nil {
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			continue
		}
		if !directoryIsQualified(t, dir) {
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			return dir
		}
		_ = os.RemoveAll(dir)
	}
	return ""
}

// destinationProbe runs the destination probe against one name under one
// parent and returns the typed refusal, or nil when the probe accepted
// the destination.
func destinationProbe(t *testing.T, parent, name string, materialize func(t *testing.T, path string)) *format.Error {
	t.Helper()
	target := filepath.Join(parent, name)
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target + ".real"); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	materialize(t, target)
	err := rejectLiveSelf(probeSource{device: 1, inode: 1}, SourceLive, target,
		publication.PolicyReplaceExisting)
	if err == nil {
		return nil
	}
	var fe *format.Error
	if !errors.As(err, &fe) {
		t.Fatalf("%v is not a typed v4 error", err)
	}
	return fe
}

// probeNamedDestination runs the probe against one node that already
// exists in the namespace, which is how the character-device class is
// pinned on a host where the test user cannot create one.
func probeNamedDestination(t *testing.T, target string) *format.Error {
	t.Helper()
	err := rejectLiveSelf(probeSource{device: 1, inode: 1}, SourceLive, target,
		publication.PolicyReplaceExisting)
	if err == nil {
		return nil
	}
	var fe *format.Error
	if !errors.As(err, &fe) {
		t.Fatalf("%v is not a typed v4 error", err)
	}
	return fe
}

// A destination on a filesystem the durability proof refuses answers the
// durability class at every node shape. Reverting directoryIdentityOf to
// a plain path stat (the order this fix removes) turns each of these
// cells into that node's own class and fails here.
func TestRejectLiveSelfDestinationOnUnqualifiedFilesystemIsDurabilityUnsupported(t *testing.T) {
	parent := unqualifiedParent(t)
	if parent == "" {
		t.Skip("no writable filesystem the durability proof refuses")
	}
	for _, shape := range destinationShapes() {
		if shape.name == "absent" || shape.name == "regular" {
			continue // the shapes the qualified-parent control pins below
		}
		t.Run(shape.name, func(t *testing.T) {
			fe := destinationProbe(t, parent, shape.name+".iprange", shape.materialize)
			if fe == nil {
				t.Fatalf("%s destination on an unqualified filesystem was accepted", shape.name)
			}
			if fe.Code != format.CodeDurabilityUnsupported {
				t.Fatalf("%s destination on an unqualified filesystem: code = %v (%v), want %v",
					shape.name, fe.Code, fe.Detail, format.CodeDurabilityUnsupported)
			}
		})
	}
}

// The character-device class, pinned through nodes the host already
// provides: before this fix the open of /dev/null succeeded and the
// descriptor was refused as not a regular file, so the answer was the
// namespace collision class on a filesystem the exchange cannot be
// durable on.
func TestRejectLiveSelfCharacterDeviceDestinationIsDurabilityUnsupported(t *testing.T) {
	for _, target := range existingUnqualifiedNodes() {
		info, err := os.Stat(target)
		if err != nil || info.Mode()&os.ModeDevice == 0 {
			t.Skipf("%s is not a character device here", target)
		}
		if directoryIsQualified(t, filepath.Dir(target)) {
			t.Skipf("%s sits on a qualified filesystem here, so it cannot pin the order", target)
		}
		fe := probeNamedDestination(t, target)
		if fe == nil {
			t.Fatalf("%s destination was accepted", target)
		}
		if fe.Code != format.CodeDurabilityUnsupported {
			t.Errorf("%s destination: code = %v (%v), want %v",
				target, fe.Code, fe.Detail, format.CodeDurabilityUnsupported)
		}
	}
}

// The same shapes on a qualified filesystem keep the per-node classes the
// probe owns. This is the control that keeps the durability refusal above
// from being satisfied by refusing every destination.
func TestRejectLiveSelfDestinationClassesOnQualifiedFilesystem(t *testing.T) {
	parent := qualifiedParent(t)
	want := map[string]format.ErrorCode{
		"absent":    0, // Rust Directory::open_regular reports Ok(None)
		"regular":   0, // a different inode is not a self-replacement
		"directory": format.CodeConflict,
		"fifo":      format.CodeConflict,
		"socket":    format.CodeIO,
		"symlink":   format.CodeConflict,
		"chardev":   format.CodeConflict,
	}
	for _, shape := range destinationShapes() {
		t.Run(shape.name, func(t *testing.T) {
			fe := destinationProbe(t, parent, shape.name+".iprange", shape.materialize)
			want := want[shape.name]
			if want == 0 {
				if fe != nil {
					t.Fatalf("%s destination was refused %v (%v), want acceptance",
						shape.name, fe.Code, fe.Detail)
				}
				return
			}
			if fe == nil {
				t.Fatalf("%s destination was accepted, want %v", shape.name, want)
			}
			if fe.Code != want {
				t.Fatalf("%s destination: code = %v (%v), want %v",
					shape.name, fe.Code, fe.Detail, want)
			}
		})
	}
}

// The absent and plain-regular destinations must still reach the
// durability refusal on an unqualified filesystem: they are the shapes
// the node classification accepts, so the only thing that can refuse
// them is the parent proof, and the pairing with a qualified work
// directory is what the finding describes.
func TestRejectLiveSelfPlainDestinationOnUnqualifiedFilesystemIsDurabilityUnsupported(t *testing.T) {
	parent := unqualifiedParent(t)
	if parent == "" {
		t.Skip("no writable filesystem the durability proof refuses")
	}
	for _, shape := range destinationShapes() {
		if shape.name != "absent" && shape.name != "regular" {
			continue
		}
		t.Run(shape.name, func(t *testing.T) {
			fe := destinationProbe(t, parent, shape.name+".iprange", shape.materialize)
			if fe == nil || fe.Code != format.CodeDurabilityUnsupported {
				t.Fatalf("%s destination on an unqualified filesystem = %v, want %v",
					shape.name, fe, format.CodeDurabilityUnsupported)
			}
		})
	}
}
