// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameres"
)

// perfCopyPatch is an original uncompressed HDIFF13 fixture: copy the full old
// source and append a literal suffix. It contains no game or official patch data.
func perfCopyPatch(old, suffix []byte) []byte {
	covers := append([]byte{0, 0}, adapterPacked(uint64(len(old)), 0)...)
	controls := adapterPacked(uint64(len(old)+len(suffix)-1), 2)
	sections := [][]byte{covers, controls, nil, suffix}
	out := []byte("HDIFF13&\x00")
	for _, n := range []int{len(old) + len(suffix), len(old), 1} {
		out = append(out, adapterPacked(uint64(n), 0)...)
	}
	for _, section := range sections {
		out = append(out, adapterPacked(uint64(len(section)), 0)...)
		out = append(out, 0)
	}
	for _, section := range sections {
		out = append(out, section...)
	}
	return out
}

// This opt-in measurement exercises the actual planner, TLS download engine,
// MD5 verification, and adapter applier using small disposable synthetic data.
// It deliberately does not commit an installation or access external servers.
func TestPerfSyntheticDownloadBenchmarks(t *testing.T) {
	if os.Getenv("ODDWARD_SYNTHETIC_DOWNLOAD_BENCH") != "1" {
		t.Skip("opt-in synthetic measurement")
	}
	for _, name := range []string{"B1", "B2", "B3"} {
		t.Run(name, func(t *testing.T) {
			h := newPatchAdapterHarness(t)
			ctx := context.Background()
			old := make([]byte, 1<<20)
			for i := range old {
				old[i] = byte((i*17 + i/251) % 251)
			}
			suffix := []byte("synthetic-next-version")
			targetData := append(append([]byte{}, old...), suffix...)
			oldID, targetID := adapterObject(old), h.serve(targetData)
			patchData := perfCopyPatch(old, suffix)
			patchID := h.serve(patchData)
			path := "Client/WindowsNoEditor/a.bin"
			if err := os.MkdirAll(filepath.Dir(filepath.Join(h.p.root, filepath.FromSlash(path))), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(h.p.root, filepath.FromSlash(path)), old, 0600); err != nil {
				t.Fatal(err)
			}
			manifest := &gameres.ResList{Tag: "baseTag", CheckHash: "next", Files: []gameres.File{{Path: path, MD5: targetID.MD5, Size: targetID.Size}}}
			graph := &gameres.PatchGraph{Edges: []gameres.PatchEntry{{Old: oldID, New: targetID, Blob: patchID, Version: "next"}}}
			local := downloadLocal{installed: map[string]gameres.Object{strings.ToUpper(path): oldID}, cached: map[gameres.Object]bool{}}
			wantNetwork := patchID.Size
			if name == "B2" {
				wantNetwork = targetID.Size
			}
			if name == "B3" {
				fullData := bytes.Repeat([]byte("second full fixture\n"), 55000)
				fullID := h.serve(fullData)
				manifest.Files = append(manifest.Files, gameres.File{Path: "Client/WindowsNoEditor/b.bin", MD5: fullID.MD5, Size: fullID.Size})
				wantNetwork += fullID.Size
			}
			rates := PerfProfile{}.Rates("")
			plan, err := gameres.BuildPlan(manifest, graph, local, gameres.Capabilities{PatchApply: name != "B2"}, gameres.AvailabilitySnapshot{}, gameres.BudgetInput{VolumeFreeBytes: math.MaxInt64, CacheFreeBytes: math.MaxInt64, ReserveBytes: 1, NetBytesPerSecond: rates.NetBytesPerSecond, IOBytesPerSecond: rates.IOBytesPerSecond, VerifyBytesPerSecond: rates.HashBytesPerSecond})
			if err != nil {
				t.Fatal(err)
			}
			h.p.plan = plan
			if name == "B2" && plan.Targets[0].Action != gameres.ActionFull || name != "B2" && plan.Targets[0].Action != gameres.ActionPatch {
				t.Fatalf("wrong primary route: %+v", plan)
			}
			if name == "B3" && (len(plan.Targets) != 2 || plan.Targets[1].Action != gameres.ActionFull || plan.Targets[1].Reason == "") {
				t.Fatalf("mixed route or fallback reason missing: %+v", plan)
			}
			task := h.p.transferTask()
			totalStart := time.Now()
			downloadStart := time.Now()
			var final gamedl.Progress
			if err = h.e.Run(ctx, task, func(p gamedl.Progress) { final = p }); err != nil {
				t.Fatal(err)
			}
			downloadDuration := time.Since(downloadStart)
			if final.NetworkBytes != wantNetwork {
				t.Fatalf("network=%d want=%d (unused patch or duplicate download)", final.NetworkBytes, wantNetwork)
			}
			applyStart := time.Now()
			fallback, err := h.b.applyDownloadPatches(ctx, h.p, h.e, nil)
			applyDuration := time.Since(applyStart)
			if err != nil || fallback {
				t.Fatalf("apply fallback=%v err=%v", fallback, err)
			}
			verifyStart := time.Now()
			for _, target := range plan.Targets {
				f, err := h.e.OpenBlob(ctx, gameres.Object{MD5: target.MD5, Size: target.Size})
				if err != nil {
					t.Fatal(err)
				}
				if err = f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			verifyDuration := time.Since(verifyStart)
			totalDuration := time.Since(totalStart)
			t.Logf("%s synthetic actual NetworkDownloadBytes=%d EngineWrittenBytes=%d PlanPeakAllocBytes=%d DownloadWallTime=%s VerificationWallTime=%s ApplyWallTime=%s StagedTotalWallTime=%s FailureFallbacks=0 PatchBytes=%d FullBytes=%d Targets=%d", name, final.NetworkBytes, final.WrittenBytes, plan.PeakAllocBytes, downloadDuration, verifyDuration, applyDuration, totalDuration, plan.PatchBytes, plan.FullBytes, len(plan.Targets))
			for _, target := range plan.Targets {
				t.Logf("route=%d reason=%q", target.Action, target.Reason)
			}
			actual, err := os.ReadFile(filepath.Join(h.p.root, filepath.FromSlash(path)))
			if err != nil || !bytes.Equal(actual, old) {
				t.Fatal("installed old source changed")
			}
			if name == "B2" && h.requestCount() != 1 {
				t.Fatalf("Full fetched unused patch: requests=%d", h.requestCount())
			}
			if name == "B1" && h.requestCount() != 1 {
				t.Fatalf("Patch fetched Full: requests=%d", h.requestCount())
			}
			if name == "B3" && h.requestCount() != 2 {
				t.Fatalf("Mixed downloaded unused object: requests=%d", h.requestCount())
			}
		})
	}
}
