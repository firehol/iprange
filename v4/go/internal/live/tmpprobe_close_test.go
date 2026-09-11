package live

import (
	"os"
	"testing"
)

func TestTmpProbeCloseAfterSidecarRestore(t *testing.T) {
	main := createLiveV4Pair(t, 2)
	r, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatal(err)
	}
	side := main + ".readers"
	renamed := side + ".old"
	if err := os.Rename(side, renamed); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(renamed, side); err != nil {
		t.Fatal(err)
	}
	res, err := r.Close()
	t.Logf("close result: %+v err=%v", res, err)
}
