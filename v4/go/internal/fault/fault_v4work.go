//go:build v4work

package fault

import (
	"os"
)

// Crash exits the process with code 86 when the environment names this
// crash point, exactly like Rust fault.rs under #[cfg(test)]: the child
// process dies at the precise physical step of the publication sequence.
func Crash(point string) {
	if os.Getenv("IPRANGE_V4_TEST_CRASH_AT") == point {
		os.Exit(86)
	}
}
