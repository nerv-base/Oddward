// SPDX-License-Identifier: GPL-3.0-or-later

package gacha

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nerv-base/Oddward/internal/database"
)

const (
	catalogFile     = "catalog.json"
	catalogFormat   = "oddward-gacha-catalog"
	catalogVersion  = 1
	catalogMaxBytes = 512 << 10
	catalogMaxItems = 8192
	catalogMaxName  = 64
	// catalogMaxID mirrors the identifier bound of gacha_item_names; a longer
	// identifier cannot be stored, so it is treated as unrecognized.
	catalogMaxID = 64

	// catalogDomain is the migration_state and mirror domain of the name
	// supplement. It is separate from Domain because it is one file, not a
	// directory of archives.
	catalogDomain        = "gacha-catalog"
	catalogImportVersion = 1
)

type diskCatalog struct {
	Format    string            `json:"format"`
	Version   int               `json:"version"`
	FetchedAt int64             `json:"fetched_at"`
	Items     map[string]string `json:"items"`
}

// LoadCatalog reads the official name supplement from the database. Names are a
// display convenience, so callers may ignore a returned error and show
// identifiers instead; this method itself never invents names. The supplement
// has no metadata row of its own, so every name row carries the catalog time and
// the newest row is what the catalog reports.
func (s *Store) LoadCatalog(ctx context.Context) (Catalog, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return Catalog{}, err
	}
	defer release()
	return s.loadCatalog(ctx)
}

func (s *Store) loadCatalog(ctx context.Context) (Catalog, error) {
	rows, err := s.db.Query(ctx, `SELECT item_id, name, updated_at FROM gacha_item_names`)
	if err != nil {
		return Catalog{}, err
	}
	defer rows.Close()
	c := Catalog{Items: map[string]Item{}}
	for rows.Next() {
		var id, name string
		var updatedAt int64
		if err := rows.Scan(&id, &name, &updatedAt); err != nil {
			return Catalog{}, err
		}
		if validCatalogName(name) != nil || Kind(id) == Unknown {
			continue
		}
		c.Items[id] = Item{Kind: Kind(id), Name: name}
		if at := database.StampInt(updatedAt); at.After(c.FetchedAt) {
			c.FetchedAt = at
		}
	}
	if err := rows.Err(); err != nil {
		return Catalog{}, err
	}
	return c, nil
}

func decodeCatalog(data []byte) (Catalog, error) {
	if len(data) > catalogMaxBytes {
		return Catalog{}, errors.New("gacha catalog exceeds 512 KiB")
	}
	if !utf8.Valid(data) {
		return Catalog{}, errors.New("gacha catalog must be UTF-8")
	}
	var d diskCatalog
	if err := json.Unmarshal(data, &d); err != nil {
		return Catalog{}, fmt.Errorf("decode gacha catalog: %w", err)
	}
	if d.Format != catalogFormat || d.Version != catalogVersion {
		return Catalog{}, errors.New("gacha catalog has an unknown format")
	}
	if d.FetchedAt < 0 {
		return Catalog{}, errors.New("gacha catalog time must not precede the Unix epoch")
	}
	c := Catalog{FetchedAt: time.UnixMilli(d.FetchedAt).UTC(), Items: map[string]Item{}}
	for id, name := range d.Items {
		if err := validCatalogName(name); err != nil {
			continue
		}
		if Kind(id) == Unknown {
			continue
		}
		c.Items[id] = Item{Kind: Kind(id), Name: name}
	}
	return c, nil
}

// SaveCatalog replaces the name supplement. Only identifiers with a recognized
// shape and non-empty names are written. The legacy catalog.json is rewritten as
// the mirror a rolled-back build reads.
func (s *Store) SaveCatalog(ctx context.Context, c Catalog) error {
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	items, err := catalogItems(c)
	if err != nil {
		return err
	}
	data, err := encodeCatalog(c.FetchedAt, items)
	if err != nil {
		return err
	}
	name := filepath.Join(s.dir, catalogFile)
	if err := checkLocalPath(ctx, name, false); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM gacha_item_names`); err != nil {
			return fmt.Errorf("replace gacha item names: %w", err)
		}
		if err := putCatalog(ctx, tx, items, c.FetchedAt); err != nil {
			return err
		}
		return s.db.RecordMirror(ctx, tx, catalogDomain, name, database.Digest(data))
	})
	if err != nil {
		return err
	}
	if err := database.WriteMirror(name, data); err != nil {
		return fmt.Errorf("gacha catalog is committed, legacy mirror not written: %w", err)
	}
	return nil
}

// importCatalog replaces the stored names with the legacy catalog file. It is
// the database.ImportFunc for catalogDomain and never rewrites the file.
func (s *Store) importCatalog(ctx context.Context) (source, detail string, err error) {
	path := filepath.Join(s.dir, catalogFile)
	data, err := readLegacyFile(ctx, path, catalogMaxBytes)
	if errors.Is(err, os.ErrNotExist) {
		return path, "no legacy catalog", nil
	}
	if err != nil {
		return path, "", fmt.Errorf("%s: %w", path, err)
	}
	c, err := decodeCatalog(data)
	if err != nil {
		return path, "", fmt.Errorf("%s: %w", path, err)
	}
	items := make(map[string]string, len(c.Items))
	for id, item := range c.Items {
		items[id] = item.Name
	}
	if err := ctx.Err(); err != nil {
		return path, "", err
	}
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM gacha_item_names`); err != nil {
			return fmt.Errorf("replace gacha item names: %w", err)
		}
		if err := putCatalog(ctx, tx, items, c.FetchedAt); err != nil {
			return err
		}
		return s.db.RecordMirror(ctx, tx, catalogDomain, path, database.Digest(data))
	})
	if err != nil {
		return path, "", err
	}
	return path, fmt.Sprintf("%d names", len(items)), nil
}

// putCatalog writes the filtered names and their catalog time inside tx.
func putCatalog(ctx context.Context, tx *sql.Tx, items map[string]string, fetchedAt time.Time) error {
	for id, name := range items {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gacha_item_names(item_id, name, updated_at) VALUES(?, ?, ?)`,
			id, name, database.Stamp(fetchedAt)); err != nil {
			return fmt.Errorf("store gacha item name: %w", err)
		}
	}
	return nil
}

// catalogItems keeps the entries a saved catalog may contain, so the rows and
// the mirror always describe the same names.
func catalogItems(c Catalog) (map[string]string, error) {
	if c.FetchedAt.UnixMilli() < 0 {
		return nil, errors.New("gacha catalog time must not precede the Unix epoch")
	}
	if len(c.Items) > catalogMaxItems {
		return nil, errors.New("gacha catalog has too many items")
	}
	items := map[string]string{}
	for id, item := range c.Items {
		if Kind(id) == Unknown || item.Name == "" || len(id) > catalogMaxID {
			continue
		}
		if err := validCatalogName(item.Name); err != nil {
			return nil, fmt.Errorf("gacha catalog name for %q: %w", id, err)
		}
		items[id] = item.Name
	}
	return items, nil
}

func encodeCatalog(fetchedAt time.Time, items map[string]string) ([]byte, error) {
	d := diskCatalog{Format: catalogFormat, Version: catalogVersion, FetchedAt: fetchedAt.UnixMilli(), Items: items}
	data, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	if len(data) > catalogMaxBytes {
		return nil, errors.New("gacha catalog exceeds 512 KiB")
	}
	return data, nil
}

func validCatalogName(name string) error {
	if name == "" {
		return errors.New("name must not be empty")
	}
	if name != strings.TrimSpace(name) {
		return errors.New("name must not have surrounding whitespace")
	}
	if utf8.RuneCountInString(name) > catalogMaxName {
		return errors.New("name is too long")
	}
	return nil
}
