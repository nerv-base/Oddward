// SPDX-License-Identifier: GPL-3.0-or-later

package gamedl_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameres"
)

var zeroTime time.Time

func identity(data []byte) gameres.Object {
	return gameres.Object{MD5: fmt.Sprintf("%x", md5.Sum(data)), Size: int64(len(data))}
}
func fixture(t *testing.T, data []byte, handler http.HandlerFunc) (*gamedl.Engine, gamedl.Task) {
	t.Helper()
	if handler == nil {
		handler = func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Accept-Encoding") != "identity" {
				t.Errorf("encoding=%q", r.Header.Get("Accept-Encoding"))
			}
			w.Header().Set("ETag", `"fixture"`)
			http.ServeContent(w, r, "fixture", zeroTime, bytes.NewReader(data))
		}
	}
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	e, err := gamedl.New(gamedl.Options{WorkRoot: t.TempDir(), StateRoot: t.TempDir(), Client: server.Client(), Resolve: func(context.Context, gameres.Source) ([]string, error) { return []string{server.URL + "/object"}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	obj := identity(data)
	return e, gamedl.Task{ID: "synthetic", Kind: gamedl.TaskPreloadFiles, ListHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Transfers: []gamedl.Transfer{{Object: obj, Source: gameres.Source{Kind: gameres.SourceCAS, Object: obj}}}}
}

func TestCASDownloadPublishesVerifiedContent(t *testing.T) {
	data := bytes.Repeat([]byte("synthetic CAS bytes"), 1000)
	e, task := fixture(t, data, nil)
	if err := e.Run(context.Background(), task, nil); err != nil {
		t.Fatalf("download: %v", err)
	}
	f, err := e.OpenBlob(context.Background(), identity(data))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("published blob differs from source")
	}
}
