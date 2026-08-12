// The v4 mmap gate scanner is a standalone stdlib-only tool: it must not
// share the v4/go module (its own source would otherwise be scanned by the
// very gate it implements).
module github.com/firehol/iprange/v4/go-gate

go 1.23.0
