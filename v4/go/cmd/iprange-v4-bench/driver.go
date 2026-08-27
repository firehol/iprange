// Case matrices and in-process/proc CSV plumbing (Rust
// benches/update_ipsets/driver.rs): the exact smoke, scale, and CI case
// lists, plus the matrix runner that isolates each case in one
// subprocess of this same executable.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime/pprof"
	"strconv"
	"strings"
)

const csvHeader = "scenario,size,aux,work_units,emitted_units,elapsed_ns,units_per_second,alloc_calls,alloc_bytes,rss_before_kib,rss_after_kib,rss_peak_kib,fds_before,fds_after,file_logical_bytes,file_physical_bytes,range_records,feeds,private_artifacts"

type Case struct {
	Name      string
	Size      int
	Auxiliary int
}

func caseOf(name string, size, auxiliary int) Case {
	return Case{Name: name, Size: size, Auxiliary: auxiliary}
}

// runMatrix prints the header and one CSV row per case, each measured in
// a fresh subprocess (Rust run_matrix).
func runMatrix(cases []Case) error {
	fmt.Println(csvHeader)
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	for _, c := range cases {
		output, err := exec.Command(executable, "case", c.Name, strconv.Itoa(c.Size), strconv.Itoa(c.Auxiliary)).CombinedOutput()
		if err != nil {
			return fmt.Errorf("start %s: %v: %s", c.Name, err, strings.TrimSpace(string(output)))
		}
		fmt.Print(string(output))
	}
	return nil
}

// runCase runs one scenario in-process and prints its single CSV row
// (Rust driver::run_case, consumed by matrix and sampling parents).
func runCase(arguments []string) error {
	if len(arguments) != 4 {
		return fmt.Errorf("case requires: case SCENARIO SIZE AUX")
	}
	size, err := strconv.Atoi(arguments[2])
	if err != nil {
		return fmt.Errorf("invalid size %q", arguments[2])
	}
	aux, err := strconv.Atoi(arguments[3])
	if err != nil {
		return fmt.Errorf("invalid auxiliary value %q", arguments[3])
	}
	// IPRANGE_CPU_PROFILE writes one pprof CPU profile of the measured
	// scenario for the performance-delta work (bench-only tooling; the
	// profile runs while the case child performs its own work).
	if profile := os.Getenv("IPRANGE_CPU_PROFILE"); profile != "" {
		file, err := os.Create(profile)
		if err != nil {
			return err
		}
		if err := pprof.StartCPUProfile(file); err != nil {
			_ = file.Close()
			return err
		}
		result, err := dispatchScenario(arguments[1], size, aux)
		pprof.StopCPUProfile()
		_ = file.Close()
		if err != nil {
			return err
		}
		fmt.Println(result.csvLine())
		return nil
	}
	result, err := dispatchScenario(arguments[1], size, aux)
	if err != nil {
		return err
	}
	fmt.Println(result.csvLine())
	return nil
}

func (r *scenarioResult) csvLine() string {
	elapsedNs := uint64(r.Measurement.elapsed.Nanoseconds())
	rate := 0.0
	if elapsedNs != 0 {
		rate = float64(r.WorkUnits) * 1_000_000_000.0 / float64(elapsedNs)
	}
	return fmt.Sprintf("%s,%d,%d,%d,%d,%d,%.3f,%d,%d,%s,%s,%s,%s,%s,%d,%s,%d,%d,%d",
		r.Name,
		r.Size,
		r.Auxiliary,
		r.WorkUnits,
		r.EmittedUnits,
		elapsedNs,
		rate,
		r.Measurement.allocations.calls,
		r.Measurement.allocations.bytes,
		optionalUint(r.Measurement.rssBeforeKib),
		optionalUint(r.Measurement.rssAfterKib),
		optionalUint(r.Measurement.rssPeakKib),
		optionalUint(r.Measurement.fdsBefore),
		optionalUint(r.Measurement.fdsAfter),
		r.File.logical,
		optionalUint(r.File.physical),
		r.RangeRecords,
		r.Feeds,
		r.PrivateArtifacts,
	)
}

func optionalUint(value *uint64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatUint(*value, 10)
}

func smokeCases() []Case {
	return []Case{
		caseOf("direct-replace", 1_000, 0),
		caseOf("direct-replace", 4_000, 0),
		caseOf("direct-replace-v6", 4_000, 0),
		caseOf("direct-commit", 4_000, 0),
		caseOf("nested-overwrite", 1_000, 0),
		caseOf("nested-overwrite", 4_000, 0),
		caseOf("first-seen-refresh", 1_000, 0),
		caseOf("first-seen-refresh", 4_000, 0),
		caseOf("last-seen-refresh", 1_000, 0),
		caseOf("last-seen-refresh", 4_000, 0),
		caseOf("feed-replace", 1_000, 8),
		caseOf("feed-replace", 1_000, 64),
		caseOf("membership-import", 1_000, 64),
		caseOf("live-membership-lookup", 4_000, 64),
		caseOf("immutable-membership-lookup", 4_000, 64),
		caseOf("live-membership-random-lookup", 4_000, 64),
		caseOf("immutable-membership-random-lookup", 4_000, 64),
		caseOf("live-feed-scan", 4_000, 64),
		caseOf("immutable-feed-scan", 4_000, 64),
		caseOf("live-direct-lookup", 4_000, 0),
		caseOf("immutable-direct-lookup", 4_000, 0),
		caseOf("live-direct-random-lookup", 4_000, 0),
		caseOf("immutable-direct-random-lookup", 4_000, 0),
		caseOf("structured-build-random", 4_000, 64),
		caseOf("structured-intern", 4_000, 64),
		caseOf("structured-assign-random", 4_000, 64),
		caseOf("structured-commit", 4_000, 64),
		caseOf("live-structured-scalar-random-lookup", 4_000, 64),
		caseOf("immutable-structured-scalar-random-lookup", 4_000, 64),
		caseOf("live-structured-threat-random-lookup", 4_000, 64),
		caseOf("immutable-structured-threat-random-lookup", 4_000, 64),
		caseOf("live-structured-scalar-scan", 4_000, 64),
		caseOf("immutable-structured-scalar-scan", 4_000, 64),
		caseOf("immutable-separate-enrichment-random-lookup", 4_000, 64),
		caseOf("live-direct-scan", 4_000, 0),
		caseOf("immutable-direct-scan", 4_000, 0),
		caseOf("live-open", 4_000, 1),
		caseOf("live-open", 4_000, 256),
		caseOf("snapshot", 4_000, 0),
		caseOf("live-validation", 4_000, 0),
		caseOf("live-membership-validation", 4_000, 64),
		caseOf("immutable-validation", 4_000, 0),
		caseOf("immutable-feed-random", 1_000, 0),
		caseOf("history-project", 1_000, 7),
		caseOf("membership-matching-feeds", 1_000, 64),
		caseOf("membership-cardinalities", 1_000, 64),
		caseOf("membership-selected-pair", 1_000, 2),
		caseOf("membership-all-pairs", 1_000, 8),
		caseOf("direct-provider-join", 1_000, 1),
		caseOf("membership-provider-join", 1_000, 1),
		caseOf("algebra-count", 1_000, 2),
		caseOf("algebra-compare", 1_000, 2),
		caseOf("algebra-publish-preserve", 1_000, 2),
		caseOf("algebra-publish-flat", 1_000, 2),
		caseOf("update-ipsets-workflow", 1_000, 7),
	}
}

func scaleCases() []Case {
	var cases []Case
	for _, size := range []int{10_000, 100_000, 1_000_000} {
		cases = append(cases,
			caseOf("direct-replace", size, 0),
			caseOf("first-seen-refresh", size, 0),
			caseOf("last-seen-refresh", size, 0),
		)
	}
	cases = append(cases,
		caseOf("direct-replace-v6", 1_000_000, 0),
		caseOf("direct-commit", 1_000_000, 0),
	)
	for _, size := range []int{10_000, 100_000, 1_000_000} {
		cases = append(cases, caseOf("nested-overwrite", size, 0))
	}
	for _, feeds := range []int{64, 256, 421} {
		cases = append(cases,
			caseOf("feed-replace", 10_000, feeds),
			caseOf("live-membership-lookup", 100_000, feeds),
			caseOf("immutable-membership-lookup", 100_000, feeds),
		)
	}
	cases = append(cases,
		caseOf("feed-replace", 100_000, 421),
		caseOf("feed-replace", 1_000_000, 421),
	)
	for _, name := range []string{
		"feed-first-ascending", "feed-second-ascending",
		"feed-first-descending", "feed-second-descending",
		"feed-first-random", "feed-second-random",
		"feed-first-overlap", "feed-second-overlap",
	} {
		second := 0
		if strings.HasPrefix(name, "feed-second-") {
			second = 1
		}
		cases = append(cases, caseOf(name, 1_000_000, second))
	}
	cases = append(cases,
		caseOf("membership-import", 10_000, 421),
		caseOf("membership-import", 100_000, 421),
		caseOf("membership-import", 1_000_000, 421),
		caseOf("live-feed-scan", 100_000, 421),
		caseOf("immutable-feed-scan", 100_000, 421),
		caseOf("live-direct-lookup", 100_000, 0),
		caseOf("immutable-direct-lookup", 100_000, 0),
		caseOf("live-direct-random-lookup", 100_000, 0),
		caseOf("immutable-direct-random-lookup", 100_000, 0),
		caseOf("live-membership-random-lookup", 100_000, 421),
		caseOf("immutable-membership-random-lookup", 100_000, 421),
		caseOf("live-direct-random-lookup", 1_000_000, 0),
		caseOf("immutable-direct-random-lookup", 1_000_000, 0),
		caseOf("live-membership-random-lookup", 1_000_000, 421),
		caseOf("immutable-membership-random-lookup", 1_000_000, 421),
		caseOf("structured-build-random", 1_000_000, 421),
		caseOf("structured-intern", 65_536, 421),
		caseOf("structured-assign-random", 1_000_000, 421),
		caseOf("structured-commit", 1_000_000, 421),
		caseOf("live-structured-scalar-random-lookup", 1_000_000, 421),
		caseOf("immutable-structured-scalar-random-lookup", 1_000_000, 421),
		caseOf("live-structured-threat-random-lookup", 1_000_000, 421),
		caseOf("immutable-structured-threat-random-lookup", 1_000_000, 421),
		caseOf("live-structured-scalar-scan", 1_000_000, 421),
		caseOf("immutable-structured-scalar-scan", 1_000_000, 421),
		caseOf("immutable-separate-enrichment-random-lookup", 1_000_000, 421),
		caseOf("live-direct-scan", 100_000, 0),
		caseOf("immutable-direct-scan", 100_000, 0),
		caseOf("live-open", 100_000, 1),
		caseOf("live-open", 100_000, 256),
		caseOf("snapshot", 100_000, 0),
		caseOf("snapshot", 1_000_000, 0),
		caseOf("live-validation", 1_000_000, 0),
		caseOf("live-membership-validation", 1_000_000, 421),
		caseOf("immutable-validation", 1_000_000, 0),
	)
	for _, size := range []int{10_000, 100_000, 1_000_000} {
		cases = append(cases,
			caseOf("immutable-feed-random", size, 0),
			caseOf("history-project", size, 7),
		)
	}
	cases = append(cases,
		caseOf("membership-matching-feeds", 100_000, 421),
		caseOf("membership-cardinalities", 1_000_000, 64),
		caseOf("membership-selected-pair", 1_000_000, 2),
		caseOf("membership-all-pairs", 1_000_000, 8),
		caseOf("membership-all-pairs", 100_000, 64),
		caseOf("direct-provider-join", 1_000_000, 1),
		caseOf("membership-provider-join", 1_000_000, 1),
		caseOf("algebra-count", 1_000_000, 2),
		caseOf("algebra-compare", 1_000_000, 2),
		caseOf("algebra-publish-preserve", 1_000_000, 2),
		caseOf("algebra-publish-flat", 1_000_000, 2),
		caseOf("direct-provider-join", 1_000_000, 421),
		caseOf("membership-provider-join", 1_000_000, 421),
		caseOf("algebra-count", 1_000_000, 421),
		caseOf("algebra-publish-preserve", 1_000_000, 421),
		caseOf("update-ipsets-workflow", 1_000_000, 7),
	)
	return cases
}

func ciCases() []Case {
	return []Case{
		caseOf("direct-replace", 1_000_000, 0),
		caseOf("last-seen-refresh", 1_000_000, 0),
		caseOf("feed-first-ascending", 1_000_000, 0),
		caseOf("feed-first-random", 1_000_000, 0),
		caseOf("live-direct-lookup", 100_000, 0),
		caseOf("live-direct-random-lookup", 100_000, 0),
		caseOf("live-direct-scan", 100_000, 0),
		caseOf("live-membership-lookup", 100_000, 421),
		caseOf("live-membership-random-lookup", 100_000, 421),
		caseOf("structured-build-random", 1_000_000, 421),
		caseOf("live-structured-scalar-random-lookup", 100_000, 421),
		caseOf("live-structured-threat-random-lookup", 100_000, 421),
		caseOf("live-structured-scalar-scan", 100_000, 421),
		caseOf("live-feed-scan", 100_000, 421),
		caseOf("membership-cardinalities", 1_000_000, 64),
		caseOf("live-validation", 1_000_000, 0),
		caseOf("live-membership-validation", 1_000_000, 421),
		caseOf("update-ipsets-workflow", 1_000_000, 7),
	}
}
