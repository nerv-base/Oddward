// SPDX-License-Identifier: GPL-3.0-or-later

package brandingcache

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nerv-base/Oddward/internal/imagecheck"
)

// Domain is this package's domain in migration_state and compat_mirrors.
const Domain = "branding"

// ImportVersion is the importer generation recorded for Domain. Bump it when the
// importer learns to read a legacy document it did not read before.
const ImportVersion = 1

// legacyImage is the fixed image name the previous build wrote next to
// metadata.json; a rolled-back build still reads that pair.
const legacyImage = "slogan.png"

// ImportLegacy adopts the slogan.png and metadata.json pair the previous build
// left behind, and is the database.ImportFunc for Domain.
//
// Adopting copies the pixels to the content-addressed file the new row names, so
// after a rollback-and-return the cache holds one record again instead of two.
// The mirror digest is the digest of the bytes actually read, which keeps the file
// unchanged and lets the next start see whether the old build rewrote it. A pair
// that is absent is a no-op; a pair that does not validate names the file it read
// and leaves both the files and the row untouched.
func (c *Cache) ImportLegacy(ctx context.Context) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	mirrorPath := c.metadataPath()
	if _, err := os.Lstat(c.dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return mirrorPath, "no legacy branding cache", nil
		}
		return mirrorPath, "", err
	}
	data, err := readMetadata(mirrorPath)
	if errors.Is(err, os.ErrNotExist) {
		return mirrorPath, "no legacy metadata", nil
	}
	if err != nil {
		return mirrorPath, "", fmt.Errorf("%s: %w", mirrorPath, err)
	}
	metadata, err := decodeMetadata(data)
	if err != nil {
		return mirrorPath, "", fmt.Errorf("%s: %w", mirrorPath, err)
	}
	source := filepath.Join(c.dir, legacyImage)
	image, err := readImage(source)
	if err != nil {
		return source, "", fmt.Errorf("%s: %w", source, err)
	}
	if err := validateRecord(Record{Image: image, Metadata: metadata}); err != nil {
		return mirrorPath, "", fmt.Errorf("%s: %w", mirrorPath, err)
	}
	name := imageName(strings.ToLower(metadata.MD5))
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return mirrorPath, "", err
	}
	if err := c.publish(ctx, name, image); err != nil {
		return source, "", err
	}
	if err := c.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return c.putRecord(ctx, tx, metadata, name, data, time.Now())
	}); err != nil {
		return mirrorPath, "", err
	}
	c.sweep(ctx, name)
	return mirrorPath, fmt.Sprintf("adopted %d image bytes", len(image)), nil
}

// decodeMetadata parses the legacy metadata document. The schema is fixed at the
// twelve fields the previous build wrote: an unknown, missing or duplicated field
// means the file was not produced by an Oddward cache, and guessing at it would
// put an unverified record in front of the user.
func decodeMetadata(data []byte) (Metadata, error) {
	if !utf8.Valid(data) {
		return Metadata{}, fmt.Errorf("%w: metadata is not UTF-8", errInvalidRecord)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := uniqueJSON(decoder); err != nil {
		return Metadata{}, fmt.Errorf("%w: %v", errInvalidRecord, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || len(fields) != 12 {
		return Metadata{}, fmt.Errorf("%w: metadata schema must contain exactly 12 fields", errInvalidRecord)
	}
	for _, name := range []string{"cover_code", "url", "etag", "md5", "bytes", "width", "height", "last_modified", "version_number", "version_name", "article_url", "fetched_at"} {
		if _, ok := fields[name]; !ok {
			return Metadata{}, fmt.Errorf("%w: missing metadata field %q", errInvalidRecord, name)
		}
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var metadata Metadata
	if err := decoder.Decode(&metadata); err != nil {
		return Metadata{}, fmt.Errorf("%w: decode metadata: %v", errInvalidRecord, err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return Metadata{}, fmt.Errorf("%w: trailing metadata data", errInvalidRecord)
	}
	return metadata, nil
}

// uniqueJSON rejects a document that repeats a field: encoding/json keeps the last
// occurrence, which would let a duplicated key describe a record the file does not
// actually state.
func uniqueJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("invalid metadata object key")
			}
			if _, exists := seen[name]; exists {
				return fmt.Errorf("duplicate metadata field %q", name)
			}
			seen[name] = struct{}{}
			if err := uniqueJSON(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := uniqueJSON(decoder); err != nil {
				return err
			}
		}
	}
	return nil
}

func readMetadata(path string) ([]byte, error) {
	file, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return imagecheck.ReadBounded(file, maxMetadataBytes)
}
