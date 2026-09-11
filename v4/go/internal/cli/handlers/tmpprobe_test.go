package handlers

import (
	"bufio"
	"encoding/json"
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
	renamed := sidecar + ".old"
	fmt.Println("rename side:", os.Rename(sidecar, renamed))
	var openResponse struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(first)), &openResponse); err != nil {
		t.Fatal(err)
	}
	var handle string
	if err := json.Unmarshal(openResponse.Result["reader"], &handle); err != nil || handle == "" {
		t.Fatalf("handle: %s err %v", openResponse.Result["reader"], err)
	}
	metaFrame := `{"jsonrpc":"2.0","id":"2","method":"iprange.v1.reader.metadata","params":{"reader":` +
		mustJSONString("@HANDLE@") + `,"delivery":{"mode":"file","path":` +
		mustJSONString(renamed) + `,"publication_policy":"replace_existing","max_output_bytes":"1048576","max_open_files":8}}}`
	metaFrame = strings.Replace(metaFrame, mustJSONString("@HANDLE@"), mustJSONString(handle), 1)
	fmt.Fprintf(pw, "%s\n", metaFrame)
	second, _ := bufio.NewReader(outR).ReadString('\n')
	fmt.Println("metadata response:", strings.TrimSpace(second)[:120])
	bytes, err := readFileShareDelete(renamed)
	fmt.Println("mapped read err:", err, "head:", bytes[:min(len(bytes), 16)])
	_ = pw.Close()
	<-done
	fmt.Println("session done")
	fmt.Println("remove main:", os.Remove(source))
	fmt.Println("remove side.old:", os.Remove(renamed))
	fmt.Println("RemoveAll:", os.RemoveAll(dir))
	fmt.Println("dir exists:", pathExists(dir))
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

