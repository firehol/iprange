//go:build v4work

package fault

import (
	"errors"
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

// Fail returns a typed error when the environment names this fault point,
// mirroring Crash for non-fatal state-machine tests: the caller observes
// the exact failure at the exact physical step and continues, so the
// unresolved-outcome behavior can be asserted in the same process. The
// arming variable is consumed on the first fire, so a retried operation
// later in the same process runs clean (one physical step, one fault).
func Fail(point string) error {
	if os.Getenv("IPRANGE_V4_TEST_FAIL_AT") == point {
		os.Unsetenv("IPRANGE_V4_TEST_FAIL_AT")
		return errors.New("injected fault: " + point)
	}
	return nil
}
