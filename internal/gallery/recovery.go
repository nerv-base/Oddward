// SPDX-License-Identifier: GPL-3.0-or-later

package gallery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// RecoveryReport summarizes archive-vs-database reconciliation work done after
// startup or an unclean exit.
type RecoveryReport struct {
	StagesRemoved  int
	LinkedRecords  int
	OrphansAdopted int
	MissingMarked  int
	CorruptMarked  int
}

// ReconcileArchive aligns the filesystem archive under archiveDir with SQLite:
//  1. Removes leftover .oddward-stage-*.tmp files from interrupted copies.
//  2. Adopts valid published archive files whose SQLite commit never finished
//     (linking to an existing asset row or creating an archived-only asset row).
//  3. Verifies existing gallery_archive rows still exist on disk and marks
//     vanished or damaged files honestly without deleting metadata.
func ReconcileArchive(ctx context.Context, store *Store, archiveDir string) (RecoveryReport, error) {
	if err := ctx.Err(); err != nil {
		return RecoveryReport{}, err
	}
	if store == nil || archiveDir == "" {
		return RecoveryReport{}, nil
	}
	cleanArchive, err := ValidateArchiveDir(ctx, archiveDir)
	if err != nil {
		return RecoveryReport{}, err
	}

	var report RecoveryReport
	originalsDir := filepath.Join(cleanArchive, "originals")

	// Step 1 & 2: walk originals/<xx>/ when the directory exists.
	if _, err := checkLocalPath(ctx, originalsDir, true, false); err == nil {
		buckets, err := os.ReadDir(originalsDir)
		if err != nil {
			return report, err
		}
		for _, bucket := range buckets {
			if err := ctx.Err(); err != nil {
				return report, err
			}
			if !bucket.IsDir() || len(bucket.Name()) != 2 {
				continue
			}
			bucketPath := filepath.Join(originalsDir, bucket.Name())
			reparse, err := isDirReparse(bucketPath)
			if err != nil || reparse {
				continue
			}
			entries, err := os.ReadDir(bucketPath)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if err := ctx.Err(); err != nil {
					return report, err
				}
				if entry.IsDir() {
					continue
				}
				name := entry.Name()
				fullPath := filepath.Join(bucketPath, name)
				if strings.HasPrefix(name, ".oddward-stage-") && strings.HasSuffix(name, ".tmp") {
					if _, err := checkLocalPath(ctx, fullPath, false, false); err == nil {
						if os.Remove(fullPath) == nil {
							report.StagesRemoved++
						}
					}
					continue
				}
				ext := strings.ToLower(filepath.Ext(name))
				stem := strings.ToLower(strings.TrimSuffix(name, ext))
				if !ValidSHA256Hex(stem) || !strings.EqualFold(stem[:2], bucket.Name()) {
					continue
				}
				relPath := "originals/" + strings.ToLower(bucket.Name()) + "/" + stem + ext
				// Check if already recorded as ready in SQLite.
				assetID, found, err := store.AssetIDBySHA256(ctx, stem)
				if err != nil {
					return report, err
				}
				if found {
					if rec, hasRec, err := store.GetArchiveByAssetID(ctx, assetID); err == nil && hasRec && rec.Status == ArchiveStatusReady && rec.RelativePath == relPath {
						continue
					}
				}
				// Unrecorded or previously missing file on disk: verify content hash and header.
				if _, err := checkLocalPath(ctx, fullPath, false, false); err != nil {
					continue
				}
				info, actualSHA, st, err := HashAndInspectFile(ctx, fullPath)
				if err != nil || !strings.EqualFold(actualSHA, stem) || st.Links != 1 {
					continue
				}
				if found {
					if err := store.RecordArchive(ctx, assetID, relPath, st.ModifiedAt, ArchiveStatusReady); err != nil {
						return report, err
					}
					report.LinkedRecords++
				} else {
					if _, err := store.UpsertArchivedOnlyAsset(ctx, stem, info, relPath, st.ModifiedAt); err != nil {
						return report, err
					}
					report.OrphansAdopted++
				}
			}
		}
	}

	// Step 3: verify recorded archive rows still exist on disk.
	records, err := store.ListArchiveRecords(ctx)
	if err != nil {
		return report, err
	}
	for _, rec := range records {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		full, err := checkSafeSubpath(ctx, cleanArchive, rec.RelativePath, false, false)
		if errors.Is(err, os.ErrNotExist) {
			if rec.Status != ArchiveStatusMissing {
				if uerr := store.UpdateArchiveStatus(ctx, rec.AssetID, ArchiveStatusMissing); uerr != nil {
					return report, uerr
				}
				report.MissingMarked++
			}
			continue
		}
		if err != nil {
			if rec.Status != ArchiveStatusCorrupt {
				if uerr := store.UpdateArchiveStatus(ctx, rec.AssetID, ArchiveStatusCorrupt); uerr != nil {
					return report, uerr
				}
				report.CorruptMarked++
			}
			continue
		}
		st, err := statRegularFile(full)
		if err != nil || st.Reparse || st.IsDir || st.Links != 1 || (rec.ByteSize > 0 && st.Size != rec.ByteSize) {
			if rec.Status != ArchiveStatusCorrupt {
				if uerr := store.UpdateArchiveStatus(ctx, rec.AssetID, ArchiveStatusCorrupt); uerr != nil {
					return report, uerr
				}
				report.CorruptMarked++
			}
			continue
		}
		if rec.Status != ArchiveStatusReady {
			// Verify full digest before restoring a previously missing/corrupt row.
			if sum, _, herr := HashFile(ctx, full, MaxImageBytes); herr == nil && strings.EqualFold(sum, rec.SHA256) {
				if uerr := store.UpdateArchiveStatus(ctx, rec.AssetID, ArchiveStatusReady); uerr != nil {
					return report, uerr
				}
				report.LinkedRecords++
			}
		}
	}

	return report, nil
}
