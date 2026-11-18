// SPDX-License-Identifier: GPL-3.0-or-later

// Package brandingcache persists the verified CN slogan image and its metadata.
// The cache is independent from launcher settings and game media caches.
package brandingcache

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nerv-base/Oddward/internal/imagecheck"
)

const (
	maxMetadataBytes = 64 << 10
	maxImageBytes    = 4 << 20
	maxImageSide     = 4096
	maxImagePixels   = 16 << 20
)

var (
	errInvalidRecord = errors.New("brandingcache: invalid record")
	errPartialPair   = errors.New("brandingcache: partial cache pair")
)

// Metadata describes the source and dimensions of the slogan image.
type Metadata struct {
	CoverCode     string    `json:"cover_code"`
	URL           string    `json:"url"`
	ETag          string    `json:"etag"`
	MD5           string    `json:"md5"`
	Bytes         int       `json:"bytes"`
	Width         int       `json:"width"`
	Height        int       `json:"height"`
	LastModified  time.Time `json:"last_modified"`
	VersionNumber string    `json:"version_number"`
	VersionName   string    `json:"version_name"`
	ArticleURL    string    `json:"article_url"`
	FetchedAt     time.Time `json:"fetched_at"`
}

// Record is one verified slogan image and its metadata.
type Record struct {
	Image    []byte   `json:"-"`
	Metadata Metadata `json:"metadata"`
}

// Cache persists one slogan record in dir. A Cache serializes its operations
// and is safe for concurrent callers within one process.
type Cache struct {
	dir string
	mu  sync.Mutex
}

// Open creates and validates an app-owned absolute cache directory.
func Open(dir string) (*Cache, error) {
	if err := validateDir(dir); err != nil {
		return nil, err
	}
	dir = filepath.Clean(dir)
	if filepath.Dir(dir) == dir {
		return nil, errors.New("brandingcache: directory must not be a volume root")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("brandingcache: create directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("brandingcache: inspect directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("brandingcache: cache path must be a real directory")
	}
	return &Cache{dir: dir}, nil
}

// Load reads and validates the complete pair. Missing files return an error
// wrapping os.ErrNotExist. A one-file pair is reported as a partial cache.
func (c *Cache) Load(ctx context.Context) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	imagePath := filepath.Join(c.dir, "slogan.png")
	metadataPath := filepath.Join(c.dir, "metadata.json")
	imageData, imageErr := readImage(imagePath)
	metadataData, metadataErr := readMetadata(metadataPath)
	if errors.Is(imageErr, os.ErrNotExist) && errors.Is(metadataErr, os.ErrNotExist) {
		return Record{}, os.ErrNotExist
	}
	if imageErr != nil || metadataErr != nil {
		if errors.Is(imageErr, os.ErrNotExist) || errors.Is(metadataErr, os.ErrNotExist) {
			return Record{}, fmt.Errorf("%w: %v", errPartialPair, errors.Join(imageErr, metadataErr))
		}
		return Record{}, errors.Join(imageErr, metadataErr)
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	metadata, err := decodeMetadata(metadataData)
	if err != nil {
		return Record{}, err
	}
	if err := validateRecord(Record{Image: imageData, Metadata: metadata}); err != nil {
		return Record{}, err
	}
	return Record{Image: imageData, Metadata: metadata}, nil
}

// Save validates and atomically stages a replacement. Both temporary files are
// created before either destination is changed, and metadata is published last.
func (c *Cache) Save(ctx context.Context, r Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRecord(r); err != nil {
		return err
	}
	metadataData, err := json.Marshal(r.Metadata)
	if err != nil {
		return fmt.Errorf("brandingcache: encode metadata: %w", err)
	}
	if len(metadataData) > maxMetadataBytes {
		return fmt.Errorf("%w: metadata exceeds %d bytes", errInvalidRecord, maxMetadataBytes)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	imageStage, err := stageFile(c.dir, "slogan-image-", r.Image, ctx)
	if err != nil {
		return err
	}
	metadataStage, err := stageFile(c.dir, "slogan-metadata-", metadataData, ctx)
	if err != nil {
		_ = os.Remove(imageStage)
		return err
	}
	defer func() {
		_ = os.Remove(imageStage)
		_ = os.Remove(metadataStage)
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	imagePath := filepath.Join(c.dir, "slogan.png")
	metadataPath := filepath.Join(c.dir, "metadata.json")
	oldImage, oldImageErr := os.ReadFile(imagePath)
	oldMetadata, oldMetadataErr := os.ReadFile(metadataPath)
	oldImageExists := oldImageErr == nil
	oldMetadataExists := oldMetadataErr == nil
	rollback := func() {
		if oldImageExists {
			_ = restoreFile(oldImage, imagePath)
		} else {
			_ = os.Remove(imagePath)
		}
		if oldMetadataExists {
			_ = restoreFile(oldMetadata, metadataPath)
		} else {
			_ = os.Remove(metadataPath)
		}
	}
	if err := replaceFile(imageStage, imagePath); err != nil {
		return fmt.Errorf("brandingcache: publish image: %w", err)
	}
	if err := ctx.Err(); err != nil {
		rollback()
		return err
	}
	if err := replaceFile(metadataStage, metadataPath); err != nil {
		rollback()
		return fmt.Errorf("brandingcache: publish metadata: %w", err)
	}
	return nil
}

func validateDir(dir string) error {
	if dir == "" || !filepath.IsAbs(dir) || !utf8.ValidString(dir) || strings.ContainsRune(dir, 0) {
		return errors.New("brandingcache: directory must be an absolute UTF-8 path")
	}
	if len(dir) > 32768 {
		return errors.New("brandingcache: directory path too long")
	}
	return nil
}

func validateRecord(r Record) error {
	if len(r.Image) == 0 || len(r.Image) > maxImageBytes {
		return fmt.Errorf("%w: image byte limit exceeded", errInvalidRecord)
	}
	config, _, err := imagecheck.Check(r.Image, imagecheck.Limits{
		MaxBytes:  maxImageBytes,
		MaxSide:   maxImageSide,
		MaxPixels: maxImagePixels,
		Formats:   []string{"png", "jpeg"},
	})
	if err != nil {
		return fmt.Errorf("%w: %v", errInvalidRecord, err)
	}
	m := r.Metadata
	for _, value := range []string{m.CoverCode, m.URL, m.ETag, m.MD5, m.VersionNumber, m.VersionName, m.ArticleURL} {
		if !utf8.ValidString(value) {
			return fmt.Errorf("%w: metadata is not UTF-8", errInvalidRecord)
		}
	}
	if m.Bytes != len(r.Image) || m.Width != config.Width || m.Height != config.Height {
		return fmt.Errorf("%w: metadata dimensions or byte count do not match image", errInvalidRecord)
	}
	if len(m.MD5) != md5.Size*2 {
		return fmt.Errorf("%w: invalid md5", errInvalidRecord)
	}
	if _, err := hex.DecodeString(m.MD5); err != nil {
		return fmt.Errorf("%w: invalid md5: %v", errInvalidRecord, err)
	}
	sum := md5.Sum(r.Image)
	if !strings.EqualFold(m.MD5, hex.EncodeToString(sum[:])) {
		return fmt.Errorf("%w: md5 mismatch", errInvalidRecord)
	}
	return nil
}

func readImage(path string) ([]byte, error) {
	f, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := imagecheck.ReadBounded(f, maxImageBytes)
	if err != nil {
		return nil, fmt.Errorf("brandingcache: read image: %w", err)
	}
	return data, nil
}

func readMetadata(path string) ([]byte, error) {
	f, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := imagecheck.ReadBounded(f, maxMetadataBytes)
	if err != nil {
		return nil, fmt.Errorf("brandingcache: read metadata: %w", err)
	}
	return data, nil
}

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
	default:
		return errors.New("unexpected metadata JSON delimiter")
	}
	end, err := decoder.Token()
	if err != nil {
		return err
	}
	if (delimiter == '{' && end != json.Delim('}')) || (delimiter == '[' && end != json.Delim(']')) {
		return errors.New("unclosed metadata JSON container")
	}
	return nil
}

func stageFile(dir, prefix string, data []byte, ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, prefix+"*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	defer func() {
		_ = f.Close()
	}()
	if err := f.Chmod(0o600); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if err := f.Sync(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func restoreFile(data []byte, destination string) error {
	stage, err := stageFile(filepath.Dir(destination), "branding-restore-", data, context.Background())
	if err != nil {
		return err
	}
	defer os.Remove(stage)
	return replaceFile(stage, destination)
}
