// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameres"
)

type repeatedByte byte

func (r repeatedByte) ReadAt(p []byte, off int64) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}
func TestSyntheticGiBBoundedMemory(t *testing.T) {
	if os.Getenv("ODDWARD_GAMEDL_LARGE") != "1" {
		t.Skip("set ODDWARD_GAMEDL_LARGE=1 for the 1 GiB synthetic resource test")
	}
	const size int64 = 256 << 20
	objects := make([]gameres.Object, 4)
	for i := range objects {
		hash := md5.New()
		buf := bytes.Repeat([]byte{byte('a' + i)}, 256<<10)
		for n := int64(0); n < size; n += int64(len(buf)) {
			hash.Write(buf)
		}
		objects[i] = gameres.Object{MD5: hex.EncodeToString(hash.Sum(nil)), Size: size}
	}
	var active, peak atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		i, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		if err != nil || i < 0 || i >= 4 {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, "fixture", zeroTime, io.NewSectionReader(repeatedByte('a'+i), 0, size))
	}))
	defer server.Close()
	e, err := gamedl.New(gamedl.Options{DB: testDB(t), WorkRoot: t.TempDir(), StateRoot: t.TempDir(), Client: server.Client(), Resolve: func(_ context.Context, s gameres.Source) ([]string, error) {
		for i, obj := range objects {
			if obj == s.Object {
				return []string{fmt.Sprintf("%s/%d", server.URL, i)}, nil
			}
		}
		return nil, fmt.Errorf("unknown synthetic object")
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	task := gamedl.Task{ID: "gib", Kind: gamedl.TaskPreloadFiles, ListHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	for _, obj := range objects {
		task.Transfers = append(task.Transfers, gamedl.Transfer{Object: obj, Source: gameres.Source{Kind: gameres.SourceCAS, Object: obj}})
	}
	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)
	var peakHeap uint64
	peakGoroutines := 0
	var final gamedl.Progress
	if err = e.Run(context.Background(), task, func(p gamedl.Progress) {
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		peakHeap = max(peakHeap, stats.HeapAlloc)
		peakGoroutines = max(peakGoroutines, runtime.NumGoroutine())
		final = p
	}); err != nil {
		t.Fatal(err)
	}
	if peakHeap > baseline.HeapAlloc+(64<<20) {
		t.Fatalf("heap growth %.1f MiB exceeds 64 MiB", float64(peakHeap-baseline.HeapAlloc)/(1<<20))
	}
	if peak.Load() > 16 || peakGoroutines > 128 {
		t.Fatalf("unbounded workers: HTTP=%d goroutines=%d", peak.Load(), peakGoroutines)
	}
	if final.Downloaded != 4*size || final.NetworkBytes != 4*size || final.WrittenBytes != 4*size {
		t.Fatalf("real byte counters=%+v", final)
	}
	for _, obj := range objects {
		f, err := e.OpenBlob(context.Background(), obj)
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	t.Logf("synthetic 1 GiB verified: peak heap growth %.1f MiB, HTTP %d, goroutines %d", float64(peakHeap-baseline.HeapAlloc)/(1<<20), peak.Load(), peakGoroutines)
}
