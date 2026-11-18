// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameres"
)

func requireKind(t *testing.T, err error, kind gamedl.ErrorKind) {
	t.Helper()
	var te *gamedl.TaskError
	if !errors.As(err, &te) || te.Kind != kind {
		t.Fatalf("error=%v, want kind=%d", err, kind)
	}
}
func assertBlob(t *testing.T, e *gamedl.Engine, obj gameres.Object, data []byte) {
	t.Helper()
	f, err := e.OpenBlob(context.Background(), obj)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := io.ReadAll(f)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("blob differs: %v", err)
	}
}

func TestRangeFallbackJoinsWorkersBeforeSequentialDownload(t *testing.T) {
	data := bytes.Repeat([]byte("concurrent-fallback"), 200000)
	var ranges, full atomic.Int32
	e, task := fixture(t, data, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			ranges.Add(1)
		} else {
			full.Add(1)
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.WriteHeader(200)
		w.Write(data)
	})
	task.Transfers[0].ChunkSize = 1 << 20
	if err := e.Run(context.Background(), task, nil); err != nil {
		t.Fatal(err)
	}
	if ranges.Load() < 1 || full.Load() != 1 {
		t.Fatalf("range=%d full=%d", ranges.Load(), full.Load())
	}
	assertBlob(t, e, identity(data), data)
}
func TestPakRangeUsesEntryIdentityAndAbsolutePakOffsets(t *testing.T) {
	pak := []byte("before-entry-content-after")
	entry := []byte("entry-content")
	e, task := fixture(t, pak, nil)
	task.Transfers[0] = gamedl.Transfer{Object: identity(entry), Source: gameres.Source{Kind: gameres.SourcePakRange, Object: identity(pak), Offset: 7, Size: int64(len(entry))}}
	if err := e.Run(context.Background(), task, nil); err != nil {
		t.Fatal(err)
	}
	assertBlob(t, e, identity(entry), entry)
	if f, err := e.OpenBlob(context.Background(), identity(pak)); err == nil {
		f.Close()
		t.Fatal("entry mislabelled as pak")
	}
}
func TestEmptyPakEntryMakesNoNetworkRequest(t *testing.T) {
	var calls atomic.Int32
	e, task := fixture(t, nil, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	task.Transfers[0].Source = gameres.Source{Kind: gameres.SourcePakRange, Object: identity([]byte("pak")), Offset: 3, Size: 0}
	if err := e.Run(context.Background(), task, nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("empty entry fetched from network")
	}
	assertBlob(t, e, identity(nil), nil)
}
func TestPatchBlobAndResourceUseSameVerificationPath(t *testing.T) {
	for _, kind := range []gameres.SourceKind{gameres.SourceCAS, gameres.SourcePatchBlob} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			data := []byte("synthetic patch blob")
			e, task := fixture(t, data, nil)
			task.Transfers[0].Source.Kind = kind
			if err := e.Run(context.Background(), task, nil); err != nil {
				t.Fatal(err)
			}
			assertBlob(t, e, identity(data), data)
		})
	}
}
func TestHTTPNotFoundDoesNotRetry(t *testing.T) {
	for _, kind := range []gameres.SourceKind{gameres.SourceCAS, gameres.SourcePatchBlob} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			var calls atomic.Int32
			e, task := fixture(t, []byte("missing"), func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(404) })
			task.Transfers[0].Source.Kind = kind
			err := e.Run(context.Background(), task, nil)
			expected := gamedl.ErrHTTP
			if kind == gameres.SourcePatchBlob {
				expected = gamedl.ErrPatchBlobMissing
			}
			requireKind(t, err, expected)
			if calls.Load() != 1 {
				t.Fatalf("404 retried %d times", calls.Load())
			}
		})
	}
}
func TestRange416ReplansExactlyOnce(t *testing.T) {
	var calls atomic.Int32
	e, task := fixture(t, []byte("missing"), func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(416) })
	requireKind(t, e.Run(context.Background(), task, nil), gamedl.ErrRangeRejected)
	if calls.Load() != 2 {
		t.Fatalf("416 requests=%d want 2", calls.Load())
	}
}
func TestShortBodyStopsAfterTwoAttempts(t *testing.T) {
	var calls atomic.Int32
	e, task := fixture(t, []byte("data"), func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Range", "bytes 0-3/4")
		w.Header().Set("Content-Length", "4")
		w.WriteHeader(206)
		w.Write([]byte("da"))
	})
	requireKind(t, e.Run(context.Background(), task, nil), gamedl.ErrNetwork)
	if calls.Load() != 2 {
		t.Fatalf("short requests=%d want 2", calls.Load())
	}
}
func TestChecksumMismatchRedownloadsExactlyOnce(t *testing.T) {
	var calls atomic.Int32
	e, task := fixture(t, []byte("good"), func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.ServeContent(w, r, "fixture", zeroTime, bytes.NewReader([]byte("evil")))
	})
	requireKind(t, e.Run(context.Background(), task, nil), gamedl.ErrChecksum)
	if calls.Load() != 2 {
		t.Fatalf("checksum rounds=%d want 2", calls.Load())
	}
}
func TestConfiguredGlobalRateLimitHasNoInitialBurst(t *testing.T) {
	data := bytes.Repeat([]byte("r"), 256<<10)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "fixture", zeroTime, bytes.NewReader(data))
	}))
	defer server.Close()
	e, err := gamedl.New(gamedl.Options{WorkRoot: t.TempDir(), StateRoot: t.TempDir(), Client: server.Client(), Resolve: func(context.Context, gameres.Source) ([]string, error) { return []string{server.URL}, nil }, Parameters: gamedl.Parameters{RateLimitKiB: 512}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	obj := identity(data)
	task := gamedl.Task{ID: "rate", Kind: gamedl.TaskPreloadFiles, ListHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Transfers: []gamedl.Transfer{{Object: obj, Source: gameres.Source{Kind: gameres.SourceCAS, Object: obj}}}}
	start := time.Now()
	if err = e.Run(context.Background(), task, nil); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 475*time.Millisecond {
		t.Fatalf("512KiB/s limit allowed burst: %v", elapsed)
	}
}
func TestBlobSourceAndRetireKeepVerifiedCache(t *testing.T) {
	data := []byte("retained cache")
	e, task := fixture(t, data, nil)
	if err := e.Run(context.Background(), task, nil); err != nil {
		t.Fatal(err)
	}
	source, err := e.BlobSource(context.Background(), identity(data))
	if err != nil {
		t.Fatal(err)
	}
	if source != "cache/blobs/"+identity(data).String() {
		t.Fatalf("source=%q", source)
	}
	if err = e.Retire(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Store().Load(context.Background(), task.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("task still owned after retire: %v", err)
	}
	assertBlob(t, e, identity(data), data)
}
func TestKernelLeaseExcludesOtherWritersAndCanBeSharedWithEngine(t *testing.T) {
	work, state := t.TempDir(), t.TempDir()
	lease, err := gamedl.Acquire(work)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if second, err := gamedl.Acquire(work); err == nil {
		second.Close()
		t.Fatal("second writer acquired lock")
	} else {
		requireKind(t, err, gamedl.ErrLocked)
	}
	opts := gamedl.Options{WorkRoot: work, StateRoot: state, Lease: lease, Client: &http.Client{}, Resolve: func(context.Context, gameres.Source) ([]string, error) { return nil, nil }}
	e, err := gamedl.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	if second, err := gamedl.Acquire(work); err == nil {
		second.Close()
		t.Fatal("engine closed caller's lease")
	}
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := gamedl.Acquire(work)
	if err != nil {
		t.Fatal(err)
	}
	again.Close()
}
func TestReopeningTamperedBlobRejectsSameSizeBytes(t *testing.T) {
	data := []byte("trusted")
	work, state := t.TempDir(), t.TempDir()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "fixture", zeroTime, bytes.NewReader(data))
	}))
	defer server.Close()
	e, err := gamedl.New(gamedl.Options{WorkRoot: work, StateRoot: state, Client: server.Client(), Resolve: func(context.Context, gameres.Source) ([]string, error) { return []string{server.URL}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	obj := identity(data)
	task := gamedl.Task{ID: "tamper", Kind: gamedl.TaskPreloadFiles, ListHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Transfers: []gamedl.Transfer{{Object: obj, Source: gameres.Source{Kind: gameres.SourceCAS, Object: obj}}}}
	if err = e.Run(context.Background(), task, nil); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(work, "cache", "blobs", obj.String()), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = e.OpenBlob(context.Background(), obj)
	requireKind(t, err, gamedl.ErrChecksum)
}
