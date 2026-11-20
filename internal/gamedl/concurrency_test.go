// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameres"
)

type requestAdmission struct {
	object  string
	release chan struct{}
}

func admissionFixture(t *testing.T) (*gamedl.Engine, gamedl.Task, <-chan requestAdmission) {
	t.Helper()
	events := make(chan requestAdmission, 64)
	objects := map[string][]byte{}
	task := gamedl.Task{ID: "concurrency", Kind: gamedl.TaskPreloadFiles, ListHash: strings.Repeat("a", 32)}
	for i := 0; i < 4; i++ {
		data := bytes.Repeat([]byte{byte(i + 1)}, 4<<20)
		obj := identity(data)
		objects[obj.MD5] = data
		task.Transfers = append(task.Transfers, gamedl.Transfer{Object: obj, Source: gameres.Source{Kind: gameres.SourceCAS, Object: obj}})
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/")
		ev := requestAdmission{key, make(chan struct{})}
		select {
		case events <- ev:
		case <-r.Context().Done():
			return
		}
		select {
		case <-ev.release:
		case <-r.Context().Done():
			return
		}
		http.ServeContent(w, r, "fixture", zeroTime, bytes.NewReader(objects[key]))
	}))
	t.Cleanup(server.Close)
	e, err := gamedl.New(gamedl.Options{DB: testDB(t), WorkRoot: t.TempDir(), StateRoot: t.TempDir(), Client: server.Client(), Resolve: func(_ context.Context, s gameres.Source) ([]string, error) {
		return []string{server.URL + "/" + s.Object.MD5}, nil
	}, Parameters: gamedl.Parameters{ChunkSize: 1 << 20, FilesInFlight: 4, ConnectionsPerFile: 4}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e, task, events
}
func nextAdmission(t *testing.T, events <-chan requestAdmission) requestAdmission {
	t.Helper()
	select {
	case ev := <-events:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("request admission did not wake")
		return requestAdmission{}
	}
}
func noAdmission(t *testing.T, events <-chan requestAdmission) {
	t.Helper()
	select {
	case ev := <-events:
		t.Fatalf("unexpected request for %s", ev.object)
	case <-time.After(100 * time.Millisecond):
	}
}
func runAdmissions(t *testing.T, e *gamedl.Engine, task gamedl.Task) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, task, nil) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			requireKind(t, err, gamedl.ErrCancelled)
		case <-time.After(3 * time.Second):
			t.Error("cancel did not wake concurrency waiters")
		}
	})
}
func TestLiveConcurrencyGrowthWakesFileAndConnectionWaiters(t *testing.T) {
	e, task, events := admissionFixture(t)
	if err := e.SetConcurrencyLimits(1, 1); err != nil {
		t.Fatal(err)
	}
	runAdmissions(t, e, task)
	admitted := []requestAdmission{nextAdmission(t, events)}
	noAdmission(t, events)
	if err := e.SetConcurrencyLimits(2, 2); err != nil {
		t.Fatal(err)
	}
	for len(admitted) < 4 {
		admitted = append(admitted, nextAdmission(t, events))
	}
	noAdmission(t, events)
	counts := map[string]int{}
	for _, ev := range admitted {
		counts[ev.object]++
	}
	if len(counts) != 2 {
		t.Fatalf("file admissions %v", counts)
	}
	for _, n := range counts {
		if n != 2 {
			t.Fatalf("connection admissions %v", counts)
		}
	}
}
func TestLiveConcurrencyReductionDrainsExistingRequests(t *testing.T) {
	e, task, events := admissionFixture(t)
	if err := e.SetConcurrencyLimits(2, 3); err != nil {
		t.Fatal(err)
	}
	runAdmissions(t, e, task)
	admitted := make([]requestAdmission, 6)
	for i := range admitted {
		admitted[i] = nextAdmission(t, events)
	}
	noAdmission(t, events)
	if err := e.SetConcurrencyLimits(1, 1); err != nil {
		t.Fatal(err)
	}
	close(admitted[0].release)
	noAdmission(t, events)
	for _, ev := range admitted[1:] {
		close(ev.release)
	}
	next := []requestAdmission{nextAdmission(t, events), nextAdmission(t, events)}
	noAdmission(t, events)
	if next[0].object == next[1].object {
		t.Fatal("reduced connections admitted twice for same file")
	}
	close(next[0].release)
	noAdmission(t, events)
	close(next[1].release)
	last := nextAdmission(t, events)
	noAdmission(t, events)
	if last.object == next[0].object || last.object == next[1].object {
		t.Fatalf("completed file admitted again: %s", last.object)
	}
}
func TestLiveConcurrencyRejectsValuesOutsideConfiguredCeilings(t *testing.T) {
	e, _, _ := admissionFixture(t)
	for _, limits := range [][2]int{{0, 1}, {1, 0}, {5, 1}, {1, 5}} {
		t.Run(fmt.Sprint(limits), func(t *testing.T) { requireKind(t, e.SetConcurrencyLimits(limits[0], limits[1]), gamedl.ErrPathUnsafe) })
	}
}
