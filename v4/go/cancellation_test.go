package iprangedb

import "testing"

// TestNilCancellationHookIsNil pins the nil-token checkpoint invariant:
// a nil token must yield a nil hook. A method value over a nil receiver
// is itself non-nil, and worker sessions treat any non-nil hook as an
// external cancellation poll (Rust CancellationToken::
// requires_external_poll), turning every validation checkpoint into a
// parent round trip and inflating validation ~200-340x. Regression for
// SOW-0027 milestone 4 (bench matrix exposed the slowdown).
func TestNilCancellationHookIsNil(t *testing.T) {
	var token *CancellationToken
	if token.hook() != nil {
		t.Fatal("nil token produced a non-nil checkpoint hook")
	}
	token = NewCancellationToken()
	if token.hook() == nil {
		t.Fatal("active token produced a nil checkpoint hook")
	}
	if err := token.hook()(); err != nil {
		t.Fatalf("uncancelled token checkpoint failed: %v", err)
	}
	token.Cancel()
	if err := token.hook()(); err == nil {
		t.Fatal("cancelled token checkpoint did not report cancellation")
	}
}
