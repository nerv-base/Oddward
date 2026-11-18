// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameres"
)

func TestMirrorSwitchChecksLengthAndDoesNotBorrowIfRange(t *testing.T) {
	data := bytes.Repeat([]byte("m"), 3<<20)
	obj := identity(data)
	var aFailures, bHeads, bGets atomic.Int32
	mirrorA := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=0-1048575" {
			aFailures.Add(1)
			w.WriteHeader(503)
			return
		}
		w.Header().Set("ETag", `"host-a"`)
		http.ServeContent(w, r, "fixture", zeroTime, bytes.NewReader(data))
	}))
	defer mirrorA.Close()
	mirrorB := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			bHeads.Add(1)
		} else {
			if bGets.Add(1) == 1 && r.Header.Get("If-Range") != "" {
				t.Errorf("borrowed host A validator %q", r.Header.Get("If-Range"))
			}
		}
		w.Header().Set("ETag", `"host-b"`)
		http.ServeContent(w, r, "fixture", zeroTime, bytes.NewReader(data))
	}))
	defer mirrorB.Close()
	e, err := gamedl.New(gamedl.Options{WorkRoot: t.TempDir(), StateRoot: t.TempDir(), Client: mirrorA.Client(), Resolve: func(context.Context, gameres.Source) ([]string, error) {
		return []string{mirrorA.URL, mirrorB.URL}, nil
	}, Parameters: gamedl.Parameters{ChunkSize: 1 << 20, ConnectionsPerFile: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	task := gamedl.Task{ID: "mirrors", Kind: gamedl.TaskPreloadFiles, ListHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Transfers: []gamedl.Transfer{{Object: obj, Source: gameres.Source{Kind: gameres.SourceCAS, Object: obj}}}}
	if err = e.Run(context.Background(), task, nil); err != nil {
		t.Fatal(err)
	}
	if aFailures.Load() != 3 || bHeads.Load() != 1 || bGets.Load() != 2 {
		t.Fatalf("A failures=%d B HEAD=%d B GET=%d", aFailures.Load(), bHeads.Load(), bGets.Load())
	}
	saved, err := e.Store().Load(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Transfers[0].Marks) != 2 {
		t.Fatalf("marks=%v", saved.Transfers[0].Marks)
	}
	assertBlob(t, e, obj, data)
}
func TestConditionalResumeAndChangedValidatorResetProgress(t *testing.T) {
	data := bytes.Repeat([]byte("v"), 3<<20)
	obj := identity(data)
	var stage atomic.Int32
	var conditional, full atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"version-one"`)
		if stage.Load() == 1 && r.Header.Get("If-Range") == `"version-one"` {
			conditional.Add(1)
			w.Header().Set("ETag", `"version-two"`)
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			w.WriteHeader(200)
			w.Write(data)
			return
		}
		if r.Header.Get("Range") == "" {
			full.Add(1)
		}
		http.ServeContent(w, r, "fixture", zeroTime, bytes.NewReader(data))
	}))
	defer server.Close()
	opts := gamedl.Options{WorkRoot: t.TempDir(), StateRoot: t.TempDir(), Client: server.Client(), Resolve: func(context.Context, gameres.Source) ([]string, error) { return []string{server.URL}, nil }, Parameters: gamedl.Parameters{ChunkSize: 1 << 20, ConnectionsPerFile: 1}}
	task := gamedl.Task{ID: "conditional", Kind: gamedl.TaskPreloadFiles, ListHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Transfers: []gamedl.Transfer{{Object: obj, Source: gameres.Source{Kind: gameres.SourceCAS, Object: obj}}}}
	e, err := gamedl.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ctx, cancel := context.WithCancel(context.Background())
	err = e.Run(ctx, task, func(p gamedl.Progress) {
		if p.Downloaded >= 1<<20 {
			cancel()
		}
	})
	cancel()
	requireKind(t, err, gamedl.ErrCancelled)
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	stage.Store(1)
	e, err = gamedl.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.Run(context.Background(), task, nil); err != nil {
		t.Fatal(err)
	}
	if conditional.Load() != 1 || full.Load() != 1 {
		t.Fatalf("conditional=%d sequential=%d", conditional.Load(), full.Load())
	}
	assertBlob(t, e, obj, data)
}
func TestRedirectToHTTPIsClassifiedAsHTTPFailure(t *testing.T) {
	e, task := fixture(t, []byte("data"), func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/forbidden", http.StatusFound)
	})
	requireKind(t, e.Run(context.Background(), task, nil), gamedl.ErrHTTP)
}
func TestAllowedHTTPSRedirectKeepsRangeAndIdentityChecks(t *testing.T) {
	data := []byte("redirected")
	var mu sync.Mutex
	var gotRange, gotEncoding string
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotRange = r.Header.Get("Range")
		gotEncoding = r.Header.Get("Accept-Encoding")
		mu.Unlock()
		http.ServeContent(w, r, "fixture", zeroTime, bytes.NewReader(data))
	}))
	defer target.Close()
	e, task := fixture(t, data, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) })
	if err := e.Run(context.Background(), task, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotRange != "bytes=0-9" || gotEncoding != "identity" {
		t.Fatalf("redirect headers: range=%q encoding=%q", gotRange, gotEncoding)
	}
	assertBlob(t, e, identity(data), data)
}
