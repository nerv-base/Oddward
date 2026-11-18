// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameres"
)

func TestGCScansWithoutDeletingDuringActiveWriter(t *testing.T) {
	work, state := t.TempDir(), t.TempDir()
	lease, err := gamedl.Acquire(work)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-3/4")
		w.WriteHeader(206)
		w.Write([]byte("data"))
	}))
	defer server.Close()
	e, err := gamedl.New(gamedl.Options{WorkRoot: work, StateRoot: state, Lease: lease, Client: server.Client(), Resolve: func(context.Context, gameres.Source) ([]string, error) { return []string{server.URL}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	obj := identity([]byte("data"))
	task := gamedl.Task{ID: "busy", Kind: gamedl.TaskPreloadFiles, ListHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Transfers: []gamedl.Transfer{{Object: obj, Source: gameres.Source{Kind: gameres.SourceCAS, Object: obj}}}}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	done := make(chan error, 1)
	go func() {
		done <- e.Run(context.Background(), task, func(gamedl.Progress) { once.Do(func() { close(entered); <-release }) })
	}()
	<-entered
	_, gcErr := gamedl.Collect(context.Background(), gamedl.GCOptions{WorkRoot: work, StateRoot: state, Lease: lease, MaxBlobBytes: 1})
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if gcErr != nil {
		t.Fatalf("active task GC must allow read-only scan: %v", gcErr)
	}
}

func TestGCRequiresCallerLease(t *testing.T) {
	_, err := gamedl.Collect(context.Background(), gamedl.GCOptions{WorkRoot: t.TempDir(), StateRoot: t.TempDir(), MaxBlobBytes: 1})
	requireKind(t, err, gamedl.ErrLocked)
}
func TestGCProtectsAllTaskReferencesAcrossVersionRoots(t *testing.T) {
	work, state := t.TempDir(), t.TempDir()
	lease, err := gamedl.Acquire(work)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	saved := identity([]byte("saved patch"))
	target := identity([]byte("target"))
	active := identity([]byte("active"))
	unreferenced := identity([]byte("unused"))
	paths := map[gameres.Object]string{saved: "cache/blobs", target: "preload/game/2/cache/blobs", active: "preload/game/3/blobs", unreferenced: "preload/game/1/cache/blobs"}
	for obj, dir := range paths {
		full := filepath.Join(work, filepath.FromSlash(dir))
		if err = os.MkdirAll(full, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(full, obj.String()), make([]byte, obj.Size), 0600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := gamedl.NewStore(state)
	if err != nil {
		t.Fatal(err)
	}
	task := gamedl.Task{ID: "referenced", Kind: gamedl.TaskPreloadFiles, ListHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: gamedl.StateStaged, Transfers: []gamedl.Transfer{{Object: saved, Source: gameres.Source{Kind: gameres.SourcePatchBlob, Object: saved}}}, Targets: []gamedl.InstallTarget{{Path: "Client/file", Object: target, Action: gameres.ActionFull}}}
	if err = store.Save(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	store.Close()
	opts := gamedl.GCOptions{WorkRoot: work, StateRoot: state, Lease: lease, Protected: []gameres.Object{active}, MaxBlobBytes: 1}
	result, err := gamedl.Collect(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Deleted) != 1 || result.DeletedBytes != unreferenced.Size {
		t.Fatalf("GC result=%+v", result)
	}
	for obj, dir := range paths {
		_, err = os.Stat(filepath.Join(work, filepath.FromSlash(dir), obj.String()))
		if obj == unreferenced {
			if !os.IsNotExist(err) {
				t.Fatalf("unreferenced survives: %v", err)
			}
		} else if err != nil {
			t.Fatalf("protected blob missing: %v", err)
		}
	}
}
func TestGCRejectsCorruptReferencesBeforeAnyDeletion(t *testing.T) {
	work, state := t.TempDir(), t.TempDir()
	lease, err := gamedl.Acquire(work)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	obj := identity([]byte("keep"))
	dir := filepath.Join(work, "cache", "blobs")
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, obj.String()), []byte("keep"), 0600)
	os.MkdirAll(filepath.Join(state, "tasks"), 0700)
	os.WriteFile(filepath.Join(state, "tasks", "broken.json"), []byte("truncated"), 0600)
	_, err = gamedl.Collect(context.Background(), gamedl.GCOptions{WorkRoot: work, StateRoot: state, Lease: lease, MaxBlobBytes: 1})
	requireKind(t, err, gamedl.ErrRecoveryRequired)
	if _, err = os.Stat(filepath.Join(dir, obj.String())); err != nil {
		t.Fatal("GC deleted before validating references")
	}
}
func TestGCExpiryAndReadOnlyUsage(t *testing.T) {
	work, state := t.TempDir(), t.TempDir()
	lease, err := gamedl.Acquire(work)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	old, newObject := identity([]byte("old")), identity([]byte("new"))
	dir := filepath.Join(work, "cache", "blobs")
	os.MkdirAll(dir, 0700)
	now := time.Now()
	for _, obj := range []gameres.Object{old, newObject} {
		os.WriteFile(filepath.Join(dir, obj.String()), make([]byte, obj.Size), 0600)
	}
	os.Chtimes(filepath.Join(dir, old.String()), now.Add(-48*time.Hour), now.Add(-48*time.Hour))
	opts := gamedl.GCOptions{WorkRoot: work, StateRoot: state, Lease: lease, Now: now}
	result, err := gamedl.Collect(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.BlobBytes != 6 || result.DeletedBytes != 0 {
		t.Fatalf("usage scan=%+v", result)
	}
	opts.MaxAge = 24 * time.Hour
	result, err = gamedl.Collect(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedBytes != 3 || len(result.Deleted) != 1 {
		t.Fatalf("expiry=%+v", result)
	}
}
