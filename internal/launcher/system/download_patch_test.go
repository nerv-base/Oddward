// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database/dbtest"
	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameres"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"golang.org/x/sys/windows"
)

func adapterObject(data []byte) gameres.Object {
	return gameres.Object{MD5: fmt.Sprintf("%x", md5.Sum(data)), Size: int64(len(data))}
}
func adapterCAS(id gameres.Object) gameres.Source {
	return gameres.Source{Kind: gameres.SourceCAS, Object: id, Size: id.Size}
}

type patchAdapterHarness struct {
	b        *GameDownloadBackend
	p        *downloadPlan
	e        *gamedl.Engine
	mu       sync.Mutex
	data     map[string][]byte
	requests []string
}

func newPatchAdapterHarness(t *testing.T) *patchAdapterHarness {
	t.Helper()
	h := &patchAdapterHarness{data: map[string][]byte{}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.requests = append(h.requests, r.URL.Path)
		data, ok := h.data[strings.TrimPrefix(r.URL.Path, "/")]
		h.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, "fixture", time.Time{}, bytes.NewReader(data))
	}))
	t.Cleanup(server.Close)
	h.p = &downloadPlan{root: t.TempDir(), workRoot: t.TempDir(), id: strings.Repeat("a", 32), key: "synthetic-install", kind: state.DownloadUpdate, margin: 32}
	h.p.delivery.Branch = "fixture"
	h.p.delivery.Config.ResVersion = "next"
	h.p.delivery.Config.ListHash = strings.Repeat("b", 32)
	db := dbtest.Open(t)
	h.b = &GameDownloadBackend{DB: db, free: func(string) (int64, error) { return math.MaxInt64, nil }}
	var err error
	h.e, err = gamedl.New(gamedl.Options{DB: db, WorkRoot: h.p.engineRoot(), StateRoot: t.TempDir(), Client: server.Client(), Resolve: func(_ context.Context, s gameres.Source) ([]string, error) {
		return []string{server.URL + "/" + s.Object.MD5}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.e.Close() })
	return h
}
func (h *patchAdapterHarness) seed(t *testing.T, data []byte) gameres.Object {
	t.Helper()
	id := adapterObject(data)
	part := "cache/tmp/seed-" + id.MD5
	if err := os.WriteFile(filepath.Join(h.p.engineRoot(), filepath.FromSlash(part)), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.e.Publish(context.Background(), id, part); err != nil {
		t.Fatal(err)
	}
	return id
}
func (h *patchAdapterHarness) serve(data []byte) gameres.Object {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := adapterObject(data)
	h.data[id.MD5] = data
	return id
}
func (h *patchAdapterHarness) requestCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.requests)
}
func (h *patchAdapterHarness) assertBlob(t *testing.T, data []byte) {
	t.Helper()
	f, err := h.e.OpenBlob(context.Background(), adapterObject(data))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	actual, err := io.ReadAll(f)
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatalf("blob mismatch: %v", err)
	}
}
func adapterTarget(path string, old, blob, new gameres.Object) gameres.Target {
	return gameres.Target{Path: path, MD5: new.MD5, Size: new.Size, Action: gameres.ActionPatch, Route: &gameres.Route{Source: old, Target: new, Steps: []gameres.PatchEntry{{Old: old, Blob: blob, New: new}}}, Fallback: &gameres.Fallback{Action: gameres.ActionFull, Full: adapterCAS(new)}}
}

func TestPatchDownloadFallbackScopesAndDeduplicates(t *testing.T) {
	h := newPatchAdapterHarness(t)
	old := adapterObject([]byte("old"))
	good := h.seed(t, []byte("verified patch blob"))
	missing := adapterObject([]byte("missing patch blob"))
	kept := adapterObject([]byte("must rebuild this locally"))
	fullData := []byte("shared full fallback")
	full := h.serve(fullData)
	h.p.plan.Targets = []gameres.Target{adapterTarget("Client/good", old, good, kept), adapterTarget("Client/bad-one", old, missing, full), adapterTarget("Client/bad-two", old, missing, full), {Path: "Client/full", MD5: full.MD5, Size: full.Size, Action: gameres.ActionFull, Full: adapterCAS(full)}}
	fallback, err := h.b.patchDownloadFallback(context.Background(), h.p, h.e, nil, &gamedl.TaskError{Kind: gamedl.ErrHTTP})
	if err != nil || !fallback {
		t.Fatalf("fallback=%v err=%v", fallback, err)
	}
	h.assertBlob(t, fullData)
	if n := h.requestCount(); n != 1 {
		t.Fatalf("expected one deduplicated Full request, got %d", n)
	}
	if _, err := h.e.OpenBlob(context.Background(), kept); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("valid patch target was downloaded: %v", err)
	}
	tasks, err := h.e.Store().List(context.Background())
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%v err=%v", tasks, err)
	}
	if task := tasks[0]; task.Purpose != "game-resource-child" || !strings.HasPrefix(task.ID, h.p.id+"-") || len(task.ID) != 57 || len(task.Transfers) != 1 {
		t.Fatalf("invalid child task: %+v", task)
	}
}

func TestPatchDownloadFallbackPreservesValidRoutesAndUsesCurrentBudget(t *testing.T) {
	h := newPatchAdapterHarness(t)
	old := adapterObject([]byte("old"))
	patch := h.seed(t, []byte("good cached patch"))
	newID := adapterObject([]byte("local output"))
	h.p.plan.Targets = []gameres.Target{adapterTarget("Client/good", old, patch, newID)}
	h.p.need = 100000
	h.b.free = func(string) (int64, error) { return h.p.margin + newID.Size, nil }
	fallback, err := h.b.patchDownloadFallback(context.Background(), h.p, h.e, nil, &gamedl.TaskError{Kind: gamedl.ErrNetwork})
	if err != nil || fallback || h.requestCount() != 0 {
		t.Fatalf("fallback=%v err=%v requests=%d", fallback, err, h.requestCount())
	}
}

func TestPatchDownloadFallbackOrdinaryPakUsesCAS(t *testing.T) {
	h := newPatchAdapterHarness(t)
	data := []byte("full CAS alternate")
	id := h.serve(data)
	h.p.plan.Targets = []gameres.Target{{Path: "Client/full", MD5: id.MD5, Size: id.Size, Action: gameres.ActionFull, Full: gameres.Source{Kind: gameres.SourcePakRange, Object: adapterObject([]byte("gone pak")), Size: id.Size}, Fallback: &gameres.Fallback{Action: gameres.ActionFull, Full: adapterCAS(id)}}}
	fallback, err := h.b.patchDownloadFallback(context.Background(), h.p, h.e, nil, &gamedl.TaskError{Kind: gamedl.ErrHTTP})
	if err != nil || fallback {
		t.Fatalf("patch fallback=%v err=%v", fallback, err)
	}
	h.assertBlob(t, data)
}

func TestPatchDownloadFallbackFatalErrorDoesNotStartNetwork(t *testing.T) {
	for _, fatal := range []error{windows.ERROR_DISK_FULL, os.ErrPermission, &gamedl.TaskError{Kind: gamedl.ErrDiskFull}, &gamedl.TaskError{Kind: gamedl.ErrPermission}, &gamedl.TaskError{Kind: gamedl.ErrPathUnsafe}} {
		t.Run(fmt.Sprint(fatal), func(t *testing.T) {
			h := newPatchAdapterHarness(t)
			old := adapterObject([]byte("old"))
			id := h.serve([]byte("new"))
			h.p.plan.Targets = []gameres.Target{adapterTarget("Client/one", old, adapterObject([]byte("missing")), id)}
			fallback, err := h.b.patchDownloadFallback(context.Background(), h.p, h.e, nil, fatal)
			if !errors.Is(err, fatal) || fallback || h.requestCount() != 0 {
				t.Fatalf("fallback=%v err=%v requests=%d", fallback, err, h.requestCount())
			}
		})
	}
}

// This original fixture emits only literal HDIFF13 output. No private data or reference code is used.
func adapterPacked(n uint64, tagBits uint) []byte {
	var tail []byte
	for n >= uint64(1)<<(7-tagBits) {
		tail = append(tail, byte(n&127))
		n >>= 7
	}
	first := byte(n)
	if len(tail) > 0 {
		first |= 1 << (7 - tagBits)
	}
	out := []byte{first}
	for i := len(tail) - 1; i >= 0; i-- {
		b := tail[i]
		if i > 0 {
			b |= 128
		}
		out = append(out, b)
	}
	return out
}
func adapterLiteral(old, new []byte) []byte {
	out := []byte("HDIFF13&\x00")
	for _, n := range []int{len(new), len(old), 0} {
		out = append(out, adapterPacked(uint64(n), 0)...)
	}
	var rle []byte
	if len(new) > 0 {
		rle = adapterPacked(uint64(len(new)-1), 2)
	}
	for _, n := range []int{0, len(rle), 0, len(new)} {
		out = append(out, adapterPacked(uint64(n), 0)...)
		out = append(out, 0)
	}
	out = append(out, rle...)
	return append(out, new...)
}
func adapterAppendBytePatch(oldSize int, suffix byte) []byte {
	out := []byte("HDIFF13&\x00")
	for _, n := range []int{oldSize + 1, oldSize, 1} {
		out = append(out, adapterPacked(uint64(n), 0)...)
	}
	cover := append([]byte{0, 0}, adapterPacked(uint64(oldSize), 0)...)
	control := adapterPacked(uint64(oldSize), 2)
	for _, n := range []int{len(cover), len(control), 0, 1} {
		out = append(out, adapterPacked(uint64(n), 0)...)
		out = append(out, 0)
	}
	out = append(out, cover...)
	out = append(out, control...)
	return append(out, suffix)
}

func (h *patchAdapterHarness) patchTarget(t *testing.T, path string, old, new []byte) gameres.Target {
	t.Helper()
	blob := h.seed(t, adapterLiteral(old, new))
	target := adapterTarget(path, adapterObject(old), blob, adapterObject(new))
	name := filepath.Join(h.p.root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, old, 0600); err != nil {
		t.Fatal(err)
	}
	return target
}
func TestApplyDownloadPatchesProgressAndReuse(t *testing.T) {
	h := newPatchAdapterHarness(t)
	old, mid, newData := []byte("old"), []byte("intermediate"), bytes.Repeat([]byte("N"), 2<<20)
	first := h.patchTarget(t, "Client/first", old, mid)
	secondBlob := h.seed(t, adapterLiteral(mid, newData))
	first.MD5, first.Size = adapterObject(newData).MD5, int64(len(newData))
	first.Route.Target = adapterObject(newData)
	first.Route.Steps = append(first.Route.Steps, gameres.PatchEntry{Old: adapterObject(mid), Blob: secondBlob, New: adapterObject(newData)})
	first.Fallback.Full = adapterCAS(adapterObject(newData))
	duplicate := first
	duplicate.Path = "Client/second"
	h.p.plan.Targets = []gameres.Target{first, duplicate}
	var events []state.Event
	started := time.Now()
	fallback, err := h.b.applyDownloadPatches(context.Background(), h.p, h.e, func(e state.Event) { events = append(events, e) })
	elapsed := time.Since(started)
	if err != nil || fallback {
		t.Fatalf("fallback=%v err=%v", fallback, err)
	}
	h.assertBlob(t, newData)
	var written int64
	var done uint16
	if limit := 6 + int(elapsed/(125*time.Millisecond)); len(events) > limit {
		t.Fatalf("rebuild reported %d progress events in %s, upper bound %d", len(events), elapsed, limit)
	}
	for _, event := range events {
		if event.Kind != state.ApplyProgress {
			continue
		}
		if event.Stage != state.StageApply || event.Downloaded < written || event.FilesDone < done || event.Rate < 0 || event.ETASecs < 0 || event.Downloaded > event.Total {
			t.Fatalf("untruthful progress %+v", event)
		}
		written, done = event.Downloaded, event.FilesDone
	}
	if written != int64(len(mid)+len(newData)) || done != 2 {
		t.Fatalf("written=%d done=%d events=%+v", written, done, events)
	}
	entries, err := os.ReadDir(filepath.Join(h.p.engineRoot(), "cache", "tmp"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("rebuild leftovers: %v %v", entries, err)
	}
	actual, err := os.ReadFile(filepath.Join(h.p.root, "Client", "first"))
	if err != nil || !bytes.Equal(actual, old) {
		t.Fatal("source changed")
	}
}
func TestApplyDownloadPatchesFallbackReasons(t *testing.T) {
	for _, sourceMismatch := range []bool{false, true} {
		t.Run(fmt.Sprint(sourceMismatch), func(t *testing.T) {
			h := newPatchAdapterHarness(t)
			old, newData := []byte("old"), []byte("new target")
			target := h.patchTarget(t, "Client/target", old, newData)
			h.serve(newData)
			if sourceMismatch {
				if err := os.WriteFile(filepath.Join(h.p.root, "Client", "target"), []byte("bad"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				target.Route.Steps[0].Blob = h.seed(t, []byte("unsupported patch"))
			}
			h.p.plan.Targets = []gameres.Target{target}
			var notices []state.Event
			fallback, err := h.b.applyDownloadPatches(context.Background(), h.p, h.e, func(e state.Event) {
				if e.Kind == state.DownloadFallbackNotice {
					notices = append(notices, e)
				}
			})
			if err != nil || !fallback || len(notices) != 1 {
				t.Fatalf("fallback=%v err=%v notices=%+v", fallback, err, notices)
			}
			if (notices[0].Body == "patch-source-mismatch") != sourceMismatch {
				t.Fatalf("wrong notice: %+v", notices[0])
			}
			h.assertBlob(t, newData)
		})
	}
}
func TestApplyDownloadPatchesRefusesInsufficientRebuildSpace(t *testing.T) {
	h := newPatchAdapterHarness(t)
	target := h.patchTarget(t, "Client/target", []byte("old"), []byte("new target"))
	h.p.plan.Targets = []gameres.Target{target}
	h.b.free = func(string) (int64, error) { return h.p.margin + target.Size - 1, nil }
	fallback, err := h.b.applyDownloadPatches(context.Background(), h.p, h.e, nil)
	if !errors.Is(err, gameres.ErrInsufficientSpace) || fallback || h.requestCount() != 0 {
		t.Fatalf("fallback=%v err=%v requests=%d", fallback, err, h.requestCount())
	}
}
func TestApplyOneDownloadPatchPublicationFailureCleansDirectory(t *testing.T) {
	h := newPatchAdapterHarness(t)
	target := h.patchTarget(t, "Client/target", []byte("old"), []byte("new"))
	target.MD5 = adapterObject([]byte("wrong target")).MD5
	if err := h.b.applyOneDownloadPatch(context.Background(), h.p, h.e, target, nil); err == nil {
		t.Fatal("accepted output mismatch")
	}
	entries, err := os.ReadDir(filepath.Join(h.p.engineRoot(), "cache", "tmp"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("rebuild leftovers: %v %v", entries, err)
	}
}
func TestPatchDownloadFallbackSharedFullObjectAvoidsDuplicateRebuildBudget(t *testing.T) {
	h := newPatchAdapterHarness(t)
	data := []byte("one shared output")
	id := h.serve(data)
	patch := h.seed(t, []byte("verified patch blob"))
	h.p.plan.Targets = []gameres.Target{adapterTarget("Client/patch", adapterObject([]byte("old")), patch, id), {Path: "Client/full", MD5: id.MD5, Size: id.Size, Action: gameres.ActionFull, Full: adapterCAS(id)}}
	h.b.free = func(string) (int64, error) { return h.p.margin + id.Size, nil }
	fallback, err := h.b.patchDownloadFallback(context.Background(), h.p, h.e, nil, &gamedl.TaskError{Kind: gamedl.ErrNetwork})
	if err != nil || fallback {
		t.Fatalf("fallback=%v err=%v", fallback, err)
	}
	h.assertBlob(t, data)
}

func TestPatchDownloadFallbackCountsIntermediatePairAndOnlyRemainingBytes(t *testing.T) {
	h := newPatchAdapterHarness(t)
	old, mid, final := adapterObject([]byte("old")), adapterObject(bytes.Repeat([]byte("M"), 100)), adapterObject(bytes.Repeat([]byte("F"), 20))
	first, second := h.seed(t, []byte("cached first patch")), h.seed(t, []byte("cached second patch"))
	good := adapterTarget("Client/good", old, first, mid)
	good.MD5, good.Size = final.MD5, final.Size
	good.Route.Target = final
	good.Route.Steps = append(good.Route.Steps, gameres.PatchEntry{Old: mid, Blob: second, New: final})
	fullData := []byte("fallback")
	full := h.serve(fullData)
	h.p.plan.Targets = []gameres.Target{good, adapterTarget("Client/missing", old, adapterObject([]byte("absent patch")), full)}
	need := h.p.margin + full.Size + mid.Size + final.Size
	h.b.free = func(string) (int64, error) { return need - 1, nil }
	if _, err := h.b.patchDownloadFallback(context.Background(), h.p, h.e, nil, &gamedl.TaskError{Kind: gamedl.ErrHTTP}); !errors.Is(err, gameres.ErrInsufficientSpace) || h.requestCount() != 0 {
		t.Fatalf("err=%v requests=%d", err, h.requestCount())
	}
	h.p.need = math.MaxInt64
	h.b.free = func(string) (int64, error) { return need, nil }
	if fallback, err := h.b.patchDownloadFallback(context.Background(), h.p, h.e, nil, &gamedl.TaskError{Kind: gamedl.ErrHTTP}); err != nil || !fallback {
		t.Fatalf("fallback=%v err=%v", fallback, err)
	}
	h.assertBlob(t, fullData)
}

func TestApplyDownloadPatchesStreamingCancellationPreservesSource(t *testing.T) {
	h := newPatchAdapterHarness(t)
	old, newData := []byte("immutable old"), bytes.Repeat([]byte("N"), 3<<20)
	h.p.plan.Targets = []gameres.Target{h.patchTarget(t, "Client/target", old, newData)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fallback, err := h.b.applyDownloadPatches(ctx, h.p, h.e, func(e state.Event) {
		if e.Kind == state.ApplyProgress && e.Downloaded > 0 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || fallback || h.requestCount() != 0 {
		t.Fatalf("fallback=%v err=%v requests=%d", fallback, err, h.requestCount())
	}
	actual, err := os.ReadFile(filepath.Join(h.p.root, "Client", "target"))
	if err != nil || !bytes.Equal(actual, old) {
		t.Fatal("source changed")
	}
	entries, err := os.ReadDir(filepath.Join(h.p.engineRoot(), "cache", "tmp"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("rebuild leftovers: %v %v", entries, err)
	}
	if _, err = h.e.OpenBlob(context.Background(), adapterObject(newData)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled output published: %v", err)
	}
}

func TestApplyDownloadPatchesFullPakFailureUsesAlternateCAS(t *testing.T) {
	h := newPatchAdapterHarness(t)
	old, newData := []byte("old"), []byte("new output")
	target := h.patchTarget(t, "Client/target", old, newData)
	target.Route.Steps[0].Blob = h.seed(t, []byte("unsupported patch"))
	final := h.serve(newData)
	pak := adapterObject(bytes.Repeat([]byte("P"), len(newData)+5))
	target.Fallback.Full = gameres.Source{Kind: gameres.SourcePakRange, Object: pak, Offset: 2, Size: final.Size}
	target.Fallback.Alternate = adapterCAS(final)
	h.p.plan.Targets = []gameres.Target{target}
	fallback, err := h.b.applyDownloadPatches(context.Background(), h.p, h.e, nil)
	if err != nil || !fallback || h.requestCount() != 2 {
		t.Fatalf("fallback=%v err=%v requests=%d", fallback, err, h.requestCount())
	}
	h.assertBlob(t, newData)
	tasks, err := h.e.Store().List(context.Background())
	if err != nil || len(tasks) != 2 || tasks[0].ID == tasks[1].ID {
		t.Fatalf("child source identities conflated: %+v %v", tasks, err)
	}
	if err = retirePatchDownloads(context.Background(), h.p, h.e); err != nil {
		t.Fatal(err)
	}
	tasks, err = h.e.Store().List(context.Background())
	if err != nil || len(tasks) != 0 {
		t.Fatalf("child tasks retained: %+v %v", tasks, err)
	}
}

func TestPatchDownloadChildDescriptorSurvivesRetry(t *testing.T) {
	h := newPatchAdapterHarness(t)
	data := []byte("retry same object")
	id := h.serve(data)
	choice := patchTransferChoice{transfer: gamedl.Transfer{Object: id, Source: adapterCAS(id)}}
	for range 2 {
		if err := h.b.runPatchDownload(context.Background(), h.p, h.e, choice); err != nil {
			t.Fatal(err)
		}
	}
	tasks, err := h.e.Store().List(context.Background())
	if err != nil || len(tasks) != 1 || h.requestCount() != 1 {
		t.Fatalf("tasks=%+v requests=%d err=%v", tasks, h.requestCount(), err)
	}
}

func TestRetirePatchDownloadsBeforeMetadataReload(t *testing.T) {
	h := newPatchAdapterHarness(t)
	task := h.p.transferTask()
	task.ID += "-owned"
	task.Purpose = patchChildPurpose
	if err := h.e.Store().Save(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	h.p.delivery.Branch = ""
	h.p.delivery.Config.ResVersion = ""
	h.p.delivery.Config.ListHash = ""
	if err := retirePatchDownloads(context.Background(), h.p, h.e); err != nil {
		t.Fatal(err)
	}
	tasks, err := h.e.Store().List(context.Background())
	if err != nil || len(tasks) != 0 {
		t.Fatalf("owned children require metadata reload: %+v %v", tasks, err)
	}
}

func TestRetirePatchDownloadsUsesParentAndPurpose(t *testing.T) {
	h := newPatchAdapterHarness(t)
	ctx := context.Background()
	base := h.p.transferTask()
	base.Transfers = nil
	base.Targets = nil
	base.Rebuilds = nil
	var keep []string
	for _, entry := range []struct {
		id, purpose, key string
		remove           bool
	}{{h.p.id + "-own", "game-resource-child", h.p.key, true}, {h.p.id + "-other-purpose", "game-resources", h.p.key, false}, {"other-parent", "game-resource-child", h.p.key, false}, {h.p.id + "-other-key", "game-resource-child", "other-install", false}} {
		task := base
		task.ID, task.Purpose, task.InstallKey = entry.id, entry.purpose, entry.key
		if err := h.e.Store().Save(ctx, task); err != nil {
			t.Fatal(err)
		}
		if !entry.remove {
			keep = append(keep, task.ID)
		}
	}
	if err := retirePatchDownloads(ctx, h.p, h.e); err != nil {
		t.Fatal(err)
	}
	tasks, err := h.e.Store().List(ctx)
	if err != nil || len(tasks) != len(keep) {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
	for _, id := range keep {
		if _, err := h.e.Store().Load(ctx, id); err != nil {
			t.Fatalf("unrelated task removed: %s: %v", id, err)
		}
	}
}
