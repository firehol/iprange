//go:build windows

// Live main-name component rules (Rust path.rs validate_main_name on
// Windows): every mixed-case reserved device stem is refused with the
// Rust reserved-spelling detail, and prefix neighbors remain valid.

package live

import "testing"

func TestCoordinationNameRuleWindowsDeviceSpellings(t *testing.T) {
	invalid := []string{
		"CON", "con", "Con.v4", "PRN", "prn", "AUX", "aux.v4",
		"NUL", "nul", "COM1", "com9", "CoM3.txt", "LPT1", "lpt9",
	}
	for _, name := range invalid {
		if detail := coordinationNameRule(name); detail != "database file name uses a reserved Windows spelling" {
			t.Fatalf("reserved spelling %q detail = %q", name, detail)
		}
	}
	valid := []string{
		"console", "CONX", "com10", "lpt0", "main.iprdb", "comms.iprdb",
	}
	for _, name := range valid {
		if detail := coordinationNameRule(name); detail != "" {
			t.Fatalf("valid name %q rejected: %q", name, detail)
		}
	}
}
