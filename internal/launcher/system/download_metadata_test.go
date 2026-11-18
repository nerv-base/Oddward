// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDownloadMetadataFallsBackOnlyToConfiguredRoots(t *testing.T) {
	var outsideCalls int
	outside := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outsideCalls++
	}))
	defer outside.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/first/PC_150/Version/Windows/config.xml":
			http.Redirect(w, r, outside.URL, http.StatusFound)
		case "/second/PC_150/Version/Windows/config.xml":
			if r.Header.Get("Accept-Encoding") != "identity" {
				t.Errorf("Accept-Encoding = %q", r.Header.Get("Accept-Encoding"))
			}
			w.Header().Set("ETag", `"generation"`)
			_, _ = w.Write([]byte(`<config><ResVersion>1.5.6</ResVersion><Extra><listHash>d41d8cd98f00b204e9800998ecf8427e</listHash></Extra></config>`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m := remoteDownloads{client: srv.Client(), roots: []string{srv.URL + "/first", srv.URL + "/second"}}
	cfg, res, err := m.config(context.Background(), "PC_150")
	if err != nil || cfg.ResVersion != "1.5.6" || res.Root != m.roots[1] || res.ETag != `"generation"` || outsideCalls != 0 {
		t.Fatalf("config = %+v, metadata = %+v, error = %v, outside calls = %d", cfg, res, err, outsideCalls)
	}
}

func TestDownloadMetadataRejectsEncodedAndOverLimit(t *testing.T) {
	for _, name := range []string{"encoded", "large", "missing"} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch name {
				case "encoded":
					w.Header().Set("Content-Encoding", "gzip")
				case "missing":
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write([]byte("0123456789"))
			}))
			defer srv.Close()
			m := remoteDownloads{client: srv.Client(), roots: []string{srv.URL}}
			_, err := m.get(context.Background(), srv.URL, srv.URL+"/metadata", 5)
			if err == nil || errors.Is(err, errDownloadNotFound) != (name == "missing") {
				t.Fatalf("failure = %v", err)
			}
		})
	}
}
