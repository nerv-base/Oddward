// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameres"
)

func TestNetworkActiveDurationExcludesVerification(t *testing.T) {
	data := []byte("duration")
	e, task := fixture(t, data, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(25 * time.Millisecond)
		http.ServeContent(w, r, "fixture", zeroTime, bytes.NewReader(data))
	})
	var verifying, staged time.Duration
	if err := e.Run(context.Background(), task, func(p gamedl.Progress) {
		if p.Stage == "verifying" {
			verifying = p.NetworkActiveDuration
			time.Sleep(50 * time.Millisecond)
		}
		if p.Stage == "staged" {
			staged = p.NetworkActiveDuration
		}
	}); err != nil {
		t.Fatal(err)
	}
	if verifying < 20*time.Millisecond {
		t.Fatalf("HTTP active duration missing: %v", verifying)
	}
	if staged != verifying {
		t.Fatalf("verification counted as network time: verifying=%v staged=%v", verifying, staged)
	}
}

func TestProgressReportsLiveSpeedBeforeChunkCommits(t *testing.T) {
	// Serve one 2 MiB chunk in two 1 MiB halves separated by >250ms so addIO
	// can emit live network throughput before the 2 MiB chunk commits.
	data := bytes.Repeat([]byte("live-speed-bytes"), 1<<17) // 2 MiB
	obj := identity(data)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(data)-1, len(data)))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		w.WriteHeader(http.StatusPartialContent)
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write(data[:1<<20])
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write(data[1<<20:])
	}))
	defer server.Close()
	e, err := gamedl.New(gamedl.Options{
		DB:         testDB(t),
		WorkRoot:   t.TempDir(),
		StateRoot:  t.TempDir(),
		Client:     server.Client(),
		Resolve:    func(context.Context, gameres.Source) ([]string, error) { return []string{server.URL}, nil },
		Parameters: gamedl.Parameters{ChunkSize: 2 << 20, ConnectionsPerFile: 1, FilesInFlight: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	task := gamedl.Task{
		ID:        "live-speed",
		Kind:      gamedl.TaskPreloadFiles,
		ListHash:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Transfers: []gamedl.Transfer{{Object: obj, Source: gameres.Source{Kind: gameres.SourceCAS, Object: obj}}},
	}
	var sawMidChunkSpeed bool
	if err := e.Run(context.Background(), task, func(p gamedl.Progress) {
		if p.Stage == "downloading" && p.Downloaded == 0 && p.NetworkBytes >= 1<<20 && p.BytesPerSecond > 0 {
			sawMidChunkSpeed = true
		}
	}); err != nil {
		t.Fatal(err)
	}
	if !sawMidChunkSpeed {
		t.Fatal("expected live BytesPerSecond progress event before the 2 MiB chunk committed")
	}
}
