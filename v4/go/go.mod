// Pure-Go reader/writer for the iprange v4 live mutable on-disk DB format.
// Cross-reads files written by the Rust reference (v4/rust/iprange-livedb); both pass
// the shared conformance corpus in ../conformance (§12). The non-os core is pure Go with
// no third-party dependencies; the optional Unix file layer (os.go) uses
// golang.org/x/sys (pure Go, no cgo) for flock/mmap.
module github.com/firehol/iprange/v4/go

go 1.23

require golang.org/x/sys v0.30.0
