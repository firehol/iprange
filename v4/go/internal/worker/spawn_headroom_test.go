//go:build (linux || darwin) && (amd64 || arm64)

// The measurement that sizes spawnDescriptorDemand was taken on Linux, and the
// two platforms here share Go's forkExec shape. FreeBSD is left out because
// syscall.Rlimit types its fields differently and the demand has not been
// measured there; design section 13.5 records the non-Linux platforms as
// unmeasured rather than passing silently.

package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/firehol/iprange/v4/go/internal/calleropen"
	"github.com/firehol/iprange/v4/go/internal/format"
)

// Design section 9.1 makes the spawn prove the descriptor headroom it is about
// to consume before forking, and section 9.4 fixes the answer when the table
// cannot resource it: the io class, with the control file removed. Neither
// half is visible to the descriptor-pressure grid, because at every band where
// the table is short of the spawn's demand the publish is refused before the
// worker arm is reached. This test drives the owner directly, in a fresh
// process whose table is tightened to a chosen number of free slots after the
// control file already exists — the state a handler reaches once its own opens
// are spent.
//
// Two obligations, one per case:
//
//   - at free = demand-1 the spawn must refuse without forking, answer the io
//     class, and leave no control file behind (dropping the headroom check is
//     otherwise observed as a doomed fork that leaks its control page);
//   - at free = demand the spawn must be permitted, so the check cannot decay
//     into the blanket startup refusal the design rejects.

const (
	spawnHeadroomEnv     = "IPRANGE_GO_WORKER_SPAWN_HEADROOM_CHILD"
	spawnHeadroomFreeEnv = "IPRANGE_GO_WORKER_SPAWN_HEADROOM_FREE"
)

func TestSpawnRefusesWhenTableCannotResourceIt(t *testing.T) {
	// The demand is pinned against the number the fork/exec handshake was
	// measured to need, not read back from the constant: a case list derived
	// from spawnDescriptorDemand stays green while the demand drifts to zero
	// (which disables the check) or to a value that refuses every spawn.
	if spawnDescriptorDemand != spawnDemandMeasured {
		t.Fatalf("spawnDescriptorDemand = %d, want the measured %d (one caller-owned null plus "+
			"the three os/exec fork/exec needs); the cases below are calibrated to that number",
			spawnDescriptorDemand, spawnDemandMeasured)
	}
	if os.Getenv(spawnHeadroomEnv) == "1" {
		free := 0
		fmt.Sscanf(os.Getenv(spawnHeadroomFreeEnv), "%d", &free)
		runSpawnHeadroomChild(free)
		return // unreachable: the child exits itself
	}
	for _, tc := range []int{spawnDemandMeasured - 1, spawnDemandMeasured} {
		t.Run(fmt.Sprintf("free=%d", tc), func(t *testing.T) {
			report := runSpawnHeadroom(t, tc)
			if tc < spawnDemandMeasured {
				if report.spawned {
					t.Fatalf("the spawn forked a worker although only %d of the %d descriptors it needs "+
						"were free (raw %q)", tc, spawnDemandMeasured, report.raw)
				}
				if report.errCode != fmt.Sprint(format.CodeIO) {
					t.Fatalf("an unresourced spawn answered %q, want the io class the reference answers "+
						"for an exhausted worker table (raw %q)", report.errCode, report.raw)
				}
				if report.controlExists {
					t.Fatalf("the refused spawn left its control file on disk, the leak section 9.4 "+
						"requires it to close (raw %q)", report.raw)
				}
				return
			}
			if report.err != "none" || !report.spawned {
				t.Fatalf("the spawn refused a table holding the %d free descriptors it needs (err %q, "+
					"spawned %v): the headroom check may not decay into a blanket refusal (raw %q)",
					tc, report.err, report.spawned, report.raw)
			}
		})
	}

	// A spawn that reached fork but failed there still owns a control page.
	// Section 9.4 requires it to be removed: a session that could not start a
	// worker must not leave the next one a stale control file. The candidate
	// here is a regular, non-executable file, which passes the existence and
	// regularity screen and then fails in Start().
	t.Run("start-failure", func(t *testing.T) {
		report := runSpawnStartFailure(t)
		if report.spawned {
			t.Fatalf("the spawn reported success for a candidate that cannot be executed (raw %q)", report.raw)
		}
		if report.errCode != fmt.Sprint(format.CodeIO) {
			t.Fatalf("a spawn that failed in Start answered %q, want the io class (raw %q)",
				report.errCode, report.raw)
		}
		if report.controlExists {
			t.Fatalf("a spawn that failed in Start left its control file on disk (raw %q)", report.raw)
		}
	})
}

type spawnHeadroomReport struct {
	err           string
	errCode       string
	spawned       bool
	controlExists bool
	freeAtSpawn   int
	raw           string
}

func runSpawnHeadroom(t *testing.T, free int) spawnHeadroomReport {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, "-test.run", "TestSpawnRefusesWhenTableCannotResourceIt")
	cmd.Env = append(os.Environ(), spawnHeadroomEnv+"=1", fmt.Sprintf("%s=%d", spawnHeadroomFreeEnv, free))
	out, cmdErr := cmd.CombinedOutput()
	text := string(out)
	report, parseErr := parseSpawnHeadroom(text)
	report.raw = text
	if parseErr != nil {
		t.Fatalf("unusable headroom report (%v, cmd %v):\n%s", parseErr, cmdErr, text)
	}
	if report.err != "none" {
		t.Fatalf("headroom child could not reach the decision: %s\n%s", report.err, text)
	}
	return report
}

func parseSpawnHeadroom(text string) (spawnHeadroomReport, error) {
	report := spawnHeadroomReport{err: "none", freeAtSpawn: -1}
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "SPAWN_HEADROOM ") {
			continue
		}
		for _, field := range strings.Fields(line) {
			name, value, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			switch name {
			case "ERR":
				report.err = value
			case "CODE":
				report.errCode = value
			case "SPAWNED":
				report.spawned = value == "1"
			case "CONTROL":
				report.controlExists = value == "1"
			case "FREE":
				fmt.Sscanf(value, "%d", &report.freeAtSpawn)
			}
		}
		return report, nil
	}
	return report, fmt.Errorf("no SPAWN_HEADROOM line in %q", text)
}

// runSpawnHeadroomChild materializes the state the owner must decide on: a
// control file that exists, and a descriptor table with exactly `free` slots
// left. Tightening happens after CreateParent so the case describes a request
// that already spent its own descriptors, which is when the spawn's headroom
// question is actually asked.
func runSpawnHeadroomChild(free int) {
	printAndExit := func(err, code string, spawned, controlExists bool, freeAtSpawn int) {
		fmt.Printf("SPAWN_HEADROOM ERR=%s CODE=%s SPAWNED=%d CONTROL=%d FREE=%d\n",
			err, code, boolToInt(spawned), boolToInt(controlExists), freeAtSpawn)
		os.Exit(0)
	}
	// Clamp the limit into a window the table read measures exactly (above its
	// scan bound the answer is bounded, not exact), so tightening below is
	// computed from a real free count.
	var current syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &current); err != nil {
		printAndExit("getrlimit:"+err.Error(), "", false, false, -1)
	}
	clamp := uint64(exactTableWindow)
	if current.Max < clamp {
		clamp = current.Max
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{Cur: clamp, Max: current.Max}); err != nil {
		printAndExit("clamp:"+err.Error(), "", false, false, -1)
	}
	control, err := CreateParent()
	if err != nil {
		printAndExit("create-control:"+err.Error(), "", false, false, -1)
	}
	path := control.path
	freeNow, ok := calleropen.FreeDescriptors()
	if !ok {
		printAndExit("free-descriptors", "", false, false, -1)
	}
	if free < 0 || free > freeNow {
		printAndExit(fmt.Sprintf("cannot-tighten-to-%d-from-%d", free, freeNow), "", false, false, freeNow)
	}
	// Lowering the soft limit below the descriptors already open is EINVAL,
	// so the new soft limit is the count in use plus the requested headroom.
	newSoft := clamp - uint64(freeNow-free)
	if newSoft < 3 {
		printAndExit(fmt.Sprintf("soft-limit-%d-too-low", newSoft), "", false, false, freeNow)
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{Cur: newSoft, Max: current.Max}); err != nil {
		printAndExit("tighten:"+err.Error(), "", false, false, freeNow)
	}
	// The candidate list points at this binary so the permitted case really
	// forks; the child never has to speak the protocol for this decision.
	workerCandidatesHook = func() ([]string, error) { return []string{os.Args[0]}, nil }
	child, spawnErr := SpawnWorker(control)
	code := ""
	spawned := child != nil && spawnErr == nil
	if spawnErr != nil {
		var fe *format.Error
		if errors.As(spawnErr, &fe) {
			code = fmt.Sprint(fe.Code)
		} else {
			code = "unclassified"
		}
	}
	freeAtSpawn, _ := calleropen.FreeDescriptors()
	_, statErr := os.Stat(path)
	if child != nil {
		child.Close()
	}
	control.Close()
	printAndExit("none", code, spawned, statErr == nil, freeAtSpawn)
}

// spawnDemandMeasured is the parent-side descriptor demand of one worker
// spawn, measured on this platform: 1 for the caller-owned null stdio plus 3
// for os/exec's fork/exec handshake. The observation is that at 3 free the
// spawn is admitted and then fails inside Start() with EMFILE, and at 4 it
// succeeds, so 4 is the smallest number that neither refuses a spawn the
// table could resource nor admits one that cannot be completed.
const spawnDemandMeasured = 4

// runSpawnStartFailure drives a spawn that passes every screen and then fails
// inside fork, in this process: no descriptor tightening is involved, only the
// candidate's executability, so the control-file obligation is observed
// directly.
func runSpawnStartFailure(t *testing.T) spawnHeadroomReport {
	t.Helper()
	control, err := CreateParent()
	if err != nil {
		t.Fatalf("create control: %v", err)
	}
	defer control.Close()
	path := control.path
	candidate := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(candidate, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatalf("stage candidate: %v", err)
	}
	workerCandidatesHook = func() ([]string, error) { return []string{candidate}, nil }
	defer func() { workerCandidatesHook = nil }()
	child, spawnErr := SpawnWorker(control)
	code := ""
	spawned := child != nil && spawnErr == nil
	if spawnErr != nil {
		var fe *format.Error
		if errors.As(spawnErr, &fe) {
			code = fmt.Sprint(fe.Code)
		} else {
			code = "unclassified:" + spawnErr.Error()
		}
	}
	_, statErr := os.Stat(path)
	if child != nil {
		child.Close()
	}
	return spawnHeadroomReport{err: "none", errCode: code, spawned: spawned, controlExists: statErr == nil}
}

// exactTableWindow is the largest soft limit whose free-descriptor answer is
// exact rather than bounded by the table read's scan window; the child clamps
// into it before tightening so the requested headroom means what it says.
const exactTableWindow = 1024

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
