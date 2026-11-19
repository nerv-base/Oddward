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

func posterJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var tinyMP4 = []byte("\x00\x00\x00\x14ftypisom\x00\x00\x00\x00isom")

func TestCachedReturnsLocalArtworkWithoutNetwork(t *testing.T) {
	dir := t.TempDir()
	posterPath := filepath.Join(dir, "poster")
	videoPath := filepath.Join(dir, "bg.mp4")
	if err := os.WriteFile(posterPath, posterJPEG(t, 16, 9), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(videoPath, tinyMP4, 0600); err != nil {
		t.Fatal(err)
	}
	noNetwork := transportFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("Cached used the network")
		return nil, nil
	})

	cached, err := New(dir, noNetwork).Cached()
	if err != nil || !bytes.Equal(cached.Video, tinyMP4) || cached.Poster == nil || cached.Poster.Bounds().Dx() != 16 {
		t.Fatalf("cached artwork: video=%d bytes, poster=%v, error=%v", len(cached.Video), cached.Poster != nil, err)
	}

	// Names left by the versioned cache are no longer referenced and are
	// removed before a new player can open one.
	legacy := filepath.Join(dir, "bg-old.mp4")
	if err := os.WriteFile(legacy, tinyMP4, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir, noNetwork).Cached(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("legacy versioned video was not removed")
	}

	// An upgrade with only a versioned cache keeps the video offline and
	// promotes it to the fixed name.
	upgraded := t.TempDir()
	if err := os.WriteFile(filepath.Join(upgraded, "bg-old.mp4"), tinyMP4, 0600); err != nil {
		t.Fatal(err)
	}
	cached, err = New(upgraded, noNetwork).Cached()
	if err != nil || !bytes.Equal(cached.Video, tinyMP4) {
		t.Fatalf("promoted video: video=%d bytes, error=%v", len(cached.Video), err)
	}
	if _, err := os.Stat(filepath.Join(upgraded, "bg.mp4")); err != nil {
		t.Fatal("legacy video was not promoted to bg.mp4")
	}
	if _, err := os.Stat(filepath.Join(upgraded, "bg-old.mp4")); !os.IsNotExist(err) {
		t.Fatal("promoted legacy video was not removed")
	}

	// A damaged video is simply absent; the still remains usable.
	if err := os.WriteFile(videoPath, []byte("not a video"), 0600); err != nil {
		t.Fatal(err)
	}
	cached, err = New(dir, noNetwork).Cached()
	if err != nil || len(cached.Video) != 0 || cached.Poster == nil {
		t.Fatalf("damaged video: video=%d bytes, poster=%v, error=%v", len(cached.Video), cached.Poster != nil, err)
	}

	// A damaged still is reported, but does not hide the usable video.
	if err := os.WriteFile(videoPath, tinyMP4, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(posterPath, []byte("not an image"), 0600); err != nil {
		t.Fatal(err)
	}
	cached, err = New(dir, noNetwork).Cached()
	if err == nil || !bytes.Equal(cached.Video, tinyMP4) || cached.Poster != nil {
		t.Fatalf("damaged poster: video=%d bytes, poster=%v, error=%v", len(cached.Video), cached.Poster != nil, err)
	}

	empty, err := New(t.TempDir(), noNetwork).Cached()
	if err != nil || len(empty.Video) != 0 || empty.Poster != nil {
		t.Fatalf("empty cache: %+v, error=%v", empty, err)
	}
}

func TestCachedPrefersPreview(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "poster"), posterJPEG(t, 64, 36), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "poster-preview.jpg"), posterJPEG(t, 32, 18), 0600); err != nil {
		t.Fatal(err)
	}
	noNetwork := transportFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("Cached used the network")
		return nil, nil
	})

	cached, err := New(dir, noNetwork).Cached()
	if err != nil || cached.Poster == nil || cached.Poster.Bounds().Dx() != 32 {
		t.Fatalf("preview: poster=%v, error=%v", cached.Poster, err)
	}

	// A damaged preview falls back to the full poster rather than hiding it.
	if err := os.WriteFile(filepath.Join(dir, "poster-preview.jpg"), []byte("not an image"), 0600); err != nil {
		t.Fatal(err)
	}
	cached, err = New(dir, noNetwork).Cached()
	if err != nil || cached.Poster == nil || cached.Poster.Bounds().Dx() != 64 {
		t.Fatalf("full poster fallback: poster=%v, error=%v", cached.Poster, err)
	}
}

func TestRefreshDownloadsOnlyDeclaredArtworkAndRetainsCache(t *testing.T) {
	posterBytes := posterJPEG(t, 16, 9)
	posterZIP := archive(t, "bg_0.jpg", posterBytes)
	video := tinyMP4
	videoZIP := archive(t, "bg.mp4", video)
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
			data = []byte("<All_Files>" + entry("bg_0.jpg", posterBytes, posterZIP) + entry("bg.mp4", wanted, zipped) + "<File Path=\"/unrelated.exe\"/></All_Files>")
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
	refresh := func(wantErr bool) ArtworkUpdate {
		t.Helper()
		update, err := New(dir, transport).Refresh(ctx)
		if (err != nil) != wantErr {
			t.Fatalf("background: update=%+v, error=%v", update, err)
		}
		return update
	}

	update := refresh(false)
	if !update.PosterChanged || !update.VideoChanged || update.Poster == nil || update.Poster.Bounds().Dx() != 16 || !bytes.Equal(update.Video, video) {
		t.Fatalf("first refresh: %+v, poster=%v", update, update.Poster != nil)
	}
	if want := []string{"/hd/publish_ob/launcher/Version.ini", manifest, assets + "bg_0.jpg.zip", assets + "bg.mp4.zip"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("requests: %v, want %v", calls, want)
	}
	videoPath := filepath.Join(dir, "bg.mp4")
	if data, err := os.ReadFile(videoPath); err != nil || !bytes.Equal(data, video) {
		t.Fatal("verified video is missing or changed")
	}
	if _, err := os.Stat(filepath.Join(dir, "poster-preview.jpg")); err != nil {
		t.Fatal("verified poster preview is missing")
	}
	if cached, err := New(dir, transport).Cached(); err != nil || !bytes.Equal(cached.Video, video) {
		t.Fatalf("cached video: %d bytes, error=%v", len(cached.Video), err)
	}

	// Unchanged files are not read into memory or replaced; only the two
	// discovery requests happen.
	calls = nil
	update = refresh(false)
	if update.PosterChanged || update.VideoChanged || update.Poster != nil || len(update.Video) != 0 {
		t.Fatalf("unchanged assets were returned: %+v", update)
	}
	if len(calls) != 2 {
		t.Fatalf("unchanged assets were downloaded again: %v", calls)
	}

	// A new still is the only delta; the cached video keeps playing.
	posterBytes = posterJPEG(t, 32, 18)
	posterZIP = archive(t, "bg_0.jpg", posterBytes)
	calls = nil
	update = refresh(false)
	if !update.PosterChanged || update.Poster == nil || update.Poster.Bounds().Dx() != 32 || update.VideoChanged || len(update.Video) != 0 {
		t.Fatalf("still delta: %+v, poster=%v", update, update.Poster != nil)
	}
	if want := []string{"/hd/publish_ob/launcher/Version.ini", manifest, assets + "bg_0.jpg.zip"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("requests: %v, want %v", calls, want)
	}
	if data, err := os.ReadFile(videoPath); err != nil || !bytes.Equal(data, video) {
		t.Fatal("unchanged video was replaced")
	}

	// A new video replaces bg.mp4 atomically; the old player reads its bytes in
	// memory, so no file handle blocks the rename.
	video = append(bytes.Clone(video), 'x')
	videoZIP = archive(t, "bg.mp4", video)
	calls = nil
	update = refresh(false)
	if !update.VideoChanged || len(update.Video) == 0 || bytes.Equal(update.Video, tinyMP4) || update.PosterChanged || update.Poster != nil {
		t.Fatalf("video delta: %+v", update)
	}
	if want := []string{"/hd/publish_ob/launcher/Version.ini", manifest, assets + "bg.mp4.zip"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("requests: %v, want %v", calls, want)
	}
	if data, err := os.ReadFile(videoPath); err != nil || !bytes.Equal(data, video) {
		t.Fatal("new video was not stored as bg.mp4")
	}

	// A bad archive loses only the changing asset; the cached video survives.
	badArchive = true
	update, err := New(dir, transport).Refresh(ctx)
	if err == nil || update.VideoChanged || update.PosterChanged {
		t.Fatalf("broken archive: %+v, error=%v", update, err)
	}
	if data, err := os.ReadFile(videoPath); err != nil || !bytes.Equal(data, video) {
		t.Fatal("broken archive replaced the cached video")
	}

	offline = true
	if _, err := New(dir, transport).Refresh(ctx); err == nil {
		t.Fatal("offline refresh succeeded")
	}
	if data, err := os.ReadFile(videoPath); err != nil || !bytes.Equal(data, video) {
		t.Fatal("offline refresh replaced the cached video")
	}
}

func TestRefreshRejectsRedirectToUnrelatedHost(t *testing.T) {
	requests := 0
	transport := transportFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://example.com/hd/publish_ob/launcher/Version.ini"}}, Body: http.NoBody}, nil
	})
	update, err := New(t.TempDir(), transport).Refresh(context.Background())
	if err == nil || requests != 1 || len(update.Video) != 0 || update.Poster != nil {
		t.Fatalf("escaped official CDN: requests=%d, update=%+v, error=%v", requests, update, err)
	}
}
