// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database/dbtest"
	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameinstall"
	"github.com/nerv-base/Oddward/internal/gameres"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// A paused 1.4.18 update on the retired PC_140 branch must not keep running
// after the trusted mirror redirects PC_140 to PC_150.
func TestSystemResumeRejectsBranchMigration(t *testing.T) {
	ctx := context.Background()
	oldPak, newPak := []byte("old-1418"), []byte("new-150")
	manifest140 := []byte(fmt.Sprintf(`<ResList version="1.4.18" tag="baseTag"><Res filename="HT/Content/Paks/a.pak" md5="%x" filesize="%d"/></ResList>`, md5.Sum(oldPak), len(oldPak)))
	manifest150 := []byte(fmt.Sprintf(`<ResList version="1.5.0" tag="baseTag"><Res filename="HT/Content/Paks/a.pak" md5="%x" filesize="%d"/></ResList>`, md5.Sum(newPak), len(newPak)))
	resCfg := base64.StdEncoding.EncodeToString([]byte(`{"appId":"1289","branchName":"PC_150","updateBranchName":"publish_Updata"}`))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/clientRes/PC_140/Version/Windows/config.xml":
			fmt.Fprintf(w, `<config><ResVersion>1.4.18</ResVersion><ResConfig>%s</ResConfig><Extra><listHash>%x</listHash></Extra></config>`, resCfg, md5.Sum(manifest140))
		case "/clientRes/PC_140/Version/Windows/version/1.4.18/ResList.bin.zip":
			w.Write(preloadSyntheticArchive(t, manifest140))
		case "/clientRes/PC_150/Version/Windows/config.xml":
			fmt.Fprintf(w, `<config><ResVersion>1.5.0</ResVersion><Extra><listHash>%x</listHash></Extra></config>`, md5.Sum(manifest150))
		case "/clientRes/PC_150/Version/Windows/version/1.5.0/ResList.bin.zip":
			w.Write(preloadSyntheticArchive(t, manifest150))
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
	root, base := preloadSystemVersionedBase(t, b, "PC_140", "1.4.18", oldPak)
	key, err := gameinstall.InstallKey(root)
	if err != nil {
		t.Fatal(err)
	}
	task := &gamedl.Task{ID: "synthetic-resume", InstallKey: key, Purpose: "game-resources", Branch: "PC_140",
		TargetVersion: "1.4.18", ListHash: base.ListHash, Kind: gamedl.TaskUpdateFiles, State: gamedl.StatePaused}
	tasks, err := gamedl.NewStore(b.DB, b.taskRoot(key))
	if err != nil {
		t.Fatal(err)
	}
	if err = tasks.Save(ctx, *task); err != nil {
		t.Fatal(err)
	}
	if err = tasks.Close(); err != nil {
		t.Fatal(err)
	}
	p := &downloadPlan{root: root, key: key, id: task.ID, kind: state.DownloadUpdate, base: base, resumeTask: task, workRoot: downloadWork(root)}
	if _, err = b.revalidateResumedDownload(ctx, p); !errors.Is(err, gameinstall.ErrStaleState) {
		t.Fatalf("resumed retired branch was not rejected: %v", err)
	}
	// The rejected task must be retired on disk, otherwise the next startup scan
	// picks the migrated branch again and fails the same way.
	scan := launcher.DownloadRequest{Root: root, Kind: state.DownloadUpdate, Effect: state.Effect{Kind: state.EffectRecoverScan, Token: 1}}
	if got := b.HandleDownload(ctx, scan, nil); got.Kind != state.DownloadRecoveryCleared {
		t.Fatalf("migrated task restored again: %+v", got)
	}
	stored, err := gamedl.NewStore(b.DB, b.taskRoot(key))
	if err != nil {
		t.Fatal(err)
	}
	defer stored.Close()
	records, err := stored.List(ctx)
	if err != nil || len(records) != 1 || records[0].State != gamedl.StateStale {
		t.Fatalf("migrated task state = %+v %v, want stale", records, err)
	}
}

func TestSystemResumeReportsCumulativeCachedAndRemainingProgress(t *testing.T) {
	ctx := context.Background()
	cachedBytes := []byte("cached-blob-already-verified-before-pause")
	remainingBytes := []byte("remaining-blob-downloaded-after-resume")
	cachedObj := adapterObject(cachedBytes)
	remainingObj := adapterObject(remainingBytes)
	manifest := []byte(fmt.Sprintf(`<ResList version="1.5.13" tag="baseTag"><Res filename="HT/Content/Paks/a.pak" md5="%s" filesize="%d"/><Res filename="HT/Content/Paks/b.pak" md5="%s" filesize="%d"/></ResList>`,
		cachedObj.MD5, cachedObj.Size, remainingObj.MD5, remainingObj.Size))
	archive := preloadSyntheticArchive(t, manifest)
	listHash := fmt.Sprintf("%x", md5.Sum(manifest))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/clientRes/PC_150/Version/Windows/config.xml":
			fmt.Fprintf(w, `<config><ResVersion>1.5.13</ResVersion><Extra><listHash>%s</listHash></Extra></config>`, listHash)
		case "/clientRes/PC_150/Version/Windows/version/1.5.13/ResList.bin.zip":
			w.Write(archive)
		case fmt.Sprintf("/clientRes/PC_150/Res/%c/%s", cachedObj.MD5[0], cachedObj):
			if r.Method != http.MethodHead {
				t.Errorf("cached blob should not be re-downloaded")
			}
			http.ServeContent(w, r, "cached", time.Time{}, bytes.NewReader(cachedBytes))
		case fmt.Sprintf("/clientRes/PC_150/Res/%c/%s", remainingObj.MD5[0], remainingObj):
			http.ServeContent(w, r, "remaining", time.Time{}, bytes.NewReader(remainingBytes))
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
	root, base := preloadSystemVersionedBase(t, b, "PC_150", "1.5.8", []byte("old-158"))
	key, err := gameinstall.InstallKey(root)
	if err != nil {
		t.Fatal(err)
	}
	// Pre-populate the first blob in CAS as if downloaded before pause.
	workRoot := downloadWork(root)
	lease, err := acquireDownloadWork(root)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := gamedl.New(gamedl.Options{
		DB: b.DB, WorkRoot: workRoot, StateRoot: b.taskRoot(key), Client: srv.Client(), Lease: lease,
		Resolve: func(context.Context, gameres.Source) ([]string, error) { return []string{srv.URL}, nil },
	})
	if err != nil {
		lease.Close()
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workRoot, "cache", "tmp"), 0700); err != nil {
		eng.Close()
		lease.Close()
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workRoot, "cache", "tmp", "cached.bin"), cachedBytes, 0600); err != nil {
		eng.Close()
		lease.Close()
		t.Fatal(err)
	}
	if err := eng.Publish(ctx, cachedObj, "cache/tmp/cached.bin"); err != nil {
		eng.Close()
		lease.Close()
		t.Fatal(err)
	}
	eng.Close()
	lease.Close()

	task := &gamedl.Task{
		ID: "paused-1513", InstallKey: key, Purpose: "game-resources", Branch: "PC_150",
		TargetVersion: "1.5.13", ListHash: listHash, Kind: gamedl.TaskUpdateFiles, State: gamedl.StatePaused,
	}
	tasks, err := gamedl.NewStore(b.DB, b.taskRoot(key))
	if err != nil {
		t.Fatal(err)
	}
	if err := tasks.Save(ctx, *task); err != nil {
		t.Fatal(err)
	}
	tasks.Close()

	m := state.InitialNormal()
	m, _ = state.Reduce(m, state.Event{Kind: state.ManagedLoad})
	m, _ = state.Reduce(m, state.Event{Kind: state.ManagedLoaded, Token: m.Managed.Request, Path: root})
	m, _ = state.Reduce(m, state.Event{Kind: state.ManagedLocated, Token: m.Managed.Request, Path: root})
	m, fx := state.Reduce(m, state.Event{Kind: state.DownloadRecoveryScanRequest})
	restored := b.HandleDownload(ctx, launcher.DownloadRequest{Root: root, Kind: m.Download.Kind, Effect: fx[0]}, nil)
	m, _ = state.Reduce(m, restored)
	if m.Download.Phase != state.DownloadSuspended {
		t.Fatalf("expected suspended restored task: %+v", m.Download)
	}
	m, fx = state.Reduce(m, state.Event{Kind: state.DownloadResume})
	if len(fx) != 1 || fx[0].Kind != state.EffectDownloadRun {
		t.Fatalf("resume effects = %+v", fx)
	}
	wantTotal := cachedObj.Size + remainingObj.Size
	var sawInitialCached, sawCompleted bool
	_ = base
	doneEv := b.HandleDownload(ctx, launcher.DownloadRequest{Root: root, Kind: m.Download.Kind, Effect: fx[0]}, func(progress state.Event) {
		m, _ = state.Reduce(m, progress)
		if m.Download.Stage == state.StageNetwork && m.Download.Total == wantTotal {
			if m.Download.Downloaded >= cachedObj.Size {
				sawInitialCached = true
			}
			if m.Download.Downloaded == wantTotal {
				sawCompleted = true
			}
		}
	})
	if doneEv.Kind != state.DownloadVerified {
		t.Fatalf("resumed run = %+v", doneEv)
	}
	if !sawInitialCached || !sawCompleted {
		t.Fatalf("resumed StageNetwork progress did not include cached + remaining bytes: initial=%v completed=%v final=%+v", sawInitialCached, sawCompleted, m.Download)
	}
}
