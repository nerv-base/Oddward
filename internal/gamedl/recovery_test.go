// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameres"
)

func TestRecoveryDropsCorruptBitmapAndTruncatedPart(t *testing.T) {
	for _, fault := range []string{"crc", "truncated-part", "wrong-length", "wrong-identity"} {
		t.Run(fault, func(t *testing.T) {
			data := bytes.Repeat([]byte("durable"), 450000)
			obj := identity(data)
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				http.ServeContent(w, r, "fixture", zeroTime, bytes.NewReader(data))
			}))
			defer server.Close()
			work, state := t.TempDir(), t.TempDir()
			opts := gamedl.Options{WorkRoot: work, StateRoot: state, Client: server.Client(), Resolve: func(context.Context, gameres.Source) ([]string, error) { return []string{server.URL}, nil }, Parameters: gamedl.Parameters{ChunkSize: 1 << 20, ConnectionsPerFile: 1}}
			task := gamedl.Task{ID: "recovery", Kind: gamedl.TaskPreloadFiles, ListHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Transfers: []gamedl.Transfer{{Object: obj, Source: gameres.Source{Kind: gameres.SourceCAS, Object: obj}}}}
			e, err := gamedl.New(opts)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			err = e.Run(ctx, task, func(p gamedl.Progress) {
				if p.Downloaded >= 1<<20 {
					cancel()
				}
			})
			cancel()
			requireKind(t, err, gamedl.ErrCancelled)
			if err = e.Close(); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(work, "cache", "tmp", "obj-"+obj.String())
			switch fault {
			case "truncated-part":
				if err = os.Truncate(filepath.Join(dir, "part"), 100); err != nil {
					t.Fatal(err)
				}
			case "wrong-length":
				if err = os.WriteFile(filepath.Join(dir, "rank.bm"), []byte("ODWBM1"), 0600); err != nil {
					t.Fatal(err)
				}
			default:
				bm, err := os.ReadFile(filepath.Join(dir, "rank.bm"))
				if err != nil {
					t.Fatal(err)
				}
				if fault == "crc" {
					bm[len(bm)-1] ^= 1
				} else {
					bm[8] ^= 1
				}
				if err = os.WriteFile(filepath.Join(dir, "rank.bm"), bm, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := calls.Load()
			e, err = gamedl.New(opts)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			recovered, err := e.Recover(context.Background(), task.ListHash)
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != before {
				t.Fatal("Recover started network work")
			}
			if len(recovered) != 1 || recovered[0].Transfers[0].BytesDone != 0 || recovered[0].State != gamedl.StatePaused {
				t.Fatalf("untrusted progress reused: %+v", recovered)
			}
			if err = e.Run(context.Background(), recovered[0], nil); err != nil {
				t.Fatal(err)
			}
			assertBlob(t, e, obj, data)
		})
	}
}
func TestTaskStoreRejectsCorruptionAndManifestChange(t *testing.T) {
	for _, fault := range []string{"truncated-task", "changed-manifest"} {
		t.Run(fault, func(t *testing.T) {
			data := []byte("task identity")
			obj := identity(data)
			work, state := t.TempDir(), t.TempDir()
			opts := gamedl.Options{WorkRoot: work, StateRoot: state, Client: &http.Client{}, Resolve: func(context.Context, gameres.Source) ([]string, error) {
				t.Error("recovery made network request")
				return nil, nil
			}}
			task := gamedl.Task{ID: "persisted", Kind: gamedl.TaskPreloadFiles, ListHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Transfers: []gamedl.Transfer{{Object: obj, Source: gameres.Source{Kind: gameres.SourceCAS, Object: obj}}}, State: gamedl.StatePaused}
			store, err := gamedl.NewStore(state)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Save(context.Background(), task); err != nil {
				t.Fatal(err)
			}
			store.Close()
			manifest := task.ListHash
			kind := gamedl.ErrStaleTask
			if fault == "truncated-task" {
				if err = os.WriteFile(filepath.Join(state, "tasks", task.ID+".json"), []byte(`{"schema":2`), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				manifest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
				kind = gamedl.ErrStaleTask
			}
			e, err := gamedl.New(opts)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			_, err = e.Recover(context.Background(), manifest)
			requireKind(t, err, kind)
		})
	}
}
func TestTaskStoreRoundTripsPatchAndTargetIdentitiesWithoutURLs(t *testing.T) {
	store, err := gamedl.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	blob, old, newObject := identity([]byte("blob")), identity([]byte("old")), identity([]byte("new"))
	task := gamedl.Task{ID: "schema", Kind: gamedl.TaskUpdateFiles, ListHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Transfers: []gamedl.Transfer{{Object: blob, Source: gameres.Source{Kind: gameres.SourcePatchBlob, Object: blob}}}, Rebuilds: []gamedl.Rebuild{{Target: newObject, Steps: []gamedl.PatchStep{{Blob: blob, Old: old, New: newObject, Source: gameres.Source{Kind: gameres.SourcePatchBlob, Object: blob}}}, Output: "rebuild/schema/file.new"}}, Targets: []gamedl.InstallTarget{{Path: "Client/Game.exe", Object: newObject, Action: gameres.ActionPatch}}}
	if err = store.Save(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Transfers[0].Object != blob || loaded.Targets[0].Object != newObject || loaded.Rebuilds[0].Steps[0].Old != old {
		t.Fatalf("object roles conflated: %+v", loaded)
	}
	listed, err := store.List(context.Background())
	if err != nil || len(listed) != 1 {
		t.Fatalf("list=%v err=%v", listed, err)
	}
}
