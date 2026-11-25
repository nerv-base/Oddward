// SPDX-License-Identifier: GPL-3.0-or-later

package gallery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nerv-base/Oddward/internal/database"
)

const (
	maxAlbumNameRunes = 64
	maxTagNameRunes   = 40
)

// Album is a user-created local grouping of gallery assets. Deleting an album
// removes only its grouping rows and never deletes underlying media files.
type Album struct {
	ID           int64
	Name         string
	Count        int
	CoverAssetID int64
}

// Tag is a user-created label attached to gallery assets for filtering and search.
type Tag struct {
	ID    int64
	Name  string
	Count int
}

func normalizeLabelName(raw string, maxRunes int) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !utf8.ValidString(trimmed) {
		return "", fmt.Errorf("%w: empty or invalid name", ErrInvalidSettings)
	}
	if utf8.RuneCountInString(trimmed) > maxRunes {
		return "", fmt.Errorf("%w: name exceeds %d characters", ErrInvalidSettings, maxRunes)
	}
	for _, r := range trimmed {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: control characters not allowed", ErrInvalidSettings)
		}
	}
	return trimmed, nil
}

// SetFavorites marks or unmarks assetIDs as favorites in one SQLite transaction.
func (s *Store) SetFavorites(ctx context.Context, assetIDs []int64, on bool) error {
	if len(assetIDs) == 0 {
		return nil
	}
	stamp := database.Stamp(s.clock())
	return s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, id := range assetIDs {
			if id <= 0 {
				continue
			}
			if on {
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO gallery_favorites(asset_id, favorited_at)
					VALUES(?, ?)
					ON CONFLICT(asset_id) DO NOTHING`, id, stamp); err != nil {
					return err
				}
			} else {
				if _, err := tx.ExecContext(ctx, `DELETE FROM gallery_favorites WHERE asset_id = ?`, id); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// ListAlbums returns all user-created custom albums ordered by name.
func (s *Store) ListAlbums(ctx context.Context) ([]Album, error) {
	rows, err := s.db.Query(ctx, `
		SELECT a.id, a.name, COUNT(ai.asset_id), COALESCE(MAX(ai.asset_id), 0)
		FROM gallery_albums a
		LEFT JOIN gallery_album_items ai ON ai.album_id = a.id
		GROUP BY a.id, a.name
		ORDER BY a.name COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("gallery: list albums: %w", err)
	}
	defer rows.Close()
	var out []Album
	for rows.Next() {
		var al Album
		if err := rows.Scan(&al.ID, &al.Name, &al.Count, &al.CoverAssetID); err != nil {
			return nil, err
		}
		out = append(out, al)
	}
	return out, rows.Err()
}

// CreateAlbum creates a new custom album with a validated unique name.
func (s *Store) CreateAlbum(ctx context.Context, rawName string) (Album, error) {
	name, err := normalizeLabelName(rawName, maxAlbumNameRunes)
	if err != nil {
		return Album{}, err
	}
	now := database.Stamp(s.clock())
	var id int64
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO gallery_albums(name, created_at, updated_at)
			VALUES(?, ?, ?)`, name, now, now)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return Album{}, fmt.Errorf("gallery: create album: %w", err)
	}
	return Album{ID: id, Name: name}, nil
}

// RenameAlbum renames an existing custom album.
func (s *Store) RenameAlbum(ctx context.Context, albumID int64, rawName string) error {
	if albumID <= 0 {
		return ErrNotFound
	}
	name, err := normalizeLabelName(rawName, maxAlbumNameRunes)
	if err != nil {
		return err
	}
	now := database.Stamp(s.clock())
	return s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE gallery_albums SET name = ?, updated_at = ? WHERE id = ?`,
			name, now, albumID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// DeleteAlbum deletes only the album metadata row (and its memberships via
// cascade); underlying media assets and files are never touched.
func (s *Store) DeleteAlbum(ctx context.Context, albumID int64) error {
	if albumID <= 0 {
		return ErrNotFound
	}
	return s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM gallery_albums WHERE id = ?`, albumID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// AddAssetsToAlbum associates assetIDs with albumID.
func (s *Store) AddAssetsToAlbum(ctx context.Context, albumID int64, assetIDs []int64) error {
	if albumID <= 0 || len(assetIDs) == 0 {
		return nil
	}
	now := database.Stamp(s.clock())
	return s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM gallery_albums WHERE id = ?`, albumID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		for _, id := range assetIDs {
			if id <= 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO gallery_album_items(album_id, asset_id, added_at)
				VALUES(?, ?, ?)
				ON CONFLICT(album_id, asset_id) DO NOTHING`, albumID, id, now); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE gallery_albums SET updated_at = ? WHERE id = ?`, now, albumID)
		return err
	})
}

// RemoveAssetsFromAlbum detaches assetIDs from albumID without removing assets.
func (s *Store) RemoveAssetsFromAlbum(ctx context.Context, albumID int64, assetIDs []int64) error {
	if albumID <= 0 || len(assetIDs) == 0 {
		return nil
	}
	return s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, id := range assetIDs {
			if id <= 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM gallery_album_items WHERE album_id = ? AND asset_id = ?`, albumID, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListTags returns all tags currently attached to at least one asset.
func (s *Store) ListTags(ctx context.Context) ([]Tag, error) {
	rows, err := s.db.Query(ctx, `
		SELECT t.id, t.name, COUNT(at.asset_id)
		FROM gallery_tags t
		JOIN gallery_asset_tags at ON at.tag_id = t.id
		GROUP BY t.id, t.name
		ORDER BY COUNT(at.asset_id) DESC, t.name COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("gallery: list tags: %w", err)
	}
	defer rows.Close()
	var out []Tag
	for rows.Next() {
		var tag Tag
		if err := rows.Scan(&tag.ID, &tag.Name, &tag.Count); err != nil {
			return nil, err
		}
		out = append(out, tag)
	}
	return out, rows.Err()
}

// AddTagToAssets creates the tag if necessary and attaches it to assetIDs.
func (s *Store) AddTagToAssets(ctx context.Context, rawName string, assetIDs []int64) error {
	if len(assetIDs) == 0 {
		return nil
	}
	name, err := normalizeLabelName(rawName, maxTagNameRunes)
	if err != nil {
		return err
	}
	now := database.Stamp(s.clock())
	return s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gallery_tags(name, created_at) VALUES(?, ?)
			ON CONFLICT(name) DO NOTHING`, name, now); err != nil {
			return err
		}
		var tagID int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM gallery_tags WHERE name = ?`, name).Scan(&tagID); err != nil {
			return err
		}
		for _, id := range assetIDs {
			if id <= 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO gallery_asset_tags(asset_id, tag_id) VALUES(?, ?)
				ON CONFLICT(asset_id, tag_id) DO NOTHING`, id, tagID); err != nil {
				return err
			}
		}
		return nil
	})
}

// RemoveTagFromAsset detaches rawName from assetID and prunes unused tag rows.
func (s *Store) RemoveTagFromAsset(ctx context.Context, assetID int64, rawName string) error {
	if assetID <= 0 {
		return nil
	}
	name := strings.TrimSpace(rawName)
	if name == "" {
		return nil
	}
	return s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var tagID int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM gallery_tags WHERE name = ?`, name).Scan(&tagID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM gallery_asset_tags WHERE asset_id = ? AND tag_id = ?`, assetID, tagID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			DELETE FROM gallery_tags
			WHERE id = ? AND NOT EXISTS (SELECT 1 FROM gallery_asset_tags WHERE tag_id = ?)`, tagID, tagID)
		return err
	})
}

// PruneMissingUnarchived deletes only stale index rows whose source files are
// unavailable AND which have no ready archive copy. It never removes files on disk.
func (s *Store) PruneMissingUnarchived(ctx context.Context) (int, error) {
	var removed int
	err := s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM gallery_asset_locations WHERE available = 0`); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			DELETE FROM gallery_assets
			WHERE NOT EXISTS (SELECT 1 FROM gallery_asset_locations l WHERE l.asset_id = gallery_assets.id AND l.available = 1)
			  AND NOT EXISTS (SELECT 1 FROM gallery_archive g WHERE g.asset_id = gallery_assets.id AND g.status = 'ready')`)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		removed = int(n)
		return nil
	})
	return removed, err
}
