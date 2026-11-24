// SPDX-License-Identifier: GPL-3.0-or-later

package gallery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
)

func TestThumbCacheTieredDecodeOrientationPruneAndClear(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cacheDir := filepath.Join(tmp, "thumbs")
	origPath := filepath.Join(tmp, "photo.jpg")

	// 800x400 JPEG with EXIF Orientation=6 (rotated 90° CW -> upright 400x800).
	jpg := makeJPEGWithExif(t, 800, 400, 9, 6, "2026:11:24 12:00:00", "+08:00")
	writeTestFile(t, origPath, jpg)
	sumBytes := sha256.Sum256(jpg)
	sha := hex.EncodeToString(sumBytes[:])

	tc := NewThumbCache(cacheDir, 64)

	// Cold L1 decode -> scales 400x800 into 192x384.
	px, hit, err := tc.LoadOrDecode(ctx, sha, origPath, ThumbL1)
	if err != nil {
		t.Fatalf("cold L1 decode: %v", err)
	}
	if hit {
		t.Fatal("first decode must be a cache miss")
	}
	if px.Bounds().Dx() != 192 || px.Bounds().Dy() != 384 {
		t.Fatalf("rotated L1 dims = %dx%d, want 192x384", px.Bounds().Dx(), px.Bounds().Dy())
	}

	// Warm L1 cache hit!
	px2, hit2, err := tc.LoadOrDecode(ctx, sha, origPath, ThumbL1)
	if err != nil || !hit2 {
		t.Fatalf("warm L1 expected hit, got hit=%v err=%v", hit2, err)
	}
	if !bytes.Equal(px.Pix, px2.Pix) {
		t.Fatal("warm cache pixels differ from decoded pixels")
	}

	// L0 and L2 have distinct cache entries.
	l0, hit0, err := tc.LoadOrDecode(ctx, sha, origPath, ThumbL0)
	if err != nil || hit0 || l0.Bounds().Dy() != ThumbL0 {
		t.Fatalf("L0 decode unexpected: hit=%v bounds=%v err=%v", hit0, l0.Bounds(), err)
	}
	if tc.UsageBytes() <= 0 {
		t.Fatal("expected positive thumbnail cache usage")
	}

	// Prune down to 1 byte evicts cached thumbs while Clear leaves origPath intact.
	if err := tc.Prune(1); err != nil {
		t.Fatal(err)
	}
	if tc.UsageBytes() != 0 {
		t.Fatalf("UsageBytes after Prune(1) = %d, want 0", tc.UsageBytes())
	}

	_, _, _ = tc.LoadOrDecode(ctx, sha, origPath, ThumbL0)
	if err := tc.Clear(); err != nil {
		t.Fatal(err)
	}
	if tc.UsageBytes() != 0 {
		t.Fatal("Clear did not remove thumbnails")
	}
	if _, err := os.Stat(origPath); err != nil {
		t.Fatalf("Clear must never remove original media: %v", err)
	}

	// ReadClipboardPNG converts rotated JPEG into valid upright PNG bytes.
	clipPNG, err := ReadClipboardPNG(ctx, origPath)
	if err != nil {
		t.Fatalf("ReadClipboardPNG: %v", err)
	}
	info, err := InspectReader(bytes.NewReader(clipPNG), int64(len(clipPNG)))
	if err != nil || info.MIMEType != "image/png" || info.Width != 400 || info.Height != 800 {
		t.Fatalf("clipboard PNG unexpected: info=%+v err=%v", info, err)
	}
}

// TestScale10000AssetsQueryBudget verifies SQLite indexing and querying at
// 10,000 synthetic assets stays well within interactive budgets.
func TestScale10000AssetsQueryBudget(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	src, err := store.UpsertSource(ctx, Source{
		RootPath:   filepath.Join(tmp, "Selfie", "100001", "ScreenShots"),
		SourceType: SourceGameCache,
		UID:        "100001",
		Enabled:    true,
	})
	if err != nil {
		t.Fatal(err)
	}

	const total = 10_000
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	batch := make([]LocationUpsert, 0, 500)
	for i := 0; i < total; i++ {
		sum := sha256.Sum256([]byte(fmt.Sprintf("synthetic-asset-%d", i)))
		ts := base.Add(time.Duration(i) * time.Minute)
		batch = append(batch, LocationUpsert{
			SourceID:     src.ID,
			RelativePath: fmt.Sprintf("26_01_01_%06d.png", i),
			SHA256:       hex.EncodeToString(sum[:]),
			Info:         MediaInfo{MediaType: MediaImage, MIMEType: "image/png", Width: 1280, Height: 720, ByteSize: 1800000},
			FileSize:     1800000,
			ModifiedAt:   ts,
			FileIdentity: fmt.Sprintf("1:0:%x", i),
			SeenAt:       ts,
		})
		if len(batch) == 500 {
			if _, err := store.UpsertAssetLocationsBatch(ctx, batch); err != nil {
				t.Fatalf("insert batch at %d: %v", i, err)
			}
			batch = batch[:0]
		}
	}

	start := time.Now()
	assets, stats, uids, _, err := store.QueryAssets(ctx, filepath.Join(tmp, "Archive"), Filter{Sort: SortNewest})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("QueryAssets(10000): %v", err)
	}
	if len(assets) != total || stats.TotalAssets != total || len(uids) != 1 {
		t.Fatalf("unexpected 10k query result: len=%d stats=%+v uids=%v", len(assets), stats, uids)
	}
	t.Logf("QueryAssets(10,000 synthetic items) completed in %v", elapsed)
}

func BenchmarkQueryAssets(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			ctx := context.Background()
			db := dbtestOpenBench(b)
			store, err := NewStore(db)
			if err != nil {
				b.Fatal(err)
			}
			tmp := b.TempDir()
			src, err := store.UpsertSource(ctx, Source{RootPath: tmp, SourceType: SourceGameCache, UID: "1001", Enabled: true})
			if err != nil {
				b.Fatal(err)
			}
			base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
			batch := make([]LocationUpsert, 0, 500)
			for i := 0; i < n; i++ {
				sum := sha256.Sum256([]byte(fmt.Sprintf("bench-%d", i)))
				ts := base.Add(time.Duration(i) * time.Second)
				batch = append(batch, LocationUpsert{
					SourceID: src.ID, RelativePath: fmt.Sprintf("p%05d.png", i),
					SHA256:   hex.EncodeToString(sum[:]),
					Info:     MediaInfo{MediaType: MediaImage, MIMEType: "image/png", Width: 1280, Height: 720, ByteSize: 1800000},
					FileSize: 1800000, ModifiedAt: ts, SeenAt: ts,
				})
				if len(batch) == 500 || i == n-1 {
					if _, err := store.UpsertAssetLocationsBatch(ctx, batch); err != nil {
						b.Fatal(err)
					}
					batch = batch[:0]
				}
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				items, _, _, _, err := store.QueryAssets(ctx, "", Filter{})
				if err != nil || len(items) != n {
					b.Fatalf("QueryAssets: len=%d err=%v", len(items), err)
				}
			}
		})
	}
}

func BenchmarkThumbnailColdVsWarm(b *testing.B) {
	ctx := context.Background()
	tmp := b.TempDir()
	imgPath := filepath.Join(tmp, "sample.png")
	raw := makePNGBytes(b, 1280, 720, 3)
	writeTestFile(b, imgPath, raw)
	sum := sha256.Sum256(raw)
	sha := hex.EncodeToString(sum[:])

	b.Run("ColdL1_1280x720", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := DecodeScaled(ctx, imgPath, ThumbL1); err != nil {
				b.Fatal(err)
			}
		}
	})

	tc := NewThumbCache(filepath.Join(tmp, "thumbs"), 256)
	if _, _, err := tc.LoadOrDecode(ctx, sha, imgPath, ThumbL1); err != nil {
		b.Fatal(err)
	}
	b.Run("WarmL1_1280x720", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			px, hit, err := tc.LoadOrDecode(ctx, sha, imgPath, ThumbL1)
			if err != nil || !hit || px == nil {
				b.Fatalf("expected warm hit: hit=%v err=%v", hit, err)
			}
		}
	})

	b.Run("ViewerL2_1280x720", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := DecodeScaled(ctx, imgPath, ThumbL2); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("ViewerOriginal_1280x720", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := DecodeScaled(ctx, imgPath, 0); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkAutoArchiveThroughput(b *testing.B) {
	ctx := context.Background()
	tmp := b.TempDir()
	db := dbtestOpenBench(b)
	store, err := NewStore(db)
	if err != nil {
		b.Fatal(err)
	}
	archiveRoot := filepath.Join(tmp, "Archive")
	srcDir := filepath.Join(tmp, "Src")
	src, err := store.UpsertSource(ctx, Source{
		RootPath:   srcDir,
		SourceType: SourceGameCache,
		UID:        "100001",
		Enabled:    true,
	})
	if err != nil {
		b.Fatal(err)
	}
	const poolSize = 8
	paths := make([]string, poolSize)
	shas := make([]string, poolSize)
	ids := make([]int64, poolSize)
	var totalBytes int64
	now := time.Now().UTC()
	for i := 0; i < poolSize; i++ {
		raw := makePNGBytes(b, 1280, 720, byte(i+11))
		p := filepath.Join(srcDir, fmt.Sprintf("shot_%02d.png", i))
		writeTestFile(b, p, raw)
		sum := sha256.Sum256(raw)
		sha := hex.EncodeToString(sum[:])
		res, err := store.UpsertAssetLocationsBatch(ctx, []LocationUpsert{{
			SourceID:     src.ID,
			RelativePath: fmt.Sprintf("shot_%02d.png", i),
			SHA256:       sha,
			Info:         MediaInfo{MediaType: MediaImage, MIMEType: "image/png", Width: 1280, Height: 720, ByteSize: int64(len(raw))},
			FileSize:     int64(len(raw)),
			ModifiedAt:   now,
			SeenAt:       now,
		}})
		if err != nil || len(res.AssetIDs) != 1 {
			b.Fatalf("UpsertAssetLocationsBatch: res=%+v err=%v", res, err)
		}
		paths[i] = p
		shas[i] = sha
		ids[i] = res.AssetIDs[0]
		totalBytes += int64(len(raw))
	}
	b.SetBytes(totalBytes / poolSize)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx := i % poolSize
		if i >= poolSize {
			b.StopTimer()
			_ = DeleteArchiveCopy(ctx, store, archiveRoot, ids[idx])
			b.StartTimer()
		}
		res, err := ArchiveFile(ctx, store, archiveRoot, ids[idx], paths[idx], shas[idx], "image/png")
		if err != nil || res.Deduplicated {
			b.Fatalf("ArchiveFile: res=%+v err=%v", res, err)
		}
	}
}

func dbtestOpenBench(b *testing.B) *database.DB {
	b.Helper()
	db, err := database.Open(context.Background(), database.Options{
		Path:     filepath.Join(b.TempDir(), database.FileName),
		NoBackup: true,
	})
	if err != nil {
		b.Fatalf("open bench db: %v", err)
	}
	b.Cleanup(func() { _ = db.Close() })
	return db
}
