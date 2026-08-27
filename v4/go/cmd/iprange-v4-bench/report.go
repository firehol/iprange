// Repeated sampling and accepted-baseline comparison (Rust
// benches/update_ipsets/report.rs): warmups plus samples per case, each
// in a fresh subprocess, semantic-result equality across samples, and
// the CI performance-disaster gate against the accepted baseline.
package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

const fixtureID = "iprange-v4-update-ipsets-v1"
const baselineID = "rust-v4-local-20260811"

const reportHeader = "scenario,size,aux,samples,min_ns,p50_ns,p90_ns,max_ns,median_units_per_second,alloc_calls,alloc_bytes,max_rss_peak_kib,max_fds_after,file_logical_bytes,range_records,feeds,accepted_median_ns,ci_limit_ns,ratio,status"

//go:embed accepted-baseline.csv
var acceptedBaseline string

type sample struct {
	scenario     string
	size         int
	auxiliary    int
	workUnits    uint64
	emittedUnits uint64
	elapsedNs    uint64
	allocCalls   uint64
	allocBytes   uint64
	rssPeakKib   *uint64
	fdsAfter     *uint64
	fileBytes    uint64
	rangeRecords uint64
	feeds        uint64
	private      uint64
}

type accepted struct {
	medianNs  uint64
	ciLimitNs uint64
}

func runRepeated(cases []Case, warmups, samples int, enforce bool) error {
	if samples <= 0 {
		return fmt.Errorf("sample count must be positive")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	metadata(warmups, samples)
	fmt.Println(reportHeader)
	var failed []string
	for _, c := range cases {
		for range warmups {
			if _, err := childSample(executable, c); err != nil {
				return err
			}
		}
		observed := make([]sample, 0, samples)
		for range samples {
			s, err := childSample(executable, c)
			if err != nil {
				return err
			}
			observed = append(observed, s)
		}
		if err := requireSameResult(observed); err != nil {
			return err
		}
		acceptedForCase, err := acceptedSample(observed[0])
		if err != nil {
			return err
		}
		summary := summarize(observed, acceptedForCase)
		fmt.Println(summary.line)
		if enforce && summary.overLimit {
			limit := uint64(0)
			if acceptedForCase != nil {
				limit = acceptedForCase.ciLimitNs
			}
			failed = append(failed, fmt.Sprintf("%s size=%d aux=%d median=%dns limit=%dns", c.Name, c.Size, c.Auxiliary, summary.medianNs, limit))
		}
	}
	if len(failed) == 0 {
		return nil
	}
	return fmt.Errorf("performance disaster gate failed: %s", strings.Join(failed, "; "))
}

type summary struct {
	line      string
	medianNs  uint64
	overLimit bool
}

func summarize(samples []sample, acceptedForCase *accepted) summary {
	elapsed := make([]uint64, len(samples))
	for i, s := range samples {
		elapsed[i] = s.elapsedNs
	}
	sort.Slice(elapsed, func(i, j int) bool { return elapsed[i] < elapsed[j] })
	first := samples[0]
	medianNs := percentile(elapsed, 50)
	var acceptedNs *uint64
	var limitNs *uint64
	if acceptedForCase != nil {
		value := acceptedForCase.medianNs
		acceptedNs = &value
		limit := acceptedForCase.ciLimitNs
		limitNs = &limit
	}
	var ratio *float64
	if acceptedNs != nil {
		value := float64(medianNs) / float64(*acceptedNs)
		ratio = &value
	}
	overLimit := true
	if limitNs != nil {
		overLimit = medianNs > *limitNs
	}
	status := "untracked"
	switch {
	case acceptedForCase == nil:
		status = "untracked"
	case overLimit:
		status = "over-limit"
	default:
		status = "within-limit"
	}
	rate := 0.0
	if medianNs != 0 {
		rate = float64(first.workUnits) * 1_000_000_000.0 / float64(medianNs)
	}
	maxAllocCalls := uint64(0)
	maxAllocBytes := uint64(0)
	var maxRSS *uint64
	var maxFds *uint64
	for _, s := range samples {
		if s.allocCalls > maxAllocCalls {
			maxAllocCalls = s.allocCalls
		}
		if s.allocBytes > maxAllocBytes {
			maxAllocBytes = s.allocBytes
		}
		if s.rssPeakKib != nil && (maxRSS == nil || *s.rssPeakKib > *maxRSS) {
			value := *s.rssPeakKib
			maxRSS = &value
		}
		if s.fdsAfter != nil && (maxFds == nil || *s.fdsAfter > *maxFds) {
			value := *s.fdsAfter
			maxFds = &value
		}
	}
	ratioText := ""
	if ratio != nil {
		ratioText = fmt.Sprintf("%.3f", *ratio)
	}
	line := fmt.Sprintf("%s,%d,%d,%d,%d,%d,%d,%d,%.3f,%d,%d,%s,%s,%d,%d,%d,%s,%s,%s,%s",
		first.scenario,
		first.size,
		first.auxiliary,
		len(samples),
		elapsed[0],
		medianNs,
		percentile(elapsed, 90),
		elapsed[len(elapsed)-1],
		rate,
		maxAllocCalls,
		maxAllocBytes,
		optionalUint(maxRSS),
		optionalUint(maxFds),
		first.fileBytes,
		first.rangeRecords,
		first.feeds,
		optionalUint(acceptedNs),
		optionalUint(limitNs),
		ratioText,
		status,
	)
	return summary{line: line, medianNs: medianNs, overLimit: overLimit}
}

func percentile(sorted []uint64, percentile int) uint64 {
	rank := (len(sorted)*percentile + 99) / 100
	index := rank - 1
	if index < 0 {
		index = 0
	}
	if index > len(sorted)-1 {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func childSample(executable string, c Case) (sample, error) {
	output, err := exec.Command(executable, "case", c.Name, strconv.Itoa(c.Size), strconv.Itoa(c.Auxiliary)).Output()
	if err != nil {
		return sample{}, fmt.Errorf("%s size=%d aux=%d exited %v", c.Name, c.Size, c.Auxiliary, err)
	}
	lines := strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n")
	var nonEmpty []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonEmpty = append(nonEmpty, strings.TrimSpace(line))
		}
	}
	if len(nonEmpty) == 0 {
		return sample{}, fmt.Errorf("%s emitted no result", c.Name)
	}
	if len(nonEmpty) != 1 {
		return sample{}, fmt.Errorf("%s emitted more than one result", c.Name)
	}
	return parseSample(nonEmpty[0], c)
}

func parseSample(line string, c Case) (sample, error) {
	fields := strings.Split(line, ",")
	if len(fields) != 19 {
		return sample{}, fmt.Errorf("%s emitted %d fields instead of 19", c.Name, len(fields))
	}
	parse := func(field int, label string) (uint64, error) {
		value, err := strconv.ParseUint(fields[field], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s has invalid %s %q", c.Name, label, fields[field])
		}
		return value, nil
	}
	parseOptional := func(field int) (*uint64, error) {
		if fields[field] == "" {
			return nil, nil
		}
		value, err := strconv.ParseUint(fields[field], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s has invalid optional field %q", c.Name, fields[field])
		}
		return &value, nil
	}
	parseSize := func(field int, label string) (int, error) {
		value, err := strconv.ParseInt(fields[field], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s has invalid %s %q", c.Name, label, fields[field])
		}
		return int(value), nil
	}
	size, err := parseSize(1, "size")
	if err != nil {
		return sample{}, err
	}
	aux, err := parseSize(2, "auxiliary")
	if err != nil {
		return sample{}, err
	}
	workUnits, err := parse(3, "work units")
	if err != nil {
		return sample{}, err
	}
	emittedUnits, err := parse(4, "emitted units")
	if err != nil {
		return sample{}, err
	}
	elapsedNs, err := parse(5, "elapsed time")
	if err != nil {
		return sample{}, err
	}
	allocCalls, err := parse(7, "allocation calls")
	if err != nil {
		return sample{}, err
	}
	allocBytes, err := parse(8, "allocation bytes")
	if err != nil {
		return sample{}, err
	}
	rssPeak, err := parseOptional(11)
	if err != nil {
		return sample{}, err
	}
	fdsAfter, err := parseOptional(13)
	if err != nil {
		return sample{}, err
	}
	fileBytes, err := parse(14, "logical bytes")
	if err != nil {
		return sample{}, err
	}
	rangeRecords, err := parse(16, "range records")
	if err != nil {
		return sample{}, err
	}
	feeds, err := parse(17, "feeds")
	if err != nil {
		return sample{}, err
	}
	private, err := parse(18, "private artifacts")
	if err != nil {
		return sample{}, err
	}
	s := sample{
		scenario:     fields[0],
		size:         size,
		auxiliary:    aux,
		workUnits:    workUnits,
		emittedUnits: emittedUnits,
		elapsedNs:    elapsedNs,
		allocCalls:   allocCalls,
		allocBytes:   allocBytes,
		rssPeakKib:   rssPeak,
		fdsAfter:     fdsAfter,
		fileBytes:    fileBytes,
		rangeRecords: rangeRecords,
		feeds:        feeds,
		private:      private,
	}
	if s.scenario != c.Name || s.size != c.Size || s.auxiliary != c.Auxiliary {
		return sample{}, fmt.Errorf("%s result identity disagrees", c.Name)
	}
	return s, nil
}

func requireSameResult(samples []sample) error {
	first := samples[0]
	for _, s := range samples[1:] {
		if s.workUnits != first.workUnits ||
			s.emittedUnits != first.emittedUnits ||
			s.auxiliary != first.auxiliary ||
			s.fileBytes != first.fileBytes ||
			s.rangeRecords != first.rangeRecords ||
			s.feeds != first.feeds ||
			s.private != first.private {
			return fmt.Errorf("%s repeated samples produced different semantic results", first.scenario)
		}
	}
	return nil
}

func acceptedSample(s sample) (*accepted, error) {
	for number, line := range strings.Split(acceptedBaseline, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "scenario,") {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) != 5 {
			return nil, fmt.Errorf("accepted baseline line %d has %d fields instead of 5", number+1, len(fields))
		}
		size, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("accepted baseline line %d has invalid size", number+1)
		}
		aux, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("accepted baseline line %d has invalid auxiliary", number+1)
		}
		if fields[0] == s.scenario && size == s.size && aux == s.auxiliary {
			medianNs, err := strconv.ParseUint(fields[3], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("accepted baseline line %d has invalid median", number+1)
			}
			limitNs, err := strconv.ParseUint(fields[4], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("accepted baseline line %d has invalid CI limit", number+1)
			}
			return &accepted{medianNs: medianNs, ciLimitNs: limitNs}, nil
		}
	}
	return nil, nil
}

func metadata(warmups, samples int) {
	fmt.Println("# benchmark=update_ipsets")
	fmt.Println("# fixture=" + fixtureID)
	fmt.Println("# baseline=" + baselineID)
	fmt.Println("# os=" + runtime.GOOS)
	fmt.Println("# arch=" + runtime.GOARCH)
	fmt.Println("# profile=optimized")
	fmt.Println("# go=" + runtime.Version())
	fmt.Println("# cpu=" + cpuModel())
	fmt.Printf("# warmups=%d\n", warmups)
	fmt.Printf("# samples=%d\n", samples)
}

func cpuModel() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "model name") {
			return strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
		}
	}
	return ""
}
