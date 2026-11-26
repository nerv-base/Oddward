// SPDX-License-Identifier: GPL-3.0-or-later

package gallery

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// makeSyntheticMP4 builds a minimal valid ISO BMFF MP4 matching the real CN
// NTE layout observed on disk: [ftyp, free, mdat, moov(mvhd, vide trak, soun trak)].
func makeSyntheticMP4(t testing.TB, width, height uint16, durationMS uint32, videoCodec, audioCodec string, payloadSize int) []byte {
	t.Helper()
	box := func(typ string, body []byte) []byte {
		out := make([]byte, 8+len(body))
		binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
		copy(out[4:8], typ)
		copy(out[8:], body)
		return out
	}

	// ftyp: isom / 0x200 / isom iso2 avc1 mp41
	ftypBody := make([]byte, 24)
	copy(ftypBody[0:4], "isom")
	binary.BigEndian.PutUint32(ftypBody[4:8], 512)
	copy(ftypBody[8:12], "isom")
	copy(ftypBody[12:16], "iso2")
	copy(ftypBody[16:20], "avc1")
	copy(ftypBody[20:24], "mp41")
	ftyp := box("ftyp", ftypBody)
	free := box("free", nil)

	// mdat payload (distinct per call using dims/duration)
	mdatPayload := bytes.Repeat([]byte{byte(width & 0xff), byte(height & 0xff), byte(durationMS & 0xff), 0x42}, max(payloadSize/4, 16))
	mdat := box("mdat", mdatPayload)

	// mvhd v0: 4(ver+flags) + 4(create) + 4(mod) + 4(timescale=1000) + 4(duration) + 80(rest) = 100
	mvhdBody := make([]byte, 100)
	binary.BigEndian.PutUint32(mvhdBody[12:16], 1000)
	binary.BigEndian.PutUint32(mvhdBody[16:20], durationMS)
	mvhd := box("mvhd", mvhdBody)

	// Video track: tkhd + mdia(hdlr="vide", minf(stbl(stsd(videoCodec))))
	tkhdBody := make([]byte, 84)
	tkhdBody[3] = 0x03 // enabled + in_movie
	binary.BigEndian.PutUint32(tkhdBody[76:80], uint32(width)<<16)
	binary.BigEndian.PutUint32(tkhdBody[80:84], uint32(height)<<16)
	tkhd := box("tkhd", tkhdBody)

	hdlrVideBody := make([]byte, 24)
	copy(hdlrVideBody[8:12], "vide")
	hdlrVide := box("hdlr", hdlrVideBody)

	stsdEntry := box(videoCodec, make([]byte, 16))
	stsdBody := make([]byte, 8+len(stsdEntry))
	binary.BigEndian.PutUint32(stsdBody[4:8], 1)
	copy(stsdBody[8:], stsdEntry)
	stsdVide := box("stsd", stsdBody)
	mdiaVide := box("mdia", append(hdlrVide, box("minf", box("stbl", stsdVide))...))
	trakVide := box("trak", append(tkhd, mdiaVide...))

	moovBody := append(mvhd, trakVide...)
	if audioCodec != "" {
		hdlrSounBody := make([]byte, 24)
		copy(hdlrSounBody[8:12], "soun")
		hdlrSoun := box("hdlr", hdlrSounBody)
		aEntry := box(audioCodec, make([]byte, 16))
		aStsdBody := make([]byte, 8+len(aEntry))
		binary.BigEndian.PutUint32(aStsdBody[4:8], 1)
		copy(aStsdBody[8:], aEntry)
		mdiaSoun := box("mdia", append(hdlrSoun, box("minf", box("stbl", box("stsd", aStsdBody)))...))
		trakSoun := box("trak", mdiaSoun)
		moovBody = append(moovBody, trakSoun...)
	}
	moov := box("moov", moovBody)

	var out bytes.Buffer
	out.Write(ftyp)
	out.Write(free)
	out.Write(mdat)
	out.Write(moov)
	return out.Bytes()
}

func TestP1VideoDiscoveryInspectionKeyFrameArchiveAndExport(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	contentRoot := filepath.Join(tmp, "Game", "Client", "WindowsNoEditor")
	shotsDir := filepath.Join(contentRoot, "Selfie", "100001", "ScreenShots")
	ffmpegDir := filepath.Join(contentRoot, "Selfie", "100001", "FFMpeg")
	picsRoot := filepath.Join(tmp, "Pictures")
	vidsRoot := filepath.Join(tmp, "Videos")
	expVidDir := filepath.Join(vidsRoot, "NevernessToEverness")
	archiveDir := filepath.Join(tmp, "Archive")
	thumbDir := filepath.Join(tmp, "Thumbs")

	for _, d := range []string{shotsDir, ffmpegDir, filepath.Join(picsRoot, "NevernessToEverness"), expVidDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Write one screenshot, one in-game MP4 + companion KeyFrame.jpg, and one exported MP4.
	writeTestFile(t, filepath.Join(shotsDir, "26_11_24_16_52_02abcd.png"), makePNGBytes(t, 1280, 720, 1))
	mp4Bytes := makeSyntheticMP4(t, 1920, 1080, 130840, "avc1", "mp4a", 4096)
	mp4Path := filepath.Join(ffmpegDir, "26_11_24_16_49_0123456789abcdef0123456789abcdef.mp4")
	kfPath := filepath.Join(ffmpegDir, "26_11_24_16_49_20_KeyFrame.jpg")
	writeTestFile(t, mp4Path, mp4Bytes)
	writeTestFile(t, kfPath, makeJPEGWithExif(t, 320, 180, 5, 1, "", ""))
	expMP4 := makeSyntheticMP4(t, 1280, 720, 5400, "hvc1", "mp4a", 2048)
	writeTestFile(t, filepath.Join(expVidDir, "26_11_24_17_00_00.mp4"), expMP4)

	db := openTestDB(t)
	svc, err := NewService(db, ServiceOptions{
		ThumbCacheDir:       thumbDir,
		ContentRootOverride: contentRoot,
		PicturesDirOverride: picsRoot,
		VideosDirOverride:   vidsRoot,
		ArchiveDirOverride:  archiveDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	if _, _, err := svc.SyncAndScan(ctx, ""); err != nil {
		t.Fatalf("SyncAndScan: %v", err)
	}
	snap, err := svc.LoadSnapshot(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	// KeyFrame.jpg must NOT be indexed as a standalone asset -> total = 1 photo + 2 videos = 3.
	if snap.Stats.TotalAssets != 3 || snap.Stats.VideoCount != 2 || snap.Stats.PhotoCount != 1 {
		t.Fatalf("unexpected counts: total=%d photos=%d videos=%d", snap.Stats.TotalAssets, snap.Stats.PhotoCount, snap.Stats.VideoCount)
	}

	// Filter ScopeVideos and verify metadata + paired keyframe poster.
	vsnap, err := svc.LoadSnapshot(ctx, Filter{Scope: ScopeVideos})
	if err != nil {
		t.Fatal(err)
	}
	if len(vsnap.Assets) != 2 {
		t.Fatalf("ScopeVideos len = %d, want 2", len(vsnap.Assets))
	}
	var cachedVid Asset
	for _, a := range vsnap.Assets {
		if a.HasCached {
			cachedVid = a
		}
	}
	if cachedVid.ID == 0 || cachedVid.MediaType != MediaVideo || cachedVid.Width != 1920 || cachedVid.Height != 1080 || cachedVid.DurationMS != 130840 || cachedVid.VideoCodec != "H.264" || cachedVid.AudioCodec != "AAC" {
		t.Fatalf("unexpected cached video metadata: %+v", cachedVid)
	}
	if cachedVid.PosterPath == "" {
		t.Fatal("cached FFMpeg video must pair with its sibling _KeyFrame.jpg")
	}

	// Thumbnail decode for cached video uses its paired KeyFrame.jpg.
	px, _, err := svc.DecodeAssetThumbnail(ctx, cachedVid, ThumbL1)
	if err != nil || px == nil {
		t.Fatalf("DecodeAssetThumbnail(video): px=%v err=%v", px, err)
	}

	// Archive cached video -> remove source MP4 & keyframe -> archived video & poster remain readable!
	if _, err := svc.ArchiveOne(ctx, cachedVid); err != nil {
		t.Fatalf("ArchiveOne(video): %v", err)
	}
	if err := os.Remove(mp4Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(kfPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.SyncAndScan(ctx, ""); err != nil {
		t.Fatal(err)
	}
	after, err := svc.LoadSnapshot(ctx, Filter{Scope: ScopeArchived})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Assets) != 1 || !after.Assets[0].Available || after.Assets[0].MediaType != MediaVideo {
		t.Fatalf("archived video must survive source removal: %+v", after.Assets)
	}
	if after.Stats.ArchiveVideoBytes <= 0 {
		t.Fatalf("expected positive ArchiveVideoBytes, got %+v", after.Stats)
	}
	// Clear disk thumb cache and verify archived poster backup still renders the video cover.
	if err := svc.ClearThumbCache(ctx); err != nil {
		t.Fatal(err)
	}
	if px2, _, err := svc.DecodeAssetThumbnail(ctx, after.Assets[0], ThumbL1); err != nil || px2 == nil {
		t.Fatalf("archived video poster backup failed after source removal: px=%v err=%v", px2, err)
	}

	// Export archived video and verify byte-exact copy.
	exportDest := filepath.Join(tmp, "exported-video.mp4")
	if err := ExportAsset(ctx, after.Assets[0], exportDest); err != nil {
		t.Fatalf("ExportAsset(video): %v", err)
	}
	gotBytes, err := os.ReadFile(exportDest)
	if err != nil || !bytes.Equal(gotBytes, mp4Bytes) {
		t.Fatalf("exported video bytes mismatch: len=%d want=%d err=%v", len(gotBytes), len(mp4Bytes), err)
	}
}

func TestP1FavoritesAlbumsTagsSearchAndBatchCleanup(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	shotsDir := filepath.Join(tmp, "Selfie", "100001", "ScreenShots")
	archiveDir := filepath.Join(tmp, "Archive")
	if err := os.MkdirAll(shotsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p1 := filepath.Join(shotsDir, "26_11_24_10_00_00_city.png")
	p2 := filepath.Join(shotsDir, "26_11_24_11_00_00_night.png")
	p3 := filepath.Join(shotsDir, "26_11_24_12_00_00_vanish.png")
	writeTestFile(t, p1, makePNGBytes(t, 64, 36, 1))
	writeTestFile(t, p2, makePNGBytes(t, 64, 36, 2))
	writeTestFile(t, p3, makePNGBytes(t, 64, 36, 3))

	db := openTestDB(t)
	svc, err := NewService(db, ServiceOptions{
		ContentRootOverride: tmp,
		PicturesDirOverride: filepath.Join(tmp, "NoPics"),
		VideosDirOverride:   filepath.Join(tmp, "NoVids"),
		ArchiveDirOverride:  archiveDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	if _, _, err := svc.SyncAndScan(ctx, ""); err != nil {
		t.Fatal(err)
	}
	snap, err := svc.LoadSnapshot(ctx, Filter{})
	if err != nil || len(snap.Assets) != 3 {
		t.Fatalf("expected 3 indexed assets, got %d err=%v", len(snap.Assets), err)
	}
	var id1, id2, id3 int64
	for _, a := range snap.Assets {
		switch a.FileName {
		case "26_11_24_10_00_00_city.png":
			id1 = a.ID
		case "26_11_24_11_00_00_night.png":
			id2 = a.ID
		case "26_11_24_12_00_00_vanish.png":
			id3 = a.ID
		}
	}

	// Favorites.
	if err := svc.SetFavorites(ctx, []int64{id1, id2}, true); err != nil {
		t.Fatalf("SetFavorites: %v", err)
	}
	favSnap, err := svc.LoadSnapshot(ctx, Filter{Scope: ScopeFavorites})
	if err != nil || len(favSnap.Assets) != 2 || favSnap.Stats.FavoriteCount != 2 {
		t.Fatalf("ScopeFavorites = %d (count=%d), want 2", len(favSnap.Assets), favSnap.Stats.FavoriteCount)
	}

	// Custom albums: create, add, filter, rename, delete (never deletes assets!).
	album, err := svc.CreateAlbum(ctx, "海特洛夜景")
	if err != nil || album.ID <= 0 {
		t.Fatalf("CreateAlbum: %+v %v", album, err)
	}
	if err := svc.AddAssetsToAlbum(ctx, album.ID, []int64{id2}); err != nil {
		t.Fatalf("AddAssetsToAlbum: %v", err)
	}
	albSnap, err := svc.LoadSnapshot(ctx, Filter{AlbumID: album.ID})
	if err != nil || len(albSnap.Assets) != 1 || albSnap.Assets[0].ID != id2 {
		t.Fatalf("Album filter unexpected: %+v err=%v", albSnap.Assets, err)
	}
	if err := svc.RenameAlbum(ctx, album.ID, "雨夜街景"); err != nil {
		t.Fatalf("RenameAlbum: %v", err)
	}

	// Tags & search query (by filename, tag, and renamed album!).
	if err := svc.AddTagToAssets(ctx, "霓虹", []int64{id1, id2}); err != nil {
		t.Fatalf("AddTagToAssets: %v", err)
	}
	tagSnap, err := svc.LoadSnapshot(ctx, Filter{Tag: "霓虹"})
	if err != nil || len(tagSnap.Assets) != 2 {
		t.Fatalf("Tag filter = %d, want 2", len(tagSnap.Assets))
	}
	qSnap, err := svc.LoadSnapshot(ctx, Filter{Query: "雨夜"})
	if err != nil || len(qSnap.Assets) != 1 || qSnap.Assets[0].ID != id2 {
		t.Fatalf("Search by album name failed: %+v", qSnap.Assets)
	}
	qFile, err := svc.LoadSnapshot(ctx, Filter{Query: "city"})
	if err != nil || len(qFile.Assets) != 1 || qFile.Assets[0].ID != id1 {
		t.Fatalf("Search by filename failed: %+v", qFile.Assets)
	}

	// Deleting album must NEVER delete underlying asset or files.
	if err := svc.DeleteAlbum(ctx, album.ID); err != nil {
		t.Fatalf("DeleteAlbum: %v", err)
	}
	if _, err := os.Stat(p2); err != nil {
		t.Fatalf("DeleteAlbum touched source file: %v", err)
	}

	// RebuildIndex must preserve favorites & tags!
	if _, err := svc.RebuildIndex(ctx, ""); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	afterRebuild, err := svc.LoadSnapshot(ctx, Filter{Scope: ScopeFavorites, Tag: "霓虹"})
	if err != nil || len(afterRebuild.Assets) != 2 {
		t.Fatalf("RebuildIndex lost favorites/tags: len=%d err=%v", len(afterRebuild.Assets), err)
	}

	// Archive id1 & id2, delete p3 from cache -> PruneMissingUnarchived removes ONLY p3!
	for _, a := range afterRebuild.Assets {
		if _, err := svc.ArchiveOne(ctx, a); err != nil {
			t.Fatalf("ArchiveOne: %v", err)
		}
	}
	_ = os.Remove(p3)
	if _, _, err := svc.SyncAndScan(ctx, ""); err != nil {
		t.Fatal(err)
	}
	pruned, err := svc.PruneMissingUnarchived(ctx)
	if err != nil || pruned != 1 {
		t.Fatalf("PruneMissingUnarchived = %d, err=%v, want 1", pruned, err)
	}
	// Batch delete Oddward archive copies -> removes ONLY archive copies, source files stay intact!
	deleted, err := svc.DeleteArchiveCopies(ctx, []int64{id1, id2, id3})
	if err != nil || deleted != 2 {
		t.Fatalf("DeleteArchiveCopies = %d, err=%v, want 2", deleted, err)
	}
	if _, err := os.Stat(p1); err != nil {
		t.Fatalf("DeleteArchiveCopies must never remove original file p1: %v", err)
	}
}

func TestP1CustomStaticBackgroundValidationPersistenceAndClear(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	dataRoot := filepath.Join(tmp, "Data")
	shotsDir := filepath.Join(tmp, "Selfie", "100001", "ScreenShots")
	if err := os.MkdirAll(shotsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	photoPath := filepath.Join(shotsDir, "hero.png")
	writeTestFile(t, photoPath, makePNGBytes(t, 320, 180, 9))
	corruptPath := filepath.Join(shotsDir, "broken.png")
	writeTestFile(t, corruptPath, []byte("not-a-valid-png"))

	db := openTestDB(t)
	svc, err := NewService(db, ServiceOptions{
		DataRoot:            dataRoot,
		ContentRootOverride: tmp,
		PicturesDirOverride: filepath.Join(tmp, "NoPics"),
		VideosDirOverride:   filepath.Join(tmp, "NoVids"),
		ArchiveDirOverride:  filepath.Join(tmp, "Archive"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	// Reject corrupt photo.
	if _, err := svc.SetCustomBackground(ctx, Asset{ID: 1, MediaType: MediaImage, Available: true, PrimaryPath: corruptPath}); !errors.Is(err, ErrInvalidMedia) {
		t.Fatalf("corrupt background must be rejected, got %v", err)
	}
	// Reject video as static background.
	if _, err := svc.SetCustomBackground(ctx, Asset{ID: 2, MediaType: MediaVideo, Available: true, PrimaryPath: photoPath}); !errors.Is(err, ErrInvalidMedia) {
		t.Fatalf("video must be rejected as static background, got %v", err)
	}

	// Valid photo succeeds, persists independently of source file, and reloads cleanly.
	img, err := svc.SetCustomBackground(ctx, Asset{ID: 3, MediaType: MediaImage, Available: true, PrimaryPath: photoPath, MIMEType: "image/png"})
	if err != nil || img == nil || img.Bounds().Dx() != 320 {
		t.Fatalf("SetCustomBackground failed: img=%v err=%v", img, err)
	}
	// Remove original game cache photo -> custom background copy must remain valid!
	if err := os.Remove(photoPath); err != nil {
		t.Fatal(err)
	}
	loaded, activePath, err := svc.LoadCustomBackground(ctx)
	if err != nil || loaded == nil || activePath == "" || loaded.Bounds().Dx() != 320 {
		t.Fatalf("LoadCustomBackground after source removal failed: loaded=%v path=%q err=%v", loaded, activePath, err)
	}

	// ClearCustomBackground restores default (no custom background).
	if err := svc.ClearCustomBackground(ctx); err != nil {
		t.Fatalf("ClearCustomBackground: %v", err)
	}
	loaded2, activePath2, err := svc.LoadCustomBackground(ctx)
	if err != nil || loaded2 != nil || activePath2 != "" {
		t.Fatalf("expected cleared custom background, got img=%v path=%q err=%v", loaded2, activePath2, err)
	}
}

func TestP1InspectMP4RejectsCorruptAndTruncated(t *testing.T) {
	tmp := t.TempDir()
	good := filepath.Join(tmp, "ok.mp4")
	writeTestFile(t, good, makeSyntheticMP4(t, 1280, 720, 4200, "avc1", "mp4a", 512))
	info, err := InspectMP4File(good)
	if err != nil || info.MediaType != MediaVideo || info.Width != 1280 || info.Height != 720 || info.DurationMS != 4200 || info.VideoCodec != "H.264" || info.AudioCodec != "AAC" {
		t.Fatalf("InspectMP4File good failed: info=%+v err=%v", info, err)
	}
	if !info.CapturedAt.IsZero() {
		t.Fatalf("MP4 without verified capture timestamp must keep CapturedAt zero, got %v", info.CapturedAt)
	}

	bad := filepath.Join(tmp, "bad.mp4")
	writeTestFile(t, bad, []byte("not an mp4 file"))
	if _, err := InspectMP4File(bad); !errors.Is(err, ErrInvalidMedia) {
		t.Fatalf("corrupt mp4 err = %v, want ErrInvalidMedia", err)
	}

	// Truncated MP4 without moov box must be rejected.
	trunc := filepath.Join(tmp, "trunc.mp4")
	raw := makeSyntheticMP4(t, 1280, 720, 4200, "avc1", "mp4a", 512)
	writeTestFile(t, trunc, raw[:40])
	if _, err := InspectMP4File(trunc); !errors.Is(err, ErrInvalidMedia) {
		t.Fatalf("truncated mp4 err = %v, want ErrInvalidMedia", err)
	}
	_ = time.Second
}
