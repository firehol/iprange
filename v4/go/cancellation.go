// Cancellation shared with bounded logical operations (Rust
// cancellation.rs parity). Checkpoints between bounded units of work
// report ErrorCancelled once Cancel has been requested; repeated Cancel
// calls are harmless.

package iprangedb

import "sync/atomic"

// CancellationToken is one shared cancellation flag. A nil token means
// no cancellation source: operations run uncancellable.
type CancellationToken struct {
	cancelled atomic.Bool
}

// NewCancellationToken returns an active token.
func NewCancellationToken() *CancellationToken {
	return &CancellationToken{}
}

// Cancel requests cancellation. Repeated requests are harmless.
func (t *CancellationToken) Cancel() {
	t.cancelled.Store(true)
}

// IsCancelled reports whether cancellation was requested.
func (t *CancellationToken) IsCancelled() bool {
	return t.cancelled.Load()
}

// check reports ErrorCancelled when cancellation was requested; it is the
// bounded-operation checkpoint hook. A nil token is the uncancellable
// checkpoint: the method value passes a nil receiver and reports nothing.
func (t *CancellationToken) check() error {
	if t == nil {
		return nil
	}
	if t.IsCancelled() {
		return &Error{Code: ErrorCancelled, Detail: "operation was cancelled"}
	}
	return nil
}
