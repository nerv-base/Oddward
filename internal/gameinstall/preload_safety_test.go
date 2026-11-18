// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"github.com/nerv-base/Oddward/internal/gameres"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPreloadEngineCacheLayoutIsRetainedWithoutCopies(t *testing.T) {
	i := preloadFixture()
	p, err := StartPreload(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	s := PreloadStore{StateRoot: t.TempDir(), WorkRoot: t.TempDir(), EngineCache: true}
	r := p.Record
	path, err := s.BlobPath(r, r.Files[0].Object)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(s.WorkRoot, "preload", r.Branch, r.Version, "cache", "blobs", r.Files[0].Object.String())
	if path != want {
		t.Fatalf("staged cache path=%q want=%q", path, want)
	}
	if err = os.WriteFile(path, []byte("new file contents"), 0600); err != nil {
		t.Fatal(err)
	}
	r.State = PreloadWaitingRelease
	r.Files[0].State = PreloadFileStaged
	recovered, err := s.Recover(context.Background(), r, i.Now)
	if err != nil || recovered.State != PreloadWaitingRelease {
		t.Fatalf("cache not recovered in place: %+v %v", recovered, err)
	}
	if err = DiscardPreload(context.Background(), s, recovered, func(context.Context) (map[gameres.Object]bool, error) { return map[gameres.Object]bool{}, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cache blob not discarded: %v", err)
	}
}

func TestPreloadPlanSnapshotPersisted(t *testing.T) {
	i := preloadFixture()
	p, e := StartPreload(context.Background(), i)
	if e != nil {
		t.Fatal(e)
	}
	s := PreloadStore{StateRoot: t.TempDir(), WorkRoot: t.TempDir()}
	if e = s.Save(context.Background(), p.Record); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(s.StateRoot, "preload", p.Record.Branch, p.Record.Version, "plan.json")
	b, e := os.ReadFile(path)
	if e != nil || !strings.Contains(string(b), p.Record.Targets[0].Object.MD5) || !strings.Contains(string(b), "downloadBytes") {
		t.Fatalf("missing target/budget snapshot: %s %v", b, e)
	}
}

type preloadCancelLocal struct{ cancel context.CancelFunc }

func (l preloadCancelLocal) Identity(string) (gameres.Object, bool) {
	l.cancel()
	return gameres.Object{}, false
}
func (l preloadCancelLocal) Trusted(string, gameres.Object) bool { return false }
func (l preloadCancelLocal) Cached(gameres.Object) bool          { return false }
func TestPreloadCancellationWhileEvaluating(t *testing.T) {
	i := preloadFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	i.Local = preloadCancelLocal{cancel}
	i.Budget.VolumeFreeBytes = 3 << 30
	i.Budget.CacheFreeBytes = 3 << 30
	if _, e := StartPreload(ctx, i); e != context.Canceled {
		t.Fatalf("evaluation cancellation lost: %v", e)
	}
}
func TestPreloadDiscoveryDiskBudget(t *testing.T) {
	i := preloadFixture()
	i.Budget.VolumeFreeBytes = 0
	v := EvaluatePreload(i)
	if v.State == PreloadAvailable || v.Reason != "disk-short" {
		t.Fatalf("unaffordable preload available: %+v", v)
	}
}
func TestPreloadStateTransitionsPersist(t *testing.T) {
	i := preloadFixture()
	p, e := StartPreload(context.Background(), i)
	if e != nil {
		t.Fatal(e)
	}
	s := PreloadStore{StateRoot: t.TempDir(), WorkRoot: t.TempDir()}
	r := p.Record
	for _, next := range []PreloadState{PreloadDownloading, PreloadPaused, PreloadDownloading, PreloadVerifying, PreloadDownloading, PreloadVerifying, PreloadStaged, PreloadWaitingRelease, PreloadReconcile, PreloadApplicable, PreloadApplying, PreloadCompleted} {
		if next == PreloadStaged {
			for n := range r.Files {
				r.Files[n].State = PreloadFileStaged
			}
		}
		r, e = s.Transition(context.Background(), r, next, i.Now.Add(time.Hour))
		if e != nil {
			t.Fatalf("transition %s: %v", next, e)
		}
		got, e := s.Load(context.Background(), r.Branch, r.Version)
		if e != nil || got.State != next {
			t.Fatalf("not persisted %s: %v", next, e)
		}
	}
	if _, e = s.Transition(context.Background(), r, PreloadApplying, i.Now.Add(time.Hour)); e == nil {
		t.Fatal("completed record reentered apply")
	}
	if _, e = s.Transition(context.Background(), p.Record, PreloadCompleted, i.Now.Add(time.Hour)); e == nil {
		t.Fatal("unverified plan completed")
	}
}
func TestPreloadRecordMalformedAndRetryLimit(t *testing.T) {
	i := preloadFixture()
	p, e := StartPreload(context.Background(), i)
	if e != nil {
		t.Fatal(e)
	}
	s := PreloadStore{StateRoot: t.TempDir(), WorkRoot: t.TempDir()}
	r := p.Record
	r.State = PreloadWaitingRelease
	r.Files[0].State = PreloadFileStaged
	r.Files[0].VerificationFailures = 2
	r.Files = append(r.Files, PreloadFile{Path: "Client/other.bin", Object: preloadID("another"), Kind: PreloadBlobFull, State: PreloadFileDownloading})
	r, e = s.Recover(context.Background(), r, i.Now)
	if e != nil || r.State != PreloadFailed {
		t.Fatalf("unbounded retry: %+v %v", r, e)
	}
	path := filepath.Join(s.StateRoot, "preload", r.Branch, r.Version, "preload.json")
	if e = os.WriteFile(path, []byte(`{"schema":1`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Load(context.Background(), r.Branch, r.Version); e == nil {
		t.Fatal("truncated record accepted")
	}
	r.Branch = "../escape"
	if e = s.Save(context.Background(), r); e == nil {
		t.Fatal("unsafe record persisted")
	}
}
func TestPreloadDiscardMissingWorkStillPersistsStale(t *testing.T) {
	i := preloadFixture()
	p, e := StartPreload(context.Background(), i)
	if e != nil {
		t.Fatal(e)
	}
	s := PreloadStore{StateRoot: t.TempDir(), WorkRoot: t.TempDir()}
	if e = s.Save(context.Background(), p.Record); e != nil {
		t.Fatal(e)
	}
	if e = DiscardPreload(context.Background(), s, p.Record, func(context.Context) (map[gameres.Object]bool, error) { return map[gameres.Object]bool{}, nil }); e != nil {
		t.Fatal(e)
	}
	got, e := s.Load(context.Background(), p.Record.Branch, p.Record.Version)
	if e != nil || got.State != PreloadStale {
		t.Fatalf("missing work left active metadata: %+v %v", got, e)
	}
}
func TestPreloadDiscardFailureAndLinksAreClosed(t *testing.T) {
	i := preloadFixture()
	p, e := StartPreload(context.Background(), i)
	if e != nil {
		t.Fatal(e)
	}
	s := PreloadStore{StateRoot: t.TempDir(), WorkRoot: t.TempDir()}
	r := p.Record
	path, e := s.BlobPath(r, r.Files[0].Object)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, []byte("new file contents"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = DiscardPreload(context.Background(), s, r, nil); e == nil {
		t.Fatal("discard without reference scan accepted")
	}
	if _, e = os.Stat(path); e != nil {
		t.Fatal("file removed before references")
	}
	// Deleting one preload never touches a sibling version or unknown file.
	sibling := filepath.Join(s.WorkRoot, "preload", r.Branch, "9.9")
	if e = os.MkdirAll(sibling, 0700); e != nil {
		t.Fatal(e)
	}
	sentinel := filepath.Join(sibling, "keep")
	if e = os.WriteFile(sentinel, []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = DiscardPreload(context.Background(), s, r, func(context.Context) (map[gameres.Object]bool, error) { return map[gameres.Object]bool{}, nil }); e != nil {
		t.Fatal(e)
	}
	if b, e := os.ReadFile(sentinel); e != nil || string(b) != "keep" {
		t.Fatal("sibling deleted")
	}
}
