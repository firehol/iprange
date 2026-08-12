package iprangedb

import (
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// Compile-time single-authority guards: the public deduplicated types are
// aliases of the internal/format authorities, so a second public
// implementation of either registry cannot compile. These declarations
// fail on any tree that redefines the public ErrorCode or Cardinality129
// as independent types (the assignment is only legal for identical,
// alias-linked types).
var (
	_ format.ErrorCode      = ErrorCode(0)
	_ format.Cardinality129 = CardinalityZero()
)

// TestNoObsoleteRetentionAPI pins binary-format-v4.md:311: the format
// defines no predefined "retention" tag (only first_seen and last_seen are
// special semantic tags, and there is no compatibility alias). The deleted
// RetentionTag symbol must never return in production sources; Go cannot
// express symbol absence in reflection, so this guard scans the module's
// non-test Go sources for the forbidden identifier.
func TestNoObsoleteRetentionAPI(t *testing.T) {
	needle := "Retention" + "Tag"
	lower := strings.ToLower(needle)
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		src := string(raw)
		if strings.Contains(src, needle) || strings.Contains(src, lower) {
			return fmt.Errorf("%s reintroduces the obsolete %s symbol", path, needle)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPublicSemanticFoundation(t *testing.T) {
	if AddressFamilyIPv4 != 4 || AddressFamilyIPv6 != 6 {
		t.Fatal("address-family registry drift")
	}
	if ValueKindDirect != 1 || ValueKindMembership != 2 || ValueKindStructured != 3 {
		t.Fatal("value-kind registry drift")
	}
	// Engine-defined direct semantics share the Rust numeric registry
	// (Generic=1, FirstSeen=2, LastSeen=3); zero is not a valid public value.
	if DirectSemanticGeneric != 1 || DirectSemanticFirstSeen != 2 || DirectSemanticLastSeen != 3 {
		t.Fatal("direct-semantic registry drift")
	}
	count, err := IPv6Inclusive(0, 0, math.MaxUint64, math.MaxUint64)
	if err != nil {
		t.Fatal(err)
	}
	if count != FullIPv6Space() {
		t.Fatalf("full IPv6 cardinality = %#v", count)
	}
	if ErrorInvalidArgument != 1 || ErrorCleanupInProgress != 64 || ErrorStructureIdExhausted != 69 {
		t.Fatalf("error registry endpoints = %d/%d/%d", ErrorInvalidArgument, ErrorCleanupInProgress, ErrorStructureIdExhausted)
	}
	// Engine-defined semantic tags and the 20 MiB metadata bound share the
	// Rust contract registries (contract.rs ValueTag::FIRST_SEEN/LAST_SEEN,
	// MAX_METADATA_UNCOMPRESSED).
	if ValueTagFirstSeen.Wire() != [16]byte{'f', 'i', 'r', 's', 't', '_', 's', 'e', 'e', 'n'} {
		t.Fatalf("first_seen tag wire = %q", ValueTagFirstSeen.Wire())
	}
	if ValueTagLastSeen.Wire() != [16]byte{'l', 'a', 's', 't', '_', 's', 'e', 'e', 'n'} {
		t.Fatalf("last_seen tag wire = %q", ValueTagLastSeen.Wire())
	}
	if MaxMetadataUncompressed != 20_971_520 {
		t.Fatalf("metadata limit = %d", MaxMetadataUncompressed)
	}
}
