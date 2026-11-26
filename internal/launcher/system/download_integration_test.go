// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"bytes"
	"context"
	"crypto/md5"
	"debug/pe"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/nerv-base/Oddward/internal/database/dbtest"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/game"
	"github.com/nerv-base/Oddward/internal/gameinstall"
	"github.com/nerv-base/Oddward/internal/gameres"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestDownloadCacheCandidatesIncludePatchChainBlobs(t *testing.T) {
	old := adapterObject([]byte("old source"))
	mid := adapterObject([]byte("intermediate"))
	newObject := adapterObject([]byte("new target"))
	first := adapterObject([]byte("first patch"))
	second := adapterObject([]byte("second patch"))
	unrelated := adapterObject([]byte("optional tag patch"))
	manifest := gameres.ResList{Files: []gameres.File{{Path: "file.bin", MD5: newObject.MD5, Size: newObject.Size}}}
	graph := &gameres.PatchGraph{Edges: []gameres.PatchEntry{
		{Old: old, New: mid, Blob: first},
		{Old: mid, New: newObject, Blob: second},
		{Old: old, New: newObject, Blob: unrelated, Section: "optional"},
	}}
	candidates := downloadCacheCandidates(manifest, graph)
	if len(candidates) != 3 || !candidates[newObject] || !candidates[first] || !candidates[second] {
		t.Fatalf("expected the final target and both baseTag patch blobs as cache candidates: %v", candidates)
	}
	if disabled := downloadCacheCandidates(manifest, nil); len(disabled) != 1 || !disabled[newObject] {
		t.Fatalf("full-only planning should only consider final targets: %v", disabled)
	}
}

func TestPresentDownloadPlanReportsSelectedActions(t *testing.T) {
	tests := []struct {
		name string
		plan gameres.Plan
		want state.RouteKind
	}{
		{"cached patch", gameres.Plan{FullBytes: 200, Targets: []gameres.Target{{Action: gameres.ActionPatch}}}, state.RoutePatch},
		{"patch only", gameres.Plan{DownloadBytes: 5, PatchBytes: 5, FullBytes: 200, Targets: []gameres.Target{{Action: gameres.ActionPatch}}}, state.RoutePatch},
		{"mixed", gameres.Plan{DownloadBytes: 205, PatchBytes: 5, FullBytes: 400, Targets: []gameres.Target{{Action: gameres.ActionPatch}, {Action: gameres.ActionFull}}}, state.RouteMixed},
		{"full", gameres.Plan{DownloadBytes: 200, FullBytes: 200, Targets: []gameres.Target{{Action: gameres.ActionFull}}}, state.RouteFull},
		{"all reused", gameres.Plan{ReusedBytes: 200, Targets: []gameres.Target{{Action: gameres.ActionReuseInstalled}, {Action: gameres.ActionInstallCached}}}, state.RouteAllReuse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &GameDownloadBackend{}
			var route state.Event
			_, err := b.presentDownloadPlan(&downloadPlan{plan: tt.plan}, state.Event{}, func(e state.Event) { route = e })
			if err != nil || route.Kind != state.DownloadRoutePlanned || route.Index != uint8(tt.want) || route.Total != tt.plan.DownloadBytes || route.PatchBytes != tt.plan.PatchBytes {
				t.Fatalf("route = %+v, want kind %v: %v", route, tt.want, err)
			}
		})
	}
}

func TestGameDownloadFirstInstallWithLocalCAS(t *testing.T) {
	t.Run("same-volume", func(t *testing.T) { testGameDownloadFirstInstall(t, false) })
	t.Run("external-cache-copy", func(t *testing.T) { testGameDownloadFirstInstall(t, true) })
}

func testGameDownloadFirstInstall(t *testing.T, external bool) {
	t.Helper()
	oldComponents := componentVersionURL
	componentVersionURL = fakeComponentManifest(t)
	t.Cleanup(func() { componentVersionURL = oldComponents })
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "new-game")
	content := bytes.Repeat([]byte("fixture: verified game resource\n"), 2048)
	replacement := append(bytes.Clone(content), '!')
	replacementPatch := adapterAppendBytePatch(len(content), '!')
	nextContent := []byte("fixture: newly added game resource")
	identity := func(data []byte) gameres.Object {
		return gameres.Object{MD5: fmt.Sprintf("%x", md5.Sum(data)), Size: int64(len(data))}
	}
	object, nextObject := identity(content), identity(nextContent)
	replacementObject, patchObject := identity(replacement), identity(replacementPatch)
	// Structural PE fixture for the read-only installation inspector.
	executable := bytes.NewBuffer(make([]byte, 64))
	copy(executable.Bytes(), "MZ")
	binary.LittleEndian.PutUint32(executable.Bytes()[60:], 64)
	executable.WriteString("PE\x00\x00")
	if err := binary.Write(executable, binary.LittleEndian, pe.FileHeader{Machine: pe.IMAGE_FILE_MACHINE_AMD64, SizeOfOptionalHeader: 240, Characteristics: pe.IMAGE_FILE_EXECUTABLE_IMAGE}); err != nil {
		t.Fatal(err)
	}
	if err := binary.Write(executable, binary.LittleEndian, pe.OptionalHeader64{Magic: 0x20b, NumberOfRvaAndSizes: 16}); err != nil {
		t.Fatal(err)
	}
	image := identity(executable.Bytes())
	casPath := func(o gameres.Object) string { return fmt.Sprintf("/%s/Res/%c/%s", bootstrapBranch, o.MD5[0], o) }
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case casPath(object):
			http.ServeContent(w, r, "old", time.Time{}, bytes.NewReader(content))
		case casPath(nextObject):
			http.ServeContent(w, r, "new", time.Time{}, bytes.NewReader(nextContent))
		case casPath(image):
			http.ServeContent(w, r, "image", time.Time{}, bytes.NewReader(executable.Bytes()))
		case casPath(patchObject):
			http.ServeContent(w, r, "patch", time.Time{}, bytes.NewReader(replacementPatch))
		case casPath(replacementObject):
			if r.Method != http.MethodHead {
				t.Errorf("expected the update to reconstruct the replacement from its patch")
			}
			http.ServeContent(w, r, "replacement", time.Time{}, bytes.NewReader(replacement))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	var running downloadProcesses
	available := int64(8 << 30)
	backend := &GameDownloadBackend{DB: dbtest.Open(t), StateRoot: t.TempDir(), Client: srv.Client(), roots: []string{srv.URL}, free: func(string) (int64, error) { return available, nil }, processes: func(context.Context) (downloadProcesses, error) { return running, nil }}
	delivery := gameDelivery{
		Branch:         bootstrapBranch,
		Config:         gameres.Config{ResVersion: "1.5.6", ListHash: "d41d8cd98f00b204e9800998ecf8427e", ResSize: object.Size + image.Size},
		ConfigResponse: downloadResponse{Root: srv.URL, URL: srv.URL + "/config.xml"},
		Manifest: gameres.ResList{Version: "1.5.6", Tag: "baseTag", Files: []gameres.File{
			{Path: "HT/Content/Paks/demo.pak", MD5: object.MD5, Size: object.Size},
			{Path: "HT/Binaries/Win64/HTGame.exe", MD5: image.MD5, Size: image.Size},
		}},
	}
	cache := ""
	if external {
		cache = t.TempDir()
		backend.downloadVolume = func(path string) (string, error) {
			if path == cache {
				return "cache", nil
			}
			return "install", nil
		}
	}
	p, err := backend.buildDownloadPlan(ctx, root, state.DownloadInstall, delivery, gameinstall.InstallRecord{}, cache)
	if err != nil {
		t.Fatal(err)
	}
	backend.putPlan(root, p)
	req := launcher.DownloadRequest{Root: root, Kind: state.DownloadInstall, Effect: state.Effect{Kind: state.EffectDownloadRun, Token: 1}}
	if got := backend.HandleDownload(ctx, req, nil); got.Kind != state.DownloadVerified {
		t.Fatalf("run = %+v", got)
	}
	target := filepath.Join(root, "Client", "WindowsNoEditor", "HT", "Content", "Paks", "demo.pak")
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("precommit target exists or failed: %v", err)
	}
	if external {
		restarted := &GameDownloadBackend{DB: backend.DB, StateRoot: backend.StateRoot, downloadVolume: backend.downloadVolume}
		restore := req
		restore.Effect.Kind = state.EffectRecoverScan
		got := restarted.HandleDownload(ctx, restore, nil)
		restored := restarted.getPlan(root)
		if got.Kind != state.DownloadRestored || restored == nil || restored.engineRoot() != p.engineRoot() {
			t.Fatalf("external task restored to wrong cache: %v", got)
		}
	}
	req.Effect.Kind = state.EffectDownloadCommit
	running.game = true
	if got := backend.HandleDownload(ctx, req, nil); got.Kind != state.DownloadRecoveryNeeded {
		t.Fatalf("game guard = %+v", got)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("guarded target exists or failed: %v", err)
	}
	running.game = false
	available = 0
	if got := backend.HandleDownload(ctx, req, nil); got.Kind != state.DownloadRecoveryNeeded {
		t.Fatalf("commit without space = %+v", got)
	}
	entries, err := os.ReadDir(filepath.Join(downloadWork(root), "bootstrap"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("space failure did not occur after staging bootstrap: %v, %v", entries, err)
	}
	available = 8 << 30
	if got := backend.HandleDownload(ctx, req, nil); got.Kind != state.DownloadCommitted || got.Message != "1.5.6" {
		t.Fatalf("commit = %+v", got)
	}
	if external {
		cacheBlob := filepath.Join(p.engineRoot(), "cache", "blobs", object.String())
		if data, err := os.ReadFile(cacheBlob); err != nil || !bytes.Equal(data, content) {
			t.Fatalf("external cache after commit = %q: %v", data, err)
		}
	}
	payload, err := os.ReadFile(target)
	if err != nil || string(payload) != string(content) {
		t.Fatalf("installed payload = %q, %v", payload, err)
	}
	for _, name := range []string{"Patch_Windows.json", "Patch_Windows.source.json"} {
		if _, err := os.Stat(filepath.Join(root, "Client", "WindowsNoEditor", "PatcherConfig", name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = os.Stat(filepath.Join(root, "NTELauncher", "NTEGame.exe")); err != nil {
		t.Fatalf("runtime components missing: %v", err)
	}
	pointer, err := os.ReadFile(filepath.Join(root, "Client", "WindowsNoEditor", "HT", "Binaries", "Win64", "GameLauncher.config"))
	if err != nil || !strings.Contains(string(pointer), "WPGameClientSDK_64.dll") {
		t.Fatalf("component pointer = %q, %v", pointer, err)
	}
	installation, err := game.Inspect(ctx, root)
	if err != nil || installation.AppID != bootstrapAppID || installation.Branch != bootstrapBranch {
		t.Fatalf("first install is not recognized: %+v, %v", installation, err)
	}

	store, err := backend.installStore()
	if err != nil {
		t.Fatal(err)
	}
	base, err := store.ReadInstall(root)
	if err != nil || base.VerifyCoverage != "full" {
		t.Fatalf("published baseline %s/%s: %v", base.Version, base.VerifyCoverage, err)
	}
	lease, err := acquireDownloadWork(root)
	if err != nil {
		t.Fatal(err)
	}
	verify := req
	verify.Effect.Kind = state.EffectInstallVerify
	result := backend.HandleDownload(ctx, verify, nil)
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if result.Kind != state.InstallVerifyFailed {
		t.Fatalf("verification ran while another writer held the installation lease: %+v", result)
	}
	if result = backend.HandleDownload(ctx, verify, nil); result.Kind != state.InstallVerified {
		t.Fatalf("unlocked verification failed: %+v", result)
	}
	delivery.Config.ResVersion, delivery.Manifest.Version = "1.5.7", "1.5.7"
	delivery.Config.ListHash = strings.Repeat("a", 32)
	delivery.Config.CheckHash = delivery.Config.ListHash
	delivery.Manifest.CheckHash = delivery.Config.CheckHash
	delivery.Config.ResSize += nextObject.Size + replacementObject.Size - object.Size
	delivery.Manifest.Files[0].MD5, delivery.Manifest.Files[0].Size = replacementObject.MD5, replacementObject.Size
	delivery.Manifest.Files = append(delivery.Manifest.Files, gameres.File{Path: "HT/Content/Paks/next.pak", MD5: nextObject.MD5, Size: nextObject.Size})
	delivery.Graph = &gameres.PatchGraph{Edges: []gameres.PatchEntry{
		{Old: object, New: replacementObject, Blob: patchObject, Version: delivery.Manifest.CheckHash},
	}}
	p, err = backend.buildDownloadPlan(ctx, root, state.DownloadUpdate, delivery, base, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.plan.DownloadBytes != nextObject.Size+patchObject.Size || p.plan.PatchBytes != patchObject.Size || p.plan.ReusedBytes != image.Size {
		t.Fatalf("update download/patch/reuse = %d/%d/%d, want %d/%d/%d", p.plan.DownloadBytes, p.plan.PatchBytes, p.plan.ReusedBytes, nextObject.Size+patchObject.Size, patchObject.Size, image.Size)
	}
	backend.putPlan(root, p)
	req.Kind, req.Effect.Kind = state.DownloadUpdate, state.EffectDownloadRun
	if got := backend.HandleDownload(ctx, req, nil); got.Kind != state.ApplyFinished || got.Enabled {
		t.Fatalf("mixed update apply = kind %d, fallback %t, %s", got.Kind, got.Enabled, got.Message)
	}
	req.Effect.Kind = state.EffectDownloadVerify
	verifyBytes := func(want int64) {
		t.Helper()
		var checked int64 = -1
		got := backend.HandleDownload(ctx, req, func(e state.Event) {
			if e.Kind == state.DownloadProgress && e.Stage == state.StageVerify {
				checked = e.Downloaded
			}
		})
		if got.Kind != state.DownloadVerified || checked != want {
			t.Fatalf("mixed update verification: kind=%d error=%q bytes=%d, want %d", got.Kind, got.Message, checked, want)
		}
	}
	// Only the next verification in this run may reuse the completed patch
	// verification. A separate verification always reopens every cache blob.
	if external {
		// A different request cannot consume the previous run's verification.
		req.Effect.Token++
		verifyBytes(nextObject.Size + replacementObject.Size)
	} else {
		verifyBytes(nextObject.Size)
	}
	verifyBytes(nextObject.Size + replacementObject.Size)
	restarted := &GameDownloadBackend{DB: backend.DB, StateRoot: backend.StateRoot, Client: backend.Client, roots: backend.roots, free: backend.free, processes: backend.processes}
	req.Effect.Kind = state.EffectRecoverScan
	if got := restarted.HandleDownload(ctx, req, nil); got.Kind != state.DownloadRestored || got.Stage != state.StageNetwork || got.Message != "1.5.7" || got.Focused {
		t.Fatalf("reopened durable task = %d stage %d version %q auto %t: %s", got.Kind, got.Stage, got.Message, got.Focused, got.Body)
	}
	if data, err := os.ReadFile(target); err != nil || !bytes.Equal(data, content) {
		t.Fatalf("patch staging changed the installed source: %v", err)
	}
	req.Effect.Kind = state.EffectDownloadCommit
	// A delayed commit must still detect any modification after verification,
	// including same-size corruption in a previously rebuilt target.
	blob := filepath.Join(p.engineRoot(), "cache", "blobs", replacementObject.String())
	if err := os.WriteFile(blob, bytes.Repeat([]byte{'x'}, len(replacement)), 0600); err != nil {
		t.Fatal(err)
	}
	started := false
	got := backend.HandleDownload(ctx, req, func(e state.Event) { started = started || e.Kind == state.DownloadCommitStart })
	if got.Kind != state.DownloadRecoveryNeeded || started {
		t.Fatalf("corrupt rebuilt blob passed preflight: kind=%d transaction started=%t", got.Kind, started)
	}
	if data, err := os.ReadFile(target); err != nil || !bytes.Equal(data, content) {
		t.Fatalf("failed preflight changed the installed resource: %v", err)
	}
	if err := os.WriteFile(blob, replacement, 0600); err != nil {
		t.Fatal(err)
	}
	if got := backend.HandleDownload(ctx, req, nil); got.Kind != state.DownloadCommitted || got.Message != "1.5.7" {
		t.Fatalf("update commit = %d %s", got.Kind, got.Message)
	}
	if data, err := os.ReadFile(target); err != nil || !bytes.Equal(data, replacement) {
		t.Fatalf("patched target did not match the manifest: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(filepath.Dir(target), "next.pak")); err != nil || !bytes.Equal(data, nextContent) {
		t.Fatalf("updated target = %q: %v", data, err)
	}
}
