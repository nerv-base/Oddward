package launcherfiles

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type zipEntry struct {
	path string
	body []byte
	zip  []byte
}

func buildComponent(t *testing.T, path string, body []byte) zipEntry {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(strings.TrimPrefix(path, "/"))
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return zipEntry{path: path, body: body, zip: buf.Bytes()}
}

func testMD5(data []byte) string {
	sum := md5.Sum(data)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func xmlManifest(baseURL string, entries []zipEntry) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString(`<All_Files><BuildInfo/><ProductVersion Version="1.0.7.1102_1"/>` + "\n")
	b.WriteString(fmt.Sprintf(`<Url BaseUrl="%s"/>`+"\n", baseURL))
	for _, e := range entries {
		b.WriteString(fmt.Sprintf(`<File Checksum="%s" Path="%s" Size="%d" ZipChecksum="%s" ZipSize="%d"/>`+"\n",
			testMD5(e.body), e.path, len(e.body), testMD5(e.zip), len(e.zip)))
	}
	b.WriteString("</All_Files>\n")
	return b.String()
}

type componentServer struct {
	mu      sync.Mutex
	entries map[string]zipEntry
	zipHits int
	srv     *httptest.Server
}

func newComponentServer(t *testing.T, entries []zipEntry) *componentServer {
	t.Helper()
	cs := &componentServer{entries: map[string]zipEntry{}}
	mux := http.NewServeMux()
	var baseURL string
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		cs.mu.Lock()
		defer cs.mu.Unlock()
		switch {
		case r.URL.Path == "/Version.ini":
			fmt.Fprintf(w, "[VERSION]\nVersion=1.0.7.1102\nBuild=ce6bd117\nFileListURL=%s/AllFiles.xml\n", baseURL)
		case r.URL.Path == "/AllFiles.xml":
			list := make([]zipEntry, 0, len(entries))
			list = append(list, entries...)
			fmt.Fprint(w, xmlManifest(baseURL, list))
		case strings.HasSuffix(r.URL.Path, ".zip"):
			e, ok := cs.entries[strings.TrimSuffix(r.URL.Path, ".zip")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			cs.zipHits++
			w.Write(e.zip)
		default:
			http.NotFound(w, r)
		}
	})
	cs.srv = httptest.NewServer(mux)
	baseURL = cs.srv.URL
	t.Cleanup(cs.srv.Close)
	for _, e := range entries {
		cs.entries[e.path] = e
	}
	return cs
}

func requiredEntries(t *testing.T) []zipEntry {
	t.Helper()
	out := make([]zipEntry, 0, len(Required))
	for i, path := range Required {
		body := []byte(fmt.Sprintf("component %d %s\n", i, path))
		if strings.HasSuffix(path, ".exe") || strings.HasSuffix(path, ".dll") {
			body = bytes.Repeat([]byte{byte(i + 1)}, 2048)
		}
		out = append(out, buildComponent(t, path, body))
	}
	return out
}

func TestFetchInstallsAndReusesComponents(t *testing.T) {
	entries := requiredEntries(t)
	cs := newComponentServer(t, entries)
	root := t.TempDir()

	ctx := context.Background()
	m, err := Fetch(ctx, cs.srv.Client(), cs.srv.URL+"/Version.ini")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	files, err := m.Select()
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if len(files) != len(Required) {
		t.Fatalf("selected %d files, want %d", len(files), len(Required))
	}
	total := TotalSize(files)
	if total <= 0 {
		t.Fatal("empty total")
	}

	var seen []int64
	err = Install(ctx, cs.srv.Client(), root, m, files, func(done, gotTotal int64) {
		if gotTotal != total {
			t.Fatalf("progress total = %d, want %d", gotTotal, total)
		}
		seen = append(seen, done)
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if len(seen) < 2 || seen[len(seen)-1] != total {
		t.Fatalf("progress = %v, want final %d", seen, total)
	}
	if !Present(root, files) {
		t.Fatal("components not present after install")
	}
	for _, e := range entries {
		got, err := os.ReadFile(filepath.Join(root, "NTELauncher", filepath.FromSlash(strings.TrimPrefix(e.path, "/"))))
		if err != nil {
			t.Fatalf("read %s: %v", e.path, err)
		}
		if !bytes.Equal(got, e.body) {
			t.Fatalf("%s content mismatch", e.path)
		}
	}
	pointer, err := os.ReadFile(filepath.Join(root, "Client", "WindowsNoEditor", "HT", "Binaries", "Win64", "GameLauncher.config"))
	if err != nil {
		t.Fatalf("read pointer: %v", err)
	}
	want := filepath.ToSlash(filepath.Join(root, "NTELauncher", "WPGameClientSDK_64.dll"))
	if strings.TrimSpace(string(pointer)) != want {
		t.Fatalf("pointer = %q, want %q", pointer, want)
	}

	before := cs.zipHits
	if err := Install(ctx, cs.srv.Client(), root, m, files, nil); err != nil {
		t.Fatalf("second install: %v", err)
	}
	if cs.zipHits != before {
		t.Fatalf("reinstall downloaded %d zips, want 0", cs.zipHits-before)
	}
}

func TestInstallRejectsBadChecksum(t *testing.T) {
	entries := requiredEntries(t)
	bad := entries[0]
	bad.path = "/NTEGame.exe"
	// Advertise a checksum that cannot match, and keep every other file valid.
	cs := newComponentServer(t, entries)
	root := t.TempDir()
	m, err := Fetch(context.Background(), cs.srv.Client(), cs.srv.URL+"/Version.ini")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	for i := range m.Files {
		if m.Files[i].Path == "/NTEGame.exe" {
			m.Files[i].MD5 = strings.Repeat("0", 32)
		}
	}
	files, err := m.Select()
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if err := Install(context.Background(), cs.srv.Client(), root, m, files, nil); err == nil {
		t.Fatal("install accepted a bad checksum")
	}
	if _, err := os.Stat(filepath.Join(root, "NTELauncher", "NTEGame.exe")); !os.IsNotExist(err) {
		t.Fatalf("bad file was installed: %v", err)
	}
}

func TestMalformedManifestIsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Version.ini" {
			fmt.Fprintf(w, "[VERSION]\nVersion=1\nFileListURL=%s/AllFiles.xml\n", "http://"+r.Host)
			return
		}
		fmt.Fprint(w, `<All_Files><Url BaseUrl="http://x"/></All_Files>`)
	}))
	defer srv.Close()
	if _, err := Fetch(context.Background(), srv.Client(), srv.URL+"/Version.ini"); err == nil {
		t.Fatal("manifest without Version was accepted")
	}
}
