// SPDX-License-Identifier: GPL-3.0-or-later

package gallery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestRealCNSamplesReadOnly verifies discovery, indexing, thumbnail decoding
// and single export against the current machine's real CN client screenshots
// and Pictures/NevernessToEverness exports strictly in read-only mode (writing
// only to t.TempDir()). Skipped unless ODDWARD_VERIFY_REAL_GALLERY=1.
// Logs only sanitized aggregate metrics; never logs raw UIDs or personal paths.
func TestRealCNSamplesReadOnly(t *testing.T) {
	if os.Getenv("ODDWARD_VERIFY_REAL_GALLERY") != "1" {
		t.Skip("set ODDWARD_VERIFY_REAL_GALLERY=1 to run read-only verification against local CN photos")
	}
	ctx := context.Background()
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(cfgDir, "Oddward", "launcher-v1.json"))
	if err != nil {
		t.Skipf("no local launcher-v1.json: %v", err)
	}
	var doc struct {
		GameRoot string `json:"gameRoot"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.GameRoot == "" {
		t.Skip("no configured gameRoot in local settings")
	}

	tmp := t.TempDir()
	db := openTestDB(t)
	svc, err := NewService(db, ServiceOptions{
		ThumbCacheDir:      filepath.Join(tmp, "thumbs"),
		ArchiveDirOverride: filepath.Join(tmp, "archive"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	// Auto-archive stays OFF so we only read the user's real directories.
	rep, _, err := svc.SyncAndScan(ctx, doc.GameRoot)
	if err != nil {
		t.Fatalf("SyncAndScan real CN install: %v", err)
	}
	snap, err := svc.LoadSnapshot(ctx, Filter{})
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if snap.Stats.TotalAssets == 0 {
		t.Skip("no real photos found on this machine")
	}
	if snap.Stats.InvalidCount != 0 {
		t.Fatalf("expected 0 invalid real photos, got %d", snap.Stats.InvalidCount)
	}

	// Verify thumbnail decode + clipboard PNG + isolated export on the first real photo.
	var photoSample, videoSample Asset
	for _, a := range snap.Assets {
		if a.MediaType == MediaImage && photoSample.ID == 0 {
			photoSample = a
		}
		if a.MediaType == MediaVideo && videoSample.ID == 0 {
			videoSample = a
		}
	}
	if photoSample.ID == 0 {
		t.Skip("no real photos found on this machine")
	}
	px, hit, err := svc.DecodeAssetThumbnail(ctx, photoSample, ThumbL1)
	if err != nil || hit || px == nil {
		t.Fatalf("cold thumbnail decode of real photo failed: hit=%v err=%v", hit, err)
	}
	px2, hit2, err := svc.DecodeAssetThumbnail(ctx, photoSample, ThumbL1)
	if err != nil || !hit2 || px2 == nil {
		t.Fatalf("warm thumbnail decode of real photo failed: hit=%v err=%v", hit2, err)
	}
	clipBytes, err := ReadClipboardPNG(ctx, photoSample.PrimaryPath)
	if err != nil || len(clipBytes) == 0 {
		t.Fatalf("ReadClipboardPNG on real photo failed: %v", err)
	}
	exportOut := filepath.Join(tmp, "exported-sample.png")
	if err := ExportAsset(ctx, photoSample, exportOut); err != nil {
		t.Fatalf("ExportAsset on real photo failed: %v", err)
	}
	if sum, _, err := HashFile(ctx, exportOut, MaxImageBytes); err != nil || sum != photoSample.SHA256 {
		t.Fatalf("exported real photo SHA256 mismatch: err=%v", err)
	}

	// Verify real CN FFMpeg video inspection, keyframe thumbnail and isolated export when present.
	if videoSample.ID != 0 {
		if videoSample.Width <= 0 || videoSample.Height <= 0 || videoSample.DurationMS <= 0 {
			t.Fatalf("real video missing parsed metadata: dims=%dx%d dur=%dms codec=%s/%s",
				videoSample.Width, videoSample.Height, videoSample.DurationMS, videoSample.VideoCodec, videoSample.AudioCodec)
		}
		if videoSample.PosterPath != "" {
			vpx, _, verr := svc.DecodeAssetThumbnail(ctx, videoSample, ThumbL1)
			if verr != nil || vpx == nil {
				t.Fatalf("real video keyframe thumbnail decode failed: vpx=%v err=%v", vpx, verr)
			}
		}
		// Pick the smallest real video for fast isolated export verification.
		smallest := videoSample
		for _, a := range snap.Assets {
			if a.MediaType == MediaVideo && a.ByteSize < smallest.ByteSize {
				smallest = a
			}
		}
		vidOut := filepath.Join(tmp, "exported-sample.mp4")
		if err := ExportAsset(ctx, smallest, vidOut); err != nil {
			t.Fatalf("ExportAsset on real video failed: %v", err)
		}
		if sum, _, err := HashFile(ctx, vidOut, MaxVideoBytes); err != nil || sum != smallest.SHA256 {
			t.Fatalf("exported real video SHA256 mismatch: err=%v", err)
		}
	}

	t.Logf("Real CN read-only verification PASSED: sources=%d total=%d photos=%d videos=%d cached=%d exported=%d distinct_uids=%d unknown_uid=%v photo_dims=%dx%d video_dims=%dx%d video_dur_ms=%d video_codec=%s/%s video_has_poster=%v",
		rep.SourcesScanned, snap.Stats.TotalAssets, snap.Stats.PhotoCount, snap.Stats.VideoCount,
		snap.Stats.CachedCount, snap.Stats.ExportedCount, len(snap.UIDs), snap.HasUnknownUID,
		photoSample.Width, photoSample.Height, videoSample.Width, videoSample.Height,
		videoSample.DurationMS, videoSample.VideoCodec, videoSample.AudioCodec, videoSample.PosterPath != "")
}
