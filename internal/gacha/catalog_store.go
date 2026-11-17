// SPDX-License-Identifier: GPL-3.0-or-later

package gacha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	catalogFile     = "catalog.json"
	catalogFormat   = "oddward-gacha-catalog"
	catalogVersion  = 1
	catalogMaxBytes = 512 << 10
	catalogMaxItems = 8192
	catalogMaxName  = 64
)

type diskCatalog struct {
	Format    string            `json:"format"`
	Version   int               `json:"version"`
	FetchedAt int64             `json:"fetched_at"`
	Items     map[string]string `json:"items"`
}

// LoadCatalog reads the official name supplement written by SaveCatalog. Names
// are a display convenience, so callers may ignore a returned error and show
// identifiers instead; this method itself never invents names.
func (s *Store) LoadCatalog(ctx context.Context) (Catalog, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return Catalog{}, err
	}
	defer release()
	return s.loadCatalog(ctx)
}

func (s *Store) loadCatalog(ctx context.Context) (Catalog, error) {
	name := filepath.Join(s.dir, catalogFile)
	if err := checkLocalPath(ctx, name, false); err != nil {
		return Catalog{}, err
	}
	info, err := os.Stat(name)
	if errors.Is(err, os.ErrNotExist) {
		return Catalog{}, nil
	}
	if err != nil {
		return Catalog{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > catalogMaxBytes {
		return Catalog{}, errors.New("gacha catalog file has an unusable shape")
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return Catalog{}, err
	}
	if err := ctx.Err(); err != nil {
		return Catalog{}, err
	}
	return decodeCatalog(data)
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

// SaveCatalog validates and atomically replaces the name supplement. Only
// identifiers with a recognized shape and non-empty names are written.
func (s *Store) SaveCatalog(ctx context.Context, c Catalog) (result error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	data, err := marshalCatalog(c)
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
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	stage, err := os.CreateTemp(s.dir, ".catalog-*.tmp")
	if err != nil {
		return err
	}
	stagedName := stage.Name()
	committed := false
	defer func() {
		_ = stage.Close()
		if !committed {
			if err := os.Remove(stagedName); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, fmt.Errorf("remove owned catalog stage: %w", err))
			}
		}
	}()
	if _, err := stage.Write(data); err != nil {
		return err
	}
	if err := stage.Sync(); err != nil {
		return err
	}
	if err := stage.Close(); err != nil {
		return err
	}
	if err := checkLocalPath(ctx, stagedName, false); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := replaceFile(stagedName, name); err != nil {
		return err
	}
	committed = true
	return nil
}

func marshalCatalog(c Catalog) ([]byte, error) {
	if c.FetchedAt.UnixMilli() < 0 {
		return nil, errors.New("gacha catalog time must not precede the Unix epoch")
	}
	if len(c.Items) > catalogMaxItems {
		return nil, errors.New("gacha catalog has too many items")
	}
	d := diskCatalog{Format: catalogFormat, Version: catalogVersion, FetchedAt: c.FetchedAt.UnixMilli(), Items: map[string]string{}}
	for id, item := range c.Items {
		if Kind(id) == Unknown || item.Name == "" {
			continue
		}
		if err := validCatalogName(item.Name); err != nil {
			return nil, fmt.Errorf("gacha catalog name for %q: %w", id, err)
		}
		d.Items[id] = item.Name
	}
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
