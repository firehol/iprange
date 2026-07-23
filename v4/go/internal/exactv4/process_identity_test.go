package exactv4

import (
	"bytes"
	"testing"
)

func processIdentityActive(start uint64) activeSlot {
	return activeSlot{
		txnID: 7, processID: 123, processStart: start, nonce: [16]byte{1},
	}
}

func linuxProcStat(command, start []byte) []byte {
	var data []byte
	data = append(data, "123 ("...)
	data = append(data, command...)
	data = append(data, ") R "...)
	data = append(data, bytes.Join([][]byte{
		[]byte("1"), []byte("2"), []byte("3"), []byte("4"), []byte("5"),
		[]byte("6"), []byte("7"), []byte("8"), []byte("9"), []byte("10"),
		[]byte("11"), []byte("12"), []byte("13"), []byte("14"), []byte("15"),
		[]byte("16"), []byte("17"), []byte("18"),
	}, []byte(" "))...)
	data = append(data, ' ')
	data = append(data, start...)
	data = append(data, " 20 21\n"...)
	return data
}

func TestLinuxProcessStartUsesFinalParenthesisAndExactField22(t *testing.T) {
	for _, test := range []struct {
		command []byte
		start   []byte
		want    uint64
	}{
		{[]byte("name with ) embedded"), []byte("424242"), 424242},
		{[]byte(") )"), []byte("18446744073709551615"), ^uint64(0)},
	} {
		got, problem := parseLinuxProcStatStart(linuxProcStat(test.command, test.start))
		if problem != 0 || got != test.want {
			t.Fatalf("parseLinuxProcStatStart() = (%d, %d), want (%d, 0)", got, problem, test.want)
		}
	}
}

func TestLinuxProcessStartRejectsMissingInvalidZeroAndOverflow(t *testing.T) {
	for _, test := range []struct {
		data []byte
		want processStartParseError
	}{
		{[]byte("123 no command end"), processStartMissingCommandEnd},
		{[]byte("123 (x) R 1 2"), processStartMissingField},
		{linuxProcStat([]byte("x"), []byte("-1")), processStartInvalidNumber},
		{linuxProcStat([]byte("x"), []byte("0")), processStartZero},
		{linuxProcStat([]byte("x"), []byte("18446744073709551616")), processStartOverflow},
	} {
		if _, problem := parseLinuxProcStatStart(test.data); problem != test.want {
			t.Errorf("parseLinuxProcStatStart() problem = %d, want %d", problem, test.want)
		}
	}
}

func TestPOSIXDeathRequiresMissingOrTwoNonzeroMismatchedTokens(t *testing.T) {
	active := processIdentityActive(10)
	proof, dead := classifyPOSIXDeath(active, posixProcessObservation{kind: posixProcessMissing})
	if !dead || proof.kind != deathProofPOSIXMissing || proof.processID != active.processID {
		t.Fatalf("missing process was not proven dead: (%+v, %v)", proof, dead)
	}
	proof, dead = classifyPOSIXDeath(active, posixProcessObservation{kind: posixProcessExists, currentStart: 11})
	if !dead || proof.kind != deathProofPOSIXPIDReused || proof.currentStart != 11 {
		t.Fatalf("reused PID was not proven dead: (%+v, %v)", proof, dead)
	}
	for _, observation := range []posixProcessObservation{
		{kind: posixProcessExists, currentStart: 10},
		{kind: posixProcessExists},
		{kind: posixProcessUncertain},
	} {
		if _, dead := classifyPOSIXDeath(active, observation); dead {
			t.Fatalf("uncertain/equal process was classified dead: %+v", observation)
		}
	}
	if _, dead := classifyPOSIXDeath(processIdentityActive(0), posixProcessObservation{
		kind: posixProcessExists, currentStart: 11,
	}); dead {
		t.Fatal("zero stored token proved PID reuse")
	}
}

func TestWindowsDeathRequiresSignaledOrTwoNonzeroMismatchedTokens(t *testing.T) {
	active := processIdentityActive(10)
	if proof, dead := classifyWindowsDeath(active, windowsProcessObservation{kind: windowsProcessSignaled}); !dead || proof.kind != deathProofWindowsSignaled {
		t.Fatalf("signaled process was not proven dead: (%+v, %v)", proof, dead)
	}
	if proof, dead := classifyWindowsDeath(active, windowsProcessObservation{
		kind: windowsProcessRunning, currentStart: 11,
	}); !dead || proof.kind != deathProofWindowsPIDReused {
		t.Fatalf("reused PID was not proven dead: (%+v, %v)", proof, dead)
	}
	if _, dead := classifyWindowsDeath(active, windowsProcessObservation{kind: windowsProcessUncertain}); dead {
		t.Fatal("uncertain process was classified dead")
	}
}
