// Measurement protocol (Rust benches/update_ipsets/measure.rs +
// timing.rs + allocation.rs): elapsed time, allocation counters through
// runtime MemStats Mallocs/TotalAlloc deltas, RSS and peak RSS from
// /proc/self/status on Linux, open fd count from /proc/self/fd, and
// logical plus physical file size. The optional
// IPRANGE_PERF_CONTROL/IPRANGE_PERF_ACK protocol matches Rust.
package main

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type allocationStats struct {
	calls uint64
	bytes uint64
}

type fileSize struct {
	logical  uint64
	physical *uint64
}

type measurement struct {
	elapsed      time.Duration
	allocations  allocationStats
	rssBeforeKib *uint64
	rssAfterKib  *uint64
	rssPeakKib   *uint64
	fdsBefore    *uint64
	fdsAfter     *uint64
}

func memAllocationStats() allocationStats {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return allocationStats{calls: stats.Mallocs, bytes: stats.TotalAlloc}
}

func operation(callback func() error) (error, measurement) {
	perfCommand([]byte("enable\n"))
	rssBefore := currentRSSKib()
	fdsBefore := openFileDescriptors()
	// One GC before the baseline keeps the allocation delta attributable
	// to the measured operation.
	runtime.GC()
	started := time.Now()
	before := memAllocationStats()
	err := callback()
	after := memAllocationStats()
	elapsed := time.Since(started)
	fdsAfter := openFileDescriptors()
	rssAfter := currentRSSKib()
	perfCommand([]byte("disable\n"))
	return err, measurement{
		elapsed: elapsed,
		allocations: allocationStats{
			calls: after.calls - before.calls,
			bytes: after.bytes - before.bytes,
		},
		rssBeforeKib: rssBefore,
		rssAfterKib:  rssAfter,
		rssPeakKib:   peakRSSKib(),
		fdsBefore:    fdsBefore,
		fdsAfter:     fdsAfter,
	}
}

func perfCommand(command []byte) {
	path := os.Getenv("IPRANGE_PERF_CONTROL")
	if path == "" {
		return
	}
	control, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		panic("open perf control " + path + ": " + err.Error())
	}
	if _, err := control.Write(command); err != nil {
		panic("write perf control " + path + ": " + err.Error())
	}
	_ = control.Close()
	ackPath := os.Getenv("IPRANGE_PERF_ACK")
	if ackPath == "" {
		return
	}
	ack, err := os.Open(ackPath)
	if err != nil {
		panic("open perf acknowledgement " + ackPath + ": " + err.Error())
	}
	defer ack.Close()
	response := make([]byte, 5)
	if _, err := ack.Read(response); err != nil {
		panic("read perf acknowledgement " + ackPath + ": " + err.Error())
	}
	if string(response) != "ack\n\x00" {
		panic("unexpected perf acknowledgement")
	}
}

func currentRSSKib() *uint64 { return statusValueKib("VmRSS:") }

func peakRSSKib() *uint64 { return statusValueKib("VmHWM:") }

func statusValueKib(label string) *uint64 {
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(status), "\n") {
		if !strings.HasPrefix(line, label) {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, label))
		if len(fields) == 0 {
			return nil
		}
		value, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return nil
		}
		return &value
	}
	return nil
}

func openFileDescriptors() *uint64 {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return nil
	}
	value := uint64(len(entries))
	return &value
}

// fileSizeOf mirrors Rust measure::file_size: logical length plus the
// physical block count (st_blocks x 512) where the platform reports it
// (unix Stat_t carries Blocks; other platforms report the logical size
// only, exactly like the Rust reported-size rules).
func fileSizeOf(path string) (fileSize, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileSize{}, err
	}
	size := fileSize{logical: uint64(info.Size())}
	if blocks := statBlocks(info); blocks > 0 {
		physical := uint64(blocks) * 512
		size.physical = &physical
	}
	return size, nil
}
