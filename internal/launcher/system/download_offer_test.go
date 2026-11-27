// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database/dbtest"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestHomeUpdateCommitsPreviouslyUnmanagedInstallation(t *testing.T) {
	ctx := context.Background()
	root := officialInstallFixture(t)
	d := preloadSystemDelivery(t, "")
	archive := preloadSyntheticArchive(t, d.XML.ResList)
	data := []byte("new")
	obj := adapterObject(data)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/PC_150/Version/Windows/config.xml":
			w.Write(d.ConfigResponse.Data)
		case "/PC_150/Version/Windows/version/1.1.0/ResList.bin.zip":
			w.Write(archive)
		case fmt.Sprintf("/PC_150/Res/%c/%s", obj.MD5[0], obj):
			http.ServeContent(w, r, "resource", time.Time{}, bytes.NewReader(data))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	b := NewGameDownloadBackend(dbtest.Open(t), t.TempDir(), nil, srv.Client(), "test")
	b.roots = []string{srv.URL}
	b.free = func(string) (int64, error) { return 8 << 30, nil }
	b.processes = func(context.Context) (downloadProcesses, error) { return downloadProcesses{}, nil }
	m := state.InitialNormal()
	m, _ = state.Reduce(m, state.Event{Kind: state.ManagedLoad})
	m, _ = state.Reduce(m, state.Event{Kind: state.ManagedLoaded, Token: m.Managed.Request, Path: root})
	m, _ = state.Reduce(m, state.Event{Kind: state.ManagedLocated, Token: m.Managed.Request, Path: root})
	var effects []state.Effect
	var sawNetworkProgress bool
	var maxNetworkDownloaded int64
	run := func(kind state.EffectKind) {
		t.Helper()
		if len(effects) != 1 || effects[0].Kind != kind {
			t.Fatalf("want effect %v, got %+v, phase=%v error=%q", kind, effects, m.Download.Phase, m.Download.Error)
		}
		req := launcher.DownloadRequest{Root: root, Kind: m.Download.Kind, Effect: effects[0]}
		e := b.HandleDownload(ctx, req, func(progress state.Event) {
			m, _ = state.Reduce(m, progress)
			if m.Download.Stage == state.StageNetwork && m.Download.Downloaded > 0 {
				sawNetworkProgress = true
				if m.Download.Downloaded > maxNetworkDownloaded {
					maxNetworkDownloaded = m.Download.Downloaded
				}
			}
		})
		m, effects = state.Reduce(m, e)
	}
	m, effects = state.Reduce(m, state.Event{Kind: state.DownloadOfferPollRequest})
	run(state.EffectDownloadOfferPoll)
	if state.Present(m).Primary != "更新游戏" {
		t.Fatal(state.Present(m))
	}
	m, effects = state.Reduce(m, state.Event{Kind: state.Primary})
	for _, kind := range []state.EffectKind{state.EffectInstallAdopt, state.EffectDownloadPlan, state.EffectDownloadRun, state.EffectDownloadCommit} {
		run(kind)
	}
	if !sawNetworkProgress || maxNetworkDownloaded != int64(len(data)) {
		t.Fatalf("StageNetwork progress did not advance through state.Reduce: saw=%v max=%d want=%d", sawNetworkProgress, maxNetworkDownloaded, len(data))
	}
	if m.Download.Phase != state.DownloadDone {
		t.Fatalf("update did not finish: %+v", m.Download)
	}
	got, err := os.ReadFile(filepath.Join(root, "Client/WindowsNoEditor/HT/Content/Paks/a.pak"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("updated resource %q: %v", got, err)
	}
	m, effects = state.Reduce(m, state.Event{Kind: state.DownloadOfferPollRequest})
	run(state.EffectDownloadOfferPoll)
	if state.Present(m).Primary != "启动游戏" || m.Download.Current != "1.1.0" {
		t.Fatalf("post-update offer: %+v, current=%s", state.Present(m), m.Download.Current)
	}
}

func TestOfferPollLocatesOfficialUpdateWithoutAdoptingInstallation(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/PC_150/Version/Windows/config.xml" {
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `<config><ResVersion>1.5.8</ResVersion><PreReleaseStatus>start</PreReleaseStatus><PreReleaseBranch>PC_160</PreReleaseBranch><PreReleaseVersion>1.6.0</PreReleaseVersion><Extra><listHash>d41d8cd98f00b204e9800998ecf8427e</listHash></Extra></config>`)
	}))
	defer srv.Close()
	for _, hasStateDir := range []bool{false, true} {
		t.Run(fmt.Sprintf("hasStateDir=%t", hasStateDir), func(t *testing.T) {
			root := officialInstallFixture(t)
			b := NewGameDownloadBackend(dbtest.Open(t), t.TempDir(), nil, srv.Client(), "test")
			b.roots = []string{srv.URL}
			installs := filepath.Join(b.StateRoot, "installs")
			if hasStateDir {
				if err := os.Mkdir(installs, 0700); err != nil {
					t.Fatal(err)
				}
			}
			preloadSystemPut(t, root, "NTELauncher/UserData/Patcher/PatcherSDK/config.xml", []byte(`<config><LocalBranch>PC_150</LocalBranch><ResVersion>1.5.6</ResVersion><Extra><listHash>d41d8cd98f00b204e9800998ecf8427e</listHash></Extra></config>`))
			ev := b.HandleDownload(context.Background(), launcher.DownloadRequest{Root: root, Effect: state.Effect{Kind: state.EffectDownloadOfferPoll}}, nil)
			if ev.Kind != state.DownloadOfferPolled || ev.CurrentVersion != "1.5.6" || ev.Message != "1.5.8" || ev.Enabled || !ev.NeedsAdoption || ev.NeedsVerification {
				t.Fatalf("formal offer: kind=%v current=%q latest=%q preload=%v", ev.Kind, ev.CurrentVersion, ev.Message, ev.Enabled)
			}
			if _, err := os.Stat(downloadWork(root)); !os.IsNotExist(err) {
				t.Fatalf("poll created a work directory: %v", err)
			}
			entries, err := os.ReadDir(installs)
			if hasStateDir && (err != nil || len(entries) != 0) || !hasStateDir && !os.IsNotExist(err) {
				t.Fatalf("poll changed installs directory: %v %v", entries, err)
			}
			var records int
			if err := b.DB.Reader().QueryRowContext(context.Background(), "SELECT count(*) FROM install_records").Scan(&records); err != nil || records != 0 {
				t.Fatalf("poll wrote install records: %d, %v", records, err)
			}
		})
	}
}
