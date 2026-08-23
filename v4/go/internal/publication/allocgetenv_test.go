//go:build never

package publication

// The v4work Getenv allocation probe is retired: os.Getenv allocates
// under the crash harness, so the allocation pin excludes v4work
// (attempt_alloc_test.go build tag) instead of tolerating the
// instrumentation cost.
