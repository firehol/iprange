// Command iprange-v4-bench ports the Rust update-ipsets benchmark matrix
// (v4/rust/iprange-livedb/benches/update_ipsets) to the public Go SDK.
//
// Modes (mirroring the Rust driver): smoke, scale, local, ci, sample,
// case, and header. Every case runs in a fresh subprocess so RSS, fd,
// and allocation facts stay isolated, exactly like the Rust harness.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func main() {
	arguments := os.Args[1:]
	if len(arguments) > 0 && arguments[0] == "--bench" {
		arguments = arguments[1:]
	}
	var err error
	switch {
	case len(arguments) == 0 || arguments[0] == "smoke":
		err = runMatrix(smokeCases())
	case arguments[0] == "scale":
		err = runMatrix(scaleCases())
	case arguments[0] == "local":
		err = runRepeated(scaleCases(), 1, 5, false)
	case arguments[0] == "ci":
		err = runRepeated(ciCases(), 1, 3, true)
	case arguments[0] == "sample":
		err = runSample(arguments)
	case arguments[0] == "case":
		err = runCase(arguments)
	case arguments[0] == "header":
		fmt.Println(csvHeader)
	default:
		err = fmt.Errorf("unknown mode %q; expected smoke, scale, local, ci, sample, or case", arguments[0])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "update-ipsets benchmark failed:", err)
		os.Exit(1)
	}
}

func runSample(arguments []string) error {
	if len(arguments) < 4 || len(arguments) > 5 {
		return fmt.Errorf("sample requires: sample SCENARIO SIZE AUX [SAMPLES]")
	}
	size, err := strconv.Atoi(arguments[2])
	if err != nil {
		return fmt.Errorf("invalid size %q", arguments[2])
	}
	aux, err := strconv.Atoi(arguments[3])
	if err != nil {
		return fmt.Errorf("invalid auxiliary value %q", arguments[3])
	}
	samples := 5
	if len(arguments) == 5 {
		samples, err = strconv.Atoi(arguments[4])
		if err != nil {
			return fmt.Errorf("invalid sample count %q", arguments[4])
		}
	}
	return runRepeated([]Case{{Name: arguments[1], Size: size, Auxiliary: aux}}, 1, samples, false)
}

// caseArg renders the case descriptor into one stable argument so
// repeated sampling and matrix runs reuse identical subprocess invocations.
func caseArg(c Case) string {
	return strings.Join([]string{"case", c.Name, strconv.Itoa(c.Size), strconv.Itoa(c.Auxiliary)}, " ")
}
