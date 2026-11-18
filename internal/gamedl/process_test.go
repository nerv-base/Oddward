// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameres"
)

func crashTask() (gamedl.Task, []byte) {
	data := bytes.Repeat([]byte("process-recovery"), 1<<19)
	obj := identity(data)
	return gamedl.Task{ID: "crash", Kind: gamedl.TaskPreloadFiles, ListHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Transfers: []gamedl.Transfer{{Object: obj, Source: gameres.Source{Kind: gameres.SourceCAS, Object: obj}}}}, data
}
func TestCrashWorker(t *testing.T) {
	if os.Getenv("ODDWARD_GAMEDL_CRASH_WORKER") == "" {
		t.Skip("child process helper")
	}
	cert, err := base64.StdEncoding.DecodeString(os.Getenv("ODDWARD_GAMEDL_CERT"))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(cert)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	e, err := gamedl.New(gamedl.Options{WorkRoot: os.Getenv("ODDWARD_GAMEDL_WORK"), StateRoot: os.Getenv("ODDWARD_GAMEDL_STATE"), Client: client, Resolve: func(context.Context, gameres.Source) ([]string, error) {
		return []string{os.Getenv("ODDWARD_GAMEDL_URL")}, nil
	}, Parameters: gamedl.Parameters{ChunkSize: 1 << 20, ConnectionsPerFile: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	task, _ := crashTask()
	if err = e.Run(context.Background(), task, func(p gamedl.Progress) {
		if p.Downloaded >= 1<<20 {
			fmt.Println("durable")
			select {}
		}
	}); err != nil {
		t.Fatal(err)
	}
}
func TestKilledProcessReleasesLockAndResumesDurableChunk(t *testing.T) {
	task, data := crashTask()
	var mu sync.Mutex
	requests := map[string]int{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests[r.Header.Get("Range")]++
		mu.Unlock()
		http.ServeContent(w, r, "fixture", zeroTime, bytes.NewReader(data))
	}))
	defer server.Close()
	work, state := t.TempDir(), t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestCrashWorker$", "-test.timeout=40s")
	cmd.Env = append(os.Environ(), "ODDWARD_GAMEDL_CRASH_WORKER=1", "ODDWARD_GAMEDL_CERT="+base64.StdEncoding.EncodeToString(server.Certificate().Raw), "ODDWARD_GAMEDL_WORK="+work, "ODDWARD_GAMEDL_STATE="+state, "ODDWARD_GAMEDL_URL="+server.URL)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	killed := false
	defer func() {
		if !killed {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	ready := make(chan bool, 1)
	go func() {
		scan := bufio.NewScanner(out)
		for scan.Scan() {
			if scan.Text() == "durable" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatalf("child did not reach durable chunk: %s", stderr.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("child checkpoint timeout")
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	killed = true
	e, err := gamedl.New(gamedl.Options{WorkRoot: work, StateRoot: state, Client: server.Client(), Resolve: func(context.Context, gameres.Source) ([]string, error) { return []string{server.URL}, nil }, Parameters: gamedl.Parameters{ChunkSize: 1 << 20, ConnectionsPerFile: 1}})
	if err != nil {
		t.Fatalf("kernel lock survived process death: %v", err)
	}
	defer e.Close()
	recovered, err := e.Recover(context.Background(), task.ListHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].Transfers[0].BytesDone != 1<<20 {
		t.Fatalf("durable checkpoint missing: %+v", recovered)
	}
	remaining, err := e.Remaining(context.Background(), recovered[0].Transfers)
	if err != nil || remaining != int64(len(data))-(1<<20) {
		t.Fatalf("recovered remaining=%d err=%v", remaining, err)
	}
	if err = e.Run(context.Background(), recovered[0], nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	first := requests["bytes=0-1048575"]
	mu.Unlock()
	if first != 1 {
		t.Fatalf("durable first chunk requested %d times", first)
	}
	assertBlob(t, e, task.Transfers[0].Object, data)
}
