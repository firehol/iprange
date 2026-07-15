package iprangedb

import (
	"context"
	"math"
	"os"
	"os/exec"
	"runtime/debug"
	"testing"
	"time"
)

func round6CyclicReaderImage(t *testing.T) []byte {
	t.Helper()
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < 800; i++ {
		ip := Ipv4Key(i * 2)
		if err := w.Append(ip, ip, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	image, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing committed image")
	}
	_, m := round5ActiveMetaPage(t, image)
	root := image[int(m.rootPgno)*PageSize : int(m.rootPgno+1)*PageSize]
	h := decodeHeader(root)
	if h.pageType != PageTypeBranch || m.treeHeight < 2 {
		t.Fatal("fixture root is not a multi-level branch")
	}
	putU32(root, PageHeaderSize, m.rootPgno)
	finalizeChecksum(root)
	return image
}

func TestRound6ReaderLookupRejectsCyclicBranchInsteadOfReturningFabricatedData(t *testing.T) {
	r, err := Open(round6CyclicReaderImage(t))
	if err != nil {
		t.Fatal(err)
	}
	if scopeID, ok := r.LookupV4(100); ok {
		t.Fatalf("lookup returned fabricated scope %d from a cyclic branch interpreted as a leaf", scopeID)
	}
}

func TestRound6ReaderScanRejectsCyclicBranchWithoutCrashOrHang(t *testing.T) {
	if os.Getenv("IPRANGE_ROUND6_SCAN_CYCLE_HELPER") == "1" {
		debug.SetMaxStack(1 << 20)
		r, err := Open(round6CyclicReaderImage(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := r.ScanV4(func(Ipv4Key, Ipv4Key, uint32) {}); err == nil {
			t.Fatal("scan accepted a cyclic branch")
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRound6ReaderScanRejectsCyclicBranchWithoutCrashOrHang$")
	cmd.Env = append(os.Environ(), "IPRANGE_ROUND6_SCAN_CYCLE_HELPER=1")
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatal("scan hung on a cyclic branch")
	}
	if err != nil {
		if len(output) > 2000 {
			output = output[:2000]
		}
		t.Fatalf("scan crashed on a cyclic branch: %v\n%s", err, output)
	}
}

func TestRound6ReaderRejectsCrossFamilyLookupAndScan(t *testing.T) {
	v4, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := v4.Append(10, 20, 1); err != nil {
		t.Fatal(err)
	}
	if err := v4.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	v4Image, _ := v4.IntoImage()
	v4Reader, err := Open(v4Image)
	if err != nil {
		t.Fatal(err)
	}
	if scopeID, ok := v4Reader.LookupV6(Ipv6Key{Lo: 10}); ok {
		t.Errorf("IPv6 lookup on IPv4 file returned fabricated scope %d", scopeID)
	}
	v4Called := false
	if err := v4Reader.ScanV6(func(Ipv6Key, Ipv6Key, uint32) { v4Called = true }); err == nil {
		t.Error("IPv6 scan on IPv4 file did not reject the family mismatch")
	}
	if v4Called {
		t.Error("IPv6 scan on IPv4 file invoked the callback")
	}

	v6, err := Create[Ipv6Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := v6.Append(Ipv6Key{Lo: 10}, Ipv6Key{Lo: 20}, 1); err != nil {
		t.Fatal(err)
	}
	if err := v6.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	v6Image, _ := v6.IntoImage()
	v6Reader, err := Open(v6Image)
	if err != nil {
		t.Fatal(err)
	}
	if scopeID, ok := v6Reader.LookupV4(10); ok {
		t.Errorf("IPv4 lookup on IPv6 file returned fabricated scope %d", scopeID)
	}
	v6Called := false
	if err := v6Reader.ScanV4(func(Ipv4Key, Ipv4Key, uint32) { v6Called = true }); err == nil {
		t.Error("IPv4 scan on IPv6 file did not reject the family mismatch")
	}
	if v6Called {
		t.Error("IPv4 scan on IPv6 file invoked the callback")
	}
}

func TestRound6ReaderScopeResolveDoesNotPanicOnMalformedEntryCount(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.ScopeIntern([]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	image, _ := w.IntoImage()
	_, m := round5ActiveMetaPage(t, image)
	scope := image[int(m.scopeTableRoot)*PageSize : int(m.scopeTableRoot+1)*PageSize]
	if decodeHeader(scope).pageType != PageTypeScopeLeaf {
		t.Fatal("fixture scope root is not a leaf")
	}
	putU16(scope, PHEntryCount, math.MaxUint16)
	finalizeChecksum(scope)
	r, err := Open(image)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("ScopeResolve panicked on malformed entry_count: %v", recovered)
		}
	}()
	if got := r.ScopeResolve(id); got != nil {
		t.Fatalf("ScopeResolve returned data from malformed scope leaf: %x", got)
	}
}
