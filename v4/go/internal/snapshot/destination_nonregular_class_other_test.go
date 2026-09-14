//go:build !windows

package snapshot

import "os"

// osRemove releases one name the test materialized.
func osRemove(path string) error { return os.Remove(path) }
