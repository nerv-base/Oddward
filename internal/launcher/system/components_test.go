package system

import (
	"archive/zip"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nerv-base/Oddward/internal/launcherfiles"
)

// fakeComponentManifest serves a minimal official-launcher manifest so tests
// never reach the vendor CDN.
func fakeComponentManifest(t *testing.T) string {
	t.Helper()
	type entry struct {
		path string
		body []byte
		zip  []byte
	}
	entries := make([]entry, 0, len(launcherfiles.Required))
	for i, path := range launcherfiles.Required {
		body := []byte(fmt.Sprintf("component %d %s", i, path))
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, err := zw.Create(strings.TrimPrefix(path, "/"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry{path: path, body: body, zip: buf.Bytes()})
	}
	var base string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/Version.ini":
			fmt.Fprintf(w, "[VERSION]\nVersion=1.0.7.1102\nFileListURL=%s/AllFiles.xml\n", base)
		case r.URL.Path == "/AllFiles.xml":
			fmt.Fprintf(w, `<All_Files><ProductVersion Version="1.0.7.1102_1"/><Url BaseUrl="%s"/>`+"\n", base)
			for _, e := range entries {
				fmt.Fprintf(w, `<File Checksum="%s" Path="%s" Size="%d" ZipChecksum="%s" ZipSize="%d"/>`+"\n",
					sum(e.body), e.path, len(e.body), sum(e.zip), len(e.zip))
			}
			fmt.Fprint(w, "</All_Files>")
		case strings.HasSuffix(r.URL.Path, ".zip"):
			for _, e := range entries {
				if e.path+".zip" == r.URL.Path {
					w.Write(e.zip)
					return
				}
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	base = srv.URL
	t.Cleanup(srv.Close)
	return srv.URL + "/Version.ini"
}

func sum(data []byte) string {
	s := md5.Sum(data)
	return strings.ToUpper(hex.EncodeToString(s[:]))
}
