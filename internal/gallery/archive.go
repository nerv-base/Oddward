// SPDX-License-Identifier: GPL-3.0-or-later

package gallery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ArchiveResult describes the outcome of archiving one asset.
type ArchiveResult struct {
	RelativePath string
	Deduplicated bool
}

// ArchiveFile copies one validated image file into Oddward's content-addressed
// archive (originals/<sha[:2]>/<sha><ext>) using a same-volume temporary file,
// streaming SHA256 verification, mid-copy source change detection, atomic
// publication and a SQLite transaction. Hard links are never used.
func ArchiveFile(ctx context.Context, store *Store, archiveDir string, assetID int64, sourcePath, expectedSHA, mimeType string) (ArchiveResult, error) {
	if err := ctx.Err(); err != nil {
		return ArchiveResult{}, err
	}
	if store == nil || assetID <= 0 {
		return ArchiveResult{}, ErrInvalidSettings
	}
	cleanArchive, err := ValidateArchiveDir(ctx, archiveDir)
	if err != nil {
		return ArchiveResult{}, fmt.Errorf("%w: %v", ErrArchiveUnavailable, err)
	}
	cleanSource, err := checkLocalPath(ctx, sourcePath, false, false)
	if err != nil {
		return ArchiveResult{}, err
	}
	relPath, err := ArchiveRelativePath(expectedSHA, mimeType)
	if err != nil {
		return ArchiveResult{}, err
	}
	destPath := joinNormalized(cleanArchive, relPath)
	destDir := filepath.Dir(destPath)

	// Fast dedup check: if the exact content-addressed file is already published
	// and matches expectedSHA, record it in SQLite without copying again.
	if existing, err := verifyExistingArchiveFile(ctx, destPath, expectedSHA); err == nil && existing {
		if err := store.RecordArchive(ctx, assetID, relPath, store.clock(), ArchiveStatusReady); err != nil {
			return ArchiveResult{}, err
		}
		return ArchiveResult{RelativePath: relPath, Deduplicated: true}, nil
	}

	srcFile, err := os.Open(cleanSource)
	if err != nil {
		return ArchiveResult{}, err
	}
	defer srcFile.Close()

	before, err := statOpenFile(srcFile)
	if err != nil {
		return ArchiveResult{}, err
	}
	if before.Reparse || before.IsDir {
		return ArchiveResult{}, ErrUnsafePath
	}
	if before.Size <= 0 {
		return ArchiveResult{}, fmt.Errorf("%w: empty source file", ErrInvalidMedia)
	}
	if before.Size > MaxImageBytes {
		return ArchiveResult{}, fmt.Errorf("%w: source exceeds byte limit", ErrTooLarge)
	}

	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return ArchiveResult{}, fmt.Errorf("%w: %v", ErrArchiveUnavailable, err)
	}
	if _, err := checkLocalPath(ctx, destDir, true, false); err != nil {
		return ArchiveResult{}, fmt.Errorf("%w: %v", ErrArchiveUnavailable, err)
	}

	// Refuse archiving when free space on the destination volume is below
	// source size plus safety headroom. Source files are never modified.
	free, err := freeSpaceFn(destDir)
	if err != nil {
		return ArchiveResult{}, fmt.Errorf("%w: check free space: %v", ErrArchiveUnavailable, err)
	}
	needed := before.Size + archiveReserveHook
	if free < needed {
		return ArchiveResult{}, fmt.Errorf("%w: need %d bytes, available %d bytes", ErrDiskFull, needed, free)
	}

	stage, err := os.CreateTemp(destDir, ".oddward-stage-*.tmp")
	if err != nil {
		return ArchiveResult{}, fmt.Errorf("%w: create stage: %v", ErrArchiveUnavailable, err)
	}
	stagePath := stage.Name()
	published := false
	defer func() {
		_ = stage.Close()
		if !published {
			_ = os.Remove(stagePath)
		}
	}()

	h := sha256.New()
	headerCap := int(min(before.Size, int64(headerProbeBytes)))
	headerBuf := make([]byte, 0, headerCap)
	buf := make([]byte, 64<<10)
	var written int64

	for {
		if err := ctx.Err(); err != nil {
			return ArchiveResult{}, err
		}
		nr, rerr := srcFile.Read(buf)
		if nr > 0 {
			chunk := buf[:nr]
			if len(headerBuf) < headerCap {
				take := min(len(chunk), headerCap-len(headerBuf))
				headerBuf = append(headerBuf, chunk[:take]...)
			}
			if _, werr := stage.Write(chunk); werr != nil {
				if isDiskFullError(werr) {
					return ArchiveResult{}, fmt.Errorf("%w: %v", ErrDiskFull, werr)
				}
				return ArchiveResult{}, werr
			}
			_, _ = h.Write(chunk)
			written += int64(nr)
			if written > MaxImageBytes {
				return ArchiveResult{}, ErrTooLarge
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return ArchiveResult{}, rerr
		}
	}

	// Verify the source did not change while streaming.
	after, err := statOpenFile(srcFile)
	if err != nil {
		return ArchiveResult{}, err
	}
	if after.Identity != before.Identity || after.Size != before.Size || !after.ModifiedAt.Equal(before.ModifiedAt) || written != before.Size {
		return ArchiveResult{}, ErrFileUnstable
	}

	// Validate copied media header and exact digest before publication.
	if _, err := InspectReader(bytesReader(headerBuf), written); err != nil {
		return ArchiveResult{}, err
	}
	actualSHA := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(actualSHA, expectedSHA) {
		return ArchiveResult{}, ErrFileUnstable
	}

	if err := stage.Sync(); err != nil {
		if isDiskFullError(err) {
			return ArchiveResult{}, fmt.Errorf("%w: %v", ErrDiskFull, err)
		}
		return ArchiveResult{}, err
	}
	if err := stage.Close(); err != nil {
		return ArchiveResult{}, err
	}
	if _, err := checkLocalPath(ctx, stagePath, false, false); err != nil {
		return ArchiveResult{}, err
	}
	if err := replaceFile(stagePath, destPath); err != nil {
		// Another Oddward process may have concurrently published the same
		// content-addressed object. Verify destination integrity before failing.
		if ok, verr := verifyExistingArchiveFile(ctx, destPath, expectedSHA); verr == nil && ok {
			if err := store.RecordArchive(ctx, assetID, relPath, store.clock(), ArchiveStatusReady); err != nil {
				return ArchiveResult{}, err
			}
			return ArchiveResult{RelativePath: relPath, Deduplicated: true}, nil
		}
		return ArchiveResult{}, fmt.Errorf("gallery: publish archive copy: %w", err)
	}
	published = true

	if err := store.RecordArchive(ctx, assetID, relPath, store.clock(), ArchiveStatusReady); err != nil {
		return ArchiveResult{}, err
	}
	return ArchiveResult{RelativePath: relPath, Deduplicated: false}, nil
}

func verifyExistingArchiveFile(ctx context.Context, path, expectedSHA string) (bool, error) {
	if _, err := checkLocalPath(ctx, path, false, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	st, err := statRegularFile(path)
	if err != nil {
		return false, err
	}
	if st.Reparse || st.IsDir || st.Links != 1 {
		return false, ErrUnsafePath
	}
	sum, _, err := HashFile(ctx, path, MaxImageBytes)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(sum, expectedSHA), nil
}

// DeleteArchiveCopy removes ONLY an Oddward-managed archive file strictly inside
// archiveDir/originals and clears its gallery_archive row. Original source files
// are never modified or removed.
func DeleteArchiveCopy(ctx context.Context, store *Store, archiveDir string, assetID int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rec, found, err := store.GetArchiveByAssetID(ctx, assetID)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	cleanArchive, err := ValidateArchiveDir(ctx, archiveDir)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrArchiveUnavailable, err)
	}
	normRel, err := NormalizeRelativePath(rec.RelativePath)
	if err != nil || !strings.HasPrefix(normRel, "originals/") {
		return ErrUnsafePath
	}
	full, err := checkSafeSubpath(ctx, cleanArchive, normRel, false, true)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return store.DeleteArchiveRecord(ctx, assetID)
}

// ExportAsset writes an exact copy of one asset to destPath via a same-volume
// temporary file and atomic replacement, verifying content integrity.
func ExportAsset(ctx context.Context, asset Asset, destPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if asset.Invalid || !asset.Available || asset.PrimaryPath == "" {
		return ErrNotFound
	}
	srcPath, err := checkLocalPath(ctx, asset.PrimaryPath, false, false)
	if err != nil {
		return err
	}
	cleanDest, err := checkLocalPath(ctx, destPath, false, true)
	if err != nil {
		return err
	}
	destDir := filepath.Dir(cleanDest)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	if _, err := checkLocalPath(ctx, destDir, true, false); err != nil {
		return err
	}

	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()
	st, err := statOpenFile(src)
	if err != nil {
		return err
	}
	if st.Reparse || st.IsDir || st.Size <= 0 || st.Size > MaxImageBytes {
		return ErrInvalidMedia
	}

	free, err := freeSpaceFn(destDir)
	if err == nil && free < st.Size {
		return fmt.Errorf("%w: insufficient space for export", ErrDiskFull)
	}

	stage, err := os.CreateTemp(destDir, ".oddward-export-*.tmp")
	if err != nil {
		return err
	}
	stagePath := stage.Name()
	committed := false
	defer func() {
		_ = stage.Close()
		if !committed {
			_ = os.Remove(stagePath)
		}
	}()

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(stage, h), io.LimitReader(src, MaxImageBytes+1)); err != nil {
		if isDiskFullError(err) {
			return fmt.Errorf("%w: %v", ErrDiskFull, err)
		}
		return err
	}
	if asset.SHA256 != "" && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), asset.SHA256) {
		return fmt.Errorf("%w: exported bytes diverged from indexed digest", ErrFileUnstable)
	}
	if err := stage.Sync(); err != nil {
		return err
	}
	if err := stage.Close(); err != nil {
		return err
	}
	if err := replaceFile(stagePath, cleanDest); err != nil {
		return err
	}
	committed = true
	return nil
}

// ExportBatch exports multiple assets into destDir without silently overwriting
// existing files (colliding filenames receive " (1)", " (2)", ... suffixes).
func ExportBatch(ctx context.Context, assets []Asset, destDir string) (ExportReport, error) {
	cleanDir, err := checkLocalPath(ctx, destDir, true, false)
	if err != nil {
		return ExportReport{}, err
	}
	var report ExportReport
	var firstErr error
	for _, a := range assets {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		name := SuggestedExportFileName(a)
		target := uniqueFilePath(cleanDir, name)
		if err := ExportAsset(ctx, a, target); err != nil {
			report.Failed++
			if firstErr == nil {
				firstErr = err
			}
			if errors.Is(err, ErrDiskFull) {
				return report, err
			}
			continue
		}
		report.Exported++
	}
	return report, firstErr
}

// SuggestedExportFileName derives a safe default file name for saving asset.
func SuggestedExportFileName(a Asset) string {
	ext := ExtensionForMIME(a.MIMEType)
	if ext == "" {
		ext = ".png"
	}
	base := strings.TrimSpace(a.FileName)
	if base == "" {
		if len(a.SHA256) >= 12 {
			base = "hethereau-" + a.SHA256[:12] + ext
		} else {
			base = fmt.Sprintf("hethereau-%d%s", a.ID, ext)
		}
	}
	base = filepath.Base(base)
	base = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 0x20 {
			return '_'
		}
		return r
	}, base)
	if filepath.Ext(base) == "" {
		base += ext
	}
	return base
}

func uniqueFilePath(dir, name string) string {
	candidate := filepath.Join(dir, name)
	if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
		return candidate
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; i < 10000; i++ {
		next := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		if _, err := os.Lstat(next); errors.Is(err, os.ErrNotExist) {
			return next
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, time.Now().UnixNano(), ext))
}

func isDiskFullError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not enough space") ||
		strings.Contains(msg, "disk full") ||
		strings.Contains(msg, "no space left") ||
		strings.Contains(msg, "0x70")
}
