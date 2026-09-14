package fslocal

import "errors"

// ErrNotLocal reports a filesystem whose durability semantics the live
// contract cannot use (Rust NamespaceError::Unsupported). Callers fold
// it into their own class: the namespace machine reports Unsupported,
// the public wire reports durability_unsupported.
var ErrNotLocal = errors.New("live file namespace lacks required local operations")
