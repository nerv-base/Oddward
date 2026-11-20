// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameres"
)

func TestPauseReopenResumesOnlyDurableChunks(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 3*(1<<16))
	obj := identity(data)
	var mu sync.Mutex
	requests := map[string]int{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests[r.Header.Get("Range")]++
		mu.Unlock()
		http.ServeContent(w, r, "object", zeroTime, bytes.NewReader(data))
	}))
	defer server.Close()
	work, state := t.TempDir(), t.TempDir()
	db := testDB(t)
	opts := gamedl.Options{DB: db, WorkRoot: work, StateRoot: state, Client: server.Client(), Resolve: func(context.Context, gameres.Source) ([]string, error) { return []string{server.URL}, nil }, Parameters: gamedl.Parameters{ChunkSize: 1 << 20, ConnectionsPerFile: 1, FilesInFlight: 1}}
	task := gamedl.Task{ID: "resume", Kind: gamedl.TaskPreloadFiles, ListHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Transfers: []gamedl.Transfer{{Object: obj, Source: gameres.Source{Kind: gameres.SourceCAS, Object: obj}}}}
	e, err := gamedl.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = e.Run(ctx, task, func(p gamedl.Progress) {
		if p.Downloaded >= 1<<20 {
			cancel()
		}
	})
	if err == nil {
		t.Fatal("pause did not stop task after durable first chunk")
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	e, err = gamedl.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.Run(context.Background(), task, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	first := requests["bytes=0-1048575"]
	mu.Unlock()
	if first != 1 {
		t.Fatalf("first chunk requested %d times after restart, want 1", first)
	}
	f, err := e.OpenBlob(context.Background(), obj)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}
