//go:build windows && e2e

// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameinstall"
	"github.com/nerv-base/Oddward/internal/gameres"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"golang.org/x/sys/windows"
)

const historicalCrashWorkerEnv = "ODDWARD_E2E_HISTORICAL_CRASH_WORKER"
const historicalCrashExit = 86

var historicalCrashPoints = []string{"after-placement-rename", "before-install-rename"}

type historicalCrashResult struct {
	ExitCode           int
	Recovered, Retried bool
}

type historicalCrashFixture struct {
	Token, Root, StateRoot, Checkpoint, TxnID string
	Old, New                                  gameinstall.InstallRecord
}

func historicalCrashRecord(root, version, listHash string, target gameinstall.InstallTarget) gameinstall.InstallRecord {
	// Deliberately identifies a one-target test subset, never a full installation.
	digest := fmt.Sprintf("%x", md5.Sum([]byte("oddward-crash-subset/"+version+"/"+listHash+"/"+target.Path+"/"+target.MD5+fmt.Sprint(target.Size))))
	return gameinstall.InstallRecord{Schema: 2, Root: root, Mode: "oddward", State: "ready", Branch: bootstrapBranch,
		Version: version, ListHash: digest, ManifestDigest: digest, VerifyCoverage: "full", VerifiedTargets: 1,
		CreatedAt: time.Now().UTC(), Targets: []gameinstall.InstallTarget{target}}
}

func historicalCrashCreate(t *testing.T, base, point string, old, next gameinstall.InstallTarget, oldList, newList string) historicalCrashFixture {
	t.Helper()
	scratch := filepath.Join(base, "historical-crash-"+point)
	if err := os.Mkdir(scratch, 0700); err != nil {
		t.Fatalf("crash scratch must be new: %v", err)
	}
	fixture := historicalCrashFixture{Token: rand.Text(), Root: filepath.Join(scratch, "install"), StateRoot: filepath.Join(scratch, "state"), Checkpoint: point, TxnID: "crash-" + rand.Text()}
	if err := os.WriteFile(filepath.Join(scratch, ".historical-crash"), []byte(fixture.Token), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(downloadWork(fixture.Root), "cache", "blobs"), 0700); err != nil {
		t.Fatal(err)
	}
	key, err := gameinstall.InstallKey(fixture.Root)
	if err != nil {
		t.Fatal(err)
	}
	fixture.Old = historicalCrashRecord(fixture.Root, "1.5.4", oldList, old)
	fixture.Old.Mode, fixture.Old.InstallKey = "fresh", key
	fixture.New = historicalCrashRecord(fixture.Root, "1.5.6", newList, next)
	fixture.New.InstallKey = key
	return fixture
}

func historicalCrashInput(f historicalCrashFixture, checkpoint func(string) error) gameinstall.TxnInput {
	b := &GameDownloadBackend{StateRoot: f.StateRoot}
	target := f.New.Targets[0]
	return gameinstall.TxnInput{Root: f.Root, WorkRoot: downloadWork(f.Root), TxnID: f.TxnID, Candidate: f.New,
		Files: []gameinstall.TxnFile{{Path: target.Path, Source: "cache/blobs/crash-new", MD5: target.MD5, Size: target.Size, Retain: true, Origin: "full"}},
		Guard: func(ctx context.Context) error { return b.guard(ctx, true) }, BackupRetention: 24 * time.Hour, Checkpoint: checkpoint}
}

func historicalCrashSeed(t *testing.T, ctx context.Context, f historicalCrashFixture) gameinstall.InstallRecord {
	t.Helper()
	b := &GameDownloadBackend{StateRoot: f.StateRoot}
	store, err := b.installStore()
	if err != nil {
		t.Fatal(err)
	}
	mutation, err := acquireDownloadMutation(f.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer mutation.Close()
	lease, err := gamedl.Acquire(downloadWork(f.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	bootstrap, err := b.stageBootstrap(ctx, &downloadPlan{root: f.Root, id: "seed-" + f.Token, delivery: gameDelivery{
		Config: gameres.Config{ResVersion: "1.5.4", ListHash: f.Old.ListHash, UpdateBranch: bootstrapUpdateBranch},
		Branch: bootstrapBranch, ConfigResponse: downloadResponse{Root: officialDownloadRoots[0]},
	}})
	if err != nil {
		t.Fatal(err)
	}
	target := f.Old.Targets[0]
	status, err := store.Commit(ctx, gameinstall.TxnInput{Root: f.Root, WorkRoot: downloadWork(f.Root), TxnID: "seed-" + f.Token, Candidate: f.Old,
		Files: []gameinstall.TxnFile{{Path: target.Path, Source: "cache/blobs/crash-old", MD5: target.MD5, Size: target.Size, Origin: "full"}}, Bootstrap: bootstrap,
		Guard: func(ctx context.Context) error { return b.guard(ctx, true) }, BackupRetention: 24 * time.Hour})
	if err != nil || !status.Published {
		t.Fatalf("seed subset: %+v %v", status, err)
	}
	record, err := store.ReadInstall(f.Root)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func historicalCrashCheck(t *testing.T, ctx context.Context, root string, target gameinstall.InstallTarget) {
	t.Helper()
	opened, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	file, err := historicalOpenFile(opened, target.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := historicalCheckFile(ctx, file, gameres.Object{MD5: target.MD5, Size: target.Size}); err != nil {
		t.Fatal(err)
	}
}

func historicalCrashExercise(t *testing.T, ctx context.Context, f historicalCrashFixture) historicalCrashResult {
	t.Helper()
	baseline := historicalCrashSeed(t, ctx, f)
	before, err := historicalUpdateTree(ctx, f.Root)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(filepath.Dir(f.Root), "fixture.json")
	if err := os.WriteFile(fixturePath, payload, 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestHistoricalCrashWorker$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), historicalCrashWorkerEnv+"="+fixturePath)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err = cmd.Run()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != historicalCrashExit || !strings.Contains(output.String(), "oddward-crash checkpoint="+f.Checkpoint) {
		t.Fatalf("expected abrupt checkpoint exit %d; got %v output=%s", historicalCrashExit, err, output.String())
	}
	result := historicalCrashResult{ExitCode: exited.ExitCode()}
	b := &GameDownloadBackend{StateRoot: f.StateRoot}
	store, err := b.installStore()
	if err != nil {
		t.Fatal(err)
	}
	// These are the same kernel objects held by the abruptly terminated child.
	mutation, err := acquireDownloadMutation(f.Root)
	if err != nil {
		t.Fatalf("dead child retained mutation guard: %v", err)
	}
	lease, err := gamedl.Acquire(downloadWork(f.Root))
	if err != nil {
		mutation.Close()
		t.Fatalf("dead child retained work lease: %v", err)
	}
	status, statusErr := store.TransactionStatus(ctx, f.Root, downloadWork(f.Root))
	closeErr := errors.Join(lease.Close(), mutation.Close())
	if statusErr != nil || closeErr != nil || !status.LaunchBlocked || status.Published || status.TxnID != f.TxnID {
		t.Fatalf("unpublished interrupted commit must block launch: %+v %v %v", status, statusErr, closeErr)
	}
	request := launcher.DownloadRequest{Root: f.Root, Kind: state.DownloadUpdate}
	request.Effect.Enabled = true
	for i := 0; i < 2; i++ {
		event, err := b.recoverDownload(ctx, request, state.Event{})
		if err != nil || event.Kind != state.DownloadRecoveryCleared || event.CurrentVersion != "1.5.4" {
			t.Fatalf("normal recovery %d: %+v %v", i, event, err)
		}
		record, err := store.ReadInstall(f.Root)
		if err != nil || !reflect.DeepEqual(record, baseline) {
			t.Fatalf("recovery did not restore exact old record: %v", err)
		}
		historicalCrashCheck(t, ctx, f.Root, f.Old.Targets[0])
		afterRecovery, treeErr := historicalUpdateTree(ctx, f.Root)
		if treeErr != nil || len(afterRecovery) != len(before) {
			t.Fatalf("recovery changed installed inventory: %v", treeErr)
		}
		for path, identity := range before {
			current, exists := afterRecovery[path]
			// Moving files out and back legitimately changes directory mtime.
			// Directory inventory and every regular file identity remain exact.
			if !exists || current.Directory != identity.Directory || !identity.Directory && current != identity {
				t.Fatalf("recovery changed old installed file/inventory: %s", path)
			}
		}
	}
	result.Recovered = true
	mutation, err = acquireDownloadMutation(f.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer mutation.Close()
	lease, err = gamedl.Acquire(downloadWork(f.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	f.TxnID = "retry-" + f.Token
	status, err = store.Commit(ctx, historicalCrashInput(f, nil))
	if err != nil || !status.Published || status.LaunchBlocked {
		t.Fatalf("retry: %+v %v", status, err)
	}
	record, err := store.ReadInstall(f.Root)
	if err != nil || record.State != "ready" || record.Version != "1.5.6" || record.BaseVersion != "1.5.4" || record.TxnID != f.TxnID || record.ListHash != f.New.ListHash || record.ManifestDigest != f.New.ManifestDigest || record.RootFileID != baseline.RootFileID || !reflect.DeepEqual(record.Targets, f.New.Targets) || record.VerifiedTargets != 1 || record.VerifyCoverage != "full" {
		t.Fatalf("retry publication evidence mismatch: %+v %v", record, err)
	}
	historicalCrashCheck(t, ctx, f.Root, f.New.Targets[0])
	// Both bootstrap files are outside this update's owned replacement target.
	after, err := historicalUpdateTree(ctx, f.Root)
	if err != nil {
		t.Fatal(err)
	}
	for path, identity := range before {
		if identity.Directory || path == f.Old.Targets[0].Path {
			continue
		}
		if after[path] != identity {
			t.Fatalf("retry changed bootstrap/unowned file %s", path)
		}
	}
	result.Retried = true
	t.Logf("abrupt_exit=%d checkpoint=%s old_version=1.5.4 old_txn=%s new_version=1.5.6 new_txn=%s whole_old_md5=%s whole_new_md5=%s independent_bytes=%d", result.ExitCode, f.Checkpoint, baseline.TxnID, record.TxnID, f.Old.Targets[0].MD5, f.New.Targets[0].MD5, f.New.Targets[0].Size)
	return result
}

// This entry runs only as a child. os.Exit intentionally bypasses every Go defer.
func TestHistoricalCrashWorker(t *testing.T) {
	path := os.Getenv(historicalCrashWorkerEnv)
	if path == "" {
		t.Skip("subprocess-only crash worker")
	}
	if filepath.Base(path) != "fixture.json" {
		t.Fatal("invalid crash fixture path")
	}
	scratch, err := downloadDirectory(filepath.Dir(path), false)
	if err != nil || !strings.HasPrefix(filepath.Base(scratch), "historical-crash-") {
		t.Fatal("worker requires a marked isolated scratch")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 1<<20 {
		t.Fatal("invalid worker payload")
	}
	var f historicalCrashFixture
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&f); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(scratch, ".historical-crash"))
	if err != nil || f.Token == "" || string(marker) != f.Token || f.Root != filepath.Join(scratch, "install") || f.StateRoot != filepath.Join(scratch, "state") || len(f.New.Targets) != 1 || len(f.Old.Targets) != 1 {
		t.Fatal("worker fixture ownership mismatch")
	}
	if f.Checkpoint != "after-placement-rename" && f.Checkpoint != "before-install-rename" {
		t.Fatal("unsupported crash checkpoint")
	}
	b := &GameDownloadBackend{StateRoot: f.StateRoot}
	store, err := b.installStore()
	if err != nil {
		t.Fatal(err)
	}
	mutation, err := acquireDownloadMutation(f.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer mutation.Close()
	lease, err := gamedl.Acquire(downloadWork(f.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	_, err = store.Commit(ctx, historicalCrashInput(f, func(point string) error {
		if point == f.Checkpoint {
			fmt.Fprintf(os.Stdout, "oddward-crash checkpoint=%s\n", point)
			os.Exit(historicalCrashExit)
		}
		return nil
	}))
	t.Fatalf("transaction did not reach abrupt exit checkpoint: %v", err)
}

func historicalCrashSynthetic(t *testing.T, point string) historicalCrashResult {
	t.Helper()
	oldBytes, newBytes := bytes.Repeat([]byte("old-verified-"), 2048), bytes.Repeat([]byte("new-verified-"), 2048)
	target := func(data []byte) gameinstall.InstallTarget {
		return gameinstall.InstallTarget{Path: downloadContent + compareSmallPath, MD5: fmt.Sprintf("%x", md5.Sum(data)), Size: int64(len(data))}
	}
	f := historicalCrashCreate(t, t.TempDir(), point, target(oldBytes), target(newBytes), "synthetic-old", "synthetic-new")
	for name, data := range map[string][]byte{"crash-old": oldBytes, "crash-new": newBytes} {
		if err := os.WriteFile(filepath.Join(downloadWork(f.Root), "cache", "blobs", name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return historicalCrashExercise(t, ctx, f)
}

func TestHistoricalCrashAbruptSubprocessRecoveryAndRetry(t *testing.T) {
	for _, point := range historicalCrashPoints {
		t.Run(point, func(t *testing.T) {
			result := historicalCrashSynthetic(t, point)
			if result.ExitCode != historicalCrashExit || !result.Recovered || !result.Retried {
				t.Fatalf("abrupt child exit and real recovery/retry were not demonstrated: %+v", result)
			}
		})
	}
}

// historicalCrashDelivery validates already captured metadata entirely offline.
// The manifest may be a raw delivery XML or its original official encrypted ZIP.
func historicalCrashDelivery(configPath, manifestPath, version string) (gameDelivery, error) {
	read := func(path string, limit int64) ([]byte, error) {
		if path == "" {
			return nil, errors.New("explicit offline metadata path is required")
		}
		if _, err := downloadDirectory(filepath.Dir(path), false); err != nil {
			return nil, err
		}
		root, err := os.OpenRoot(filepath.Dir(path))
		if err != nil {
			return nil, err
		}
		defer root.Close()
		file, err := historicalOpenFile(root, filepath.Base(path))
		if err != nil {
			return nil, err
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, limit+1))
		if err != nil || int64(len(data)) > limit {
			return nil, errors.New("offline metadata exceeds bound")
		}
		return data, nil
	}
	configData, err := read(configPath, 8<<20)
	if err != nil {
		return gameDelivery{}, err
	}
	cfg, err := gameres.ParseConfig(configData)
	if err != nil || cfg.ResVersion != version || cfg.ResSize <= 0 {
		return gameDelivery{}, fmt.Errorf("offline config version/size mismatch: %w", err)
	}
	data, err := read(manifestPath, 32<<20)
	if err != nil {
		return gameDelivery{}, err
	}
	if bytes.HasPrefix(data, []byte("PK")) {
		xml, openErr := gameres.OpenManifestZIP(data)
		if openErr != nil {
			return gameDelivery{}, openErr
		}
		data = xml.ResList
	}
	if fmt.Sprintf("%x", md5.Sum(data)) != cfg.ListHash {
		return gameDelivery{}, errors.New("offline delivery manifest listHash mismatch")
	}
	manifest, err := gameres.ParseResList(data)
	if err != nil || manifest.Version != version {
		return gameDelivery{}, fmt.Errorf("offline delivery version mismatch: %w", err)
	}
	canonical, err := historicalSelection(manifest, nil)
	if err != nil {
		return gameDelivery{}, err
	}
	var total int64
	for _, target := range canonical {
		total += target.Size
	}
	if total != cfg.ResSize || len(canonical) != 334 {
		return gameDelivery{}, errors.New("offline complete manifest target count/size mismatch")
	}
	return gameDelivery{Config: cfg, Manifest: manifest, Branch: bootstrapBranch, ConfigResponse: downloadResponse{Root: officialDownloadRoots[0]}}, nil
}

func TestHistoricalCrashOfflineMetadataRequiresCompleteHashBoundVersion(t *testing.T) {
	var manifest strings.Builder
	manifest.WriteString(`<ResList version="1.5.4" tag="baseTag">`)
	for i := 0; i < 334; i++ {
		fmt.Fprintf(&manifest, `<Res filename="Client/WindowsNoEditor/fixture-%d.bin" md5="9dd4e461268c8034f5c8564e155c67a6" filesize="1"/>`, i)
	}
	manifest.WriteString(`</ResList>`)
	data := []byte(manifest.String())
	digest := fmt.Sprintf("%x", md5.Sum(data))
	for _, scenario := range []string{"valid", "wrong-hash", "wrong-version", "wrong-size"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			version, hash, size := "1.5.4", digest, 334
			switch scenario {
			case "wrong-hash":
				hash = strings.Repeat("1", 32)
			case "wrong-version":
				version = "1.5.6"
			case "wrong-size":
				size++
			}
			config := fmt.Sprintf(`<config><ResVersion>%s</ResVersion><ResSize>%d</ResSize><Tag>baseTag</Tag><Hash>abcdef</Hash><Extra><BaseTag><item name="baseTag"/></BaseTag><listHash>%s</listHash></Extra></config>`, version, size, hash)
			configPath, manifestPath := filepath.Join(root, "config.xml"), filepath.Join(root, "manifest.xml")
			if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, data, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := historicalCrashDelivery(configPath, manifestPath, "1.5.4")
			if scenario == "valid" {
				if err != nil || got.Config.ListHash != digest || len(got.Manifest.Files) != 334 {
					t.Fatalf("complete hash-bound offline manifest rejected: %v", err)
				}
			} else if err == nil {
				t.Fatal("unproven offline version/identity was accepted")
			}
		})
	}
}

// Open an existing source without granting concurrent write/delete sharing. The
// first rooted open proves containment/single-link; the second handle must have
// exactly that same identity. No active download/cache lease is opened or changed.
func historicalCrashReadGuard(root *os.Root, relative string) (*os.File, error) {
	first, err := historicalOpenFile(root, relative)
	if err != nil {
		return nil, err
	}
	defer first.Close()
	var before windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(first.Fd()), &before); err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(filepath.Join(root.Name(), filepath.FromSlash(relative)))
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	guard := os.NewFile(uintptr(handle), relative)
	var after windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &after); err != nil || before.VolumeSerialNumber != after.VolumeSerialNumber || before.FileIndexHigh != after.FileIndexHigh || before.FileIndexLow != after.FileIndexLow || after.NumberOfLinks != 1 || after.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		guard.Close()
		return nil, errors.New("source read guard identity changed")
	}
	return guard, nil
}

func TestHistoricalCrashReadGuardPreservesSourceAndReleasesSharing(t *testing.T) {
	path := t.TempDir()
	data := []byte("independent verified source")
	if err := os.WriteFile(filepath.Join(path, "source"), data, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	guard, err := historicalCrashReadGuard(root, "source")
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	writer, writeErr := root.OpenFile("source", os.O_WRONLY, 0)
	if writeErr == nil {
		writer.Close()
		t.Fatal("source read guard allowed a concurrent writer")
	}
	if err := root.Rename("source", "moved"); err == nil {
		t.Fatal("source read guard allowed concurrent rename")
	}
	object := gameres.Object{MD5: fmt.Sprintf("%x", md5.Sum(data)), Size: int64(len(data))}
	if err := historicalCheckFile(context.Background(), guard, object); err != nil {
		t.Fatal(err)
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	if err := root.Rename("source", "moved"); err != nil {
		t.Fatalf("closed source guard retained sharing restriction: %v", err)
	}
	destination := t.TempDir()
	source := historicalCrashSource{Root: path, Path: "moved"}
	if err := historicalCrashCopy(context.Background(), source, destination, "independent", object); err != nil {
		t.Fatal(err)
	}
	sourceInfo, err := root.Stat("moved")
	if err != nil {
		t.Fatal(err)
	}
	copyInfo, err := os.Stat(filepath.Join(destination, "independent"))
	if err != nil || os.SameFile(sourceInfo, copyInfo) {
		t.Fatalf("resource fixture copy reused source identity: %v", err)
	}
	wrong := object
	wrong.MD5 = strings.Repeat("1", 32)
	if err := historicalCrashCopy(context.Background(), source, destination, "untrusted", wrong); err == nil {
		t.Fatal("source hash mismatch accepted")
	}
	if _, err := os.Lstat(filepath.Join(destination, "untrusted")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unverified source created an output file")
	}
}

type historicalCrashSource struct{ Root, Path string }

func historicalCrashSources(base string, object gameres.Object, targetPath string) []historicalCrashSource {
	out := []historicalCrashSource{{filepath.Join(base, "patch", "inputs"), object.String()},
		{filepath.Join(base, "historical-state", "prepared-cache"), "cache/blobs/" + object.String()},
		{downloadWork(filepath.Join(base, "fresh")), "cache/blobs/" + object.String()}}
	for _, name := range []string{"route-B1", "route-B2", "route-B3", "fresh"} {
		out = append(out, historicalCrashSource{filepath.Join(base, name), targetPath})
	}
	return out
}

func historicalCrashChoose(base string, object gameres.Object, target string) (historicalCrashSource, error) {
	for _, candidate := range historicalCrashSources(base, object, target) {
		if _, err := os.Lstat(filepath.Join(candidate.Root, filepath.FromSlash(candidate.Path))); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return historicalCrashSource{}, err
		}
		if _, err := downloadDirectory(candidate.Root, false); err != nil {
			return historicalCrashSource{}, err
		}
		root, err := os.OpenRoot(candidate.Root)
		if err != nil {
			return historicalCrashSource{}, err
		}
		file, openErr := historicalCrashReadGuard(root, candidate.Path)
		if openErr == nil {
			info, statErr := file.Stat()
			file.Close()
			root.Close()
			if statErr != nil {
				return historicalCrashSource{}, statErr
			}
			if info.Size() == object.Size {
				return candidate, nil
			}
		} else {
			root.Close()
		}
	}
	return historicalCrashSource{}, errors.New("no available independent complete official source; do not download solely for this test")
}

func historicalCrashCopy(ctx context.Context, source historicalCrashSource, destination, part string, object gameres.Object) error {
	root, err := os.OpenRoot(source.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	input, err := historicalCrashReadGuard(root, source.Path)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := historicalCheckFile(ctx, input, object); err != nil {
		return err
	}
	outputRoot, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer outputRoot.Close()
	output, err := outputRoot.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	n, copyErr := io.CopyBuffer(output, historicalReader{ctx: ctx, reader: io.LimitReader(input, object.Size+1)}, make([]byte, 256<<10))
	err = errors.Join(copyErr, output.Sync(), output.Close())
	if err != nil {
		return err
	}
	if n != object.Size {
		return errors.New("source copy size changed")
	}
	if err := historicalCheckFile(ctx, input, object); err != nil {
		return err
	}
	copyFile, err := historicalOpenFile(outputRoot, part)
	if err != nil {
		return err
	}
	defer copyFile.Close()
	return historicalCheckFile(ctx, copyFile, object)
}

// Real resources remain opt-in even though this test performs zero network I/O.
// Set the usual NETWORK/root markers plus the four explicit offline metadata
// paths. Sources must already exist; all writes stay in new marked scratch roots.
func TestRealGameResourcesHistoricalCrashRecovery(t *testing.T) {
	base := historicalWorkspace(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	old, err := historicalCrashDelivery(os.Getenv("ODDWARD_E2E_HISTORICAL_CONFIG"), os.Getenv("ODDWARD_E2E_HISTORICAL_CRASH_OLD_MANIFEST"), "1.5.4")
	if err != nil {
		t.Fatal(err)
	}
	next, err := historicalCrashDelivery(os.Getenv("ODDWARD_E2E_HISTORICAL_CRASH_CURRENT_CONFIG"), os.Getenv("ODDWARD_E2E_HISTORICAL_CRASH_CURRENT_MANIFEST"), "1.5.6")
	if err != nil {
		t.Fatal(err)
	}
	var oldTarget, newTarget gameinstall.InstallTarget
	var oldSource, newSource historicalCrashSource
	for _, path := range []string{compareSmallPath, compareLargePath} {
		a, b := comparisonFile(t, old, path), comparisonFile(t, next, path)
		oldObject, newObject := gameres.Object{MD5: a.MD5, Size: a.Size}, gameres.Object{MD5: b.MD5, Size: b.Size}
		if oldObject == newObject {
			t.Fatal("official versions have identical crash target")
		}
		targetPath := downloadContent + path
		oldSource, err = historicalCrashChoose(base, oldObject, targetPath)
		if err != nil {
			continue
		}
		newSource, err = historicalCrashChoose(base, newObject, targetPath)
		if err != nil {
			continue
		}
		oldTarget = gameinstall.InstallTarget{Path: targetPath, MD5: a.MD5, Size: a.Size}
		newTarget = gameinstall.InstallTarget{Path: targetPath, MD5: b.MD5, Size: b.Size}
		break
	}
	if oldTarget.Path == "" {
		t.Fatal("no pair of already available complete official changed objects; no network fallback")
	}
	b := &GameDownloadBackend{}
	if err := b.guard(ctx, true); err != nil {
		t.Fatal(err)
	}
	for _, point := range historicalCrashPoints {
		t.Run(point, func(t *testing.T) {
			free, err := b.space(base)
			if err != nil || free < oldTarget.Size+2*newTarget.Size+(2<<30) {
				t.Fatalf("insufficient independent scratch headroom: %v", err)
			}
			fixture := historicalCrashCreate(t, base, point, oldTarget, newTarget, old.Config.ListHash, next.Config.ListHash)
			if err := historicalCrashCopy(ctx, oldSource, downloadWork(fixture.Root), "cache/blobs/crash-old", gameres.Object{MD5: oldTarget.MD5, Size: oldTarget.Size}); err != nil {
				t.Fatal(err)
			}
			if err := historicalCrashCopy(ctx, newSource, downloadWork(fixture.Root), "cache/blobs/crash-new", gameres.Object{MD5: newTarget.MD5, Size: newTarget.Size}); err != nil {
				t.Fatal(err)
			}
			result := historicalCrashExercise(t, ctx, fixture)
			if result.ExitCode != historicalCrashExit || !result.Recovered || !result.Retried {
				t.Fatalf("real abrupt crash acceptance incomplete: %+v", result)
			}
		})
	}
}
