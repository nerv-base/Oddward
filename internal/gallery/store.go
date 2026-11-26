// SPDX-License-Identifier: GPL-3.0-or-later

package gallery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
)

// Store persists gallery settings, discovered sources, deduplicated assets,
// multi-source file locations and archive records in Oddward's SQLite database.
type Store struct {
	db  *database.DB
	now func() time.Time
}

// NewStore wraps an open Oddward database.
func NewStore(db *database.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("gallery: database is required")
	}
	return &Store{db: db, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *Store) clock() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

// LoadSettings returns the persisted gallery preferences or DefaultSettings for
// a fresh database.
func (s *Store) LoadSettings(ctx context.Context) (Settings, error) {
	var autoArchive, autoDiscover, thumbMB int
	var archiveDir, customBG string
	err := s.db.QueryRow(ctx, `
		SELECT auto_archive, archive_dir, auto_discover_game, thumb_cache_mb, custom_background
		FROM gallery_settings WHERE id = 1`,
	).Scan(&autoArchive, &archiveDir, &autoDiscover, &thumbMB, &customBG)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultSettings(), nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("gallery: load settings: %w", err)
	}
	return Settings{
		AutoArchive:      autoArchive == 1,
		ArchiveDir:       archiveDir,
		AutoDiscoverGame: autoDiscover == 1,
		ThumbCacheMB:     thumbMB,
		CustomBackground: customBG,
	}, nil
}

// SaveSettings validates and persists gallery preferences without clobbering
// the persisted custom launcher background path.
func (s *Store) SaveSettings(ctx context.Context, cfg Settings) error {
	if cfg.ThumbCacheMB == 0 {
		cfg.ThumbCacheMB = DefaultThumbCacheMB
	}
	if cfg.ThumbCacheMB < MinThumbCacheMB || cfg.ThumbCacheMB > MaxThumbCacheMB {
		return fmt.Errorf("%w: thumb_cache_mb %d out of range [%d,%d]", ErrInvalidSettings, cfg.ThumbCacheMB, MinThumbCacheMB, MaxThumbCacheMB)
	}
	if cfg.ArchiveDir != "" {
		clean, err := ValidateArchiveDir(ctx, cfg.ArchiveDir)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidSettings, err)
		}
		cfg.ArchiveDir = clean
	}
	return s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO gallery_settings(id, auto_archive, archive_dir, auto_discover_game, thumb_cache_mb, updated_at)
			VALUES(1, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				auto_archive = excluded.auto_archive,
				archive_dir = excluded.archive_dir,
				auto_discover_game = excluded.auto_discover_game,
				thumb_cache_mb = excluded.thumb_cache_mb,
				updated_at = excluded.updated_at`,
			boolInt(cfg.AutoArchive), cfg.ArchiveDir, boolInt(cfg.AutoDiscoverGame), cfg.ThumbCacheMB, database.Stamp(s.clock()),
		)
		return err
	})
}

// SaveCustomBackground persists or clears the active custom static background
// file path without modifying other gallery settings.
func (s *Store) SaveCustomBackground(ctx context.Context, path string) error {
	def := DefaultSettings()
	return s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO gallery_settings(id, auto_archive, archive_dir, auto_discover_game, thumb_cache_mb, custom_background, updated_at)
			VALUES(1, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				custom_background = excluded.custom_background,
				updated_at = excluded.updated_at`,
			boolInt(def.AutoArchive), def.ArchiveDir, boolInt(def.AutoDiscoverGame), def.ThumbCacheMB, path, database.Stamp(s.clock()),
		)
		return err
	})
}

// UpsertSource registers or updates a source directory and returns its row ID.
func (s *Store) UpsertSource(ctx context.Context, src Source) (Source, error) {
	if src.RootPath == "" {
		return Source{}, fmt.Errorf("%w: empty source root", ErrUnsafePath)
	}
	clean := filepath.Clean(src.RootPath)
	src.RootPath = clean
	var id int64
	err := s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO gallery_sources(root_path, source_type, channel, uid, enabled, last_scan_at)
			VALUES(?, ?, ?, ?, ?, ?)
			ON CONFLICT(root_path) DO UPDATE SET
				source_type = excluded.source_type,
				channel = excluded.channel,
				uid = excluded.uid,
				enabled = excluded.enabled`,
			src.RootPath, string(src.SourceType), nullString(src.Channel), nullString(src.UID),
			boolInt(src.Enabled), database.Stamp(src.LastScanAt),
		)
		if err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT id FROM gallery_sources WHERE root_path = ?`, src.RootPath).Scan(&id)
	})
	if err != nil {
		return Source{}, fmt.Errorf("gallery: upsert source: %w", err)
	}
	src.ID = id
	return src, nil
}

// ListSources returns all registered sources ordered by type and path.
func (s *Store) ListSources(ctx context.Context) ([]Source, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, root_path, source_type, channel, uid, enabled, last_scan_at
		FROM gallery_sources
		ORDER BY source_type, uid, root_path`)
	if err != nil {
		return nil, fmt.Errorf("gallery: list sources: %w", err)
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		var src Source
		var st string
		var channel, uid sql.NullString
		var enabled int
		var lastScan sql.NullInt64
		if err := rows.Scan(&src.ID, &src.RootPath, &st, &channel, &uid, &enabled, &lastScan); err != nil {
			return nil, err
		}
		src.SourceType = SourceType(st)
		src.Channel = channel.String
		src.UID = uid.String
		src.Enabled = enabled == 1
		src.LastScanAt = database.StampTime(lastScan)
		out = append(out, src)
	}
	return out, rows.Err()
}

// RemoveCustomSource deletes a user-added custom source and its location rows.
// Any archived assets remain intact in gallery_assets and gallery_archive.
func (s *Store) RemoveCustomSource(ctx context.Context, sourceID int64) error {
	return s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM gallery_sources WHERE id = ? AND source_type = 'custom'`, sourceID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// DisableSourcesByTypeNotIn disables stale auto-discovered sources (for example
// after switching game installation root) without deleting archive records.
func (s *Store) DisableSourcesByTypeNotIn(ctx context.Context, srcType SourceType, activeRoots map[string]bool) error {
	return s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, root_path FROM gallery_sources WHERE source_type = ? AND enabled = 1`, string(srcType))
		if err != nil {
			return err
		}
		var staleIDs []int64
		for rows.Next() {
			var id int64
			var root string
			if err := rows.Scan(&id, &root); err != nil {
				rows.Close()
				return err
			}
			if !activeRoots[strings.ToLower(filepath.Clean(root))] {
				staleIDs = append(staleIDs, id)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, id := range staleIDs {
			if _, err := tx.ExecContext(ctx, `UPDATE gallery_sources SET enabled = 0 WHERE id = ?`, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// TouchSourceScan records the latest completed scan timestamp for sourceID.
func (s *Store) TouchSourceScan(ctx context.Context, sourceID int64, at time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE gallery_sources SET last_scan_at = ? WHERE id = ?`, database.Stamp(at), sourceID)
	return err
}

// LocationMeta holds the lightweight change-detection tuple stored per location.
type LocationMeta struct {
	ID           int64
	AssetID      int64
	FileSize     int64
	ModifiedAt   time.Time
	FileIdentity string
	Available    bool
}

// SourceLocationMap returns all existing locations for sourceID keyed by
// normalized relative_path, enabling fast incremental scans.
func (s *Store) SourceLocationMap(ctx context.Context, sourceID int64) (map[string]LocationMeta, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, asset_id, relative_path, file_size, modified_at, file_identity, available
		FROM gallery_asset_locations
		WHERE source_id = ?`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]LocationMeta)
	for rows.Next() {
		var rel string
		var m LocationMeta
		var mod int64
		var ident sql.NullString
		var avail int
		if err := rows.Scan(&m.ID, &m.AssetID, &rel, &m.FileSize, &mod, &ident, &avail); err != nil {
			return nil, err
		}
		m.ModifiedAt = database.StampInt(mod)
		m.FileIdentity = ident.String
		m.Available = avail == 1
		out[rel] = m
	}
	return out, rows.Err()
}

// LocationUpsert describes one indexed asset and location to commit in a batch.
type LocationUpsert struct {
	SourceID      int64
	RelativePath  string
	SHA256        string
	Info          MediaInfo
	FileSize      int64
	ModifiedAt    time.Time
	FileIdentity  string
	Invalid       bool
	InvalidReason string
	SeenAt        time.Time
}

// UpsertBatchResult reports IDs created or updated by UpsertAssetLocationsBatch.
type UpsertBatchResult struct {
	AssetIDs []int64
	Added    int
	Updated  int
}

// UpsertAssetLocationsBatch inserts or updates deduplicated assets and their
// file locations inside a single SQLite write transaction.
func (s *Store) UpsertAssetLocationsBatch(ctx context.Context, items []LocationUpsert) (UpsertBatchResult, error) {
	if len(items) == 0 {
		return UpsertBatchResult{}, nil
	}
	res := UpsertBatchResult{AssetIDs: make([]int64, len(items))}
	err := s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for i, item := range items {
			assetID, created, err := upsertAssetTx(ctx, tx, item)
			if err != nil {
				return err
			}
			res.AssetIDs[i] = assetID
			if created {
				res.Added++
			} else {
				res.Updated++
			}
			_, err = tx.ExecContext(ctx, `
				INSERT INTO gallery_asset_locations(
					asset_id, source_id, relative_path, file_size, modified_at, file_identity, available, last_seen_at)
				VALUES(?, ?, ?, ?, ?, ?, 1, ?)
				ON CONFLICT(source_id, relative_path) DO UPDATE SET
					asset_id = excluded.asset_id,
					file_size = excluded.file_size,
					modified_at = excluded.modified_at,
					file_identity = excluded.file_identity,
					available = 1,
					last_seen_at = excluded.last_seen_at`,
				assetID, item.SourceID, item.RelativePath, item.FileSize,
				database.Stamp(item.ModifiedAt), nullString(item.FileIdentity), database.Stamp(item.SeenAt),
			)
			if err != nil {
				return err
			}
		}
		return nil
	})
	return res, err
}

func upsertAssetTx(ctx context.Context, tx *sql.Tx, item LocationUpsert) (assetID int64, created bool, err error) {
	var existingID int64
	var existingInvalid int
	err = tx.QueryRowContext(ctx, `SELECT id, invalid FROM gallery_assets WHERE sha256 = ?`, item.SHA256).Scan(&existingID, &existingInvalid)
	if err == nil {
		// Upgrade previously invalid row if valid bytes with same digest exist,
		// or backfill missing EXIF/video metadata discovered from another location.
		if existingInvalid == 1 && !item.Invalid {
			_, err = tx.ExecContext(ctx, `
				UPDATE gallery_assets
				SET media_type = ?, mime_type = ?, byte_size = ?, width = ?, height = ?,
				    duration_ms = COALESCE(?, duration_ms),
				    video_codec = CASE WHEN ? != '' THEN ? ELSE video_codec END,
				    audio_codec = CASE WHEN ? != '' THEN ? ELSE audio_codec END,
				    poster_path = CASE WHEN ? != '' THEN ? ELSE poster_path END,
				    captured_at = COALESCE(captured_at, ?),
				    invalid = 0, invalid_reason = ''
				WHERE id = ?`,
				string(item.Info.MediaType), item.Info.MIMEType, item.Info.ByteSize, item.Info.Width, item.Info.Height,
				nullInt64(item.Info.DurationMS),
				item.Info.VideoCodec, item.Info.VideoCodec,
				item.Info.AudioCodec, item.Info.AudioCodec,
				item.Info.PosterPath, item.Info.PosterPath,
				database.Stamp(item.Info.CapturedAt), existingID,
			)
			if err != nil {
				return 0, false, err
			}
		} else if !item.Info.CapturedAt.IsZero() || item.Info.DurationMS > 0 || item.Info.VideoCodec != "" || item.Info.AudioCodec != "" || item.Info.PosterPath != "" {
			_, err = tx.ExecContext(ctx, `
				UPDATE gallery_assets
				SET captured_at = COALESCE(captured_at, ?),
				    duration_ms = COALESCE(duration_ms, ?),
				    video_codec = CASE WHEN video_codec = '' AND ? != '' THEN ? ELSE video_codec END,
				    audio_codec = CASE WHEN audio_codec = '' AND ? != '' THEN ? ELSE audio_codec END,
				    poster_path = CASE WHEN poster_path = '' AND ? != '' THEN ? ELSE poster_path END
				WHERE id = ?`,
				database.Stamp(item.Info.CapturedAt),
				nullInt64(item.Info.DurationMS),
				item.Info.VideoCodec, item.Info.VideoCodec,
				item.Info.AudioCodec, item.Info.AudioCodec,
				item.Info.PosterPath, item.Info.PosterPath,
				existingID,
			)
			if err != nil {
				return 0, false, err
			}
		}
		return existingID, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	mime := item.Info.MIMEType
	if mime == "" {
		mime = "application/octet-stream"
	}
	mediaType := item.Info.MediaType
	if mediaType == "" {
		mediaType = MediaImage
	}
	byteSize := item.Info.ByteSize
	if byteSize <= 0 {
		byteSize = item.FileSize
	}
	reason := item.InvalidReason
	if len(reason) > 128 {
		reason = reason[:128]
	}
	r, err := tx.ExecContext(ctx, `
		INSERT INTO gallery_assets(
			sha256, media_type, mime_type, byte_size, width, height, duration_ms,
			video_codec, audio_codec, poster_path,
			captured_at, discovered_at, invalid, invalid_reason)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.SHA256, string(mediaType), mime, byteSize, item.Info.Width, item.Info.Height,
		nullInt64(item.Info.DurationMS),
		item.Info.VideoCodec, item.Info.AudioCodec, item.Info.PosterPath,
		database.Stamp(item.Info.CapturedAt), database.Stamp(item.SeenAt),
		boolInt(item.Invalid), reason,
	)
	if err != nil {
		return 0, false, err
	}
	id, err := r.LastInsertId()
	return id, true, err
}

// UpsertArchivedOnlyAsset inserts or retrieves an asset discovered solely in the
// Oddward archive directory during crash recovery.
func (s *Store) UpsertArchivedOnlyAsset(ctx context.Context, sha256Hex string, info MediaInfo, relArchivePath string, archivedAt time.Time) (int64, error) {
	var assetID int64
	err := s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		id, _, err := upsertAssetTx(ctx, tx, LocationUpsert{
			SHA256:   sha256Hex,
			Info:     info,
			FileSize: info.ByteSize,
			SeenAt:   archivedAt,
		})
		if err != nil {
			return err
		}
		assetID = id
		_, err = tx.ExecContext(ctx, `
			INSERT INTO gallery_archive(asset_id, relative_path, archived_at, status)
			VALUES(?, ?, ?, 'ready')
			ON CONFLICT(asset_id) DO UPDATE SET
				relative_path = excluded.relative_path,
				archived_at = excluded.archived_at,
				status = 'ready'`,
			assetID, relArchivePath, database.Stamp(archivedAt),
		)
		return err
	})
	return assetID, err
}

// MarkMissingLocations marks locations for sourceID that are not present in seen
// as unavailable. It never deletes asset or archive rows.
func (s *Store) MarkMissingLocations(ctx context.Context, sourceID int64, seen map[string]bool) (int, error) {
	var marked int
	err := s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, relative_path FROM gallery_asset_locations
			WHERE source_id = ? AND available = 1`, sourceID)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			var rel string
			if err := rows.Scan(&id, &rel); err != nil {
				rows.Close()
				return err
			}
			if !seen[rel] {
				ids = append(ids, id)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE gallery_asset_locations SET available = 0 WHERE id = ?`, id); err != nil {
				return err
			}
			marked++
		}
		return nil
	})
	return marked, err
}

// MarkLocationUnavailable marks one specific location unavailable (on watcher delete/rename).
func (s *Store) MarkLocationUnavailable(ctx context.Context, sourceID int64, relPath string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE gallery_asset_locations SET available = 0
		WHERE source_id = ? AND relative_path = ?`, sourceID, relPath)
	return err
}

// TouchLocationSeen updates last_seen_at for unchanged files in one transaction.
func (s *Store) TouchLocationsSeen(ctx context.Context, ids []int64, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	stamp := database.Stamp(at)
	return s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `UPDATE gallery_asset_locations SET last_seen_at = ?, available = 1 WHERE id = ?`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, id := range ids {
			if _, err := stmt.ExecContext(ctx, stamp, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// RecordArchive commits or updates an archive copy row for assetID.
func (s *Store) RecordArchive(ctx context.Context, assetID int64, relPath string, archivedAt time.Time, status ArchiveStatus) error {
	return s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO gallery_archive(asset_id, relative_path, archived_at, status)
			VALUES(?, ?, ?, ?)
			ON CONFLICT(asset_id) DO UPDATE SET
				relative_path = excluded.relative_path,
				archived_at = excluded.archived_at,
				status = excluded.status`,
			assetID, relPath, database.Stamp(archivedAt), string(status),
		)
		return err
	})
}

// UpdateArchiveStatus updates the status of an existing archive record.
func (s *Store) UpdateArchiveStatus(ctx context.Context, assetID int64, status ArchiveStatus) error {
	_, err := s.db.Exec(ctx, `UPDATE gallery_archive SET status = ? WHERE asset_id = ?`, string(status), assetID)
	return err
}

// DeleteArchiveRecord removes the archive row for assetID (called only after the
// user explicitly confirmed deleting the Oddward-managed archive file).
func (s *Store) DeleteArchiveRecord(ctx context.Context, assetID int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM gallery_archive WHERE asset_id = ?`, assetID)
	return err
}

// GetArchiveByAssetID returns the archive record for assetID if present.
func (s *Store) GetArchiveByAssetID(ctx context.Context, assetID int64) (ArchiveRecord, bool, error) {
	var rec ArchiveRecord
	var archivedAt int64
	var status string
	err := s.db.QueryRow(ctx, `
		SELECT asset_id, relative_path, archived_at, status
		FROM gallery_archive WHERE asset_id = ?`, assetID).
		Scan(&rec.AssetID, &rec.RelativePath, &archivedAt, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return ArchiveRecord{}, false, nil
	}
	if err != nil {
		return ArchiveRecord{}, false, err
	}
	rec.ArchivedAt = database.StampInt(archivedAt)
	rec.Status = ArchiveStatus(status)
	return rec, true, nil
}

// AssetIDBySHA256 looks up an asset's ID by content hash.
func (s *Store) AssetIDBySHA256(ctx context.Context, sha256Hex string) (int64, bool, error) {
	var id int64
	err := s.db.QueryRow(ctx, `SELECT id FROM gallery_assets WHERE sha256 = ?`, sha256Hex).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// ListArchiveRecords returns every archive row along with its asset SHA256.
func (s *Store) ListArchiveRecords(ctx context.Context) ([]struct {
	ArchiveRecord
	SHA256   string
	ByteSize int64
}, error) {
	rows, err := s.db.Query(ctx, `
		SELECT g.asset_id, g.relative_path, g.archived_at, g.status, a.sha256, a.byte_size
		FROM gallery_archive g
		JOIN gallery_assets a ON a.id = g.asset_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []struct {
		ArchiveRecord
		SHA256   string
		ByteSize int64
	}
	for rows.Next() {
		var row struct {
			ArchiveRecord
			SHA256   string
			ByteSize int64
		}
		var at int64
		var status string
		if err := rows.Scan(&row.AssetID, &row.RelativePath, &at, &status, &row.SHA256, &row.ByteSize); err != nil {
			return nil, err
		}
		row.ArchivedAt = database.StampInt(at)
		row.Status = ArchiveStatus(status)
		out = append(out, row)
	}
	return out, rows.Err()
}

// QueryAssets loads deduplicated assets, resolves locations/sources/archives,
// organization metadata (favorites/albums/tags), applies filter/sort and
// returns aggregate Stats in one consistent read pass.
func (s *Store) QueryAssets(ctx context.Context, archiveDir string, filter Filter) ([]Asset, Stats, []string, bool, error) {
	assetRows, err := s.db.Query(ctx, `
		SELECT a.id, a.sha256, a.media_type, a.mime_type, a.byte_size, a.width, a.height,
		       a.duration_ms, a.video_codec, a.audio_codec, a.poster_path,
		       a.captured_at, a.discovered_at, a.invalid, a.invalid_reason,
		       ar.relative_path, ar.status
		FROM gallery_assets a
		LEFT JOIN gallery_archive ar ON ar.asset_id = a.id`)
	if err != nil {
		return nil, Stats{}, nil, false, fmt.Errorf("gallery: query assets: %w", err)
	}
	defer assetRows.Close()

	byID := make(map[int64]*Asset)
	all := make([]*Asset, 0, 256)
	for assetRows.Next() {
		var a Asset
		var mediaType string
		var durationMS, capturedAt sql.NullInt64
		var discoveredAt int64
		var invalid int
		var archiveRel, archiveStatus sql.NullString
		if err := assetRows.Scan(
			&a.ID, &a.SHA256, &mediaType, &a.MIMEType, &a.ByteSize, &a.Width, &a.Height,
			&durationMS, &a.VideoCodec, &a.AudioCodec, &a.PosterPath,
			&capturedAt, &discoveredAt, &invalid, &a.InvalidReason,
			&archiveRel, &archiveStatus,
		); err != nil {
			return nil, Stats{}, nil, false, err
		}
		a.MediaType = MediaType(mediaType)
		if durationMS.Valid {
			a.DurationMS = durationMS.Int64
		}
		a.CapturedAt = database.StampTime(capturedAt)
		a.DiscoveredAt = database.StampInt(discoveredAt)
		a.Invalid = invalid == 1
		if archiveRel.Valid && archiveRel.String != "" {
			a.ArchiveStatus = ArchiveStatus(archiveStatus.String)
			if archiveDir != "" {
				a.ArchivePath = joinNormalized(archiveDir, archiveRel.String)
			}
			if a.ArchiveStatus == ArchiveStatusReady && a.ArchivePath != "" {
				a.HasArchived = true
				a.Available = true
				a.PrimaryPath = a.ArchivePath
			}
		}
		ptr := &a
		byID[a.ID] = ptr
		all = append(all, ptr)
	}
	if err := assetRows.Err(); err != nil {
		return nil, Stats{}, nil, false, err
	}

	// Load favorites.
	if favRows, ferr := s.db.Query(ctx, `SELECT asset_id FROM gallery_favorites`); ferr == nil {
		for favRows.Next() {
			var aid int64
			if favRows.Scan(&aid) == nil {
				if a, ok := byID[aid]; ok {
					a.Favorited = true
				}
			}
		}
		_ = favRows.Close()
	}

	// Load album names and memberships for filtering & search.
	albumNames := make(map[int64]string)
	if albRows, aerr := s.db.Query(ctx, `SELECT id, name FROM gallery_albums`); aerr == nil {
		for albRows.Next() {
			var id int64
			var name string
			if albRows.Scan(&id, &name) == nil {
				albumNames[id] = name
			}
		}
		_ = albRows.Close()
	}
	if itemRows, ierr := s.db.Query(ctx, `SELECT album_id, asset_id FROM gallery_album_items ORDER BY album_id`); ierr == nil {
		for itemRows.Next() {
			var albID, aid int64
			if itemRows.Scan(&albID, &aid) == nil {
				if a, ok := byID[aid]; ok {
					a.AlbumIDs = append(a.AlbumIDs, albID)
				}
			}
		}
		_ = itemRows.Close()
	}

	// Load tags attached to assets.
	if tagRows, terr := s.db.Query(ctx, `
		SELECT at.asset_id, t.name
		FROM gallery_asset_tags at
		JOIN gallery_tags t ON t.id = at.tag_id
		ORDER BY t.name COLLATE NOCASE`); terr == nil {
		for tagRows.Next() {
			var aid int64
			var name string
			if tagRows.Scan(&aid, &name) == nil {
				if a, ok := byID[aid]; ok {
					a.Tags = append(a.Tags, name)
				}
			}
		}
		_ = tagRows.Close()
	}

	locRows, err := s.db.Query(ctx, `
		SELECT l.id, l.asset_id, l.source_id, s.source_type, s.root_path, s.uid,
		       l.relative_path, l.file_size, l.modified_at, l.file_identity, l.available, l.last_seen_at
		FROM gallery_asset_locations l
		JOIN gallery_sources s ON s.id = l.source_id
		WHERE s.enabled = 1`)
	if err != nil {
		return nil, Stats{}, nil, false, fmt.Errorf("gallery: query locations: %w", err)
	}
	defer locRows.Close()

	uidSets := make(map[int64]map[string]bool)
	allUIDSet := make(map[string]bool)
	hasUnknownUID := false

	for locRows.Next() {
		var loc Location
		var st string
		var uid, ident sql.NullString
		var mod, seen int64
		var avail int
		if err := locRows.Scan(
			&loc.ID, &loc.AssetID, &loc.SourceID, &st, &loc.SourceRoot, &uid,
			&loc.RelativePath, &loc.FileSize, &mod, &ident, &avail, &seen,
		); err != nil {
			return nil, Stats{}, nil, false, err
		}
		a, ok := byID[loc.AssetID]
		if !ok {
			continue
		}
		loc.SourceType = SourceType(st)
		loc.UID = uid.String
		loc.ModifiedAt = database.StampInt(mod)
		loc.FileIdentity = ident.String
		loc.Available = avail == 1
		loc.LastSeenAt = database.StampInt(seen)
		a.Locations = append(a.Locations, loc)

		if loc.ModifiedAt.After(a.ModifiedAt) {
			a.ModifiedAt = loc.ModifiedAt
		}
		if a.FileName == "" || (loc.Available && a.SourcePath == "") {
			a.FileName = filepath.Base(filepath.FromSlash(loc.RelativePath))
		}
		if loc.Available {
			a.Available = true
			full := loc.FullPath()
			if a.SourcePath == "" {
				a.SourcePath = full
			}
			if a.PrimaryPath == "" {
				a.PrimaryPath = full
			}
			switch loc.SourceType {
			case SourceGameCache:
				a.HasCached = true
			case SourceExported:
				a.HasExported = true
			case SourceCustom:
				a.HasCustom = true
			}
		}
		if loc.UID != "" {
			set := uidSets[a.ID]
			if set == nil {
				set = make(map[string]bool)
				uidSets[a.ID] = set
			}
			set[loc.UID] = true
			allUIDSet[loc.UID] = true
		}
	}
	if err := locRows.Err(); err != nil {
		return nil, Stats{}, nil, false, err
	}

	var stats Stats
	filtered := make([]Asset, 0, len(all))
	for _, a := range all {
		// Drop assets that neither have any location in an enabled source nor an archive record.
		if len(a.Locations) == 0 && !a.HasArchived && a.ArchivePath == "" {
			continue
		}
		if a.FileName == "" && a.ArchivePath != "" {
			a.FileName = filepath.Base(a.ArchivePath)
		}
		if !a.CapturedAt.IsZero() {
			a.DisplayTime = a.CapturedAt
		} else if !a.ModifiedAt.IsZero() {
			a.DisplayTime = a.ModifiedAt
		} else {
			a.DisplayTime = a.DiscoveredAt
		}
		if a.MediaType == MediaVideo {
			a.PosterPath = resolveVideoPosterPath(archiveDir, *a)
		}
		if set := uidSets[a.ID]; len(set) > 0 {
			a.UIDs = make([]string, 0, len(set))
			for u := range set {
				a.UIDs = append(a.UIDs, u)
			}
			sort.Strings(a.UIDs)
		} else {
			hasUnknownUID = true
		}

		stats.TotalAssets++
		if a.MediaType == MediaVideo {
			stats.VideoCount++
		} else {
			stats.PhotoCount++
		}
		if a.Favorited {
			stats.FavoriteCount++
		}
		if a.Invalid {
			stats.InvalidCount++
		} else {
			if a.HasCached {
				stats.CachedCount++
			}
			if a.HasExported || a.HasCustom {
				stats.ExportedCount++
			}
			if a.HasArchived {
				stats.ArchivedCount++
				stats.ArchiveCount++
				stats.ArchiveBytes += a.ByteSize
				if a.MediaType == MediaVideo {
					stats.ArchiveVideoCount++
					stats.ArchiveVideoBytes += a.ByteSize
				} else {
					stats.ArchivePhotoCount++
					stats.ArchivePhotoBytes += a.ByteSize
				}
			}
			if !a.Available {
				stats.MissingCount++
			}
		}

		if matchesFilter(*a, filter, albumNames) {
			filtered = append(filtered, *a)
		}
	}

	sortAssets(filtered, filter.Sort)

	uids := make([]string, 0, len(allUIDSet))
	for u := range allUIDSet {
		uids = append(uids, u)
	}
	sort.Strings(uids)
	return filtered, stats, uids, hasUnknownUID, nil
}

func resolveVideoPosterPath(archiveDir string, a Asset) string {
	// Prefer Oddward's archived poster backup so video thumbnails survive game cache cleanup.
	if archiveDir != "" && ValidSHA256Hex(a.SHA256) {
		if rel, err := ArchivePosterRelativePath(a.SHA256); err == nil {
			cand := joinNormalized(archiveDir, rel)
			if st, serr := statRegularFile(cand); serr == nil && !st.Reparse && !st.IsDir && st.Size > 0 {
				return cand
			}
		}
	}
	if a.PosterPath != "" {
		if st, serr := statRegularFile(a.PosterPath); serr == nil && !st.Reparse && !st.IsDir && st.Size > 0 {
			return a.PosterPath
		}
	}
	if a.SourcePath != "" {
		if kf := FindVideoKeyFrame(a.SourcePath, a.ModifiedAt, a.DurationMS); kf != "" {
			return kf
		}
	}
	return ""
}

func matchesFilter(a Asset, f Filter, albumNames map[int64]string) bool {
	switch f.Scope {
	case ScopeCached:
		if !a.HasCached {
			return false
		}
	case ScopeExported:
		if !a.HasExported && !a.HasCustom {
			return false
		}
	case ScopeArchived:
		if !a.HasArchived {
			return false
		}
	case ScopeFavorites:
		if !a.Favorited {
			return false
		}
	case ScopeVideos:
		if a.MediaType != MediaVideo {
			return false
		}
	}
	if f.UID == UIDUnknown {
		if len(a.UIDs) > 0 {
			return false
		}
	} else if f.UID != "" {
		if !slices.Contains(a.UIDs, f.UID) {
			return false
		}
	}
	if f.AlbumID > 0 {
		if !slices.Contains(a.AlbumIDs, f.AlbumID) {
			return false
		}
	}
	if tag := strings.TrimSpace(f.Tag); tag != "" {
		found := false
		for _, t := range a.Tags {
			if strings.EqualFold(t, tag) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		tokens := strings.Fields(strings.ToLower(q))
		haystack := buildSearchHaystack(a, albumNames)
		for _, tok := range tokens {
			if !strings.Contains(haystack, tok) {
				return false
			}
		}
	}
	return true
}

func buildSearchHaystack(a Asset, albumNames map[int64]string) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(a.FileName))
	b.WriteByte(' ')
	if a.MediaType == MediaVideo {
		b.WriteString("video 视频 mp4 ")
	} else {
		b.WriteString("photo image 照片 图片 ")
	}
	if a.VideoCodec != "" {
		b.WriteString(strings.ToLower(a.VideoCodec))
		b.WriteByte(' ')
	}
	if a.AudioCodec != "" {
		b.WriteString(strings.ToLower(a.AudioCodec))
		b.WriteByte(' ')
	}
	for _, u := range a.UIDs {
		b.WriteString(strings.ToLower(u))
		b.WriteByte(' ')
	}
	for _, t := range a.Tags {
		b.WriteString(strings.ToLower(t))
		b.WriteByte(' ')
	}
	for _, id := range a.AlbumIDs {
		if name := albumNames[id]; name != "" {
			b.WriteString(strings.ToLower(name))
			b.WriteByte(' ')
		}
	}
	if !a.DisplayTime.IsZero() {
		lt := a.DisplayTime.Local()
		b.WriteString(lt.Format("2006-01-02 2006/01/02 2006年01月02日"))
	}
	return b.String()
}

func sortAssets(items []Asset, order SortOrder) {
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		switch order {
		case SortOldest:
			if !a.DisplayTime.Equal(b.DisplayTime) {
				return a.DisplayTime.Before(b.DisplayTime)
			}
		case SortLargest:
			if a.ByteSize != b.ByteSize {
				return a.ByteSize > b.ByteSize
			}
			if !a.DisplayTime.Equal(b.DisplayTime) {
				return a.DisplayTime.After(b.DisplayTime)
			}
		default: // SortNewest
			if !a.DisplayTime.Equal(b.DisplayTime) {
				return a.DisplayTime.After(b.DisplayTime)
			}
		}
		return a.ID > b.ID
	})
}

// ResetIndex clears indexed locations and unorganized/unarchived assets so a
// clean full rescan can rebuild metadata while preserving settings, custom
// sources, archive records, favorites, custom albums and tags.
func (s *Store) ResetIndex(ctx context.Context) error {
	return s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM gallery_asset_locations`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM gallery_assets
			WHERE id NOT IN (SELECT asset_id FROM gallery_archive)
			  AND id NOT IN (SELECT asset_id FROM gallery_favorites)
			  AND id NOT IN (SELECT asset_id FROM gallery_album_items)
			  AND id NOT IN (SELECT asset_id FROM gallery_asset_tags)`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE gallery_sources SET last_scan_at = NULL`); err != nil {
			return err
		}
		return nil
	})
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullInt64(v int64) sql.NullInt64 {
	if v <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: v, Valid: true}
}
