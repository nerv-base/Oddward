// SPDX-License-Identifier: GPL-3.0-or-later

package launchmedia

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func archive(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, err := z.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRefreshDownloadsOnlyDeclaredArtworkAndRetainsCache(t *testing.T) {
	var poster bytes.Buffer
	if err := jpeg.Encode(&poster, image.NewRGBA(image.Rect(0, 0, 16, 9)), nil); err != nil {
		t.Fatal(err)
	}
	video := []byte("\x00\x00\x00\x14ftypisom\x00\x00\x00\x00isom")
	videoZIP, posterZIP := archive(t, "bg.mp4", video), archive(t, "bg_0.jpg", poster.Bytes())
	const manifest = "/hd/publish_ob/launcher/2.0_1/AllFiles.xml"
	const assets = "/hd/publish_ob/launcher/2.0_1/ResFilesM/1289/bgimgs/"
	entry := func(name string, raw, zipped []byte) string {
		return fmt.Sprintf(`<File Path="/ResFilesM/1289/bgimgs/%s" Size="%d" ZipSize="%d" Checksum="%x" ZipChecksum="%x"/>`, name, len(raw), len(zipped), md5.Sum(raw), md5.Sum(zipped))
	}
	var calls []string
	var offline, badArchive bool
	transport := transportFunc(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Path)
		if offline {
			return nil, fmt.Errorf("offline")
		}
		var data []byte
		switch r.URL.Path {
		case "/hd/publish_ob/launcher/Version.ini":
			data = []byte("[VERSION]\nFileListURL=https://yhcdn1.wmupd.com" + manifest + "\n")
		case manifest:
			wanted := video
			zipped := videoZIP
			if badArchive {
				wanted = append(bytes.Clone(video), 'x')
				zipped = archive(t, "../bg.mp4", wanted)
			}
			data = []byte("<All_Files>" + entry("bg_0.jpg", poster.Bytes(), posterZIP) + entry("bg.mp4", wanted, zipped) + "<File Path=\"/unrelated.exe\"/></All_Files>")
		case assets + "bg.mp4.zip":
			data = videoZIP
			if badArchive {
				data = archive(t, "../bg.mp4", append(bytes.Clone(video), 'x'))
			}
		case assets + "bg_0.jpg.zip":
			data = posterZIP
		default:
			t.Fatalf("unexpected fetch: %s", r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data))}, nil
	})
	dir := t.TempDir()
	ctx := context.Background()
	load := func(wantErr bool) {
		t.Helper()
		path, art, err := New(dir, transport).Load(ctx)
		if (err != nil) != wantErr || filepath.Base(path) != "bg.mp4" || art == nil || art.Bounds().Dx() != 16 {
			t.Fatalf("background: path=%q, poster=%v, error=%v", path, art != nil, err)
		}
		if bytes, err := os.ReadFile(path); err != nil || string(bytes) != string(video) {
			t.Fatal("verified video is missing or changed")
		}
	}
	load(false)
	if want := []string{"/hd/publish_ob/launcher/Version.ini", manifest, assets + "bg_0.jpg.zip", assets + "bg.mp4.zip"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("requests: %v, want %v", calls, want)
	}
	calls = nil
	load(false)
	if len(calls) != 2 {
		t.Fatalf("unchanged assets were downloaded again: %v", calls)
	}
	badArchive = true
	load(true)
	offline = true
	load(true)
}

func TestRefreshRejectsRedirectToUnrelatedHost(t *testing.T) {
	requests := 0
	transport := transportFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://example.com/hd/publish_ob/launcher/Version.ini"}}, Body: http.NoBody}, nil
	})
	video, poster, err := New(t.TempDir(), transport).Load(context.Background())
	if err == nil || requests != 1 || video != "" || poster != nil {
		t.Fatalf("escaped official CDN: requests=%d, video=%q, poster=%v, error=%v", requests, video, poster != nil, err)
	}
}
