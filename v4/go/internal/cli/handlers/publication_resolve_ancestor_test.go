package handlers

import (
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// publication.resolve with an ancestor directory that does not exist
// reports the canonical missing-name class (Rust publication resolver:
// NamespaceError::Missing folds through namespace_error to
// Error::NameNotFound). A retained-directory error that reached the
// public boundary unfolded is untyped there, and the wire adapter can
// only classify typed SDK errors, so it would degrade the refusal to
// the generic io code.
func TestPublicationResolveMissingAncestorIsNameNotFound(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent-ancestor", "out.iprange")
	st := rpc.NewSessionState()
	_, herr := PublicationResolve(st, mustJSON(t, map[string]any{
		"path":            missing,
		"resolution_mode": "complete",
	}))
	if herr == nil {
		t.Fatal("publication.resolve accepted a missing ancestor directory")
	}
	if herr.Code != "name_not_found" {
		t.Fatalf("publication.resolve missing ancestor code = %q, want name_not_found", herr.Code)
	}
}

// The same arm with an existing parent and only the leaf missing stays
// unresolvable, so the fold must not turn every missing name into the
// missing-name class.
func TestPublicationResolveMissingLeafStaysUnresolvable(t *testing.T) {
	leaf := filepath.Join(t.TempDir(), "absent.iprange")
	st := rpc.NewSessionState()
	_, herr := PublicationResolve(st, mustJSON(t, map[string]any{
		"path":            leaf,
		"resolution_mode": "complete",
	}))
	if herr == nil {
		t.Fatal("publication.resolve accepted a missing leaf")
	}
	if herr.Code == "io" {
		t.Fatalf("publication.resolve missing leaf degraded to io: %+v", herr)
	}
}
