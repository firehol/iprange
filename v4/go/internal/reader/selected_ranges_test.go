//go:build v4work

package reader

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// TestSelectedRunsCoverage verifies that the selected-runs scan emits
// every physical membership range exactly once for both the all-catalog
// (no lookahead) and fully-named (lookahead) scope forms: a regression
// guard for the 4c pointer-alias defect that dropped and duplicated runs
// on the pending path and allocated one heap object per emitted run.
func TestSelectedRunsCoverage(t *testing.T) {
	path := copyFixture(t, "membership-ipv4.iprdb", "probe-runscan.iprdb")
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// Physical membership intervals of the fixture: one membership range
	// record per catalog feed.
	physical, err := r.membershipPhysicalSum()
	if err != nil {
		t.Fatal(err)
	}
	for _, named := range []bool{false, true} {
		var scope *ScopeData
		if named {
			scope, err = r.ResolveNamedFeeds(allProbeNames(r), 1<<30, nil)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			scope, err = r.ResolveAllFeeds(1<<30, nil)
			if err != nil {
				t.Fatal(err)
			}
		}
		cursor, err := r.NewMembershipRangeCursor4()
		if err != nil {
			t.Fatal(err)
		}
		stream := &membershipIterator{cursor: cursor.state, family: format.AddressFamilyIPv4}
		sel, err := newSelectedRanges(r, scope, stream, ops4, newOperationHeap(1<<30))
		if err != nil {
			t.Fatal(err)
		}
		var runs uint64
		var emitted uint64
		var previous addrKey
		havePrevious := false
		for {
			run, ok, err := sel.next(nil)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				break
			}
			runs++
			emitted += 1 + addrU64(run.to) - addrU64(run.from)
			if havePrevious && !(previous.Less(run.from)) {
				t.Fatalf("named=%v overlapping runs: previous to=%v run from=%v", named, previous, run.from)
			}
			previous = run.to
			havePrevious = true
		}
		if emitted != physical {
			t.Fatalf("named=%v runs=%d emitted-addresses=%d physical-addresses=%d", named, runs, emitted, physical)
		}
	}
}

// membershipPhysicalSum returns the inclusive address count of every
// physical membership interval of the fixture.
func (r *ImmutableReader) membershipPhysicalSum() (uint64, error) {
	cursor, err := r.NewMembershipRangeCursor4()
	if err != nil {
		return 0, err
	}
	var sum uint64
	for {
		rec, ok, err := cursor.Next()
		if err != nil {
			return 0, err
		}
		if !ok {
			break
		}
		sum += 1 + uint64(rec.To) - uint64(rec.From)
	}
	return sum, nil
}

func addrU64(k addrKey) uint64 { return uint64(k.hi)<<32 | uint64(k.lo) }

func allProbeNames(r *ImmutableReader) []string {
	names := make([]string, 0, r.meta.ActiveFeedCount)
	cursor, err := r.NewFeedCursor()
	if err != nil {
		panic(err)
	}
	for {
		entry, ok, err := cursor.Next()
		if err != nil {
			panic(err)
		}
		if !ok {
			break
		}
		names = append(names, string(entry.Name))
	}
	return names
}
