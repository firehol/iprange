package handlers

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// Temporary diagnostics probe for wave-19.13 Windows cleanup: prints
// which file cannot be removed after the session finishes.
func TestTmpProbeSessionCleanup(t *testing.T) {
	dir, err := os.MkdirTemp("", "tmpprobe-")
	if err != nil {
		t.Fatal(err)
	}
	source := newLiveFeed(t, dir, "live2.db")
	sidecar := source + ".readers"
	registerHandlers()
	openFrame := `{"jsonrpc":"2.0","id":"1","method":"iprange.v1.reader.open","params":{"source":{"path":` +
		mustJSONString(source) + `,"mode":"live"}}}`
	session := rpc.NewSession()
	pr, pw := io.Pipe()
	defer pr.Close()
	outR, outW := io.Pipe()
	defer outR.Close()
	done := make(chan error, 1)
	go func() { done <- session.Run(pr, outW) }()
	fmt.Fprintf(pw, "%s\n", openFrame)
	first, _ := bufio.NewReader(outR).ReadString('\n')
	fmt.Println("open response:", strings.TrimSpace(first)[:80])
	_ = pw.Close()
	<-done
	fmt.Println("session done")
	fmt.Println("remove main:", os.Remove(source))
	fmt.Println("rename side:", os.Rename(sidecar, sidecar+".old"))
	fmt.Println("remove side.old:", os.Remove(sidecar+".old"))
	fmt.Println("RemoveAll:", os.RemoveAll(dir))
	fmt.Println("dir exists:", pathExists(dir))
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

