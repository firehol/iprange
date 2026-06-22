// Pure-Go reader/writer for the iprange v3 binary threat-intel format.
// Byte-identical to the Rust reference (v3/rust/iprange-format); both pass the shared
// conformance corpus in ../conformance. Core has no third-party dependencies; the
// optional mmap reader (added later) uses golang.org/x/sys (pure Go, no cgo).
module github.com/firehol/iprange/v3/go

go 1.23
