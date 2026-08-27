// Public validated feed names (Rust feed::FeedName parity): the exact
// v4 name grammar is 1..255 lowercase ASCII letters and digits with
// '_', '-' and '.' allowed inside; the first and last characters must
// be alphanumeric. The Go binding is a validated string value: Go
// strings are immutable and comparable, so the fixed 255-byte copy of
// the Rust value type maps onto a string without losing the
// copy-by-value semantics.

package iprangedb

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// FeedName is one validated structural feed name. The zero value is
// invalid; construct names with NewFeedName.
type FeedName string

// NewFeedName validates one feed name and returns its value (Rust
// FeedName::new; the invalid class is ErrorNameInvalid).
func NewFeedName(name string) (FeedName, error) {
	if !format.FeedNameValidString(name) {
		return "", &Error{Code: ErrorNameInvalid, Detail: "feed name is invalid"}
	}
	return FeedName(name), nil
}

// String returns the exact name (Rust FeedName::as_str).
func (n FeedName) String() string { return string(n) }
