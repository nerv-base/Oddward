// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
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
	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameinstall"
	"github.com/nerv-base/Oddward/internal/gameres"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func preloadSyntheticContainer(t *testing.T, xml []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	z := zlib.NewWriter(&compressed)
	if _, err := z.Write(xml); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	plain := append([]byte(nil), compressed.Bytes()...)
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(padding)}, padding)...)
	block, err := aes.NewCipher([]byte("1289@Patcher0000"))
	if err != nil {
		t.Fatal(err)
	}
	encrypted := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, []byte("PatcherSDK000000")).CryptBlocks(encrypted, plain)
	container := append([]byte("PatcherXML0\x00"), make([]byte, 4)...)
	binary.LittleEndian.PutUint32(container[len(container)-4:], uint32(len(xml)))
	return append(container, encrypted...)
}

// This encoder wraps tiny synthetic XML only; it never reads official metadata.
func preloadSyntheticArchive(t *testing.T, xml []byte) []byte {
	t.Helper()
	return preloadSyntheticArchiveWithDiff(t, xml, nil)
}

func preloadSyntheticArchiveWithDiff(t *testing.T, resListXML, lastDiffXML []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	entry, err := archive.Create("ResList.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = entry.Write(preloadSyntheticContainer(t, resListXML)); err != nil {
		t.Fatal(err)
	}
	if len(lastDiffXML) > 0 {
		diffEntry, err := archive.Create("lastdiff.bin")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = diffEntry.Write(preloadSyntheticContainer(t, lastDiffXML)); err != nil {
			t.Fatal(err)
		}
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestSystemPreloadSyntheticStageReconcileApply(t *testing.T) {
	ctx := context.Background()
	released := false
	casCalls := 0
	var delivery gameDelivery
	var manifestZIP []byte
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg, _ := gameres.ConfigURL(delivery.ConfigResponse.Root, "PC_150")
		manifest, _ := gameres.ManifestURL(delivery.ConfigResponse.Root, "PC_150", "1.1.0")
		cas, _ := gameres.CASURL(delivery.ConfigResponse.Root, "PC_160", gameres.Object{MD5: delivery.Manifest.Files[0].MD5, Size: 3})
		switch "https://" + r.Host + r.URL.Path {
		case cfg:
			if released {
				w.Write(delivery.ConfigResponse.Data)
			} else {
				fmt.Fprintf(w, `<config><ResVersion>1.0.0</ResVersion><PreReleaseStatus>start</PreReleaseStatus><PreReleaseBranch>PC_160</PreReleaseBranch><PreReleaseVersion>1.1.0</PreReleaseVersion><Extra><listHash>%s</listHash></Extra></config>`, delivery.Config.ListHash)
			}
		case manifest:
			w.Write(manifestZIP)
		case cas:
			if r.Method == http.MethodGet {
				casCalls++
			}
			http.ServeContent(w, r, "synthetic", time.Time{}, bytes.NewReader([]byte("new")))
		default:
			t.Errorf("unexpected synthetic route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	delivery = preloadSystemDelivery(t, srv.URL+"/clientRes")
	manifestZIP = preloadSyntheticArchive(t, delivery.XML.ResList)
	b := &GameDownloadBackend{DB: dbtest.Open(t), StateRoot: t.TempDir(), Client: srv.Client(), roots: []string{delivery.ConfigResponse.Root}, free: func(string) (int64, error) { return 8 << 30, nil }, processes: func(context.Context) (downloadProcesses, error) { return downloadProcesses{}, nil }, preloadQuery: func(context.Context, string, string) (gameDelivery, error) { return delivery, nil }}
	root := preloadSystemBase(t, b)
	request := launcher.DownloadRequest{Root: root, Kind: state.DownloadPreload}
	request.Effect.Kind = state.EffectDownloadPlan
	if event := b.HandleDownload(ctx, request, nil); event.Kind != state.DownloadPlanned {
		t.Fatalf("preload plan: %+v", event)
	}
	p := b.getPlan(root)
	if p == nil || p.preload == nil {
		t.Fatal("preload plan missing")
	}
	object := p.preload.Files[0].Object
	blob := filepath.Join(p.workRoot, "cache", "blobs", object.String())
	request.Effect.Kind = state.EffectPreloadRun
	if event := b.HandleDownload(ctx, request, nil); event.Kind != state.PreloadStaged {
		t.Fatalf("preload stage: %+v", event)
	}
	before, err := os.Stat(blob)
	if err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(root, "Client", "WindowsNoEditor", "HT", "Content", "Paks", "a.pak")
	if data, err := os.ReadFile(installed); err != nil || string(data) != "old" {
		t.Fatal("preload modified installation")
	}
	released = true
	request.Effect = state.Effect{Kind: state.EffectPreloadCheck, Enabled: true}
	if event := b.HandleDownload(ctx, request, nil); event.Kind != state.PreloadAvailableToApply {
		t.Fatalf("preload reconcile: %+v", event)
	}
	after, err := os.Stat(blob)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("reconcile copied staged bytes: %v", err)
	}
	request.Effect = state.Effect{Kind: state.EffectPreloadApply}
	if event := b.HandleDownload(ctx, request, nil); event.Kind != state.PreloadApplied {
		t.Fatalf("preload apply: %+v", event)
	}
	if data, err := os.ReadFile(installed); err != nil || string(data) != "new" {
		t.Fatalf("applied bytes: %q %v", data, err)
	}
	store, err := b.preloadStore(root)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Load(ctx, "PC_160", "1.1.0")
	if err != nil || record.State != gameinstall.PreloadCompleted {
		t.Fatalf("preload publication: %+v %v", record, err)
	}
	if casCalls != 1 {
		t.Fatalf("staged blob downloaded %d times", casCalls)
	}
	if _, err := os.Stat(p.workRoot); !os.IsNotExist(err) {
		t.Fatalf("completed preload work directory was not cleaned up: %v", err)
	}
}

func TestSystemPreloadPatchStageReconcileApplyAndCleanup(t *testing.T) {
	ctx := context.Background()
	oldBytes := bytes.Repeat([]byte("old-game-resource-payload-block-"), 1024)
	newBytes := append(bytes.Clone(oldBytes), '!')
	patchBytes := adapterAppendBytePatch(len(oldBytes), '!')
	oldObj := adapterObject(oldBytes)
	newObj := adapterObject(newBytes)
	patchObj := adapterObject(patchBytes)

	manifest := []byte(fmt.Sprintf(`<ResList version="1.1.0" tag="baseTag"><Res filename="HT/Content/Paks/a.pak" md5="%s" filesize="%d"/></ResList>`, newObj.MD5, newObj.Size))
	diffXML := []byte(fmt.Sprintf(`<PatchList><Patch oldfile="%s" newfile="%s" patch="%s" v="chk110"/></PatchList>`, oldObj, newObj, patchObj))
	listHash := fmt.Sprintf("%x", md5.Sum(manifest))
	diffHash := fmt.Sprintf("%x", md5.Sum(diffXML))
	cfgFormal := []byte(fmt.Sprintf(`<config><ResVersion>1.1.0</ResVersion><Hash>chk110</Hash><Extra><listHash>%s</listHash><diffHash>%s</diffHash></Extra></config>`, listHash, diffHash))
	zipData := preloadSyntheticArchiveWithDiff(t, manifest, diffXML)

	released := false
	patchGets, fullGets := 0, 0
	var rootURL string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/clientRes/PC_150/Version/Windows/config.xml":
			if released {
				_, _ = w.Write(cfgFormal)
			} else {
				fmt.Fprintf(w, `<config><ResVersion>1.0.0</ResVersion><PreReleaseStatus>start</PreReleaseStatus><PreReleaseBranch>PC_150</PreReleaseBranch><PreReleaseVersion>1.1.0</PreReleaseVersion><Extra><listHash>%s</listHash></Extra></config>`, listHash)
			}
		case "/clientRes/PC_150/Version/Windows/version/1.1.0/config.xml":
			_, _ = w.Write(cfgFormal)
		case "/clientRes/PC_150/Version/Windows/version/1.1.0/ResList.bin.zip":
			_, _ = w.Write(zipData)
		case fmt.Sprintf("/clientRes/PC_150/Res/%c/%s", patchObj.MD5[0], patchObj):
			if r.Method == http.MethodGet {
				patchGets++
			}
			http.ServeContent(w, r, "patch", time.Time{}, bytes.NewReader(patchBytes))
		case fmt.Sprintf("/clientRes/PC_150/Res/%c/%s", newObj.MD5[0], newObj):
			if r.Method == http.MethodGet {
				fullGets++
			}
			http.ServeContent(w, r, "full", time.Time{}, bytes.NewReader(newBytes))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	rootURL = srv.URL + "/clientRes"

	b := NewGameDownloadBackend(dbtest.Open(t), t.TempDir(), nil, srv.Client(), "test")
	b.roots = []string{rootURL}
	b.free = func(string) (int64, error) { return 8 << 30, nil }
	b.processes = func(context.Context) (downloadProcesses, error) { return downloadProcesses{}, nil }

	root := t.TempDir()
	pe := make([]byte, 90)
	copy(pe, "MZ")
	binary.LittleEndian.PutUint32(pe[60:64], 64)
	copy(pe[64:68], "PE\x00\x00")
	binary.LittleEndian.PutUint16(pe[68:70], 0x8664)
	binary.LittleEndian.PutUint16(pe[84:86], 2)
	binary.LittleEndian.PutUint16(pe[86:88], 2)
	binary.LittleEndian.PutUint16(pe[88:90], 0x20b)
	preloadSystemPut(t, root, "Client/WindowsNoEditor/HT/Binaries/Win64/HTGame.exe", pe)
	preloadSystemPut(t, root, "Client/WindowsNoEditor/PatcherConfig/Patch_Windows.json", []byte(`{"appId":"1289","branchName":"PC_150"}`))
	preloadSystemPut(t, root, "Client/WindowsNoEditor/HT/Content/Paks/a.pak", oldBytes)
	oldManifest := []byte(fmt.Sprintf(`<ResList version="1.0.0" tag="baseTag"><Res filename="HT/Content/Paks/a.pak" md5="%s" filesize="%d"/></ResList>`, oldObj.MD5, oldObj.Size))
	preloadSystemPut(t, root, "NTELauncher/UserData/Patcher/PatcherSDK/ResList.xml", oldManifest)
	preloadSystemPut(t, root, "NTELauncher/UserData/Patcher/PatcherSDK/config.xml", []byte(fmt.Sprintf(`<config><ResVersion>1.0.0</ResVersion><Extra><listHash>%x</listHash></Extra></config>`, md5.Sum(oldManifest))))
	instStore, err := b.installStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instStore.AdoptQuick(ctx, root, nil); err != nil {
		t.Fatal(err)
	}
	if res, err := instStore.VerifyInstallation(ctx, root, nil); err != nil || len(res.Mismatches) != 0 {
		t.Fatalf("verify: %+v %v", res, err)
	}

	req := launcher.DownloadRequest{Root: root, Kind: state.DownloadPreload, Effect: state.Effect{Kind: state.EffectDownloadPlan}}
	if ev := b.HandleDownload(ctx, req, nil); ev.Kind != state.DownloadPlanned {
		t.Fatalf("plan = %+v", ev)
	}
	p := b.getPlan(root)
	if p == nil || p.preload == nil || p.preload.Route != gameinstall.PreloadRoutePatch {
		t.Fatalf("expected patch preload route, got %+v", p)
	}
	workRoot := p.workRoot
	req.Effect.Kind = state.EffectPreloadRun
	if ev := b.HandleDownload(ctx, req, nil); ev.Kind != state.PreloadStaged {
		t.Fatalf("stage = %+v", ev)
	}
	if patchGets != 1 || fullGets != 0 {
		t.Fatalf("stage downloads patch=%d full=%d", patchGets, fullGets)
	}
	installed := filepath.Join(root, "Client", "WindowsNoEditor", "HT", "Content", "Paks", "a.pak")
	if got, err := os.ReadFile(installed); err != nil || !bytes.Equal(got, oldBytes) {
		t.Fatal("preload modified installed bytes before apply")
	}

	released = true
	req.Effect = state.Effect{Kind: state.EffectPreloadCheck, Enabled: true}
	if ev := b.HandleDownload(ctx, req, nil); ev.Kind != state.PreloadAvailableToApply {
		t.Fatalf("reconcile = %+v", ev)
	}
	req.Effect = state.Effect{Kind: state.EffectPreloadApply}
	var applyReports []state.Event
	if ev := b.HandleDownload(ctx, req, func(e state.Event) { applyReports = append(applyReports, e) }); ev.Kind != state.PreloadApplied || ev.Message != "1.1.0" {
		t.Fatalf("apply = %+v", ev)
	}
	// Rebuilding staged patches is a real phase the UI must be able to show.
	started, progressed := false, false
	for _, report := range applyReports {
		switch report.Kind {
		case state.ApplyStarted:
			started = report.FilesTotal == 1
		case state.ApplyProgress:
			progressed = progressed || report.Stage == state.StageApply
		}
	}
	if !started || !progressed {
		t.Fatalf("preload patch rebuild reported started=%t progress=%t: %+v", started, progressed, applyReports)
	}
	if got, err := os.ReadFile(installed); err != nil || !bytes.Equal(got, newBytes) {
		t.Fatalf("applied patch output mismatch: len=%d err=%v", len(got), err)
	}
	if patchGets != 1 || fullGets != 0 {
		t.Fatalf("apply re-downloaded patch=%d full=%d", patchGets, fullGets)
	}
	if _, err := os.Stat(workRoot); !os.IsNotExist(err) {
		t.Fatalf("patch preload work directory survived apply: %v", err)
	}
}

func TestSystemPreloadCrossBranchPromotionDeltaReconcileApply(t *testing.T) {
	ctx := context.Background()
	sharedBytes := []byte("shared-between-150-and-153")
	preDeltaBytes := []byte("delta-v150")
	formalDeltaBytes := []byte("delta-v153-updated")
	sharedObj := adapterObject(sharedBytes)
	preDeltaObj := adapterObject(preDeltaBytes)
	formalDeltaObj := adapterObject(formalDeltaBytes)

	preManifest := []byte(fmt.Sprintf(`<ResList version="1.5.0" tag="baseTag"><Res filename="HT/Content/Paks/a.pak" md5="%s" filesize="%d"/><Res filename="HT/Content/Paks/b.pak" md5="%s" filesize="%d"/></ResList>`,
		sharedObj.MD5, sharedObj.Size, preDeltaObj.MD5, preDeltaObj.Size))
	formalManifest := []byte(fmt.Sprintf(`<ResList version="1.5.3" tag="baseTag"><Res filename="HT/Content/Paks/a.pak" md5="%s" filesize="%d"/><Res filename="HT/Content/Paks/b.pak" md5="%s" filesize="%d"/></ResList>`,
		sharedObj.MD5, sharedObj.Size, formalDeltaObj.MD5, formalDeltaObj.Size))
	preHash := fmt.Sprintf("%x", md5.Sum(preManifest))
	formalHash := fmt.Sprintf("%x", md5.Sum(formalManifest))
	preZip := preloadSyntheticArchive(t, preManifest)
	formalZip := preloadSyntheticArchive(t, formalManifest)
	preCfg := []byte(fmt.Sprintf(`<config><ResVersion>1.5.0</ResVersion><Extra><listHash>%s</listHash></Extra></config>`, preHash))
	formalCfg := []byte(fmt.Sprintf(`<config><ResVersion>1.5.3</ResVersion><UpdateBranchName>publish_Updata</UpdateBranchName><Extra><listHash>%s</listHash></Extra></config>`, formalHash))
	resCfg := base64.StdEncoding.EncodeToString([]byte(`{"appId":"1289","branchName":"PC_150","updateBranchName":"publish_Updata"}`))

	released := false
	sharedGets, formalDeltaGets := 0, 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/clientRes/PC_140/Version/Windows/config.xml":
			if released {
				fmt.Fprintf(w, `<config><ResVersion>1.4.18</ResVersion><PreReleaseStatus>end</PreReleaseStatus><PreReleaseBranch>PC_150</PreReleaseBranch><PreReleaseVersion>1.5.0</PreReleaseVersion><ResConfig>%s</ResConfig><Extra><listHash>%s</listHash></Extra></config>`, resCfg, preHash)
			} else {
				fmt.Fprintf(w, `<config><ResVersion>1.4.18</ResVersion><PreReleaseStatus>start</PreReleaseStatus><PreReleaseBranch>PC_150</PreReleaseBranch><PreReleaseVersion>1.5.0</PreReleaseVersion><Extra><listHash>%s</listHash></Extra></config>`, preHash)
			}
		case "/clientRes/PC_150/Version/Windows/config.xml":
			_, _ = w.Write(formalCfg)
		case "/clientRes/PC_150/Version/Windows/version/1.5.0/config.xml":
			_, _ = w.Write(preCfg)
		case "/clientRes/PC_150/Version/Windows/version/1.5.0/ResList.bin.zip":
			_, _ = w.Write(preZip)
		case "/clientRes/PC_150/Version/Windows/version/1.5.3/ResList.bin.zip":
			_, _ = w.Write(formalZip)
		case fmt.Sprintf("/clientRes/PC_150/Res/%c/%s", sharedObj.MD5[0], sharedObj):
			if r.Method == http.MethodGet {
				sharedGets++
			}
			http.ServeContent(w, r, "shared", time.Time{}, bytes.NewReader(sharedBytes))
		case fmt.Sprintf("/clientRes/PC_150/Res/%c/%s", preDeltaObj.MD5[0], preDeltaObj):
			http.ServeContent(w, r, "predelta", time.Time{}, bytes.NewReader(preDeltaBytes))
		case fmt.Sprintf("/clientRes/PC_150/Res/%c/%s", formalDeltaObj.MD5[0], formalDeltaObj):
			if r.Method == http.MethodGet {
				formalDeltaGets++
			}
			http.ServeContent(w, r, "formaldelta", time.Time{}, bytes.NewReader(formalDeltaBytes))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	b := NewGameDownloadBackend(dbtest.Open(t), t.TempDir(), nil, srv.Client(), "test")
	b.roots = []string{srv.URL + "/clientRes"}
	b.free = func(string) (int64, error) { return 8 << 30, nil }
	b.processes = func(context.Context) (downloadProcesses, error) { return downloadProcesses{}, nil }

	root := t.TempDir()
	pe := make([]byte, 90)
	copy(pe, "MZ")
	binary.LittleEndian.PutUint32(pe[60:64], 64)
	copy(pe[64:68], "PE\x00\x00")
	binary.LittleEndian.PutUint16(pe[68:70], 0x8664)
	binary.LittleEndian.PutUint16(pe[84:86], 2)
	binary.LittleEndian.PutUint16(pe[86:88], 2)
	binary.LittleEndian.PutUint16(pe[88:90], 0x20b)
	preloadSystemPut(t, root, "Client/WindowsNoEditor/HT/Binaries/Win64/HTGame.exe", pe)
	preloadSystemPut(t, root, "Client/WindowsNoEditor/PatcherConfig/Patch_Windows.json", []byte(`{"appId":"1289","branchName":"PC_140","gameGetServerListUrl":["https://cdn.example.invalid/list.json"]}`))
	preloadSystemPut(t, root, "Client/WindowsNoEditor/HT/Content/Paks/a.pak", []byte("old-140"))
	oldManifest := []byte(fmt.Sprintf(`<ResList version="1.4.18" tag="baseTag"><Res filename="HT/Content/Paks/a.pak" md5="%x" filesize="7"/></ResList>`, md5.Sum([]byte("old-140"))))
	preloadSystemPut(t, root, "NTELauncher/UserData/Patcher/PatcherSDK/ResList.xml", oldManifest)
	preloadSystemPut(t, root, "NTELauncher/UserData/Patcher/PatcherSDK/config.xml", []byte(fmt.Sprintf(`<config><ResVersion>1.4.18</ResVersion><Extra><listHash>%x</listHash></Extra></config>`, md5.Sum(oldManifest))))
	instStore, err := b.installStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instStore.AdoptQuick(ctx, root, nil); err != nil {
		t.Fatal(err)
	}
	if res, err := instStore.VerifyInstallation(ctx, root, nil); err != nil || len(res.Mismatches) != 0 {
		t.Fatalf("verify: %+v %v", res, err)
	}

	// 1. Pre-download PC_150/1.5.0 while PC_140 is on 1.4.18 and PC_150 head is already 1.5.3.
	req := launcher.DownloadRequest{Root: root, Kind: state.DownloadPreload, Effect: state.Effect{Kind: state.EffectDownloadPlan}}
	if ev := b.HandleDownload(ctx, req, nil); ev.Kind != state.DownloadPlanned || ev.Message != "1.5.0" {
		t.Fatalf("preload plan = %+v", ev)
	}
	workRoot := b.getPlan(root).workRoot
	req.Effect.Kind = state.EffectPreloadRun
	if ev := b.HandleDownload(ctx, req, nil); ev.Kind != state.PreloadStaged {
		t.Fatalf("preload stage = %+v", ev)
	}
	if sharedGets != 1 || formalDeltaGets != 0 {
		t.Fatalf("unexpected downloads during preload: shared=%d formalDelta=%d", sharedGets, formalDeltaGets)
	}

	// 2. Formal release promotes PC_140 -> PC_150 (1.5.3) via <ResConfig>.
	released = true
	latest, _, _, err := b.preloadOfferSnapshot(ctx, root)
	if err != nil || latest != "1.5.3" {
		t.Fatalf("offer after branch promotion = %q err=%v", latest, err)
	}

	// 3. First reconcile reuses a.pak and plans only b.pak delta.
	req.Effect = state.Effect{Kind: state.EffectPreloadCheck, Enabled: true}
	if ev := b.HandleDownload(ctx, req, nil); ev.Kind != state.DownloadPlanned || ev.Message != "1.5.3" || ev.Total != formalDeltaObj.Size {
		t.Fatalf("delta reconcile = %+v, want total %d", ev, formalDeltaObj.Size)
	}
	req.Effect = state.Effect{Kind: state.EffectPreloadRun}
	if ev := b.HandleDownload(ctx, req, nil); ev.Kind != state.PreloadStaged {
		t.Fatalf("supplemental stage = %+v", ev)
	}

	// 4. Second reconcile confirms applicable, then apply switches branch and cleans work dir.
	req.Effect = state.Effect{Kind: state.EffectPreloadCheck, Enabled: true}
	if ev := b.HandleDownload(ctx, req, nil); ev.Kind != state.PreloadAvailableToApply {
		t.Fatalf("second reconcile = %+v", ev)
	}
	req.Effect = state.Effect{Kind: state.EffectPreloadApply}
	if ev := b.HandleDownload(ctx, req, nil); ev.Kind != state.PreloadApplied || ev.Message != "1.5.3" {
		t.Fatalf("apply = %+v", ev)
	}
	if sharedGets != 1 || formalDeltaGets != 1 {
		t.Fatalf("reused file re-downloaded: shared=%d formalDelta=%d", sharedGets, formalDeltaGets)
	}
	if got, err := os.ReadFile(filepath.Join(root, "Client", "WindowsNoEditor", "HT", "Content", "Paks", "a.pak")); err != nil || !bytes.Equal(got, sharedBytes) {
		t.Fatalf("a.pak = %q %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "Client", "WindowsNoEditor", "HT", "Content", "Paks", "b.pak")); err != nil || !bytes.Equal(got, formalDeltaBytes) {
		t.Fatalf("b.pak = %q %v", got, err)
	}
	rec, err := instStore.ReadInstall(root)
	if err != nil || rec.Branch != "PC_150" || rec.Version != "1.5.3" {
		t.Fatalf("install record after branch switch = %+v %v", rec, err)
	}
	inspected, err := game.Inspect(ctx, root)
	if err != nil || inspected.Branch != "PC_150" {
		t.Fatalf("Patch_Windows.json branch after switch = %+v %v", inspected, err)
	}
	if _, err := os.Stat(workRoot); !os.IsNotExist(err) {
		t.Fatalf("preload work dir not cleaned after cross-branch apply: %v", err)
	}
}

// preloadSystemParkedApplying stages a synthetic preload record and leaves it
// in applying with no transaction journal, exactly like a process exit between
// the record transition and the journal write.
func preloadSystemParkedApplying(t *testing.T, b *GameDownloadBackend, root, applyTxn string) gameinstall.PreloadRecord {
	t.Helper()
	ctx := context.Background()
	d := preloadSystemDelivery(t, "https://synthetic.invalid/clientRes")
	_, snapshot, err := downloadPreloadEvidence(d, []string{d.ConfigResponse.Root}, true)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := gameinstall.StartPreload(ctx, gameinstall.PreloadInput{
		Advertisement: gameres.Config{PreReleaseStatus: "start", PreReleaseBranch: d.Branch, PreReleaseVersion: d.Config.ResVersion},
		Snapshot:      snapshot, CurrentBranch: "PC_150", CurrentVersion: "1.0.0", BaselineTrusted: true,
		Budget: gameres.BudgetInput{VolumeFreeBytes: 8 << 30, CacheFreeBytes: 8 << 30}, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := b.preloadStore(root)
	if err != nil {
		t.Fatal(err)
	}
	r := planned.Record
	r.State = gameinstall.PreloadApplying
	r.ApplyTxnID = applyTxn
	if err = store.Save(ctx, r); err != nil {
		t.Fatal(err)
	}
	return r
}

// A preload whose transaction was interrupted and rolled back stays parked in
// applying. Recovery must release it instead of demanding recovery forever.
func TestSystemRecoverScanReleasesRolledBackPreloadApplying(t *testing.T) {
	ctx := context.Background()
	b := &GameDownloadBackend{DB: dbtest.Open(t), StateRoot: t.TempDir(), free: func(string) (int64, error) { return 8 << 30, nil }, processes: func(context.Context) (downloadProcesses, error) { return downloadProcesses{}, nil }}
	root := preloadSystemBase(t, b)
	store, err := b.preloadStore(root)
	if err != nil {
		t.Fatal(err)
	}
	r := preloadSystemParkedApplying(t, b, root, "interrupted-apply")
	// The rollback already removed its journal, so nothing is pending and no
	// installation record publishes that transaction.
	install, err := b.installStore()
	if err != nil {
		t.Fatal(err)
	}
	status, err := install.TransactionStatus(ctx, root, downloadWork(root))
	if err != nil || status.LaunchBlocked || status.Published {
		t.Fatalf("unexpected transaction status: %+v %v", status, err)
	}

	req := launcher.DownloadRequest{Root: root, Kind: state.DownloadPreload, Effect: state.Effect{Kind: state.EffectRecoverScan, Token: 1, Enabled: true}}
	event := b.HandleDownload(ctx, req, nil)
	if event.Kind != state.DownloadRecoveryCleared || event.Body != "stale" {
		t.Fatalf("recovery scan = %+v, want cleared/stale", event)
	}
	loaded, err := store.Load(ctx, r.Branch, r.Version)
	if err != nil || loaded.State != gameinstall.PreloadStale {
		t.Fatalf("preload record after recovery = %+v %v", loaded, err)
	}

	// The next startup scan must not demand recovery again; the user can
	// recheck or discard the now stale preload.
	scan := req
	scan.Effect.Enabled = false
	scan.Effect.Token = 2
	if got := b.HandleDownload(ctx, scan, nil); got.Kind != state.DownloadRestored || got.Body != "stale" {
		t.Fatalf("second scan = %+v, want restored/stale", got)
	}
}

// A read-only startup scan cannot clear anything, so a preload still parked in
// applying must be reported as requiring recovery rather than as cleared.
func TestSystemRecoverScanRequiresRecoveryForParkedPreloadApplying(t *testing.T) {
	ctx := context.Background()
	b := &GameDownloadBackend{DB: dbtest.Open(t), StateRoot: t.TempDir(), free: func(string) (int64, error) { return 8 << 30, nil }, processes: func(context.Context) (downloadProcesses, error) { return downloadProcesses{}, nil }}
	root := preloadSystemBase(t, b)
	r := preloadSystemParkedApplying(t, b, root, "interrupted-apply")

	scan := launcher.DownloadRequest{Root: root, Kind: state.DownloadPreload, Effect: state.Effect{Kind: state.EffectRecoverScan, Token: 1}}
	if event := b.HandleDownload(ctx, scan, nil); event.Kind != state.DownloadRecoveryNeeded {
		t.Fatalf("startup scan reported %+v, want recovery needed", event)
	}
	store, err := b.preloadStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.Load(ctx, r.Branch, r.Version); err != nil || loaded.State != gameinstall.PreloadApplying {
		t.Fatalf("startup scan changed the parked record: %+v %v", loaded, err)
	}
}

// preloadSystemSeedPriorTxn republishes an installation under its own
// transaction so the record carries a TxnID. TransactionStatus then reports
// Published even though the latest preload apply never committed.
func preloadSystemSeedPriorTxn(t *testing.T, b *GameDownloadBackend, root string, base gameinstall.InstallRecord) gameinstall.InstallRecord {
	t.Helper()
	ctx := context.Background()
	store, err := b.installStore()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(downloadWork(root), 0700); err != nil {
		t.Fatal(err)
	}
	candidate := base
	candidate.TxnID, candidate.State = "", "ready"
	input := gameinstall.TxnInput{Root: root, WorkRoot: downloadWork(root), TxnID: "prior-txn", Candidate: candidate,
		Guard: func(context.Context) error { return nil }, BackupRetention: 24 * time.Hour,
		FreeBytes: func(context.Context) (int64, error) { return 8 << 30, nil }}
	for _, target := range base.Targets {
		input.Files = append(input.Files, gameinstall.TxnFile{Path: target.Path, MD5: target.MD5, Size: target.Size, Origin: "installed"})
	}
	status, err := store.Commit(ctx, input)
	if err != nil || !status.Published || status.LaunchBlocked {
		t.Fatalf("seed prior transaction: %+v %v", status, err)
	}
	record, err := store.ReadInstall(root)
	if err != nil || record.TxnID != "prior-txn" {
		t.Fatalf("seeded install record = %+v %v", record, err)
	}
	return record
}

// An installation that already published an earlier transaction still reports
// Published, so an interrupted preload apply must still be released instead of
// sticking in applying forever.
func TestSystemRecoverScanReleasesParkedPreloadAfterPriorPublication(t *testing.T) {
	ctx := context.Background()
	b := &GameDownloadBackend{DB: dbtest.Open(t), StateRoot: t.TempDir(), free: func(string) (int64, error) { return 8 << 30, nil }, processes: func(context.Context) (downloadProcesses, error) { return downloadProcesses{}, nil }}
	root, base := preloadSystemVersionedBase(t, b, "PC_150", "1.0.0", []byte("old"))
	preloadSystemSeedPriorTxn(t, b, root, base)
	r := preloadSystemParkedApplying(t, b, root, "interrupted-apply")
	install, err := b.installStore()
	if err != nil {
		t.Fatal(err)
	}
	status, err := install.TransactionStatus(ctx, root, downloadWork(root))
	if err != nil || status.LaunchBlocked || !status.Published {
		t.Fatalf("seeded transaction status = %+v %v", status, err)
	}

	scan := launcher.DownloadRequest{Root: root, Kind: state.DownloadPreload, Effect: state.Effect{Kind: state.EffectRecoverScan, Token: 1}}
	if event := b.HandleDownload(ctx, scan, nil); event.Kind != state.DownloadRecoveryNeeded {
		t.Fatalf("startup scan ignored the parked apply: %+v", event)
	}
	recover := scan
	recover.Effect.Enabled, recover.Effect.Token = true, 2
	if event := b.HandleDownload(ctx, recover, nil); event.Kind != state.DownloadRecoveryCleared || event.Body != "stale" {
		t.Fatalf("explicit recovery = %+v, want cleared/stale", event)
	}
	store, err := b.preloadStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.Load(ctx, r.Branch, r.Version); err != nil || loaded.State != gameinstall.PreloadStale {
		t.Fatalf("preload record after prior-publication recovery = %+v %v", loaded, err)
	}
	record, err := install.ReadInstall(root)
	if err != nil || record.Version != "1.0.0" || record.TxnID != "prior-txn" {
		t.Fatalf("recovery touched the published installation: %+v %v", record, err)
	}
	recheck := scan
	recheck.Effect.Token = 3
	if got := b.HandleDownload(ctx, recheck, nil); got.Kind == state.DownloadRecoveryNeeded {
		t.Fatalf("released preload still demands recovery: %+v", got)
	}
}

// A paused hotfix and a staged preload can coexist. The startup scan must
// resume the hotfix and leave the preload record untouched for the reconcile.
func TestSystemRecoverScanPrefersPausedHotfixOverStagedPreload(t *testing.T) {
	ctx := context.Background()
	b := &GameDownloadBackend{DB: dbtest.Open(t), StateRoot: t.TempDir(), free: func(string) (int64, error) { return 8 << 30, nil }, processes: func(context.Context) (downloadProcesses, error) { return downloadProcesses{}, nil }}
	root, _ := preloadSystemVersionedBase(t, b, "PC_150", "1.0.0", []byte("old"))
	key, err := gameinstall.InstallKey(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	staged := gameinstall.PreloadRecord{Schema: 1, State: gameinstall.PreloadWaitingRelease, Branch: "PC_160", Version: "1.1.0",
		ListHash: strings.Repeat("a", 32), Route: gameinstall.PreloadRouteFull, SavedAt: now, UpdatedAt: now,
		Targets: []gameinstall.PreloadTarget{{Path: "Client/WindowsNoEditor/HT/Content/Paks/a.pak", Object: gameres.Object{MD5: strings.Repeat("b", 32), Size: 3}, Action: gameres.ActionFull}}}
	if err = os.MkdirAll(downloadWork(root), 0700); err != nil {
		t.Fatal(err)
	}
	if err = preloadSystemMakeWork(root, staged); err != nil {
		t.Fatal(err)
	}
	store, err := b.preloadStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(ctx, staged); err != nil {
		t.Fatal(err)
	}
	tasks, err := gamedl.NewStore(b.DB, b.taskRoot(key))
	if err != nil {
		t.Fatal(err)
	}
	paused := gamedl.Task{ID: "paused-hotfix", InstallKey: key, Purpose: "game-resources", Branch: "PC_150",
		TargetVersion: "1.1.0", ListHash: strings.Repeat("c", 32), Kind: gamedl.TaskUpdateFiles, State: gamedl.StatePaused}
	if err = tasks.Save(ctx, paused); err != nil {
		t.Fatal(err)
	}
	if err = tasks.Close(); err != nil {
		t.Fatal(err)
	}

	scan := launcher.DownloadRequest{Root: root, Kind: state.DownloadPreload, Effect: state.Effect{Kind: state.EffectRecoverScan, Token: 1}}
	event := b.HandleDownload(ctx, scan, nil)
	if event.Kind != state.DownloadRestored || event.Index != uint8(state.DownloadUpdate) || event.Stage != state.StageNetwork || event.Message != "1.1.0" || event.Body != "waiting-release:1.1.0" || event.Focused {
		t.Fatalf("startup scan restored %+v, want the paused hotfix reports the staged preload", event)
	}
	p := b.getPlan(root)
	if p == nil || p.resumeTask == nil || p.resumeTask.ID != paused.ID || p.kind != state.DownloadUpdate {
		t.Fatalf("resumed plan = %+v, want the paused hotfix", p)
	}
	if loaded, err := store.Load(ctx, staged.Branch, staged.Version); err != nil || loaded.State != gameinstall.PreloadWaitingRelease {
		t.Fatalf("staged preload record after hotfix restore = %+v %v", loaded, err)
	}
}

// A WaitingRelease record can outlive its staging directory. The startup scan
// must report it as stale instead of demanding recovery on every launch, which
// would also hide a paused hotfix that is ready to resume.
func TestSystemRecoverScanToleratesMissingPreloadStagingDirectory(t *testing.T) {
	for _, tt := range []struct {
		name   string
		hotfix bool
	}{
		{name: "paused-hotfix", hotfix: true},
		{name: "no-task"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			b := &GameDownloadBackend{DB: dbtest.Open(t), StateRoot: t.TempDir(), free: func(string) (int64, error) { return 8 << 30, nil }, processes: func(context.Context) (downloadProcesses, error) { return downloadProcesses{}, nil }}
			root, _ := preloadSystemVersionedBase(t, b, "PC_150", "1.0.0", []byte("old"))
			key, err := gameinstall.InstallKey(root)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			staged := gameinstall.PreloadRecord{Schema: 1, State: gameinstall.PreloadWaitingRelease, Branch: "PC_160", Version: "1.1.0",
				ListHash: strings.Repeat("a", 32), Route: gameinstall.PreloadRouteFull, SavedAt: now, UpdatedAt: now,
				Targets: []gameinstall.PreloadTarget{{Path: "Client/WindowsNoEditor/HT/Content/Paks/a.pak", Object: gameres.Object{MD5: strings.Repeat("b", 32), Size: 3}, Action: gameres.ActionFull}}}
			if err = os.MkdirAll(downloadWork(root), 0700); err != nil {
				t.Fatal(err)
			}
			if err = preloadSystemMakeWork(root, staged); err != nil {
				t.Fatal(err)
			}
			store, err := b.preloadStore(root)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Save(ctx, staged); err != nil {
				t.Fatal(err)
			}
			if tt.hotfix {
				tasks, err := gamedl.NewStore(b.DB, b.taskRoot(key))
				if err != nil {
					t.Fatal(err)
				}
				paused := gamedl.Task{ID: "paused-hotfix", InstallKey: key, Purpose: "game-resources", Branch: "PC_150",
					TargetVersion: "1.1.0", ListHash: strings.Repeat("c", 32), Kind: gamedl.TaskUpdateFiles, State: gamedl.StatePaused}
				if err = tasks.Save(ctx, paused); err != nil {
					t.Fatal(err)
				}
				if err = tasks.Close(); err != nil {
					t.Fatal(err)
				}
			}
			// The staged bytes disappear while the record still claims them.
			if err = os.RemoveAll(preloadSystemWork(root, staged)); err != nil {
				t.Fatal(err)
			}

			scan := launcher.DownloadRequest{Root: root, Kind: state.DownloadPreload, Effect: state.Effect{Kind: state.EffectRecoverScan, Token: 1}}
			event := b.HandleDownload(ctx, scan, nil)
			if event.Kind == state.DownloadRecoveryNeeded {
				t.Fatalf("startup scan demanded recovery for a deleted staging directory: %+v", event)
			}
			if tt.hotfix {
				if event.Kind != state.DownloadRestored || event.Index != uint8(state.DownloadUpdate) || event.Message != "1.1.0" || event.Body != "stale:1.1.0" {
					t.Fatalf("startup scan = %+v, want the paused hotfix reporting the stale preload", event)
				}
				if p := b.getPlan(root); p == nil || p.resumeTask == nil || p.resumeTask.ID != "paused-hotfix" || p.kind != state.DownloadUpdate {
					t.Fatalf("resumed plan = %+v, want the paused hotfix", p)
				}
				return
			}
			if event.Kind != state.DownloadRestored || event.Index != uint8(state.DownloadPreload) || event.Stage != state.StageIdle || event.Body != "stale" {
				t.Fatalf("startup scan = %+v, want restored/stale preload", event)
			}
		})
	}
}
