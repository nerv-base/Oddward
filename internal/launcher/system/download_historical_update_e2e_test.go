//go:build windows && e2e

// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/game"
	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameinstall"
	"github.com/nerv-base/Oddward/internal/gameres"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"golang.org/x/sys/windows"
)

type historicalUpdateFile struct {
	Directory                   bool
	Size                        int64
	Modified                    time.Time
	Volume, IndexHigh, IndexLow uint32
	Checksum                    string
}

// Samples detect transfer-time writes without rereading 84 GB after every
// phase. Small files are hashed completely. Production TrustedTargets and the
// transaction additionally verify complete old and candidate MD5 identities.
func historicalUpdateSample(ctx context.Context, root *os.Root, path string) (historicalUpdateFile, error) {
	file, err := historicalOpenFile(root, path)
	if err != nil {
		return historicalUpdateFile{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return historicalUpdateFile{}, err
	}
	var identity windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &identity); err != nil {
		return historicalUpdateFile{}, err
	}
	hash := sha256.New()
	const sampleBytes int64 = 64 << 10
	offsets := []int64{0}
	length := info.Size()
	if info.Size() > 1<<20 {
		length = sampleBytes
		offsets = []int64{0, (info.Size() - sampleBytes) / 2, info.Size() - sampleBytes}
	}
	for _, offset := range offsets {
		var position [16]byte
		binary.LittleEndian.PutUint64(position[:8], uint64(offset))
		binary.LittleEndian.PutUint64(position[8:], uint64(length))
		_, _ = hash.Write(position[:])
		n, err := io.CopyBuffer(hash, historicalReader{ctx, io.NewSectionReader(file, offset, length)}, make([]byte, 64<<10))
		if err != nil || n != length {
			return historicalUpdateFile{}, fmt.Errorf("historical sample was incomplete: %w", errors.Join(err, io.ErrUnexpectedEOF))
		}
	}
	return historicalUpdateFile{Size: info.Size(), Modified: info.ModTime(), Volume: identity.VolumeSerialNumber,
		IndexHigh: identity.FileIndexHigh, IndexLow: identity.FileIndexLow, Checksum: hex.EncodeToString(hash.Sum(nil))}, nil
}

func historicalUpdateTree(ctx context.Context, root string) (map[string]historicalUpdateFile, error) {
	opened, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	tree := make(map[string]historicalUpdateFile)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if strings.EqualFold(relative, ".oddward") {
			if !entry.IsDir() {
				return gameres.ErrPathUnsafe
			}
			return filepath.SkipDir
		}
		// FindFirstFile-backed DirEntry metadata can lag a completed child
		// write on Windows. Read fresh metadata through the opened root.
		info, err := opened.Lstat(relative)
		if err != nil {
			return err
		}
		if downloadReparse(info) {
			return gameres.ErrPathUnsafe
		}
		relative = filepath.ToSlash(relative)
		if info.IsDir() {
			tree[relative] = historicalUpdateFile{Directory: true, Modified: info.ModTime()}
			return nil
		}
		tree[relative], err = historicalUpdateSample(ctx, opened, relative)
		return err
	})
	return tree, err
}

func historicalUpdateSameTree(ctx context.Context, root string, before map[string]historicalUpdateFile) error {
	after, err := historicalUpdateTree(ctx, root)
	if err != nil {
		return err
	}
	for path, identity := range before {
		if current, exists := after[path]; !exists || !reflect.DeepEqual(identity, current) {
			return fmt.Errorf("installed tree changed before commit: %s before=%+v after=%+v exists=%v", path, identity, current, exists)
		}
	}
	for path := range after {
		if _, exists := before[path]; !exists {
			return fmt.Errorf("installed tree gained a path before commit: %s", path)
		}
	}
	return nil
}

// Count consumed HTTP response-body bytes, including retries and errors. These
// measurements exclude HTTP headers/TLS overhead and never replace the planner.
// This transport neither changes requests nor injects responses or capabilities.
type historicalUpdateTransport struct {
	base  http.RoundTripper
	mu    sync.Mutex
	byID  map[string]int64
	total int64
}

func (x *historicalUpdateTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := x.base.RoundTrip(request)
	if err == nil && response.Body != nil && request.Method == http.MethodGet {
		response.Body = historicalUpdateBody{ReadCloser: response.Body, add: func(n int) {
			x.mu.Lock()
			x.total += int64(n)
			x.byID[filepath.Base(request.URL.Path)] += int64(n)
			x.mu.Unlock()
		}}
	}
	return response, err
}

type historicalUpdateBody struct {
	io.ReadCloser
	add func(int)
}

func (body historicalUpdateBody) Read(buffer []byte) (int, error) {
	n, err := body.ReadCloser.Read(buffer)
	body.add(n)
	return n, err
}

func (x *historicalUpdateTransport) reset() {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.total = 0
	clear(x.byID)
}

func (x *historicalUpdateTransport) evidence() (int64, map[string]int64) {
	x.mu.Lock()
	defer x.mu.Unlock()
	copy := make(map[string]int64, len(x.byID))
	for id, bytes := range x.byID {
		copy[id] = bytes
	}
	return x.total, copy
}

func historicalUpdateTargetSet(record gameinstall.InstallRecord, expected []gameres.Target) error {
	if len(record.Targets) != len(expected) {
		return errors.New("published target count differs from official manifest")
	}
	identities := make(map[string]gameres.Object, len(expected))
	for _, target := range expected {
		identities[strings.ToUpper(target.Path)] = gameres.Object{MD5: target.MD5, Size: target.Size}
	}
	for _, target := range record.Targets {
		key := strings.ToUpper(target.Path)
		if identities[key] != (gameres.Object{MD5: target.MD5, Size: target.Size}) {
			return fmt.Errorf("published target differs from official manifest: %s", target.Path)
		}
		delete(identities, key)
	}
	if len(identities) != 0 {
		return errors.New("official targets are missing from publication")
	}
	return nil
}

func historicalUpdateAction(action gameres.ActionKind) string {
	switch action {
	case gameres.ActionReuseInstalled:
		return "reuse-installed"
	case gameres.ActionInstallCached:
		return "install-cached"
	case gameres.ActionPatch:
		return "patch"
	case gameres.ActionFull:
		return "full"
	default:
		return fmt.Sprintf("unknown-%d", action)
	}
}

func historicalUpdateReuseTargets(planned []gameres.Target, source gameinstall.InstallRecord, expected []gameres.Target) ([]gameres.Target, error) {
	installed := make(map[string]gameres.Object)
	manifest := make(map[string]gameres.Object)
	for _, target := range source.Targets {
		key := strings.ToUpper(target.Path)
		if _, exists := installed[key]; exists {
			return nil, gameinstall.ErrStaleState
		}
		installed[key] = gameres.Object{MD5: target.MD5, Size: target.Size}
	}
	for _, target := range expected {
		key := strings.ToUpper(target.Path)
		if _, exists := manifest[key]; exists {
			return nil, gameinstall.ErrStaleState
		}
		manifest[key] = gameres.Object{MD5: target.MD5, Size: target.Size}
	}
	var selected []gameres.Target
	seen := make(map[string]bool)
	for _, target := range planned {
		if target.Action != gameres.ActionFull {
			continue
		}
		path, err := downloadTargetPath(target.Path)
		key := strings.ToUpper(target.Path)
		object := gameres.Object{MD5: target.MD5, Size: target.Size}
		if err != nil || path != target.Path || seen[key] || object.Size <= 0 || installed[key] != object || manifest[key] != object {
			return nil, fmt.Errorf("unproven Full cache seed %s: %w", target.Path, gameinstall.ErrStaleState)
		}
		seen[key] = true
		selected = append(selected, target)
	}
	return selected, nil
}

type historicalUpdateSeedStats struct {
	Files, CachedFiles                                  int
	Bytes, CachedBytes, RemainingBefore, RemainingAfter int64
}

func historicalUpdateSeedBlobs(t *testing.T, ctx context.Context, source, destination *os.Root, engine *gamedl.Engine, targets []gameres.Target) (historicalUpdateSeedStats, error) {
	t.Helper()
	var stats historicalUpdateSeedStats
	seen := make(map[gameres.Object]bool)
	for _, target := range targets {
		if target.Action != gameres.ActionFull {
			return stats, errors.New("cache seeding only accepts planned Full targets")
		}
		object := gameres.Object{MD5: target.MD5, Size: target.Size}
		if seen[object] {
			continue
		}
		seen[object] = true
		path, err := engine.BlobSource(ctx, object)
		if err == nil {
			file, err := historicalOpenFile(destination, path)
			if err != nil {
				return stats, err
			}
			if err := file.Close(); err != nil {
				return stats, err
			}
			stats.CachedFiles++
			stats.CachedBytes += object.Size
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return stats, err
		}
		guard, err := historicalCrashReadGuard(source, target.Path)
		if err != nil {
			return stats, err
		}
		part := "cache/tmp/historical-current-" + rand.Text() + ".part"
		copyErr := historicalCopyCommon(ctx, source, destination, target.Path, part, object)
		if err := errors.Join(copyErr, guard.Close()); err != nil {
			return stats, err
		}
		if err := engine.Publish(ctx, object, part); err != nil {
			return stats, err
		}
		stats.Files++
		stats.Bytes += object.Size
		t.Logf("historical independent cache seed path=%s object=%s bytes=%d", target.Path, object, object.Size)
	}
	return stats, nil
}

func historicalUpdateMaybeSeedCurrent(t *testing.T, ctx context.Context, backend *GameDownloadBackend, plan *downloadPlan, base string, expected []gameres.Target) (stats historicalUpdateSeedStats, resultErr error) {
	t.Helper()
	if os.Getenv("ODDWARD_E2E_HISTORICAL_REUSE_CURRENT") != "1" {
		t.Log("historical current-install cache seeding disabled; product engine will resume only existing verified cache/bitmap data")
		return stats, nil
	}
	if plan.engineRoot() != downloadWork(plan.root) {
		return stats, errors.New("historical seed requires the installation-owned plan cache")
	}
	sourcePath, err := downloadDirectory(filepath.Join(base, "fresh"), false)
	if err != nil || strings.EqualFold(sourcePath, plan.root) {
		return stats, errors.New("verified independent fresh installation is required for cache seeding")
	}
	if _, err := downloadDirectory(filepath.Join(base, "install-state"), false); err != nil {
		return stats, err
	}
	mutation, err := acquireDownloadMutation(sourcePath)
	if err != nil {
		return stats, err
	}
	defer mutation.Close()
	if _, err := downloadDirectory(downloadWork(sourcePath), false); err != nil {
		return stats, err
	}
	sourceLease, err := gamedl.Acquire(downloadWork(sourcePath))
	if err != nil {
		return stats, err
	}
	defer sourceLease.Close()
	sourceBackend := &GameDownloadBackend{StateRoot: filepath.Join(base, "install-state")}
	sourceStore, err := sourceBackend.installStore()
	if err != nil {
		return stats, err
	}
	record, err := sourceStore.ReadInstall(sourcePath)
	if err != nil || record.State != "ready" || record.Version != "1.5.6" || record.Version != plan.delivery.Config.ResVersion || record.Branch != bootstrapBranch || record.VerifyCoverage != "full" || record.VerifiedTargets != len(expected) || len(record.Targets) != len(expected) || record.ListHash != plan.delivery.Config.ListHash || record.ManifestDigest != plan.delivery.Config.ListHash || record.TxnID == "" || record.RootFileID == "" || record.BaselineMismatch {
		return stats, fmt.Errorf("fresh publication does not match pinned complete current manifest: %w", gameinstall.ErrStaleState)
	}
	status, err := sourceStore.TransactionStatus(ctx, sourcePath, downloadWork(sourcePath))
	if err != nil || status.LaunchBlocked {
		return stats, gameinstall.ErrRecoveryRequired
	}
	if err := historicalUpdateTargetSet(record, expected); err != nil {
		return stats, err
	}
	selected, err := historicalUpdateReuseTargets(plan.plan.Targets, record, expected)
	if err != nil {
		return stats, err
	}
	before, err := historicalUpdateTree(ctx, sourcePath)
	if err != nil {
		return stats, err
	}
	defer func() {
		checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		current, readErr := sourceStore.ReadInstall(sourcePath)
		if readErr == nil && !reflect.DeepEqual(current, record) {
			readErr = errors.New("cache seeding changed exact source install record")
		}
		resultErr = errors.Join(resultErr, readErr, historicalUpdateSameTree(checkCtx, sourcePath, before))
	}()
	if err := backend.guard(ctx, true); err != nil {
		return stats, err
	}
	destinationLease, err := acquireDownloadWork(plan.root)
	if err != nil {
		return stats, err
	}
	defer destinationLease.Close()
	engine, err := backend.openPlanEngine(ctx, plan, destinationLease)
	if err != nil {
		return stats, err
	}
	defer engine.Close()
	source, err := os.OpenRoot(sourcePath)
	if err != nil {
		return stats, err
	}
	defer source.Close()
	destination, err := os.OpenRoot(plan.engineRoot())
	if err != nil {
		return stats, err
	}
	defer destination.Close()
	task := plan.transferTask()
	remainingBefore, err := engine.Remaining(ctx, task.Transfers)
	if err != nil {
		return stats, err
	}
	// Existing partial ranks are retained; copying needs a full independent
	// object in addition to those allocations. Use actual free bytes and reserve.
	var needed int64
	seen := make(map[gameres.Object]bool)
	for _, target := range selected {
		object := gameres.Object{MD5: target.MD5, Size: target.Size}
		if seen[object] {
			continue
		}
		seen[object] = true
		if _, err := engine.BlobSource(ctx, object); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return stats, err
		}
		needed += object.Size
	}
	free, err := backend.space(plan.root)
	if err != nil || needed > free || plan.margin > free-needed {
		return stats, errors.New("insufficient actual space for independent current-install cache copies and reserve")
	}
	stats, err = historicalUpdateSeedBlobs(t, ctx, source, destination, engine, selected)
	if err != nil {
		return stats, err
	}
	stats.RemainingBefore = remainingBefore
	stats.RemainingAfter, err = engine.Remaining(ctx, task.Transfers)
	return stats, err
}

func TestHistoricalUpdateReuseSelectsOnlyPlannedFullExactIdentities(t *testing.T) {
	full := gameres.Target{Path: downloadContent + "full.bin", MD5: strings.Repeat("1", 32), Size: 7, Action: gameres.ActionFull}
	planned := []gameres.Target{full}
	for i, action := range []gameres.ActionKind{gameres.ActionPatch, gameres.ActionReuseInstalled, gameres.ActionInstallCached} {
		planned = append(planned, gameres.Target{Path: fmt.Sprintf("%sother-%d.bin", downloadContent, i), MD5: strings.Repeat("2", 32), Size: 9, Action: action})
	}
	before := append([]gameres.Target(nil), planned...)
	source := gameinstall.InstallRecord{Targets: []gameinstall.InstallTarget{{Path: full.Path, MD5: full.MD5, Size: full.Size}}}
	got, err := historicalUpdateReuseTargets(planned, source, []gameres.Target{full})
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], full) {
		t.Fatalf("only exact planned Full target should be seeded: %+v %v", got, err)
	}
	if !reflect.DeepEqual(planned, before) {
		t.Fatal("cache reuse changed original plan decisions")
	}
	for _, scenario := range []string{"source-hash", "manifest-size", "missing-source"} {
		t.Run(scenario, func(t *testing.T) {
			record := source
			record.Targets = append([]gameinstall.InstallTarget(nil), source.Targets...)
			manifest := []gameres.Target{full}
			switch scenario {
			case "source-hash":
				record.Targets[0].MD5 = strings.Repeat("3", 32)
			case "manifest-size":
				manifest[0].Size++
			case "missing-source":
				record.Targets = nil
			}
			if _, err := historicalUpdateReuseTargets(planned, record, manifest); err == nil {
				t.Fatal("unproven current-install target was accepted")
			}
		})
	}
}

func TestHistoricalUpdateReuseDisabledDoesNotOpenAnySource(t *testing.T) {
	t.Setenv("ODDWARD_E2E_HISTORICAL_REUSE_CURRENT", "0")
	stats, err := historicalUpdateMaybeSeedCurrent(t, context.Background(), nil, nil, "missing source must not be opened", nil)
	if err != nil || stats != (historicalUpdateSeedStats{}) {
		t.Fatalf("disabled flag changed default execution path: %+v %v", stats, err)
	}
}

func TestHistoricalUpdateReuseCopiesIndependentCacheAndEngineResumes(t *testing.T) {
	ctx := context.Background()
	sourcePath := t.TempDir()
	data := []byte("current independently verified complete target")
	target := gameres.Target{Path: downloadContent + "full.bin", MD5: fmt.Sprintf("%x", md5.Sum(data)), Size: int64(len(data)), Action: gameres.ActionFull}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(sourcePath, filepath.FromSlash(target.Path))), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, filepath.FromSlash(target.Path)), data, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := os.OpenRoot(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	before, err := historicalUpdateTree(ctx, sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	lease, err := gamedl.Acquire(work)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	engine, err := gamedl.New(gamedl.Options{WorkRoot: work, StateRoot: t.TempDir(), Lease: lease, Client: &http.Client{}, Resolve: func(context.Context, gameres.Source) ([]string, error) {
		return nil, errors.New("network resolver must not be used by cache seeding")
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	destination, err := os.OpenRoot(work)
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	object := gameres.Object{MD5: target.MD5, Size: target.Size}
	transfers := []gamedl.Transfer{{Object: object, Source: gameres.Source{Kind: gameres.SourceCAS, Object: object, Size: object.Size}}}
	missing, err := engine.Remaining(ctx, transfers)
	if err != nil || missing != object.Size {
		t.Fatalf("fixture must start uncached: %d %v", missing, err)
	}
	stats, err := historicalUpdateSeedBlobs(t, ctx, source, destination, engine, []gameres.Target{target})
	if err != nil || stats.Files != 1 || stats.Bytes != object.Size || stats.CachedFiles != 0 {
		t.Fatalf("independent complete-target seed failed: %+v %v", stats, err)
	}
	missing, err = engine.Remaining(ctx, transfers)
	if err != nil || missing != 0 {
		t.Fatalf("ordinary engine did not resume from verified published cache: %d %v", missing, err)
	}
	path, err := engine.BlobSource(ctx, object)
	if err != nil {
		t.Fatal(err)
	}
	sourceInfo, err := source.Stat(target.Path)
	if err != nil {
		t.Fatal(err)
	}
	cacheInfo, err := destination.Stat(path)
	if err != nil || os.SameFile(sourceInfo, cacheInfo) {
		t.Fatalf("cache reused source file identity: %v", err)
	}
	stats, err = historicalUpdateSeedBlobs(t, ctx, source, destination, engine, []gameres.Target{target})
	if err != nil || stats.Files != 0 || stats.Bytes != 0 || stats.CachedFiles != 1 || stats.CachedBytes != object.Size {
		t.Fatalf("repeat seed did not reuse verified CAS: %+v %v", stats, err)
	}
	wrong := target
	wrong.MD5 = strings.Repeat("1", 32)
	if _, err := historicalUpdateSeedBlobs(t, ctx, source, destination, engine, []gameres.Target{wrong}); err == nil {
		t.Fatal("source whose whole MD5 differs from selected target was published")
	}
	if _, err := engine.BlobSource(ctx, gameres.Object{MD5: wrong.MD5, Size: wrong.Size}); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unverified source acquired a CAS publication")
	}
	if err := historicalUpdateSameTree(ctx, sourcePath, before); err != nil {
		t.Fatalf("source installation was modified by cache seeding: %v", err)
	}
}

// Run after HistoricalBaseline, with its marked workspace and config env. Use
// `go test -tags e2e -run '^TestRealGameResourcesHistoricalUpdate$' -timeout 30h`:
// this test caps its context at 24h, reserving five minutes before an earlier Go
// test deadline for graceful cancellation and unchanged-root evidence.
//
// A transfer timeout leaves the 1.5.4 installation intact and retains verified
// blobs/bitmaps. Rerun this test without deleting/resetting any cache: the normal
// planner revalidates identities and the engine resumes partial transfers. If a
// commit was interrupted, inspect/resolve the product recovery state first; the
// test fails closed on an active transaction and never guesses rollback. An
// already-published 1.5.6 root is retained as evidence, never relabelled as 1.5.4.
// This test never launches the game or explicitly cleans its root/state/cache.
// ODDWARD_E2E_HISTORICAL_REUSE_CURRENT=1 separately opts into independent copies
// from the verified fresh installation after normal plan selection. Original
// planned network bytes remain logged; seeded cache bytes and actual wire bytes
// are separate evidence. Patch results are never seeded.
func TestRealGameResourcesHistoricalUpdate(t *testing.T) {
	base := historicalWorkspace(t)
	root, err := downloadDirectory(filepath.Join(base, "historical"), false)
	if err != nil {
		t.Fatal("HistoricalBaseline must succeed first: ", err)
	}
	stateRoot, err := historicalStateRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	limit := 24 * time.Hour
	if deadline, exists := t.Deadline(); exists {
		limit = min(limit, time.Until(deadline)-5*time.Minute)
	}
	if limit <= time.Minute {
		t.Fatal("historical update needs a longer outer test timeout; use -timeout 30h")
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	transport := &historicalUpdateTransport{base: http.DefaultTransport, byID: map[string]int64{}}
	backend := &GameDownloadBackend{StateRoot: stateRoot, Client: &http.Client{Transport: transport}, Version: "historical-update-e2e"}
	if err := backend.guard(ctx, true); err != nil {
		t.Fatal(err)
	}
	store, err := backend.installStore()
	if err != nil {
		t.Fatal(err)
	}
	original, err := store.ReadInstall(root)
	if err != nil || original.Version != "1.5.4" || original.State != "ready" || original.VerifyCoverage != "full" || original.VerifiedTargets != 334 || len(original.Targets) != 334 || original.TxnID == "" || original.RootFileID == "" || original.BaselineMismatch || original.Branch != bootstrapBranch {
		t.Fatalf("historical baseline must be a complete ready 1.5.4 publication: %v", err)
	}
	status, err := store.TransactionStatus(ctx, root, downloadWork(root))
	if err != nil || status.LaunchBlocked {
		t.Fatalf("resolve product transaction recovery before retry: status=%+v: %v", status, err)
	}
	before, err := historicalUpdateTree(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	initialization := []string{downloadContent + "PatcherConfig/Patch_Windows.json", downloadContent + "PatcherConfig/Patch_Windows.source.json"}
	for _, path := range initialization {
		if identity, exists := before[path]; !exists || identity.Directory || identity.Checksum == "" {
			t.Fatalf("missing historical initialization fingerprint: %s", path)
		}
	}
	commitEntered, published := false, false
	started := time.Now()
	defer func() {
		total, byID := transport.evidence()
		t.Logf("historical update consumed_response_body_bytes=%d elapsed=%s commit_entered=%v published=%v", total, time.Since(started), commitEntered, published)
		ids := make([]string, 0, len(byID))
		for id := range byID {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			t.Logf("historical wire object=%s body_bytes=%d", id, byID[id])
		}
		if !commitEntered {
			checkCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
			defer stop()
			if err := historicalUpdateSameTree(checkCtx, root, before); err != nil {
				t.Errorf("precommit failure changed historical installation: %v", err)
			}
			current, err := store.ReadInstall(root)
			if err != nil || !reflect.DeepEqual(current, original) {
				t.Errorf("precommit failure changed exact old publication: %v", err)
			}
		} else if !published {
			t.Log("commit began; preserve all evidence and use normal transaction recovery before retry")
		}
	}()
	unchanged := func(phase string) {
		t.Helper()
		if err := historicalUpdateSameTree(ctx, root, before); err != nil {
			t.Fatalf("%s: %v", phase, err)
		}
		record, err := store.ReadInstall(root)
		if err != nil || !reflect.DeepEqual(record, original) {
			t.Fatalf("%s changed exact old install record before commit: %v", phase, err)
		}
	}
	old, err := historicalDelivery(ctx, backend.metadata(), os.Getenv("ODDWARD_E2E_HISTORICAL_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	oldTargets, err := historicalSelection(old.Manifest, nil)
	if err != nil || original.ListHash != old.Config.ListHash || original.ManifestDigest != old.Config.ListHash {
		t.Fatalf("baseline is not the selected official 1.5.4 delivery: %v", err)
	}
	if err := historicalUpdateTargetSet(original, oldTargets); err != nil {
		t.Fatal(err)
	}
	current, err := backend.metadata().delivery(ctx, bootstrapBranch, "1.5.6")
	if err != nil || current.Config.ResVersion != "1.5.6" {
		t.Fatalf("current official release no longer matches pinned 1.5.6: %v", err)
	}
	expected, err := historicalSelection(current.Manifest, nil)
	if err != nil || len(expected) != 334 {
		t.Fatalf("expected complete 334-target official 1.5.6 manifest: %v", err)
	}
	// No test capabilities, routes, process observations, free-space values or
	// availability results are injected into this production planner.
	plan, err := backend.buildDownloadPlan(ctx, root, state.DownloadUpdate, current, original, "")
	if err != nil {
		t.Fatal(err)
	}
	prefs, err := backend.preferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !patchApplyAdmitted && plan.hasPatch() {
		t.Fatal("production plan crossed the closed patch admission gate")
	}
	t.Logf("historical update from=%s old_list=%s old_txn=%s to=%s new_list=%s txn=%s patch_admitted=%v preference_patch=%v targets=%d network_plan=%d patch=%d full=%d cached=%d reused=%d peak=%d reserve=%d free=%d context_limit=%s", original.Version, original.ListHash, original.TxnID, current.Config.ResVersion, current.Config.ListHash, plan.id, patchApplyAdmitted, patchApplyAdmitted, len(plan.plan.Targets), plan.plan.DownloadBytes, plan.plan.PatchBytes, plan.plan.FullBytes, plan.plan.CachedBytes, plan.plan.ReusedBytes, plan.plan.PeakAllocBytes, plan.margin, plan.free, limit)
	counts := make(map[string]int)
	for _, target := range plan.plan.Targets {
		action := historicalUpdateAction(target.Action)
		counts[action]++
		t.Logf("historical plan action=%s path=%s md5=%s size=%d reason=%s", action, target.Path, target.MD5, target.Size, target.Reason)
		if target.Action == gameres.ActionPatch && target.Route != nil {
			for _, step := range target.Route.Steps {
				t.Logf("historical patch old=%s new=%s blob=%s", step.Old, step.New, step.Blob)
			}
		}
	}
	t.Logf("historical action counts reuse=%d cached=%d patch=%d full=%d", counts["reuse-installed"], counts["install-cached"], counts["patch"], counts["full"])
	unchanged("production planning")
	var last atomic.Int64
	var fallbackNotices atomic.Int64
	report := func(event state.Event) {
		if event.Kind == state.DownloadFallbackNotice {
			fallbackNotices.Add(1)
			t.Logf("historical production fallback notice=%s", event.Message)
		}
		if event.Kind == state.DownloadProgress || event.Kind == state.ApplyProgress {
			seconds := int64(time.Since(started) / time.Second)
			previous := last.Load()
			if seconds >= previous+30 && last.CompareAndSwap(previous, seconds) {
				t.Logf("historical update event=%d stage=%d bytes=%d/%d rate=%d elapsed=%ds", event.Kind, event.Stage, event.Downloaded, event.Total, event.Rate, seconds)
			}
		}
	}
	planned, err := backend.presentDownloadPlan(plan, state.Event{}, report)
	if err != nil || planned.Kind != state.DownloadPlanned || planned.Enabled {
		t.Fatalf("production plan could not be run: event=%d: %v", planned.Kind, err)
	}
	seeded, err := historicalUpdateMaybeSeedCurrent(t, ctx, backend, plan, base, expected)
	if err != nil {
		t.Fatal("opt-in verified current-install cache seeding: ", err)
	}
	t.Logf("historical cache reuse enabled=%v seeded_files=%d seeded_cache_bytes=%d already_cached_files=%d already_cached_bytes=%d original_plan_expected_network=%d engine_resume_measured=%v engine_resume_missing_before=%d engine_resume_missing_after=%d", os.Getenv("ODDWARD_E2E_HISTORICAL_REUSE_CURRENT") == "1", seeded.Files, seeded.Bytes, seeded.CachedFiles, seeded.CachedBytes, plan.plan.DownloadBytes, os.Getenv("ODDWARD_E2E_HISTORICAL_REUSE_CURRENT") == "1", seeded.RemainingBefore, seeded.RemainingAfter)
	unchanged("optional independent current-install cache seeding")
	transport.reset()
	req := launcher.DownloadRequest{Root: root, Kind: state.DownloadUpdate, Effect: state.Effect{Kind: state.EffectDownloadRun}}
	runStarted := time.Now()
	transferred, err := backend.runDownload(ctx, req, state.Event{}, report)
	if err != nil || transferred.Kind != state.DownloadVerified && transferred.Kind != state.ApplyFinished {
		t.Fatalf("production update transfer event=%d: %v", transferred.Kind, err)
	}
	unchanged("production transfer/apply")
	req.Effect.Kind = state.EffectDownloadVerify
	verified, err := backend.verifyPlan(ctx, req, state.Event{}, report)
	if err != nil || verified.Kind != state.DownloadVerified {
		t.Fatalf("production update verify event=%d: %v", verified.Kind, err)
	}
	unchanged("production staged verification")
	t.Logf("historical transfer+verify=%s fallback_notices=%d", time.Since(runStarted), fallbackNotices.Load())
	if err := backend.guard(ctx, true); err != nil {
		t.Fatal(err)
	}
	req.Effect.Kind = state.EffectDownloadCommit
	commitEntered = true
	commitStarted := time.Now()
	committed, err := backend.commitDownload(ctx, req, state.Event{}, report)
	if err != nil || committed.Kind != state.DownloadCommitted || committed.Message != "1.5.6" {
		t.Fatalf("production update commit event=%d version=%s: %v", committed.Kind, committed.Message, err)
	}
	published = true
	record, err := store.ReadInstall(root)
	if err != nil || record.Version != "1.5.6" || record.BaseVersion != "1.5.4" || record.State != "ready" || record.Mode != "oddward" || record.Branch != bootstrapBranch || record.ListHash != current.Config.ListHash || record.ManifestDigest != current.Config.ListHash || record.TxnID != plan.id || record.TxnID == original.TxnID || record.RootFileID != original.RootFileID || record.VerifyCoverage != "full" || record.VerifiedTargets != 334 || record.PublishedAt.IsZero() || record.BaselineMismatch {
		t.Fatalf("product transaction did not publish exact new generation: %+v: %v", record, err)
	}
	if err := historicalUpdateTargetSet(record, expected); err != nil {
		t.Fatal(err)
	}
	installed, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer installed.Close()
	var bytes int64
	for _, target := range expected {
		file, err := historicalOpenFile(installed, target.Path)
		if err != nil {
			t.Fatal(err)
		}
		err = historicalCheckFile(ctx, file, gameres.Object{MD5: target.MD5, Size: target.Size})
		file.Close()
		if err != nil {
			t.Fatalf("published 1.5.6 target %s failed whole MD5: %v", target.Path, err)
		}
		bytes += target.Size
	}
	if bytes != current.Config.ResSize {
		t.Fatalf("published byte total=%d official=%d", bytes, current.Config.ResSize)
	}
	owned := make(map[string]bool, len(original.Targets))
	for _, target := range original.Targets {
		owned[strings.ToUpper(target.Path)] = true
	}
	for path, identity := range before {
		if identity.Directory || owned[strings.ToUpper(path)] {
			continue
		}
		after, err := historicalUpdateSample(ctx, installed, path)
		if err != nil || !reflect.DeepEqual(after, identity) {
			t.Fatalf("update changed initialization or an unowned file %s: %v", path, err)
		}
	}
	for _, path := range initialization {
		t.Logf("historical initialization preserved path=%s fingerprint=%s", path, before[path].Checksum)
	}
	installation, err := game.Inspect(ctx, root)
	if err != nil || installation.AppID != bootstrapAppID || installation.Branch != bootstrapBranch {
		t.Fatalf("updated installation inspection failed: %v", err)
	}
	status, err = store.TransactionStatus(ctx, root, downloadWork(root))
	if err != nil || status.LaunchBlocked {
		t.Fatalf("published update remains launch-blocked: status=%+v: %v", status, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "NTELauncher")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("official setup tree unexpectedly exists: %v", err)
	}
	t.Logf("historical update published version=%s base=%s targets=%d verified_bytes=%d list=%s old_txn=%s new_txn=%s commit_and_whole_verify=%s", record.Version, record.BaseVersion, len(record.Targets), bytes, record.ListHash, original.TxnID, record.TxnID, time.Since(commitStarted))
}

func TestHistoricalUpdateSampleDetectsChangedBytesWithRestoredMetadata(t *testing.T) {
	path := t.TempDir()
	name := filepath.Join(path, "large.bin")
	data := make([]byte, 2<<20)
	for index := range data {
		data[index] = byte(index % 251)
	}
	if err := os.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	before, err := historicalUpdateSample(context.Background(), root, "large.bin")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	file, err := root.OpenFile("large.bin", os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteAt([]byte("changed sampled bytes"), 1<<20)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(name, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := historicalUpdateSample(context.Background(), root, "large.bin")
	if err != nil {
		t.Fatal(err)
	}
	if before.Size != after.Size || !before.Modified.Equal(after.Modified) {
		t.Fatal("synthetic mutation did not preserve size/time")
	}
	if before.Checksum == after.Checksum || reflect.DeepEqual(before, after) {
		t.Fatal("historical transfer samples accepted changed file bytes with restored metadata")
	}
	if _, err := historicalUpdateSample(context.Background(), root, "../outside.bin"); !errors.Is(err, gameres.ErrPathUnsafe) {
		t.Fatalf("unsafe sample path accepted: %v", err)
	}
}

func TestHistoricalUpdateTreeExcludesOnlyWorkAndDetectsInventoryChanges(t *testing.T) {
	path := t.TempDir()
	for name, data := range map[string]string{"Client/small.bin": "old", ".oddward/cache/data": "cache"} {
		name = filepath.Join(path, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	before, err := historicalUpdateTree(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	dirInfo, statErr := os.Stat(filepath.Join(path, "Client"))
	if statErr != nil {
		t.Fatal(statErr)
	}
	if !before["Client"].Modified.Equal(dirInfo.ModTime()) {
		t.Fatal("snapshot used stale Windows directory enumeration metadata")
	}
	if err := historicalUpdateSameTree(ctx, path, before); err != nil {
		t.Fatalf("snapshot changed without any intervening test write: %v", err)
	}
	if len(before) != 2 || before["Client/small.bin"].Checksum == "" {
		t.Fatalf("unexpected installed tree snapshot: %+v", before)
	}
	if err := os.WriteFile(filepath.Join(path, ".oddward", "cache", "new"), []byte("new cache"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := historicalUpdateSameTree(ctx, path, before); err != nil {
		t.Fatalf("working-cache transfer was treated as installation mutation: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "unexpected.bin"), []byte("new installation path"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := historicalUpdateSameTree(ctx, path, before); err == nil {
		t.Fatal("precommit new installation file was not detected")
	}
}
