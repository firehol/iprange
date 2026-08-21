package iprangedb

// Cursor constructor gate regressions (Rust generation.rs
// require_direct/require_membership_family parity): the kind and family
// pre-checks fire at cursor open with the typed refusal classes, before
// any page is touched.

import "testing"

func TestDirectCursorKindGate(t *testing.T) {
	// A membership database is not direct: the cursor must refuse with
	// WrongValueKind (Rust public path Database::direct_cursor_v4 ->
	// generation.rs require_direct: "direct lookup requires a
	// direct-value database"), never return a cursor that decodes
	// membership IDs as values.
	db := mustOpen(t, "rust/membership-ipv4.iprdb")
	defer db.Close()
	for _, open := range []struct {
		name string
		fn   func() error
	}{
		{"v4", func() error { _, err := db.DirectCursorV4(RangeDirectionForward); return err }},
		{"v6", func() error { _, err := db.DirectCursorV6(RangeDirectionForward); return err }},
	} {
		if err := open.fn(); err == nil {
			t.Fatalf("%s: direct cursor opened on a membership database", open.name)
		} else if code := errorCode(err); code != ErrorWrongValueKind {
			t.Fatalf("%s: direct cursor on membership class = %d, want WrongValueKind (%v)", open.name, code, err)
		}
	}
}

func TestDirectCursorFamilyGate(t *testing.T) {
	// Kind is right but the family is wrong: WrongAddressFamily (Rust
	// require_direct family check after the kind check).
	db := mustOpen(t, "rust/direct-ipv4.iprdb")
	defer db.Close()
	if _, err := db.DirectCursorV6(RangeDirectionForward); err == nil {
		t.Fatal("DirectCursorV6 opened on an IPv4 direct database")
	} else if code := errorCode(err); code != ErrorWrongAddressFamily {
		t.Fatalf("class = %d, want WrongAddressFamily (%v)", code, err)
	}
}

func TestFeedRangeCursorFamilyGate(t *testing.T) {
	// Membership-capable kind with the wrong address family: the
	// constructor refuses with WrongAddressFamily (Rust
	// require_membership_family), instead of failing later with an
	// internal decode error.
	db := mustOpen(t, "rust/membership-ipv6.iprdb")
	defer db.Close()
	if _, err := db.FeedRangeCursorV4("global", RangeDirectionForward); err == nil {
		t.Fatal("FeedRangeCursorV4 opened on an IPv6 membership database")
	} else if code := errorCode(err); code != ErrorWrongAddressFamily {
		t.Fatalf("class = %d, want WrongAddressFamily (%v)", code, err)
	}
	db4 := mustOpen(t, "rust/membership-ipv4.iprdb")
	defer db4.Close()
	if _, err := db4.FeedRangeCursorV6("global", RangeDirectionForward); err == nil {
		t.Fatal("FeedRangeCursorV6 opened on an IPv4 membership database")
	} else if code := errorCode(err); code != ErrorWrongAddressFamily {
		t.Fatalf("v6 class = %d, want WrongAddressFamily (%v)", code, err)
	}
}

func TestFeedRangeCursorKindGate(t *testing.T) {
	// A direct database is not membership-capable: WrongValueKind at
	// open, before the feed lookup.
	db := mustOpen(t, "rust/direct-ipv4.iprdb")
	defer db.Close()
	if _, err := db.FeedRangeCursorV4("global", RangeDirectionForward); err == nil {
		t.Fatal("FeedRangeCursorV4 opened on a direct database")
	} else if code := errorCode(err); code != ErrorWrongValueKind {
		t.Fatalf("class = %d, want WrongValueKind (%v)", code, err)
	}
}
