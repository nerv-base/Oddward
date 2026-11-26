// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nerv-base/Oddward/internal/database/dbtest"
	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameinstall"
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
