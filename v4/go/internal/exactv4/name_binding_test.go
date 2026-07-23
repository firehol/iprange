package exactv4

import "testing"

var (
	basenameCommitmentSink [32]byte
	basenameErrorSink      *basenameBindingError
)

func TestBasenameCommitmentMatchesCrossLanguageGolden(t *testing.T) {
	want := [32]byte{
		0x58, 0x1c, 0x42, 0x34, 0xbf, 0xf2, 0x93, 0x4f,
		0xab, 0x8a, 0x83, 0x4b, 0x0c, 0x4b, 0x38, 0x98,
		0xac, 0xc6, 0xe6, 0xe0, 0x01, 0x92, 0x7a, 0xe1,
		0xc0, 0x9d, 0x09, 0xb6, 0xf4, 0xa8, 0x3c, 0x20,
	}
	got, err := basenameCommitment(basenamePOSIXBytes, []byte("main.iprdb"))
	if err != nil || got != want {
		t.Fatalf("POSIX commitment = (%x, %v), want %x", got, err, want)
	}
}

func TestBasenameCommitmentValidatesExactPlatformComponents(t *testing.T) {
	if _, err := basenameCommitment(basenameEncoding(3), []byte("main.iprdb")); err == nil || err.code != basenameBindingInvalidEncoding {
		t.Fatalf("unknown basename encoding accepted: %v", err)
	}
	for _, invalid := range [][]byte{nil, {}, {'.'}, {'.', '.'}, {'a', '/', 'b'}, {'a', 0, 'b'}} {
		if _, err := basenameCommitment(basenamePOSIXBytes, invalid); err == nil {
			t.Fatalf("invalid POSIX component %x accepted", invalid)
		}
	}

	validWindows := []byte{'m', 0, 0x3d, 0xd8, 0x00, 0xde}
	if _, err := basenameCommitment(basenameWindowsUTF16, validWindows); err != nil {
		t.Fatalf("valid Windows component rejected: %v", err)
	}
	for _, invalid := range [][]byte{
		nil,
		{'x'},
		{0x00, 0xd8},
		{0x00, 0xdc},
		{0x00, 0xd8, 'x', 0},
		{'/', 0},
		{'\\', 0},
		{0, 0},
	} {
		if _, err := basenameCommitment(basenameWindowsUTF16, invalid); err == nil {
			t.Fatalf("invalid Windows component %x accepted", invalid)
		}
	}
}

func TestBasenameCommitmentDoesNotAllocate(t *testing.T) {
	posix := []byte("main.iprdb")
	windows := []byte{'m', 0, 0x3d, 0xd8, 0x00, 0xde}
	allocations := testing.AllocsPerRun(1000, func() {
		basenameCommitmentSink, basenameErrorSink = basenameCommitment(basenamePOSIXBytes, posix)
		basenameCommitmentSink, basenameErrorSink = basenameCommitment(basenameWindowsUTF16, windows)
	})
	if allocations != 0 {
		t.Fatalf("basename commitment allocated %.2f objects per run", allocations)
	}
}
