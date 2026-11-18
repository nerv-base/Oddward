// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameinstall"
	"github.com/nerv-base/Oddward/internal/gameres"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func preloadSystemPut(t *testing.T, root, name string, data []byte) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func preloadSystemBase(t *testing.T, b *GameDownloadBackend) string {
	t.Helper()
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
	preloadSystemPut(t, root, "Client/WindowsNoEditor/HT/Content/Paks/a.pak", []byte("old"))
	manifest := []byte(fmt.Sprintf(`<ResList version="1.0.0" tag="baseTag"><Res filename="HT/Content/Paks/a.pak" md5="%x" filesize="3"/></ResList>`, md5.Sum([]byte("old"))))
	preloadSystemPut(t, root, "NTELauncher/UserData/Patcher/PatcherSDK/ResList.xml", manifest)
	preloadSystemPut(t, root, "NTELauncher/UserData/Patcher/PatcherSDK/config.xml", []byte(fmt.Sprintf(`<config><ResVersion>1.0.0</ResVersion><Extra><listHash>%x</listHash></Extra></config>`, md5.Sum(manifest))))
	s, err := b.installStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdoptQuick(context.Background(), root, nil); err != nil {
		t.Fatal(err)
	}
	r, err := s.VerifyInstallation(context.Background(), root, nil)
	if err != nil || len(r.Mismatches) != 0 {
		t.Fatalf("verify fixture: %+v %v", r, err)
	}
	return root
}
func preloadSystemDelivery(t *testing.T, root string) gameDelivery {
	t.Helper()
	manifest := []byte(fmt.Sprintf(`<ResList version="1.1.0" tag="baseTag"><Res filename="HT/Content/Paks/a.pak" md5="%x" filesize="3"/></ResList>`, md5.Sum([]byte("new"))))
	cfgData := []byte(fmt.Sprintf(`<config><ResVersion>1.1.0</ResVersion><Extra><listHash>%x</listHash></Extra></config>`, md5.Sum(manifest)))
	cfg, err := gameres.ParseConfig(cfgData)
	if err != nil {
		t.Fatal(err)
	}
	xml := gameres.ManifestXML{ResList: manifest}
	m, g, err := gameres.ParseDelivery(cfg, xml)
	if err != nil {
		t.Fatal(err)
	}
	return gameDelivery{Config: cfg, Manifest: m, Graph: g, XML: xml, Branch: "PC_160", ConfigResponse: downloadResponse{Data: cfgData, Root: root, URL: root + "/observed-preload/config.xml"}}
}
func TestNewGameDownloadBackendWiresOfficialPreloadQuery(t *testing.T) {
	backend := NewGameDownloadBackend(t.TempDir(), nil, nil, "test")
	if backend.preloadQuery == nil {
		t.Fatal("production backend did not wire the official pre-release query")
	}
}

func TestSystemPreloadFormalHistoryCountsDistinctNewerVersions(t *testing.T) {
	r := gameinstall.PreloadRecord{Version: "1.1.0"}
	for _, version := range []string{"1.0.0", "1.1.0", "1.2.0", "1.2.0", "1.3.0"} {
		r = downloadPreloadObserveFormal(r, version)
	}
	if len(r.ObservedFormalVersions) != 2 || r.ObservedFormalVersions[0] != "1.2.0" || r.ObservedFormalVersions[1] != "1.3.0" {
		t.Fatalf("formal expiry history=%v", r.ObservedFormalVersions)
	}
}

func TestSystemPreloadEvidenceRequiresObservedTrustedCoherentRoute(t *testing.T) {
	good := preloadSystemDelivery(t, "https://synthetic.invalid/clientRes")
	for _, name := range []string{"valid", "unknown-route", "other-root", "cross-origin", "tampered", "traversal"} {
		t.Run(name, func(t *testing.T) {
			d := good
			known := true
			switch name {
			case "unknown-route":
				known = false
			case "other-root":
				d.ConfigResponse.Root = "https://untrusted.invalid/clientRes"
			case "cross-origin":
				d.ConfigResponse.URL = "https://untrusted.invalid/query"
			case "tampered":
				d.XML.ResList = []byte("wrong")
			case "traversal":
				d.Branch = "../PC_160"
			}
			_, snapshot, err := downloadPreloadEvidence(d, []string{good.ConfigResponse.Root}, known)
			if name == "valid" {
				if err != nil || !snapshot.Verified || snapshot.Manifest.Files[0].Path != "Client/WindowsNoEditor/HT/Content/Paks/a.pak" {
					t.Fatalf("evidence: %+v %v", snapshot, err)
				}
			} else if err == nil {
				t.Fatal("untrusted preload evidence accepted")
			}
		})
	}
}
func TestSystemPreloadDiscoveryCarriesPerformanceRateFallbacks(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<config><ResVersion>1.0.0</ResVersion><Extra><listHash>d41d8cd98f00b204e9800998ecf8427e</listHash></Extra></config>`)
	}))
	defer srv.Close()
	b := &GameDownloadBackend{StateRoot: t.TempDir(), Client: srv.Client(), roots: []string{srv.URL}, free: func(string) (int64, error) { return 8 << 30, nil }, processes: func(context.Context) (downloadProcesses, error) { return downloadProcesses{}, nil }}
	root := preloadSystemBase(t, b)
	input, _, _, err := b.preloadDiscovery(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	rates, _ := b.downloadPerfRates(context.Background(), root)
	if input.Budget.NetBytesPerSecond != rates.NetBytesPerSecond || input.Budget.VerifyBytesPerSecond != rates.HashBytesPerSecond || input.Budget.IOBytesPerSecond != rates.IOBytesPerSecond {
		t.Fatalf("planning lost rate estimates: %+v expected %+v", input.Budget, rates)
	}
}

func TestSystemPreloadUnknownQueryRouteFailsClosedWithoutFetching(t *testing.T) {
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/clientRes/PC_150/Version/Windows/config.xml" {
			t.Errorf("fabricated route %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `<config><ResVersion>1.0.0</ResVersion><PreReleaseStatus>start</PreReleaseStatus><PreReleaseBranch>PC_160</PreReleaseBranch><PreReleaseVersion>1.1.0</PreReleaseVersion><Extra><listHash>d41d8cd98f00b204e9800998ecf8427e</listHash></Extra></config>`)
	}))
	defer srv.Close()
	b := &GameDownloadBackend{StateRoot: t.TempDir(), Client: srv.Client(), roots: []string{srv.URL + "/clientRes"}, free: func(string) (int64, error) { return 8 << 30, nil }, processes: func(context.Context) (downloadProcesses, error) { return downloadProcesses{}, nil }}
	root := preloadSystemBase(t, b)
	event := b.HandleDownload(context.Background(), launcher.DownloadRequest{Root: root, Kind: state.DownloadPreload, Effect: state.Effect{Kind: state.EffectPreloadCheck}}, nil)
	if event.Kind != state.PreloadChecked || event.Enabled || event.Body != "unverified" || calls != 1 {
		t.Fatalf("discovery: %+v calls=%d", event, calls)
	}
	latest, preload, offered, err := b.preloadOfferSnapshot(context.Background(), root)
	if err != nil || latest != "1.0.0" || preload != "1.1.0" || offered || calls != 2 {
		t.Fatalf("read-only offer snapshot: latest=%q preload=%q offered=%v err=%v calls=%d", latest, preload, offered, err, calls)
	}
	if _, err := os.Stat(downloadWork(root)); !os.IsNotExist(err) {
		t.Fatalf("unverified route created work tree: %v", err)
	}
}
func TestSystemPreloadStagedAndRestoreRetainDedicatedCache(t *testing.T) {
	b := &GameDownloadBackend{StateRoot: t.TempDir(), free: func(string) (int64, error) { return 8 << 30, nil }, processes: func(context.Context) (downloadProcesses, error) { return downloadProcesses{}, nil }}
	root := preloadSystemBase(t, b)
	d := preloadSystemDelivery(t, "https://synthetic.invalid/clientRes")
	b.roots = []string{d.ConfigResponse.Root}
	_, snapshot, err := downloadPreloadEvidence(d, b.roots, true)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := gameinstall.StartPreload(context.Background(), gameinstall.PreloadInput{Advertisement: gameres.Config{PreReleaseStatus: "start", PreReleaseBranch: d.Branch, PreReleaseVersion: d.Config.ResVersion}, Snapshot: snapshot, CurrentBranch: "PC_150", CurrentVersion: "1.0.0", BaselineTrusted: true, Budget: gameres.BudgetInput{VolumeFreeBytes: 8 << 30, CacheFreeBytes: 8 << 30}, Now: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	key, err := gameinstall.InstallKey(root)
	if err != nil {
		t.Fatal(err)
	}
	p := &downloadPlan{root: root, key: key, id: "synthetic-preload", kind: state.DownloadPreload, delivery: d, plan: planned.Plan, preload: &planned.Record, workRoot: filepath.Join(downloadWork(root), "preload", d.Branch, d.Config.ResVersion)}
	if err = os.MkdirAll(p.workRoot, 0700); err != nil {
		t.Fatal(err)
	}
	engine, err := gamedl.New(gamedl.Options{WorkRoot: p.workRoot, StateRoot: b.taskRoot(key), Client: http.DefaultClient, Resolve: func(context.Context, gameres.Source) ([]string, error) {
		t.Fatal("unexpected network")
		return nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	object := planned.Record.Files[0].Object
	blob := filepath.Join(p.workRoot, "cache", "blobs", object.String())
	if err = os.WriteFile(blob, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(blob)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.preloadStaged(context.Background(), p, engine); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(blob)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("staging copied/moved cache: %v", err)
	}
	// Restore owns the root and dedicated lease, so close the active engine first.
	if err = engine.Close(); err != nil {
		t.Fatal(err)
	}
	version, note, err := b.preloadRestore(context.Background(), root)
	if err != nil || version != "1.1.0" || note != "waiting-release" {
		t.Fatalf("restore skipped reconcile: %q %q %v", version, note, err)
	}
	if _, err = os.Stat(filepath.Join(p.workRoot, "blobs")); !os.IsNotExist(err) {
		t.Fatalf("extra blob copy exists: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "Client", "WindowsNoEditor", "HT", "Content", "Paks", "a.pak")); err != nil || string(got) != "old" {
		t.Fatal("preload changed installed bytes")
	}
}
