//go:build windows && e2e

// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/game"
	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameinstall"
	"github.com/nerv-base/Oddward/internal/gameres"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"golang.org/x/sys/windows"
)

// historicalSelection compares identities only to choose sources. Assembly must
// independently hash every copied or downloaded file before publication.
func historicalSelection(manifest gameres.ResList, current []gameinstall.InstallTarget) ([]gameres.Target, error) {
	manifest, err := canonicalDownloadManifest(manifest)
	if err != nil {
		return nil, err
	}
	common := make(map[string]gameres.Object, len(current))
	for _, target := range current {
		path, err := downloadTargetPath(target.Path)
		if err != nil {
			return nil, err
		}
		key := strings.ToUpper(path)
		if _, exists := common[key]; exists {
			return nil, fmt.Errorf("duplicate current target %s", path)
		}
		common[key] = gameres.Object{MD5: target.MD5, Size: target.Size}
	}
	var targets []gameres.Target
	seen := make(map[string]bool)
	add := func(path string, object gameres.Object, source gameres.Source) error {
		key := strings.ToUpper(path)
		if seen[key] {
			return fmt.Errorf("duplicate historical target %s", path)
		}
		seen[key] = true
		target := gameres.Target{Path: path, MD5: object.MD5, Size: object.Size, Action: gameres.ActionFull, Full: source}
		if common[key] == object {
			target.Action = gameres.ActionReuseInstalled
		}
		targets = append(targets, target)
		return nil
	}
	for _, file := range manifest.Files {
		object := gameres.Object{MD5: file.MD5, Size: file.Size}
		if err := add(file.Path, object, gameres.Source{Kind: gameres.SourceCAS, Object: object, Size: object.Size}); err != nil {
			return nil, err
		}
	}
	for _, pak := range manifest.Packages {
		for _, entry := range pak.Entries {
			object := gameres.Object{MD5: entry.MD5, Size: entry.Size}
			if err := add(entry.Path, object, gameres.Source{Kind: gameres.SourcePakRange, Object: pak.Pak, Offset: entry.Offset, Size: entry.Size}); err != nil {
				return nil, err
			}
		}
	}
	return targets, nil
}

func TestHistoricalSelectionIncludesPackedChangesAndOnlyExactCommonFiles(t *testing.T) {
	prefix := "Client/WindowsNoEditor/HT/Content/Paks/"
	old := gameres.Object{MD5: "11111111111111111111111111111111", Size: 7}
	common := gameres.Object{MD5: "22222222222222222222222222222222", Size: 9}
	pak := gameres.Object{MD5: "33333333333333333333333333333333", Size: 100}
	manifest := gameres.ResList{Files: []gameres.File{{Path: prefix + "old.bin", MD5: old.MD5, Size: old.Size}, {Path: prefix + "common.bin", MD5: common.MD5, Size: common.Size}}, Packages: []gameres.Package{{Pak: pak, Entries: []gameres.Entry{{Path: prefix + "packed.bin", Pak: pak, Offset: 20, Size: old.Size, MD5: old.MD5}}}}}
	current := []gameinstall.InstallTarget{{Path: prefix + "old.bin", MD5: common.MD5, Size: old.Size}, {Path: prefix + "common.bin", MD5: common.MD5, Size: common.Size}, {Path: prefix + "packed.bin", MD5: old.MD5, Size: old.Size + 1}}
	got, err := historicalSelection(manifest, current)
	if err != nil || len(got) != 3 {
		t.Fatalf("complete historical selection got=%v: %v", got, err)
	}
	if got[0].Action != gameres.ActionFull || got[0].Full.Kind != gameres.SourceCAS || got[0].Full.Object != old {
		t.Fatalf("old-only CAS source=%+v", got[0])
	}
	if got[1].Action != gameres.ActionReuseInstalled {
		t.Fatalf("identical common file not selected for independent copy: %+v", got[1])
	}
	if got[2].Action != gameres.ActionFull || got[2].Full.Kind != gameres.SourcePakRange || got[2].Full.Object != pak || got[2].Full.Offset != 20 || got[2].Full.Size != old.Size {
		t.Fatalf("packed historical source=%+v", got[2])
	}
}

func TestHistoricalCommonCopiesAreIndependentAndRejectUntrustedSources(t *testing.T) {
	for _, mode := range []string{"independent", "corrupt", "hardlinked", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sourcePath, destinationPath := t.TempDir(), t.TempDir()
			want := []byte("correct source")
			digest := md5.Sum(want)
			object := gameres.Object{MD5: hex.EncodeToString(digest[:]), Size: int64(len(want))}
			actual := want
			if mode == "corrupt" {
				actual = []byte("corrupt source")
			}
			if err := os.WriteFile(filepath.Join(sourcePath, "common.bin"), actual, 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "hardlinked" {
				if err := os.Link(filepath.Join(sourcePath, "common.bin"), filepath.Join(sourcePath, "other.bin")); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "cancelled" {
				cancel()
			}
			source, err := os.OpenRoot(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			destination, err := os.OpenRoot(destinationPath)
			if err != nil {
				t.Fatal(err)
			}
			defer destination.Close()
			err = historicalCopyCommon(ctx, source, destination, "common.bin", "copy.part", object)
			if mode != "independent" {
				wantErr := gameinstall.ErrStaleState
				if mode == "hardlinked" {
					wantErr = gameres.ErrPathUnsafe
				} else if mode == "cancelled" {
					wantErr = context.Canceled
				}
				if !errors.Is(err, wantErr) {
					t.Fatalf("untrusted source error=%v, want %v", err, wantErr)
				}
				if _, err := destination.Lstat("copy.part"); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed copy left a publishable part: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				file, err := historicalOpenFile(destination, "copy.part")
				if err != nil {
					t.Fatal(err)
				}
				err = historicalCheckFile(ctx, file, object)
				file.Close()
				if err != nil {
					t.Fatal(err)
				}
				original, err := source.Stat("common.bin")
				if err != nil {
					t.Fatal(err)
				}
				copyInfo, err := destination.Stat("copy.part")
				if err != nil || os.SameFile(original, copyInfo) {
					t.Fatalf("copied historical file shares source identity: %v", err)
				}
			}
			unchanged, err := source.ReadFile("common.bin")
			if err != nil || string(unchanged) != string(actual) {
				t.Fatalf("source changed during historical copy: %v", err)
			}
		})
	}
}

func TestHistoricalDeliveryBindsCachedConfigToLiveArchive(t *testing.T) {
	plain := []byte(`<ResList version="1.5.4" tag="baseTag"><Res filename="Client/WindowsNoEditor/HT/Content/Paks/a.pak" md5="11111111111111111111111111111111" filesize="7"/></ResList>`)
	archive := preloadSyntheticArchive(t, plain)
	digest := md5.Sum(plain)
	for _, mode := range []string{"valid", "wrong-list-hash", "wrong-size", "wrong-version"} {
		t.Run(mode, func(t *testing.T) {
			version, hash, size := "1.5.4", hex.EncodeToString(digest[:]), 7
			if mode == "wrong-list-hash" {
				hash = "22222222222222222222222222222222"
			} else if mode == "wrong-size" {
				size++
			} else if mode == "wrong-version" {
				version = "1.5.5"
			}
			config := []byte(fmt.Sprintf(`<config><ResVersion>%s</ResVersion><ResSize>%d</ResSize><Extra><listHash>%s</listHash></Extra></config>`, version, size, hash))
			configPath := filepath.Join(t.TempDir(), "config.xml")
			if err := os.WriteFile(configPath, config, 0600); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				if request.URL.Path != "/clientRes/PC_150/Version/Windows/version/1.5.4/ResList.bin.zip" {
					t.Errorf("unexpected metadata path %s", request.URL.Path)
					http.NotFound(w, request)
					return
				}
				_, _ = w.Write(archive)
			}))
			defer server.Close()
			metadata := remoteDownloads{client: server.Client(), roots: []string{server.URL + "/clientRes"}}
			delivery, err := historicalDelivery(context.Background(), metadata, configPath)
			if mode == "valid" {
				if err != nil || delivery.Config.ResVersion != "1.5.4" || delivery.Config.ListHash != hash || delivery.Config.ResSize != 7 || len(delivery.Manifest.Files) != 1 || calls.Load() != 1 {
					t.Fatalf("verified historical delivery=%+v calls=%d: %v", delivery.Config, calls.Load(), err)
				}
			} else if err == nil {
				t.Fatal("mismatching cached historical metadata was accepted")
			}
			if mode == "wrong-version" && calls.Load() != 0 {
				t.Fatal("unselected historical version made a network request")
			}
		})
	}
}

// historicalDelivery is shared with opt-in comparison tests. Only the explicitly
// selected cached public config is local; the archive must be fetched from an
// official origin and match both of that config's plaintext list/diff hashes.
func historicalDelivery(ctx context.Context, metadata remoteDownloads, configPath string) (gameDelivery, error) {
	if configPath == "" {
		return gameDelivery{}, errors.New("ODDWARD_E2E_HISTORICAL_CONFIG must select the cached 1.5.4 public config")
	}
	file, err := os.Open(configPath)
	if err != nil {
		return gameDelivery{}, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 8<<20 {
		file.Close()
		return gameDelivery{}, errors.New("invalid cached historical config")
	}
	data, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
	file.Close()
	if err != nil || len(data) > 8<<20 {
		return gameDelivery{}, errors.New("unable to read bounded cached historical config")
	}
	config, err := gameres.ParseConfig(data)
	if err != nil || config.ResVersion != "1.5.4" || config.ResSize <= 0 {
		return gameDelivery{}, errors.New("historical config is not a valid 1.5.4 delivery")
	}
	var failures []error
	for _, root := range metadata.roots {
		address, err := gameres.ManifestURL(root, bootstrapBranch, config.ResVersion)
		if err != nil {
			return gameDelivery{}, err
		}
		response, err := metadata.get(ctx, root, address, 32<<20)
		if err == nil {
			var xml gameres.ManifestXML
			xml, err = gameres.OpenManifestZIP(response.Data)
			if err == nil {
				var manifest gameres.ResList
				var graph *gameres.PatchGraph
				manifest, graph, err = gameres.ParseDelivery(config, xml)
				if err == nil {
					return gameDelivery{Config: config, Manifest: manifest, Graph: graph, XML: xml, ConfigResponse: response, Branch: bootstrapBranch}, nil
				}
			}
		}
		if ctx.Err() != nil {
			return gameDelivery{}, ctx.Err()
		}
		failures = append(failures, err)
	}
	return gameDelivery{}, errors.Join(append(failures, errDownloadMetadata)...)
}

func historicalWorkspace(t *testing.T) string {
	t.Helper()
	if testing.Short() || os.Getenv("ODDWARD_E2E_NETWORK") != "1" {
		t.Skip("official historical resources require ODDWARD_E2E_NETWORK=1")
	}
	base, err := downloadDirectory(os.Getenv("ODDWARD_E2E_DOWNLOAD_ROOT"), false)
	if err != nil || !strings.EqualFold(filepath.VolumeName(base), "D:") || !strings.HasPrefix(filepath.Base(base), "Oddward-validation-") {
		t.Fatal("choose an explicitly marked D-volume Oddward-validation-* workspace")
	}
	marker, err := os.ReadFile(filepath.Join(base, ".oddward-validation"))
	if err != nil || strings.TrimSpace(string(marker)) != filepath.Base(base) {
		t.Fatal("missing isolated D workspace marker")
	}
	if os.Getenv("ODDWARD_E2E_HISTORICAL_CONFIG") == "" {
		t.Fatal("explicit cached historical public config path is required")
	}
	return base
}

func historicalStateRoot(base string) (string, error) {
	path := filepath.Join(base, "historical-state")
	if err := os.Mkdir(path, 0700); err == nil {
		if err := os.WriteFile(filepath.Join(path, ".historical-validation"), []byte(filepath.Base(base)), 0600); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return "", err
	}
	if _, err := downloadDirectory(path, false); err != nil {
		return "", err
	}
	marker, err := os.ReadFile(filepath.Join(path, ".historical-validation"))
	if err != nil || string(marker) != filepath.Base(base) {
		return "", errors.New("historical state is not owned by this validation workspace")
	}
	return path, nil
}

func historicalPlan(base string, old, current gameDelivery) (*downloadPlan, error) {
	currentFiles, err := historicalSelection(current.Manifest, nil)
	if err != nil {
		return nil, err
	}
	var currentTargets []gameinstall.InstallTarget
	for _, target := range currentFiles {
		currentTargets = append(currentTargets, gameinstall.InstallTarget{Path: target.Path, MD5: target.MD5, Size: target.Size})
	}
	targets, err := historicalSelection(old.Manifest, currentTargets)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(base, "historical")
	key, err := gameinstall.InstallKey(root)
	if err != nil {
		return nil, err
	}
	id := "historical-" + old.Config.ListHash + "-" + current.Config.ListHash[:16]
	return &downloadPlan{root: root, key: key, id: id, kind: state.DownloadInstall, delivery: old, plan: gameres.Plan{Targets: targets}, margin: 2 << 30}, nil
}

func historicalOpenPrepared(ctx context.Context, backend *GameDownloadBackend, stateRoot string, lease *gamedl.Lease) (*gamedl.Engine, error) {
	cache := filepath.Join(stateRoot, "prepared-cache")
	tasks := filepath.Join(stateRoot, "prepared-tasks")
	for _, path := range []string{cache, tasks} {
		if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if _, err := downloadDirectory(path, false); err != nil {
			return nil, err
		}
	}
	metadata := backend.metadata()
	return gamedl.New(gamedl.Options{WorkRoot: cache, StateRoot: tasks, Client: metadata.client, Resolve: metadata.resolve(bootstrapBranch), Lease: lease, UserAgent: "Oddward/historical-e2e"})
}

// TestRealGameResourcesHistoricalPrepare can run while the separate first-install
// test is still downloading. It writes only historical-state/prepared-* and has
// its own lease. The lead explicitly schedules it; this test starts no other job.
func TestRealGameResourcesHistoricalPrepare(t *testing.T) {
	base := historicalWorkspace(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	stateRoot, err := historicalStateRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	backend := &GameDownloadBackend{StateRoot: stateRoot, Version: "historical-e2e"}
	if err := backend.guard(ctx, true); err != nil {
		t.Fatal(err)
	}
	old, err := historicalDelivery(ctx, backend.metadata(), os.Getenv("ODDWARD_E2E_HISTORICAL_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	current, err := backend.metadata().delivery(ctx, bootstrapBranch, "1.5.6")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := historicalPlan(base, old, current)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := historicalOpenPrepared(ctx, backend, stateRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	task := plan.transferTask()
	task.Purpose = "historical-baseline-preparation"
	remaining, err := engine.Remaining(ctx, task.Transfers)
	if err != nil {
		t.Fatal(err)
	}
	free, err := downloadFree(base)
	if err != nil || free < remaining+plan.margin {
		t.Fatalf("historical preparation requires %d free bytes, have %d: %v", remaining+plan.margin, free, err)
	}
	var total int64
	for _, transfer := range task.Transfers {
		total += transfer.Object.Size
	}
	t.Logf("historical preparation old=%s list=%s targets=%d exclusive_objects=%d bytes=%d remaining=%d free=%d", old.Config.ResVersion, old.Config.ListHash, len(plan.plan.Targets), len(task.Transfers), total, remaining, free)
	started := time.Now()
	var last atomic.Int64
	if err := engine.Run(ctx, task, func(progress gamedl.Progress) {
		seconds := int64(time.Since(started) / time.Second)
		previous := last.Load()
		if seconds >= previous+30 && last.CompareAndSwap(previous, seconds) {
			t.Logf("historical preparation bytes=%d/%d network=%d elapsed=%ds", progress.Downloaded, progress.Total, progress.NetworkBytes, seconds)
		}
	}); err != nil {
		t.Fatal(err)
	}
	completed, err := engine.Store().Load(ctx, task.ID)
	if err != nil || completed.State != gamedl.StateStaged {
		t.Fatalf("historical cache was not staged: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(base, "historical")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preparation unexpectedly created an installation: %v", err)
	}
	t.Logf("historical preparation verified objects=%d bytes=%d elapsed=%s", len(task.Transfers), total, time.Since(started))
}

func historicalOpenFile(root *os.Root, path string) (*os.File, error) {
	// The synthetic prefix lets the path validator check components without
	// applying the manifest's leading-letter rule to CAS object filenames.
	clean, err := gameres.NormalizePath("X/" + path)
	if err != nil || clean != "X/"+path {
		return nil, gameres.ErrPathUnsafe
	}
	parts := strings.Split(path, "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return nil, err
		}
		if downloadReparse(info) || i < len(parts)-1 && !info.IsDir() {
			return nil, gameres.ErrPathUnsafe
		}
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	var identity windows.ByHandleFileInformation
	info, statErr := file.Stat()
	if err = windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &identity); err != nil || statErr != nil || !info.Mode().IsRegular() || identity.NumberOfLinks != 1 || identity.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		file.Close()
		return nil, gameres.ErrPathUnsafe
	}
	return file, nil
}

type historicalReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r historicalReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func historicalCheckFile(ctx context.Context, file *os.File, object gameres.Object) error {
	info, err := file.Stat()
	if err != nil || info.Size() != object.Size {
		return fmt.Errorf("historical file size differs: %w", gameinstall.ErrStaleState)
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	hash := md5.New()
	n, err := io.CopyBuffer(hash, io.LimitReader(historicalReader{ctx, file}, object.Size+1), make([]byte, 256<<10))
	if err != nil || n != object.Size || hex.EncodeToString(hash.Sum(nil)) != object.MD5 {
		return fmt.Errorf("historical file whole checksum differs: %w", errors.Join(err, gameinstall.ErrStaleState))
	}
	_, err = file.Seek(0, 0)
	return err
}

// historicalCopyCommon always makes an independent ordinary file, checks the
// copied bytes and then rechecks the untouched source; cache publication adds
// the engine's own size/hash/single-link validation.
func historicalCopyCommon(ctx context.Context, source, destination *os.Root, path, part string, object gameres.Object) error {
	input, err := historicalOpenFile(source, path)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := destination.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		output.Close()
		if !keep {
			_ = destination.Remove(part)
		}
	}()
	hash := md5.New()
	n, err := io.CopyBuffer(io.MultiWriter(output, hash), io.LimitReader(historicalReader{ctx, input}, object.Size+1), make([]byte, 256<<10))
	if err != nil || n != object.Size || hex.EncodeToString(hash.Sum(nil)) != object.MD5 {
		return fmt.Errorf("independent common copy differs: %w", errors.Join(err, gameinstall.ErrStaleState))
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	if err := historicalCheckFile(ctx, input, object); err != nil {
		return err
	}
	keep = true
	return nil
}

func TestHistoricalCopyAcceptsDecimalLeadingCASName(t *testing.T) {
	contents := []byte("abc")
	object := gameres.Object{MD5: "900150983cd24fb0d6963f7d28e17f72", Size: int64(len(contents))}
	source, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	if err := source.WriteFile(object.String(), contents, 0600); err != nil {
		t.Fatal(err)
	}
	if err := historicalCopyCommon(t.Context(), source, destination, object.String(), "isolated.part", object); err != nil {
		t.Fatal(err)
	}
	a, err := source.Stat(object.String())
	if err != nil {
		t.Fatal(err)
	}
	b, err := destination.Stat("isolated.part")
	if err != nil || os.SameFile(a, b) {
		t.Fatalf("expected an independent copy: %v", err)
	}
}

// TestRealGameResourcesHistoricalBaseline requires the verified first-install
// root plus a finished Prepare run. It never downloads missing resource bodies.
// The resulting true 1.5.4 root/state are retained for the lead's later same-root
// 1.5.6 update. Failed evidence is retained, never recursively removed.
func TestRealGameResourcesHistoricalBaseline(t *testing.T) {
	base := historicalWorkspace(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	stateRoot, err := historicalStateRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "historical")
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("historical installation child must not exist; keep prior run evidence")
	}
	backend := &GameDownloadBackend{StateRoot: stateRoot, Version: "historical-e2e"}
	guard := func(ctx context.Context) error { return backend.guard(ctx, true) }
	if err := guard(ctx); err != nil {
		t.Fatal(err)
	}
	old, err := historicalDelivery(ctx, backend.metadata(), os.Getenv("ODDWARD_E2E_HISTORICAL_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	current, err := backend.metadata().delivery(ctx, bootstrapBranch, "1.5.6")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := historicalPlan(base, old, current)
	if err != nil {
		t.Fatal(err)
	}
	currentRoot, err := downloadDirectory(filepath.Join(base, "fresh"), false)
	if err != nil {
		t.Fatal(err)
	}
	currentExclusion, err := acquireDownloadMutation(currentRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer currentExclusion.Close()
	currentLease, err := gamedl.Acquire(downloadWork(currentRoot))
	if err != nil {
		t.Fatal(err)
	}
	defer currentLease.Close()
	currentBackend := &GameDownloadBackend{StateRoot: filepath.Join(base, "install-state")}
	currentStore, err := currentBackend.installStore()
	if err != nil {
		t.Fatal(err)
	}
	original, err := currentStore.ReadInstall(currentRoot)
	if err != nil || original.State != "ready" || original.Version != current.Config.ResVersion || original.ListHash != current.Config.ListHash || original.ManifestDigest != current.Config.ListHash || original.Branch != bootstrapBranch || original.VerifyCoverage != "full" || original.VerifiedTargets != len(original.Targets) || original.BaselineMismatch {
		t.Fatalf("current isolated source is not fully published with the observed official metadata: %v", err)
	}
	preparedPath := filepath.Join(stateRoot, "prepared-cache")
	if _, err := downloadDirectory(preparedPath, false); err != nil {
		t.Fatal("run the historical preparation entry first: ", err)
	}
	preparedLease, err := gamedl.Acquire(preparedPath)
	if err != nil {
		t.Fatal(err)
	}
	defer preparedLease.Close()
	prepared, err := historicalOpenPrepared(ctx, backend, stateRoot, preparedLease)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	preparedFiles, err := os.OpenRoot(preparedPath)
	if err != nil {
		t.Fatal(err)
	}
	defer preparedFiles.Close()
	var commonBytes int64
	imports := make(map[gameres.Object]string)
	for _, target := range plan.plan.Targets {
		object := gameres.Object{MD5: target.MD5, Size: target.Size}
		if target.Action == gameres.ActionReuseInstalled {
			commonBytes += target.Size
			continue
		}
		if _, exists := imports[object]; !exists {
			path, err := prepared.BlobSource(ctx, object)
			if err != nil {
				t.Fatalf("prepared object %s is not fully verified; run Prepare first: %v", object, err)
			}
			verified, err := historicalOpenFile(preparedFiles, path)
			if err != nil {
				t.Fatalf("prepared object %s is linked or unsafe: %v", object, err)
			}
			verified.Close()
			imports[object] = path
		}
	}
	free, err := downloadFree(base)
	if err != nil || free < commonBytes+plan.margin {
		t.Fatalf("historical assembly requires %d additional free bytes, have %d: %v", commonBytes+plan.margin, free, err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	lease, err := acquireDownloadWork(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	engine, err := backend.newDownloadEngine(ctx, plan, lease)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	workspace, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	// Both caches are verified descendants on the same explicit D volume. Move
	// each old blob rather than doubling ~50 GB; both leases remain held.
	for object, path := range imports {
		if err := guard(ctx); err != nil {
			t.Fatal(err)
		}
		if err := workspace.Rename("historical-state/prepared-cache/"+path, "historical/.oddward/"+path); err != nil {
			t.Fatalf("import %s: %v", object, err)
		}
	}
	currentFiles, err := os.OpenRoot(currentRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer currentFiles.Close()
	work, err := os.OpenRoot(downloadWork(root))
	if err != nil {
		t.Fatal(err)
	}
	defer work.Close()
	copied := make(map[gameres.Object]bool)
	started := time.Now()
	for _, target := range plan.plan.Targets {
		if target.Action != gameres.ActionReuseInstalled {
			continue
		}
		object := gameres.Object{MD5: target.MD5, Size: target.Size}
		if _, exists := imports[object]; exists || copied[object] {
			continue
		}
		if err := guard(ctx); err != nil {
			t.Fatal(err)
		}
		part := "cache/tmp/historical-copy-" + rand.Text() + ".part"
		if err := historicalCopyCommon(ctx, currentFiles, work, target.Path, part, object); err != nil {
			t.Fatal(err)
		}
		if err := engine.Publish(ctx, object, part); err != nil {
			t.Fatal(err)
		}
		copied[object] = true
		if len(copied)%25 == 0 {
			t.Logf("independent common copies=%d elapsed=%s", len(copied), time.Since(started))
		}
	}
	candidate := gameinstall.InstallRecord{Schema: 2, InstallKey: plan.key, Root: root, Mode: "fresh", State: "ready", Branch: bootstrapBranch, Version: old.Config.ResVersion, ListHash: old.Config.ListHash, ManifestDigest: old.Config.ListHash, VerifyCoverage: "full", VerifiedTargets: len(plan.plan.Targets), CreatedAt: time.Now().UTC()}
	input := gameinstall.TxnInput{Root: root, WorkRoot: downloadWork(root), TxnID: plan.id, Guard: guard, BackupRetention: 24 * time.Hour, RequiredFreeBytes: plan.margin, FreeBytes: func(context.Context) (int64, error) { return downloadFree(root) }}
	remaining := make(map[gameres.Object]int)
	for _, target := range plan.plan.Targets {
		remaining[gameres.Object{MD5: target.MD5, Size: target.Size}]++
	}
	for _, target := range plan.plan.Targets {
		object := gameres.Object{MD5: target.MD5, Size: target.Size}
		path, err := engine.BlobSource(ctx, object)
		if err != nil {
			t.Fatal(err)
		}
		remaining[object]--
		file := gameinstall.TxnFile{Path: target.Path, Source: path, MD5: target.MD5, Size: target.Size, Retain: remaining[object] > 0, Origin: "historical-verified"}
		input.Files = append(input.Files, file)
		if file.Retain {
			input.RequiredFreeBytes += file.Size
		}
		candidate.Targets = append(candidate.Targets, gameinstall.InstallTarget{Path: file.Path, MD5: file.MD5, Size: file.Size})
	}
	input.Candidate = candidate
	input.Bootstrap, err = backend.stageBootstrap(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	store, err := backend.installStore()
	if err != nil {
		t.Fatal(err)
	}
	status, err := store.Commit(ctx, input)
	if err != nil || !status.Published || status.LaunchBlocked {
		t.Fatalf("historical product transaction status=%+v: %v", status, err)
	}
	record, err := store.ReadInstall(root)
	if err != nil || record.Version != "1.5.4" || record.TxnID != plan.id || record.ListHash != old.Config.ListHash || record.ManifestDigest != old.Config.ListHash || record.State != "ready" || record.VerifyCoverage != "full" || record.VerifiedTargets != len(candidate.Targets) || len(record.Targets) != len(candidate.Targets) {
		t.Fatalf("historical publication was not complete: %v", err)
	}
	installation, err := game.Inspect(ctx, root)
	if err != nil || installation.AppID != bootstrapAppID || installation.Branch != bootstrapBranch {
		t.Fatalf("historical installation was not recognized: %v", err)
	}
	installed, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer installed.Close()
	var verifiedBytes int64
	for _, target := range candidate.Targets {
		file, err := historicalOpenFile(installed, target.Path)
		if err != nil {
			t.Fatal(err)
		}
		err = historicalCheckFile(ctx, file, gameres.Object{MD5: target.MD5, Size: target.Size})
		file.Close()
		if err != nil {
			t.Fatalf("published old target %s: %v", target.Path, err)
		}
		verifiedBytes += target.Size
	}
	if verifiedBytes != old.Config.ResSize {
		t.Fatalf("historical target byte total=%d want=%d", verifiedBytes, old.Config.ResSize)
	}
	unchanged, err := currentStore.ReadInstall(currentRoot)
	if err != nil || unchanged.TxnID != original.TxnID || unchanged.Version != original.Version || unchanged.ListHash != original.ListHash || unchanged.ManifestDigest != original.ManifestDigest || unchanged.RootFileID != original.RootFileID {
		t.Fatalf("current source publication changed: %v", err)
	}
	for _, installRoot := range []string{root, currentRoot} {
		if _, err := os.Lstat(filepath.Join(installRoot, "NTELauncher")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("official launcher setup unexpectedly exists in isolated installation: %v", err)
		}
	}
	t.Logf("true historical baseline published version=%s list=%s txn=%s targets=%d bytes=%d common_bytes=%d elapsed=%s; no launch", record.Version, record.ListHash, record.TxnID, len(record.Targets), verifiedBytes, commonBytes, time.Since(started))
}
