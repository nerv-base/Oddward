// SPDX-License-Identifier: GPL-3.0-or-later

package gallery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const (
	scanBatchSize        = 100
	defaultStableWait    = 120 * time.Millisecond
	defaultStableRetries = 3
)

// Scanner incrementally indexes enabled media sources and optionally archives
// newly discovered game-cache images when auto-archive is enabled.
type Scanner struct {
	store         *Store
	stableWait    time.Duration
	stableRetries int
}

// NewScanner builds a Scanner backed by store.
func NewScanner(store *Store) *Scanner {
	return &Scanner{
		store:         store,
		stableWait:    defaultStableWait,
		stableRetries: defaultStableRetries,
	}
}

// ScanOptions configures one scan pass.
type ScanOptions struct {
	ArchiveDir  string
	AutoArchive bool
}

// ScanAll scans all enabled sources, updates SQLite incrementally and optionally
// archives newly indexed game-cache images.
func (sc *Scanner) ScanAll(ctx context.Context, sources []Source, opts ScanOptions) (ScanReport, error) {
	var total ScanReport
	for _, src := range sources {
		if !src.Enabled {
			continue
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
		sub, err := sc.ScanSource(ctx, src, opts)
		total.SourcesScanned += sub.SourcesScanned
		total.FilesSeen += sub.FilesSeen
		total.Unchanged += sub.Unchanged
		total.Added += sub.Added
		total.Updated += sub.Updated
		total.MissingMarked += sub.MissingMarked
		total.InvalidCount += sub.InvalidCount
		total.Archived += sub.Archived
		total.ArchiveSkipped += sub.ArchiveSkipped
		total.ArchiveErrors += sub.ArchiveErrors
		if sub.DiskFull {
			total.DiskFull = true
			opts.AutoArchive = false // stop further archiving after disk full
		}
		if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return total, err
		}
	}
	return total, nil
}

// ScanSource incrementally walks one source root, skipping unchanged files via
// (file_size, modified_at, file_identity) and batching SQLite commits.
func (sc *Scanner) ScanSource(ctx context.Context, src Source, opts ScanOptions) (ScanReport, error) {
	var report ScanReport
	if err := ctx.Err(); err != nil {
		return report, err
	}
	cleanRoot, err := checkLocalPath(ctx, src.RootPath, true, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Source folder currently absent: mark existing locations unavailable.
			marked, merr := sc.store.MarkMissingLocations(ctx, src.ID, nil)
			report.MissingMarked = marked
			return report, merr
		}
		return report, err
	}
	report.SourcesScanned = 1

	existing, err := sc.store.SourceLocationMap(ctx, src.ID)
	if err != nil {
		return report, err
	}

	now := sc.store.clock()
	seen := make(map[string]bool, len(existing))
	var touchIDs []int64
	var pending []LocationUpsert
	type archiveCandidate struct {
		path     string
		sha256   string
		mimeType string
	}
	var toArchive []archiveCandidate

	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		batchRes, err := sc.store.UpsertAssetLocationsBatch(ctx, pending)
		if err != nil {
			return err
		}
		report.Added += batchRes.Added
		report.Updated += batchRes.Updated
		if opts.AutoArchive && src.SourceType == SourceGameCache && opts.ArchiveDir != "" && !report.DiskFull {
			for i, item := range pending {
				if item.Invalid {
					continue
				}
				toArchive = append(toArchive, archiveCandidate{
					path:     joinNormalized(cleanRoot, item.RelativePath),
					sha256:   item.SHA256,
					mimeType: item.Info.MIMEType,
				})
				_ = batchRes.AssetIDs[i]
			}
		}
		pending = pending[:0]
		return nil
	}

	walkErr := filepath.WalkDir(cleanRoot, func(path string, d fs.DirEntry, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			return nil
		}
		if path == cleanRoot {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(cleanRoot, path)
		if rerr != nil {
			return nil
		}
		normRel, rerr := NormalizeRelativePath(rel)
		if rerr != nil {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			reparse, rerr := isDirReparse(path)
			if rerr != nil || reparse {
				return filepath.SkipDir
			}
			return nil
		}
		if IsIgnoredCandidate(src.SourceType, normRel) {
			return nil
		}

		st, serr := statRegularFile(path)
		if serr != nil || st.Reparse || st.IsDir {
			return nil
		}
		seen[normRel] = true
		report.FilesSeen++

		if prev, ok := existing[normRel]; ok &&
			prev.Available &&
			prev.FileSize == st.Size &&
			prev.ModifiedAt.Equal(st.ModifiedAt) &&
			(prev.FileIdentity == "" || prev.FileIdentity == st.Identity) {
			report.Unchanged++
			touchIDs = append(touchIDs, prev.ID)
			return nil
		}

		item, ok := sc.inspectForIndex(ctx, src.ID, normRel, path, st, now)
		if !ok {
			return nil
		}
		if item.Invalid {
			report.InvalidCount++
		}
		pending = append(pending, item)
		if len(pending) >= scanBatchSize {
			return flush()
		}
		return nil
	})
	if walkErr != nil {
		return report, walkErr
	}
	if err := flush(); err != nil {
		return report, err
	}
	if err := sc.store.TouchLocationsSeen(ctx, touchIDs, now); err != nil {
		return report, err
	}
	marked, err := sc.store.MarkMissingLocations(ctx, src.ID, seen)
	if err != nil {
		return report, err
	}
	report.MissingMarked = marked
	_ = sc.store.TouchSourceScan(ctx, src.ID, now)

	// Archive newly discovered game-cache files after indexing completes.
	if opts.AutoArchive && src.SourceType == SourceGameCache && opts.ArchiveDir != "" && !report.DiskFull {
		for _, cand := range toArchive {
			if err := ctx.Err(); err != nil {
				return report, err
			}
			assetID, found, err := sc.store.AssetIDBySHA256(ctx, cand.sha256)
			if err != nil || !found {
				continue
			}
			if rec, has, err := sc.store.GetArchiveByAssetID(ctx, assetID); err == nil && has && rec.Status == ArchiveStatusReady {
				report.ArchiveSkipped++
				continue
			}
			res, err := ArchiveFile(ctx, sc.store, opts.ArchiveDir, assetID, cand.path, cand.sha256, cand.mimeType)
			if err != nil {
				if errors.Is(err, ErrDiskFull) {
					report.DiskFull = true
					report.ArchiveErrors++
					break
				}
				report.ArchiveErrors++
				continue
			}
			if res.Deduplicated {
				report.ArchiveSkipped++
			} else {
				report.Archived++
			}
		}
	}

	return report, nil
}

// ScanSingleFile handles one file event from the watcher: waits for the file to
// stabilize, validates headers, updates SQLite and auto-archives if enabled.
func (sc *Scanner) ScanSingleFile(ctx context.Context, src Source, relPath string, opts ScanOptions) (changed bool, diskFull bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, false, err
	}
	normRel, err := NormalizeRelativePath(relPath)
	if err != nil {
		return false, false, nil
	}
	if IsIgnoredCandidate(src.SourceType, normRel) {
		return false, false, nil
	}
	fullPath, err := checkSafeSubpath(ctx, src.RootPath, normRel, false, false)
	if errors.Is(err, os.ErrNotExist) {
		if merr := sc.store.MarkLocationUnavailable(ctx, src.ID, normRel); merr != nil {
			return false, false, merr
		}
		return true, false, nil
	}
	if err != nil {
		return false, false, nil
	}

	st, err := sc.waitFileStable(ctx, fullPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			_ = sc.store.MarkLocationUnavailable(ctx, src.ID, normRel)
			return true, false, nil
		}
		return false, false, err
	}

	now := sc.store.clock()
	item, ok := sc.inspectForIndex(ctx, src.ID, normRel, fullPath, st, now)
	if !ok {
		return false, false, nil
	}
	res, err := sc.store.UpsertAssetLocationsBatch(ctx, []LocationUpsert{item})
	if err != nil {
		return false, false, err
	}

	if opts.AutoArchive && src.SourceType == SourceGameCache && opts.ArchiveDir != "" && !item.Invalid && len(res.AssetIDs) == 1 {
		if _, aerr := ArchiveFile(ctx, sc.store, opts.ArchiveDir, res.AssetIDs[0], fullPath, item.SHA256, item.Info.MIMEType); aerr != nil {
			if errors.Is(aerr, ErrDiskFull) {
				return true, true, aerr
			}
		}
	}
	return true, false, nil
}

// ArchiveExistingGameCache performs a one-time archive pass over all currently
// indexed, available, valid game-cache assets that do not yet have a ready copy.
func (sc *Scanner) ArchiveExistingGameCache(ctx context.Context, archiveDir string) (ArchiveReport, error) {
	var report ArchiveReport
	cleanDir, err := ValidateArchiveDir(ctx, archiveDir)
	if err != nil {
		return report, fmt.Errorf("%w: %v", ErrArchiveUnavailable, err)
	}
	assets, _, _, _, err := sc.store.QueryAssets(ctx, cleanDir, Filter{Scope: ScopeCached})
	if err != nil {
		return report, err
	}
	for _, a := range assets {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if a.Invalid || !a.Available || a.HasArchived {
			report.Skipped++
			continue
		}
		srcPath := a.SourcePath
		if srcPath == "" {
			report.Skipped++
			continue
		}
		res, aerr := ArchiveFile(ctx, sc.store, cleanDir, a.ID, srcPath, a.SHA256, a.MIMEType)
		if aerr != nil {
			if errors.Is(aerr, ErrDiskFull) {
				report.DiskFull = true
				report.Failed++
				return report, aerr
			}
			report.Failed++
			continue
		}
		if res.Deduplicated {
			report.Dedups++
		} else {
			report.Archived++
		}
	}
	return report, nil
}

func (sc *Scanner) inspectForIndex(ctx context.Context, sourceID int64, normRel, fullPath string, st fileStat, now time.Time) (LocationUpsert, bool) {
	info, digest, finalStat, err := HashAndInspectFile(ctx, fullPath)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrFileUnstable) || errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrUnsafePath) {
		return LocationUpsert{}, false
	}
	if err != nil {
		// Record corrupt/oversized image candidate so incremental scans do not
		// re-read it repeatedly unless the file actually changes on disk.
		if !ValidSHA256Hex(digest) {
			sum := sha256.Sum256([]byte(fmt.Sprintf("invalid:%d:%s:%d:%d", sourceID, normRel, st.Size, st.ModifiedAt.UnixNano())))
			digest = hex.EncodeToString(sum[:])
		}
		reason := classifyInvalidReason(err)
		mt := info.MediaType
		if mt == "" {
			mt = MediaImage
		}
		return LocationUpsert{
			SourceID:      sourceID,
			RelativePath:  normRel,
			SHA256:        digest,
			Info:          MediaInfo{MediaType: mt, MIMEType: "application/octet-stream", ByteSize: st.Size},
			FileSize:      st.Size,
			ModifiedAt:    st.ModifiedAt,
			FileIdentity:  st.Identity,
			Invalid:       true,
			InvalidReason: reason,
			SeenAt:        now,
		}, true
	}
	return LocationUpsert{
		SourceID:     sourceID,
		RelativePath: normRel,
		SHA256:       digest,
		Info:         info,
		FileSize:     finalStat.Size,
		ModifiedAt:   finalStat.ModifiedAt,
		FileIdentity: finalStat.Identity,
		SeenAt:       now,
	}, true
}

func classifyInvalidReason(err error) string {
	switch {
	case errors.Is(err, ErrTooLarge):
		return "文件尺寸或体积超出安全上限"
	case errors.Is(err, ErrInvalidMedia):
		return "媒体文件已损坏或格式不受支持"
	default:
		return "无法读取媒体文件"
	}
}

// waitFileStable confirms a newly created or modified file has stopped growing
// and can be opened cleanly before hashing/archiving.
func (sc *Scanner) waitFileStable(ctx context.Context, path string) (fileStat, error) {
	wait := sc.stableWait
	if wait <= 0 {
		wait = defaultStableWait
	}
	retries := sc.stableRetries
	if retries <= 0 {
		retries = defaultStableRetries
	}
	var prev fileStat
	havePrev := false
	for attempt := 0; attempt <= retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return fileStat{}, err
		}
		st, err := statRegularFile(path)
		if err != nil {
			return fileStat{}, err
		}
		if st.Reparse || st.IsDir {
			return fileStat{}, ErrUnsafePath
		}
		if st.Size > 0 {
			// Verify the file can be opened for read (not exclusively locked by writer).
			if f, oerr := os.Open(path); oerr == nil {
				_ = f.Close()
				if havePrev && st.Size == prev.Size && st.ModifiedAt.Equal(prev.ModifiedAt) && st.Identity == prev.Identity {
					return st, nil
				}
				prev = st
				havePrev = true
			}
		}
		if attempt == retries {
			break
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fileStat{}, ctx.Err()
		case <-timer.C:
		}
	}
	if havePrev {
		return prev, nil
	}
	return fileStat{}, ErrFileUnstable
}
