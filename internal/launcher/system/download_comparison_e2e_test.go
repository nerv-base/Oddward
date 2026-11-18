//go:build windows && e2e

// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/nerv-base/Oddward/internal/gameinstall"
	"github.com/nerv-base/Oddward/internal/gameres"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"golang.org/x/sys/windows"
)

const compareLargePath = "HT/Content/PatchPaks/pakchunk1-Windows_0_P.ucas"
const compareSmallPath = "HT/Binaries/Win64/HTGame.exe"

// TestRealGameResourceRoutes uses complete official files in deliberately small
// independent test installations. The full production manifests are tested by
// TestRealGameResourcesFirstInstall and the historical acceptance tests.
func TestRealGameResourceRoutes(t *testing.T) {
	if testing.Short() || os.Getenv("ODDWARD_E2E_NETWORK") != "1" || os.Getenv("ODDWARD_E2E_DOWNLOAD_ROUTES") != "1" {
		t.Skip("isolated comparison of official objects requires explicit network and routes opt-in")
	}
	base := realDownloadWorkspace(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	backend := &GameDownloadBackend{}
	metadata := backend.metadata()
	previous, err := historicalDelivery(ctx, metadata, os.Getenv("ODDWARD_E2E_HISTORICAL_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	current, err := metadata.delivery(ctx, bootstrapBranch, "1.5.6")
	if err != nil {
		t.Fatal(err)
	}
	oldLarge, newLarge := comparisonFile(t, previous, compareLargePath), comparisonFile(t, current, compareLargePath)
	oldSmall, newSmall := comparisonFile(t, previous, compareSmallPath), comparisonFile(t, current, compareSmallPath)
	id := func(f gameres.File) gameres.Object { return gameres.Object{MD5: f.MD5, Size: f.Size} }
	if newLarge.Size < 1<<30 || id(oldLarge) == id(newLarge) || id(oldSmall) == id(newSmall) {
		t.Fatal("comparison requires changed official targets including one above 1 GiB")
	}
	routes := current.Graph.Routes(id(oldLarge), id(newLarge), nil, 1)
	if len(routes) == 0 || len(routes[0].Steps) != 1 {
		t.Fatal("verified current manifest has no direct route for comparison")
	}
	patch := routes[0].Steps[0].Blob
	var patchNetwork, fullNetwork int64
	for _, scenario := range []string{"B1", "B2", "B3", "D6"} {
		t.Run(scenario, func(t *testing.T) {
			oldFiles, newFiles := []gameres.File{oldLarge}, []gameres.File{newLarge}
			if scenario == "B3" {
				oldFiles = append(oldFiles, oldSmall)
				newFiles = append(newFiles, newSmall)
			}
			root := filepath.Join(base, "route-"+scenario)
			stateRoot := filepath.Join(base, "route-"+scenario+"-state")
			for _, path := range []string{root, stateRoot} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("comparison paths must not exist")
				}
			}
			transport := &comparisonTransport{base: http.DefaultTransport, read: map[string]int64{}}
			b := &GameDownloadBackend{StateRoot: stateRoot, Client: &http.Client{Transport: transport}, Version: "local-e2e"}
			old := comparisonSubset(previous, oldFiles)
			next := comparisonSubset(current, newFiles)
			next.Graph = &gameres.PatchGraph{Edges: routes[0].Steps}
			req := launcher.DownloadRequest{Root: root, Kind: state.DownloadInstall}
			p, err := b.buildDownloadPlan(ctx, root, req.Kind, old, gameinstall.InstallRecord{}, "")
			if err != nil || p.need > p.free {
				t.Fatalf("old fixture plan: %v", err)
			}
			if err := comparisonSeedBaseline(ctx, b, p, base, id(oldLarge)); err != nil {
				t.Fatal(err)
			}
			b.putPlan(root, p)
			comparisonRun(t, ctx, b, req, func(state.Event) {}, false)
			comparisonCommit(t, ctx, b, req, old.Config.ResVersion)
			store, err := b.installStore()
			if err != nil {
				t.Fatal(err)
			}
			baseline, err := store.ReadInstall(root)
			if err != nil || baseline.Version != old.Config.ResVersion || baseline.VerifyCoverage != "full" {
				t.Fatalf("baseline evidence: %v", err)
			}
			req.Kind = state.DownloadUpdate
			p, err = b.buildDownloadPlan(ctx, root, req.Kind, next, baseline, "")
			if err != nil {
				t.Fatal(err)
			}
			canonical, err := canonicalDownloadManifest(next.Manifest)
			if err != nil {
				t.Fatal(err)
			}
			local := downloadLocal{installed: map[string]gameres.Object{}}
			for _, target := range baseline.Targets {
				local.installed[strings.ToUpper(target.Path)] = gameres.Object{MD5: target.MD5, Size: target.Size}
			}
			caps := gameres.Capabilities{PatchApply: scenario != "B2"}
			availability := b.downloadAvailability(ctx, next.Branch, downloadProbeCandidates(canonical, next.Graph, local, caps.PatchApply), time.Now())
			p.plan, err = gameres.BuildPlan(&canonical, next.Graph, local, caps, availability, gameres.BudgetInput{VolumeFreeBytes: p.free, CacheFreeBytes: p.free, ReserveBytes: p.margin})
			if err != nil {
				t.Fatal(err)
			}
			b.putPlan(root, p)
			wantPatch := scenario != "B2"
			// FullBytes also includes the output size of patch actions.
			fullRouteBytes := p.plan.DownloadBytes - p.plan.PatchBytes
			if p.hasPatch() != wantPatch || (wantPatch && p.plan.PatchBytes != patch.Size) || (scenario == "B3" && fullRouteBytes != newSmall.Size) {
				t.Fatalf("unexpected routes: patch=%d full network=%d", p.plan.PatchBytes, fullRouteBytes)
			}
			transport.reset()
			if scenario == "D6" {
				transport.unavailable = patch.String()
			}
			peak := comparisonMeasureAllocation(ctx, p.engineRoot())
			defer peak.stop()
			started := time.Now()
			var applyStarted, verifyStarted time.Time
			var fallback int
			var rebuilt int64
			report := func(e state.Event) {
				switch e.Kind {
				case state.ApplyStarted:
					applyStarted = time.Now()
				case state.ApplyProgress:
					rebuilt = max(rebuilt, e.Downloaded)
				case state.DownloadFallbackNotice:
					fallback++
				case state.DownloadProgress:
					if e.Stage == state.StageVerify && verifyStarted.IsZero() {
						verifyStarted = time.Now()
					}
				}
			}
			comparisonRun(t, ctx, b, req, report, scenario == "D6")
			verifiedAt := time.Now()
			unchanged, err := store.TrustedTargets(ctx, root)
			if err != nil || len(unchanged) != len(oldFiles) {
				t.Fatalf("old identities changed before commit: %v", err)
			}
			commitStarted := time.Now()
			comparisonCommit(t, ctx, b, req, next.Config.ResVersion)
			published, err := store.ReadInstall(root)
			if err != nil || published.ListHash != next.Config.ListHash || published.VerifyCoverage != "full" || len(published.Targets) != len(newFiles) {
				t.Fatalf("new identities were not published: %v", err)
			}
			transport.mu.Lock()
			network, patchWire, largeWire, smallWire := transport.total, transport.read[patch.String()], transport.read[id(newLarge).String()], transport.read[id(newSmall).String()]
			transport.mu.Unlock()
			wantFallback := scenario == "D6"
			if (fallback > 0) != wantFallback || (patchWire > 0) != (wantPatch && !wantFallback) || (largeWire > 0) != (!wantPatch || wantFallback) || (smallWire > 0) != (scenario == "B3") {
				t.Fatalf("network decision: patch=%d large=%d small=%d fallback=%d", patchWire, largeWire, smallWire, fallback)
			}
			if network != patchWire+largeWire+smallWire || network < p.plan.DownloadBytes && !wantFallback {
				t.Fatalf("unexpected network traffic: total=%d plan=%d", network, p.plan.DownloadBytes)
			}
			if scenario == "B1" {
				patchNetwork = network
			}
			if scenario == "B2" {
				fullNetwork = network
			}
			applyTime := time.Duration(0)
			if !applyStarted.IsZero() && !verifyStarted.IsZero() {
				applyTime = verifyStarted.Sub(applyStarted)
			}
			t.Logf("%s network=%d plan=%d patch=%d full=%d reconstructed=%d fallback_notices=%d allocated_peak_sample=%d plan_peak=%d apply=%s staged=%s commit=%s", scenario, network, p.plan.DownloadBytes, patchWire, largeWire+smallWire, rebuilt, fallback, peak.maximum.Load(), p.plan.PeakAllocBytes, applyTime, verifiedAt.Sub(started), time.Since(commitStarted))
		})
	}
	if patchNetwork > 0 && fullNetwork > 0 {
		t.Logf("B1/B2 wire proportion=%.4f%%", 100*float64(patchNetwork)/float64(fullNetwork))
	}
}

func realDownloadWorkspace(t *testing.T) string {
	t.Helper()
	base, err := downloadDirectory(os.Getenv("ODDWARD_E2E_DOWNLOAD_ROOT"), false)
	if err != nil || !strings.HasPrefix(filepath.Base(base), "Oddward-validation-") {
		t.Fatal("choose a new local Oddward-validation-* test workspace")
	}
	marker, err := os.ReadFile(filepath.Join(base, ".oddward-validation"))
	if err != nil || strings.TrimSpace(string(marker)) != filepath.Base(base) {
		t.Fatal("missing isolated workspace marker")
	}
	return base
}

func comparisonFile(t *testing.T, d gameDelivery, path string) gameres.File {
	t.Helper()
	for _, f := range d.Manifest.Files {
		want, err := downloadTargetPath(f.Path)
		if err == nil && strings.EqualFold(want, downloadContent+path) {
			return f
		}
	}
	t.Fatalf("missing direct target in %s manifest: %s", d.Config.ResVersion, path)
	return gameres.File{}
}

func comparisonSubset(d gameDelivery, files []gameres.File) gameDelivery {
	d.Manifest.Files, d.Manifest.Packages, d.Graph = files, nil, nil
	d.Config.ResSize = 0
	d.Config.ListHash = fmt.Sprintf("%x", md5.Sum([]byte("oddward-e2e-subset-"+d.Config.ListHash)))
	for _, f := range files {
		d.Config.ResSize += f.Size
	}
	return d
}

// Reuse the independently downloaded old sample only if present. Every test
// installs a separate copy and the measured update still accesses the CDN.
func comparisonSeedBaseline(ctx context.Context, b *GameDownloadBackend, p *downloadPlan, base string, object gameres.Object) error {
	sourceDir := filepath.Join(base, "patch", "inputs")
	if _, err := os.Lstat(filepath.Join(sourceDir, object.String())); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := downloadDirectory(sourceDir, false); err != nil {
		return err
	}
	source, err := os.OpenRoot(sourceDir)
	if err != nil {
		return err
	}
	defer source.Close()
	if err := os.Mkdir(p.root, 0700); err != nil {
		return err
	}
	lease, err := acquireDownloadWork(p.root)
	if err != nil {
		return err
	}
	defer lease.Close()
	engine, err := b.openPlanEngine(ctx, p, lease)
	if err != nil {
		return err
	}
	defer engine.Close()
	destination, err := os.OpenRoot(p.engineRoot())
	if err != nil {
		return err
	}
	defer destination.Close()
	const part = "cache/tmp/comparison-baseline.part"
	if err := historicalCopyCommon(ctx, source, destination, object.String(), part, object); err != nil {
		return err
	}
	return engine.Publish(ctx, object, part)
}

func comparisonRun(t *testing.T, ctx context.Context, b *GameDownloadBackend, req launcher.DownloadRequest, report func(state.Event), fallback bool) {
	t.Helper()
	event, err := b.runDownload(ctx, req, state.Event{}, report)
	if err == nil && event.Kind == state.ApplyFinished {
		if event.Enabled != fallback {
			t.Fatalf("fallback=%v, expected %v", event.Enabled, fallback)
		}
		event, err = b.verifyPlan(ctx, req, state.Event{}, report)
	}
	if err != nil || event.Kind != state.DownloadVerified {
		t.Fatalf("download event=%d: %v", event.Kind, err)
	}
}

func comparisonCommit(t *testing.T, ctx context.Context, b *GameDownloadBackend, req launcher.DownloadRequest, version string) {
	t.Helper()
	event, err := b.commitDownload(ctx, req, state.Event{}, func(state.Event) {})
	if err != nil || event.Kind != state.DownloadCommitted || event.Message != version {
		t.Fatalf("commit event=%d version=%q: %v", event.Kind, event.Message, err)
	}
}

type comparisonTransport struct {
	base        http.RoundTripper
	unavailable string
	mu          sync.Mutex
	read        map[string]int64
	total       int64
}

func (x *comparisonTransport) reset() {
	x.mu.Lock()
	defer x.mu.Unlock()
	clear(x.read)
	x.total = 0
}

func (x *comparisonTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	id := filepath.Base(r.URL.Path)
	if r.Method == http.MethodGet && x.unavailable != "" && id == x.unavailable {
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: http.NoBody, Request: r}, nil
	}
	response, err := x.base.RoundTrip(r)
	if err == nil && r.Method == http.MethodGet && (response.StatusCode == http.StatusOK || response.StatusCode == http.StatusPartialContent) {
		response.Body = comparisonBody{ReadCloser: response.Body, count: func(n int) {
			x.mu.Lock()
			x.total += int64(n)
			x.read[id] += int64(n)
			x.mu.Unlock()
		}}
	}
	return response, err
}

type comparisonBody struct {
	io.ReadCloser
	count func(int)
}

func (r comparisonBody) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.count(n)
	return n, err
}

type comparisonPeak struct {
	maximum atomic.Int64
	cancel  context.CancelFunc
	done    chan struct{}
}

func comparisonMeasureAllocation(ctx context.Context, root string) *comparisonPeak {
	ctx, cancel := context.WithCancel(ctx)
	peak := &comparisonPeak{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(peak.done)
		timer := time.NewTicker(250 * time.Millisecond)
		defer timer.Stop()
		for {
			var bytes int64
			// A rename during this walk can expose one allocation at two names.
			seen := map[[3]uint32]struct{}{}
			_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if err != nil || !entry.Type().IsRegular() {
					return nil
				}
				// FileStandardInfo's first field is AllocationSize, including
				// actual allocated bytes for a sparse transfer part.
				name, err := windows.UTF16PtrFromString(path)
				if err != nil {
					return nil
				}
				handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
					windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
					nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
				if err != nil {
					return nil
				}
				defer windows.CloseHandle(handle)
				var info windows.ByHandleFileInformation
				if err = windows.GetFileInformationByHandle(handle, &info); err != nil {
					return nil
				}
				key := [3]uint32{info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow}
				if _, duplicate := seen[key]; duplicate {
					return nil
				}
				var standard struct {
					AllocationSize, EndOfFile int64
					NumberOfLinks             uint32
					DeletePending, Directory  byte
				}
				if err = windows.GetFileInformationByHandleEx(handle, windows.FileStandardInfo, (*byte)(unsafe.Pointer(&standard)), uint32(unsafe.Sizeof(standard))); err == nil {
					seen[key] = struct{}{}
					bytes += standard.AllocationSize
				}
				return nil
			})
			peak.maximum.Store(max(bytes, peak.maximum.Load()))
			select {
			case <-timer.C:
			case <-ctx.Done():
				return
			}
		}
	}()
	return peak
}

func TestComparisonAllocationDoesNotCountAnObjectTwice(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "source")
	if err := os.WriteFile(original, make([]byte, 64<<10), 0600); err != nil {
		t.Fatal(err)
	}
	var one, two int64
	sample := comparisonMeasureAllocation(t.Context(), root)
	sample.stop()
	one = sample.maximum.Load()
	if err := os.Link(original, filepath.Join(root, "alias")); err != nil {
		t.Skipf("filesystem cannot create a measurement fixture alias: %v", err)
	}
	sample = comparisonMeasureAllocation(t.Context(), root)
	sample.stop()
	two = sample.maximum.Load()
	if one == 0 || two != one {
		t.Fatalf("one allocation measured %d bytes before alias and %d bytes after", one, two)
	}
}

func (p *comparisonPeak) stop() {
	p.cancel()
	<-p.done
}
