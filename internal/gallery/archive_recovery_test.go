// SPDX-License-Identifier: GPL-3.0-or-later

package gallery

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAutoArchiveOffDoesNotCopyAndOnDeduplicatesWithoutHardLinks(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	tmp := t.TempDir()
	contentRoot := filepath.Join(tmp, "Game", "Client", "WindowsNoEditor")
	archiveDir := filepath.Join(tmp, "Archive")
	picsRoot := filepath.Join(tmp, "Pictures")

	samePhoto := makePNGBytes(t, 64, 36, 11)
	framedPhoto := makePNGBytes(t, 72, 44, 12) // distinct frame variant

	shot1 := filepath.Join(contentRoot, "Selfie", "10001", "ScreenShots", "26_11_24_10_00_01aaaa.png")
	shot2 := filepath.Join(contentRoot, "Selfie", "10002", "ScreenShots", "26_11_24_10_00_01bbbb.png")
	exp1 := filepath.Join(picsRoot, "NevernessToEverness", "26_11_24_10_00_01.png")
	writeTestFile(t, shot1, samePhoto)
	writeTestFile(t, shot2, samePhoto) // identical bytes under second UID!
	writeTestFile(t, exp1, framedPhoto)

	svc, err := NewService(db, ServiceOptions{
		ContentRootOverride: contentRoot,
		PicturesDirOverride: picsRoot,
		ArchiveDirOverride:  archiveDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	// Default settings: auto_archive MUST be false.
	snap, err := svc.LoadSnapshot(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Settings.AutoArchive {
		t.Fatal("AutoArchive must default to false")
	}

	// Scan with auto-archive OFF -> nothing copied to archiveDir.
	rep, _, err := svc.SyncAndScan(ctx, "")
	if err != nil {
		t.Fatalf("SyncAndScan (auto off): %v", err)
	}
	if rep.Archived != 0 {
		t.Fatalf("Archived = %d with auto_archive off, want 0", rep.Archived)
	}
	if _, err := os.Stat(filepath.Join(archiveDir, "originals")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("originals dir must not be created while auto_archive is off: %v", err)
	}

	snap, err = svc.LoadSnapshot(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Assets) != 2 {
		t.Fatalf("deduplicated asset count = %d, want 2 (samePhoto + framedPhoto)", len(snap.Assets))
	}
	// Verify the deduplicated asset tracks BOTH UIDs!
	var shared Asset
	for _, a := range snap.Assets {
		if a.HasCached {
			shared = a
		}
	}
	if len(shared.UIDs) != 2 || shared.UIDs[0] != "10001" || shared.UIDs[1] != "10002" {
		t.Fatalf("shared asset UIDs = %v, want [10001 10002]", shared.UIDs)
	}

	// One-time ArchiveExisting copies shared game-cache photo once (no hard link!).
	archRep, err := svc.ArchiveExisting(ctx)
	if err != nil {
		t.Fatalf("ArchiveExisting: %v", err)
	}
	if archRep.Archived != 1 {
		t.Fatalf("ArchiveExisting archived = %d, want 1", archRep.Archived)
	}
	snap, err = svc.LoadSnapshot(ctx, Filter{Scope: ScopeArchived})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Assets) != 1 || !snap.Assets[0].HasArchived {
		t.Fatalf("expected 1 archived asset, got %+v", snap.Assets)
	}
	archPath := snap.Assets[0].ArchivePath
	st, err := statRegularFile(archPath)
	if err != nil {
		t.Fatalf("stat archive file: %v", err)
	}
	if st.Links != 1 {
		t.Fatalf("archive copy must be an independent copy (Links=1), got Links=%d", st.Links)
	}
	gotBytes, err := os.ReadFile(archPath)
	if err != nil || !bytes.Equal(gotBytes, samePhoto) {
		t.Fatal("archived bytes must match source photo byte-for-byte")
	}

	// Remove BOTH game-cache originals: archived copy MUST remain available and readable!
	if err := os.Remove(shot1); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(shot2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.SyncAndScan(ctx, ""); err != nil {
		t.Fatal(err)
	}
	snap, err = svc.LoadSnapshot(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var archivedAfterDelete Asset
	for _, a := range snap.Assets {
		if a.HasArchived {
			archivedAfterDelete = a
		}
	}
	if !archivedAfterDelete.Available || archivedAfterDelete.PrimaryPath != archPath {
		t.Fatalf("archived asset must stay readable after source cache removal: %+v", archivedAfterDelete)
	}
	if archivedAfterDelete.HasCached {
		t.Fatal("deleted source cache must no longer report HasCached")
	}

	// Remove unarchived exported photo: MUST be marked Missing (Available=false).
	if err := os.Remove(exp1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.SyncAndScan(ctx, ""); err != nil {
		t.Fatal(err)
	}
	snap, err = svc.LoadSnapshot(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Stats.MissingCount != 1 {
		t.Fatalf("MissingCount = %d, want 1", snap.Stats.MissingCount)
	}
}

func TestArchiveDiskFullStopsSafelyAndPreservesSource(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	srcFile := filepath.Join(tmp, "src.png")
	archiveDir := filepath.Join(tmp, "Archive")
	raw := makePNGBytes(t, 48, 27, 21)
	writeTestFile(t, srcFile, raw)

	info, sha, st, err := HashAndInspectFile(ctx, srcFile)
	if err != nil {
		t.Fatal(err)
	}
	src, err := store.UpsertSource(ctx, Source{RootPath: tmp, SourceType: SourceGameCache, UID: "100", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	res, err := store.UpsertAssetLocationsBatch(ctx, []LocationUpsert{{
		SourceID: src.ID, RelativePath: "src.png", SHA256: sha, Info: info,
		FileSize: st.Size, ModifiedAt: st.ModifiedAt, FileIdentity: st.Identity, SeenAt: store.clock(),
	}})
	if err != nil {
		t.Fatal(err)
	}

	origFree := freeSpaceFn
	freeSpaceFn = func(string) (int64, error) { return 1024, nil } // simulate full disk
	defer func() { freeSpaceFn = origFree }()

	if _, err := ArchiveFile(ctx, store, archiveDir, res.AssetIDs[0], srcFile, sha, info.MIMEType); !errors.Is(err, ErrDiskFull) {
		t.Fatalf("ArchiveFile on full disk err = %v, want ErrDiskFull", err)
	}
	// Source file must remain completely untouched.
	after, err := os.ReadFile(srcFile)
	if err != nil || !bytes.Equal(after, raw) {
		t.Fatal("source file was altered on disk-full archive failure")
	}
	// No archive row committed.
	if _, found, err := store.GetArchiveByAssetID(ctx, res.AssetIDs[0]); err != nil || found {
		t.Fatalf("failed disk-full archive must not commit row: found=%v err=%v", found, err)
	}
}

func TestCrashRecoveryReconcilesStagesOrphansAndMissing(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	archiveDir := filepath.Join(tmp, "Archive")

	// 1. Create a stale .oddward-stage-*.tmp file left by a crash.
	stageFile := filepath.Join(archiveDir, "originals", "ab", ".oddward-stage-crash.tmp")
	writeTestFile(t, stageFile, []byte("partial bytes"))

	// 2. Create an asset in SQLite whose archive file was published on disk
	//    before SQLite committed (simulated crash between MoveFileEx and Tx).
	imgA := makePNGBytes(t, 32, 32, 41)
	srcA := filepath.Join(tmp, "srcA.png")
	writeTestFile(t, srcA, imgA)
	infoA, shaA, stA, err := HashAndInspectFile(ctx, srcA)
	if err != nil {
		t.Fatal(err)
	}
	src, err := store.UpsertSource(ctx, Source{RootPath: tmp, SourceType: SourceGameCache, UID: "200", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	resA, err := store.UpsertAssetLocationsBatch(ctx, []LocationUpsert{{
		SourceID: src.ID, RelativePath: "srcA.png", SHA256: shaA, Info: infoA,
		FileSize: stA.Size, ModifiedAt: stA.ModifiedAt, FileIdentity: stA.Identity, SeenAt: store.clock(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	relA, err := ArchiveRelativePath(shaA, infoA.MIMEType)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, joinNormalized(archiveDir, relA), imgA)

	// 3. Create an orphaned valid archive file with NO SQLite asset row at all.
	imgB := makePNGBytes(t, 40, 30, 42)
	srcB := filepath.Join(tmp, "orphan_tmp.png")
	writeTestFile(t, srcB, imgB)
	infoB, shaB, _, err := HashAndInspectFile(ctx, srcB)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(srcB)
	relB, err := ArchiveRelativePath(shaB, infoB.MIMEType)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, joinNormalized(archiveDir, relB), imgB)

	// 4. Create an archive DB row whose file on disk was externally deleted.
	imgC := makePNGBytes(t, 24, 24, 43)
	srcC := filepath.Join(tmp, "srcC.png")
	writeTestFile(t, srcC, imgC)
	infoC, shaC, stC, err := HashAndInspectFile(ctx, srcC)
	if err != nil {
		t.Fatal(err)
	}
	resC, err := store.UpsertAssetLocationsBatch(ctx, []LocationUpsert{{
		SourceID: src.ID, RelativePath: "srcC.png", SHA256: shaC, Info: infoC,
		FileSize: stC.Size, ModifiedAt: stC.ModifiedAt, FileIdentity: stC.Identity, SeenAt: store.clock(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	relC, err := ArchiveRelativePath(shaC, infoC.MIMEType)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordArchive(ctx, resC.AssetIDs[0], relC, store.clock(), ArchiveStatusReady); err != nil {
		t.Fatal(err)
	}

	rep, err := ReconcileArchive(ctx, store, archiveDir)
	if err != nil {
		t.Fatalf("ReconcileArchive: %v", err)
	}
	if rep.StagesRemoved != 1 {
		t.Fatalf("StagesRemoved = %d, want 1", rep.StagesRemoved)
	}
	if _, err := os.Stat(stageFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale stage file was not removed")
	}
	if rep.LinkedRecords != 1 {
		t.Fatalf("LinkedRecords = %d, want 1", rep.LinkedRecords)
	}
	if rec, ok, err := store.GetArchiveByAssetID(ctx, resA.AssetIDs[0]); err != nil || !ok || rec.Status != ArchiveStatusReady {
		t.Fatalf("asset A archive record not recovered: ok=%v rec=%+v err=%v", ok, rec, err)
	}
	if rep.OrphansAdopted != 1 {
		t.Fatalf("OrphansAdopted = %d, want 1", rep.OrphansAdopted)
	}
	if _, found, err := store.AssetIDBySHA256(ctx, shaB); err != nil || !found {
		t.Fatalf("orphan asset B not adopted: found=%v err=%v", found, err)
	}
	if rep.MissingMarked != 1 {
		t.Fatalf("MissingMarked = %d, want 1", rep.MissingMarked)
	}
	if rec, ok, err := store.GetArchiveByAssetID(ctx, resC.AssetIDs[0]); err != nil || !ok || rec.Status != ArchiveStatusMissing {
		t.Fatalf("missing archive C should be marked missing: ok=%v rec=%+v err=%v", ok, rec, err)
	}
}

func TestConcurrentArchiveAndBatchExportAndDeleteArchiveOnly(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	archiveDir := filepath.Join(tmp, "Archive")
	exportDir := filepath.Join(tmp, "Export")
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		t.Fatal(err)
	}

	raw := makePNGBytes(t, 48, 32, 55)
	srcPath := filepath.Join(tmp, "photo.png")
	writeTestFile(t, srcPath, raw)
	info, sha, st, err := HashAndInspectFile(ctx, srcPath)
	if err != nil {
		t.Fatal(err)
	}
	src, err := store.UpsertSource(ctx, Source{RootPath: tmp, SourceType: SourceGameCache, UID: "300", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	res, err := store.UpsertAssetLocationsBatch(ctx, []LocationUpsert{{
		SourceID: src.ID, RelativePath: "photo.png", SHA256: sha, Info: info,
		FileSize: st.Size, ModifiedAt: st.ModifiedAt, FileIdentity: st.Identity, SeenAt: store.clock(),
	}})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate concurrent cross-process archive collision on the same content hash.
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, aerr := ArchiveFile(ctx, store, archiveDir, res.AssetIDs[0], srcPath, sha, info.MIMEType)
			errs <- aerr
		}()
	}
	wg.Wait()
	close(errs)
	for aerr := range errs {
		if aerr != nil {
			t.Fatalf("concurrent ArchiveFile failed: %v", aerr)
		}
	}

	assets, _, _, _, err := store.QueryAssets(ctx, archiveDir, Filter{})
	if err != nil || len(assets) != 1 {
		t.Fatalf("QueryAssets: len=%d err=%v", len(assets), err)
	}
	// Batch export twice to the same directory verifies collision suffixing.
	rep, err := ExportBatch(ctx, []Asset{assets[0], assets[0]}, exportDir)
	if err != nil || rep.Exported != 2 {
		t.Fatalf("ExportBatch: rep=%+v err=%v", rep, err)
	}
	b1, err := os.ReadFile(filepath.Join(exportDir, "photo.png"))
	if err != nil || !bytes.Equal(b1, raw) {
		t.Fatal("first exported photo bytes mismatch")
	}
	b2, err := os.ReadFile(filepath.Join(exportDir, "photo (1).png"))
	if err != nil || !bytes.Equal(b2, raw) {
		t.Fatal("second collided exported photo bytes mismatch")
	}

	// DeleteArchiveCopy must remove ONLY archive file, keeping original game source intact!
	if err := DeleteArchiveCopy(ctx, store, archiveDir, assets[0].ID); err != nil {
		t.Fatalf("DeleteArchiveCopy: %v", err)
	}
	if _, err := os.Stat(assets[0].ArchivePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("archive copy should be removed after DeleteArchiveCopy")
	}
	if orig, err := os.ReadFile(srcPath); err != nil || !bytes.Equal(orig, raw) {
		t.Fatal("DeleteArchiveCopy must NEVER modify or delete original source photo")
	}
}
