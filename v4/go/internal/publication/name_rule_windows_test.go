//go:build windows

// Reserved Windows device-stem name rules (Rust path.rs
// validate_windows_name + is_windows_device_name). The stem before the
// first dot is compared ASCII-case-insensitively (Rust
// eq_ignore_ascii_case), so every mixed-case spelling of CON/PRN/AUX/NUL
// and COM1..9/LPT1..9 is refused; neighbors that merely share a prefix
// remain valid.

package publication

import "testing"

func TestWindowsDeviceStemSpellingsAreRefused(t *testing.T) {
	invalid := []string{
		"CON", "con", "Con", "cOn", "CON.v4", "con.v4", "Con.txt",
		"PRN", "prn", "Prn", "AUX", "aux", "AuX", "NUL", "nul", "NuL",
		"COM1", "com1", "Com9", "CoM4.v4", "LPT1", "lpt1", "LPT9", "LpT8.txt",
	}
	for _, name := range invalid {
		if !mainNameComponentRule(name) {
			t.Fatalf("reserved device spelling %q accepted", name)
		}
		if !invalidMainName(name) {
			t.Fatalf("reserved device spelling %q accepted as a main name", name)
		}
		if ValidDestinationName("C:" + name) {
			t.Fatalf("reserved device spelling %q accepted as a destination", name)
		}
	}
}

func TestWindowsDeviceStemNeighborsRemainValid(t *testing.T) {
	valid := []string{
		"console", "CONX", "con0", "com10", "com1x", "lpt0", "lpt10",
		"lpt1x", "main.iprdb", "CONSOLE.txt", "comms.iprdb",
	}
	for _, name := range valid {
		if mainNameComponentRule(name) {
			t.Fatalf("valid name %q rejected", name)
		}
		if invalidMainName(name) {
			t.Fatalf("valid main name %q rejected", name)
		}
	}
}
