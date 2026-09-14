// Package fslocal owns the durability proof of one open directory:
// whether the filesystem behind it supports the namespace operations
// the live contract needs (same-directory atomic exchange, parent
// synchronization, a proven name_max).
//
// Rust publishes this predicate once (publication::namespace::unix
// require_local_filesystem, with the Windows arm require_local_ntfs)
// and every directory bind in the engine reaches it through
// Directory::open. Go has two binds — the retained-directory machine of
// internal/live and the namespace proof of internal/mapping, which
// internal/live also imports — so the predicate lives below both and
// each caller folds the answer into its own error vocabulary.
package fslocal
