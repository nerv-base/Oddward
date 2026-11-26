// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
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

func TestDownloadMetadataVersionedDeliverySurvivesBranchAdvancement(t *testing.T) {
	manifest := []byte(fmt.Sprintf(`<ResList version="1.5.0" tag="baseTag"><Res filename="HT/Content/Paks/a.pak" md5="%x" filesize="3"/></ResList>`, md5.Sum([]byte("new"))))
	listHash := fmt.Sprintf("%x", md5.Sum(manifest))
	zipData := preloadSyntheticArchive(t, manifest)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/clientRes/PC_150/Version/Windows/config.xml":
			// Branch head has moved on to 1.5.8.
			fmt.Fprintf(w, `<config><ResVersion>1.5.8</ResVersion><Extra><listHash>%s</listHash></Extra></config>`, listHash)
		case "/clientRes/PC_150/Version/Windows/version/1.5.0/config.xml":
			fmt.Fprintf(w, `<config><ResVersion>1.5.0</ResVersion><Extra><listHash>%s</listHash></Extra></config>`, listHash)
		case "/clientRes/PC_150/Version/Windows/version/1.5.0/ResList.bin.zip":
			_, _ = w.Write(zipData)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m := remoteDownloads{client: srv.Client(), roots: []string{srv.URL + "/clientRes"}}
	got, err := m.delivery(context.Background(), "PC_150", "1.5.0")
	if err != nil || got.Branch != "PC_150" || got.Config.ResVersion != "1.5.0" || got.ConfigResponse.URL != srv.URL+"/clientRes/PC_150/Version/Windows/version/1.5.0/config.xml" {
		t.Fatalf("versioned delivery = %+v, err = %v", got, err)
	}
}

func TestDownloadMetadataFormalFollowsResConfigAndRejectsLoopsOrForeignApp(t *testing.T) {
	manifest := []byte(fmt.Sprintf(`<ResList version="1.5.3" tag="baseTag"><Res filename="HT/Content/Paks/a.pak" md5="%x" filesize="3"/></ResList>`, md5.Sum([]byte("new"))))
	listHash := fmt.Sprintf("%x", md5.Sum(manifest))
	zipData := preloadSyntheticArchive(t, manifest)
	enc := func(raw string) string { return base64.StdEncoding.EncodeToString([]byte(raw)) }
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/clientRes/PC_140/Version/Windows/config.xml":
			fmt.Fprintf(w, `<config><ResVersion>1.4.18</ResVersion><PreReleaseStatus>end</PreReleaseStatus><PreReleaseBranch>PC_150</PreReleaseBranch><PreReleaseVersion>1.5.0</PreReleaseVersion><ResConfig>%s</ResConfig><Extra><listHash>%s</listHash></Extra></config>`,
				enc(`{"appId":"1289","branchName":"PC_150","updateBranchName":"publish_Updata","gameResUrl":["https://evil.invalid/clientRes"]}`), listHash)
		case "/clientRes/PC_150/Version/Windows/config.xml":
			fmt.Fprintf(w, `<config><ResVersion>1.5.3</ResVersion><Extra><listHash>%s</listHash></Extra></config>`, listHash)
		case "/clientRes/PC_150/Version/Windows/version/1.5.3/ResList.bin.zip":
			_, _ = w.Write(zipData)
		case "/clientRes/LOOP_A/Version/Windows/config.xml":
			fmt.Fprintf(w, `<config><ResVersion>1.0.0</ResVersion><ResConfig>%s</ResConfig><Extra><listHash>%s</listHash></Extra></config>`,
				enc(`{"appId":"1289","branchName":"LOOP_B"}`), listHash)
		case "/clientRes/LOOP_B/Version/Windows/config.xml":
			fmt.Fprintf(w, `<config><ResVersion>1.0.0</ResVersion><ResConfig>%s</ResConfig><Extra><listHash>%s</listHash></Extra></config>`,
				enc(`{"appId":"1289","branchName":"LOOP_A"}`), listHash)
		case "/clientRes/FOREIGN/Version/Windows/config.xml":
			fmt.Fprintf(w, `<config><ResVersion>1.0.0</ResVersion><ResConfig>%s</ResConfig><Extra><listHash>%s</listHash></Extra></config>`,
				enc(`{"appId":"9999","branchName":"PC_150"}`), listHash)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m := remoteDownloads{client: srv.Client(), roots: []string{srv.URL + "/clientRes"}}
	cfg, res, branch, err := m.formalConfig(context.Background(), "PC_140")
	if err != nil || branch != "PC_150" || cfg.ResVersion != "1.5.3" || res.Root != srv.URL+"/clientRes" {
		t.Fatalf("formalConfig = %+v %+v %q err=%v", cfg, res, branch, err)
	}
	deliv, err := m.formalDelivery(context.Background(), "PC_140")
	if err != nil || deliv.Branch != "PC_150" || deliv.Config.ResVersion != "1.5.3" {
		t.Fatalf("formalDelivery = %+v err=%v", deliv, err)
	}
	if _, _, _, err := m.formalConfig(context.Background(), "LOOP_A"); err == nil {
		t.Fatal("accepted redirect loop")
	}
	if _, _, _, err := m.formalConfig(context.Background(), "FOREIGN"); err == nil {
		t.Fatal("accepted foreign appId redirect")
	}
}

func TestDownloadMetadataConfigAddsCacheBustingQueryAndPreservesCanonicalURL(t *testing.T) {
	manifest := []byte(fmt.Sprintf(`<ResList version="1.5.8" tag="baseTag"><Res filename="HT/Content/Paks/a.pak" md5="%x" filesize="3"/></ResList>`, md5.Sum([]byte("new"))))
	listHash := fmt.Sprintf("%x", md5.Sum(manifest))
	zipData := preloadSyntheticArchive(t, manifest)
	var headQuery, headCacheControl, versionQuery string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/clientRes/PC_150/Version/Windows/config.xml":
			headQuery = r.URL.Query().Get("tValue")
			headCacheControl = r.Header.Get("Cache-Control")
			fmt.Fprintf(w, `<config><ResVersion>1.5.8</ResVersion><Extra><listHash>%s</listHash></Extra></config>`, listHash)
		case "/clientRes/PC_150/Version/Windows/version/1.5.8/config.xml":
			versionQuery = r.URL.RawQuery
			fmt.Fprintf(w, `<config><ResVersion>1.5.8</ResVersion><Extra><listHash>%s</listHash></Extra></config>`, listHash)
		case "/clientRes/PC_150/Version/Windows/version/1.5.8/ResList.bin.zip":
			_, _ = w.Write(zipData)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m := remoteDownloads{client: srv.Client(), roots: []string{srv.URL + "/clientRes"}}
	cfg, res, err := m.config(context.Background(), "PC_150")
	if err != nil || cfg.ResVersion != "1.5.8" {
		t.Fatalf("config = %+v err=%v", cfg, err)
	}
	if headQuery == "" {
		t.Fatal("mutable branch config.xml request missing tValue cache-busting query")
	}
	if headCacheControl != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", headCacheControl)
	}
	if res.URL != srv.URL+"/clientRes/PC_150/Version/Windows/config.xml" {
		t.Fatalf("provenance URL polluted by query: %q", res.URL)
	}
	got, err := m.delivery(context.Background(), "PC_150", "1.5.8")
	if err != nil || got.Config.ResVersion != "1.5.8" {
		t.Fatalf("versioned delivery = %+v err=%v", got, err)
	}
	if versionQuery != "" {
		t.Fatalf("immutable versioned config.xml had unexpected query %q", versionQuery)
	}
}
