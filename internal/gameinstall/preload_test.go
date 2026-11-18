// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/gameres"
)

func preloadID(s string) gameres.Object {
	return gameres.Object{MD5: fmt.Sprintf("%x", md5.Sum([]byte(s))), Size: int64(len(s))}
}

type preloadTestLocal struct {
	ids   map[string]gameres.Object
	cache map[gameres.Object]bool
}

func (l preloadTestLocal) Identity(p string) (gameres.Object, bool) { o, ok := l.ids[p]; return o, ok }
func (l preloadTestLocal) Trusted(p string, o gameres.Object) bool  { return l.ids[p] == o }
func (l preloadTestLocal) Cached(o gameres.Object) bool             { return l.cache[o] }
func preloadFixture() PreloadInput {
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	id := preloadID("new file contents")
	c := gameres.Config{ResVersion: "1.1", ListHash: preloadID("manifest").MD5, CheckHash: "next", BaseTag: "baseTag"}
	return PreloadInput{Advertisement: gameres.Config{PreReleaseStatus: "start", PreReleaseBranch: "PC_160", PreReleaseVersion: "1.1"}, CurrentBranch: "PC_150", CurrentVersion: "1.0", BaselineTrusted: true, Now: now, Snapshot: PreloadManifest{Branch: "PC_160", Config: c, Manifest: &gameres.ResList{Version: "1.1", Tag: "baseTag", CheckHash: "next", Files: []gameres.File{{Path: "Client/game.bin", MD5: id.MD5, Size: id.Size}}}, Verified: true, RouteKnown: true, SameOrigin: true, Available: true}, Budget: gameres.BudgetInput{VolumeFreeBytes: 1 << 40, CacheFreeBytes: 1 << 40}, Local: preloadTestLocal{}, Availability: gameres.AvailabilitySnapshot{At: now, TTL: time.Hour, Objects: map[gameres.Object]gameres.ObjectStatus{id: gameres.StatusReachable}}}
}
func TestPreloadDiscoveryFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		change       func(*PreloadInput)
	}{
		{"absent", "not-open", func(i *PreloadInput) { i.Advertisement.PreReleaseStatus = "" }},
		{"unknown", "unknown-status", func(i *PreloadInput) { i.Advertisement.PreReleaseStatus = "surprise" }},
		{"route", "branch-unresolvable", func(i *PreloadInput) { i.Snapshot.RouteKnown = false }},
		{"unverified", "branch-unresolvable", func(i *PreloadInput) { i.Snapshot.Verified = false }},
		{"traversal", "branch-unresolvable", func(i *PreloadInput) { i.Advertisement.PreReleaseBranch = "PC_..160" }},
		{"not-newer", "not-newer", func(i *PreloadInput) { i.CurrentVersion = "1.10" }},
		{"baseline", "baseline-untrusted", func(i *PreloadInput) { i.BaselineTrusted = false }},
		{"official", "official-running", func(i *PreloadInput) { i.OfficialRunning = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := preloadFixture()
			tc.change(&i)
			v := EvaluatePreload(i)
			if v.State == PreloadAvailable || v.Reason != tc.reason {
				t.Fatalf("got %+v want %s", v, tc.reason)
			}
		})
	}
	i := preloadFixture()
	v := EvaluatePreload(i)
	if v.State != PreloadAvailable || len(v.Notes) != 1 {
		t.Fatalf("distinct branch should be available: %+v", v)
	}
}
func TestPreloadPlanCapabilityAndCancellation(t *testing.T) {
	i := preloadFixture()
	target := i.Snapshot.Manifest.Files[0]
	old := preloadID("old contents")
	blob := preloadID("p")
	i.Local = preloadTestLocal{ids: map[string]gameres.Object{target.Path: old}}
	i.Snapshot.Graph = &gameres.PatchGraph{Edges: []gameres.PatchEntry{{Old: old, New: gameres.Object{MD5: target.MD5, Size: target.Size}, Blob: blob, Version: "next"}}}
	for _, enabled := range []bool{false, true} {
		i.Capabilities.PatchApply = enabled
		p, err := StartPreload(context.Background(), i)
		if err != nil {
			t.Fatal(err)
		}
		if (p.Record.Route == PreloadRoutePatch) != enabled {
			t.Fatalf("enabled=%v got %s", enabled, p.Record.Route)
		}
		if enabled && len(p.Record.Patches) != 1 {
			t.Fatal("patch identity missing")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := StartPreload(ctx, i); err != context.Canceled {
		t.Fatalf("cancel: %v", err)
	}
}
func TestPreloadStoreRecoveryAndDiscardReferences(t *testing.T) {
	i := preloadFixture()
	p, err := StartPreload(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	s := PreloadStore{StateRoot: t.TempDir(), WorkRoot: t.TempDir()}
	r := p.Record
	r.State = PreloadWaitingRelease
	r.Files[0].State = PreloadFileStaged
	path, err := s.BlobPath(r, r.Files[0].Object)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("new file contents"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.Save(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load(context.Background(), r.Branch, r.Version)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != r.State {
		t.Fatal("state not persisted")
	}
	recovered, err := s.Recover(context.Background(), loaded, i.Now)
	if err != nil || recovered.State != PreloadWaitingRelease {
		t.Fatalf("recover: %+v %v", recovered, err)
	}
	if err = os.WriteFile(path, []byte("bad file contents"), 0600); err != nil {
		t.Fatal(err)
	}
	recovered, err = s.Recover(context.Background(), loaded, i.Now)
	if err != nil || recovered.State != PreloadDownloading {
		t.Fatalf("corruption accepted: %+v %v", recovered, err)
	}
	id := r.Files[0].Object
	scan := func(context.Context) (map[gameres.Object]bool, error) { return map[gameres.Object]bool{id: true}, nil }
	if err = DiscardPreload(context.Background(), s, r, scan); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("referenced blob deleted")
	}
	if err = DiscardPreload(context.Background(), s, r, func(context.Context) (map[gameres.Object]bool, error) { return map[gameres.Object]bool{}, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unreferenced blob retained")
	}
}
