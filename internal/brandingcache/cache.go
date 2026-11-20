// SPDX-License-Identifier: GPL-3.0-or-later

package brandingcache

import (
	"bytes"
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/imagecheck"
)

const (
	// maxMetadataBytes bounds the legacy metadata.json the importer reads.
	maxMetadataBytes = 64 << 10
	maxImageBytes    = 4 << 20
	maxImageSide     = 4096
	maxImagePixels   = 16 << 20

	// imagePrefix and imageSuffix make the slogan image content-addressed: the
	// file name carries the digest of the pixels in it.
	imagePrefix = "slogan-"
	imageSuffix = ".png"
	// stagePrefix names the temporary files a save publishes through.
	stagePrefix = ".branding-stage-"
	// metadataMirror is the legacy metadata file a rolled-back build reads.
	metadataMirror = "metadata.json"
)

// The metadata row names its image file by digest, so a save can never publish a
// record and pixels that disagree. The file is written first under the name the
// digest gives it, the row is committed after, and the previous file is removed
// only once the commit landed. An interruption therefore leaves either the
// previous pair or a file no row names, and both are readable.
//
// That is why the transaction journal of the previous design is gone: with fixed
// file names a crash could pair new pixels with old metadata, so the journal had
// to restore the old bytes before the cache was served. Content addressing
// removes the possibility, leaving only unreferenced files to sweep.

// errInvalidRecord identifies a record this package refuses to store or serve.
var errInvalidRecord = errors.New("brandingcache: invalid record")

// Metadata describes the slogan image the launcher shows. The JSON tags are the
// legacy metadata.json schema and must not change: a rolled-back build reads that
// file, and its loader rejects any other shape.
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

// Record is a slogan image with the metadata that describes it.
type Record struct {
	Image    []byte
	Metadata Metadata
}

// Cache is the slogan image cache. The branding_record row is the authority for
// the metadata and names the content-addressed file that holds the pixels.
type Cache struct {
	db  *database.DB
	dir string
	mu  sync.Mutex
}

// Open prepares dir for a cache backed by db. The directory must be the absolute
// app-owned path; nothing outside it is read or written.
func Open(db *database.DB, dir string) (*Cache, error) {
	if db == nil {
		return nil, errors.New("brandingcache: the cache needs a database")
	}
	if err := validateDir(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return nil, fmt.Errorf("%w: %q is not a plain directory", errInvalidRecord, dir)
	}
	return &Cache{db: db, dir: dir}, nil
}

// Load returns the cached slogan, or an error wrapping os.ErrNotExist when
// nothing usable is cached. A row whose pixels are gone is treated as no cache so
// the launcher draws its built-in artwork instead of describing an image it
// cannot show.
func (c *Cache) Load(ctx context.Context) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	stored, err := c.stored(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, os.ErrNotExist
	}
	if err != nil {
		return Record{}, err
	}
	digest, ok := parseImageName(stored.imagePath)
	if !ok || !strings.EqualFold(digest, stored.metadata.MD5) {
		return Record{}, fmt.Errorf("%w: %q is not the file its record names", errInvalidRecord, stored.imagePath)
	}
	image, err := readImage(filepath.Join(c.dir, stored.imagePath))
	if err != nil {
		return Record{}, fmt.Errorf("%w: %v", os.ErrNotExist, err)
	}
	record := Record{Image: image, Metadata: stored.metadata}
	if err := validateRecord(record); err != nil {
		return Record{}, err
	}
	c.sweep(ctx, stored.imagePath)
	return record, nil
}

// Save validates the record, publishes its image under the digest the name
// carries, and commits the row that names it. The previous image is removed only
// after the commit, so an interrupted save leaves the previous record loadable.
func (c *Cache) Save(ctx context.Context, record Record) error {
	if err := validateRecord(record); err != nil {
		return err
	}
	mirror, err := json.Marshal(record.Metadata)
	if err != nil {
		return fmt.Errorf("brandingcache: encode the mirror: %w", err)
	}
	name := imageName(strings.ToLower(record.Metadata.MD5))
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	previous := ""
	if stored, err := c.stored(ctx); err == nil {
		previous = stored.imagePath
	}
	if err := c.publish(ctx, name, record.Image); err != nil {
		return err
	}
	now := time.Now()
	if err := c.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return c.putRecord(ctx, tx, record.Metadata, name, mirror, now)
	}); err != nil {
		return err
	}
	// The row is authoritative from here on; a mirror that cannot be written only
	// costs the rolled-back build its metadata.
	_ = database.WriteMirror(c.metadataPath(), mirror)
	if previous != "" && previous != name {
		_ = os.Remove(filepath.Join(c.dir, previous))
	}
	c.sweep(ctx, name)
	return nil
}

// putRecord writes the row that names imagePath and records the digest of the
// mirror bytes in the same transaction, so a commit is exactly the moment the row
// and the mirror it declares agree.
func (c *Cache) putRecord(ctx context.Context, tx *sql.Tx, metadata Metadata, imagePath string, mirror []byte, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO branding_record(
			id, cover_code, url, etag, md5, bytes, width, height, last_modified,
			version_number, version_name, article_url, fetched_at, image_path, updated_at)
		VALUES(1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			cover_code = excluded.cover_code, url = excluded.url, etag = excluded.etag,
			md5 = excluded.md5, bytes = excluded.bytes, width = excluded.width,
			height = excluded.height, last_modified = excluded.last_modified,
			version_number = excluded.version_number, version_name = excluded.version_name,
			article_url = excluded.article_url, fetched_at = excluded.fetched_at,
			image_path = excluded.image_path, updated_at = excluded.updated_at`,
		metadata.CoverCode, metadata.URL, metadata.ETag, metadata.MD5, metadata.Bytes,
		metadata.Width, metadata.Height, database.Stamp(metadata.LastModified),
		metadata.VersionNumber, metadata.VersionName, metadata.ArticleURL,
		stampOrZero(metadata.FetchedAt), imagePath, stampOrZero(now)); err != nil {
		return fmt.Errorf("brandingcache: store the record: %w", err)
	}
	return c.db.RecordMirror(ctx, tx, Domain, c.metadataPath(), database.Digest(mirror))
}

// stored is the committed row, without touching the image file.
type stored struct {
	metadata  Metadata
	imagePath string
}

func (c *Cache) stored(ctx context.Context) (stored, error) {
	var (
		result       stored
		lastModified sql.NullInt64
		fetchedAt    int64
	)
	err := c.db.QueryRow(ctx, `
		SELECT cover_code, url, etag, md5, bytes, width, height, last_modified,
			version_number, version_name, article_url, fetched_at, image_path
		FROM branding_record WHERE id = 1`).Scan(
		&result.metadata.CoverCode, &result.metadata.URL, &result.metadata.ETag,
		&result.metadata.MD5, &result.metadata.Bytes, &result.metadata.Width,
		&result.metadata.Height, &lastModified, &result.metadata.VersionNumber,
		&result.metadata.VersionName, &result.metadata.ArticleURL, &fetchedAt,
		&result.imagePath)
	if err != nil {
		return stored{}, err
	}
	result.metadata.LastModified = database.StampTime(lastModified)
	result.metadata.FetchedAt = database.StampInt(fetchedAt)
	return result, nil
}

// publish writes the image under its content-addressed name. Identical bytes are
// already published, so an unchanged slogan costs no write.
func (c *Cache) publish(ctx context.Context, name string, image []byte) error {
	path := filepath.Join(c.dir, name)
	if published, err := readImage(path); err == nil && bytes.Equal(published, image) {
		return nil
	}
	stage, err := stageFile(c.dir, stagePrefix, image, ctx)
	if err != nil {
		return err
	}
	if err := replaceFile(stage, path); err != nil {
		_ = os.Remove(stage)
		return err
	}
	return nil
}

// sweep removes image files no committed row names: the leftovers of a save that
// lost its commit, or of an interrupted replace. Only this cache's own naming
// scheme is swept; the legacy slogan.png, metadata.json and transaction.json
// belong to the build that can be rolled back to and are never touched.
func (c *Cache) sweep(ctx context.Context, keep string) {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return
		}
		name := entry.Name()
		if name == keep || !entry.Type().IsRegular() {
			continue
		}
		if _, ok := parseImageName(name); ok {
			_ = os.Remove(filepath.Join(c.dir, name))
			continue
		}
		if strings.HasPrefix(name, stagePrefix) {
			_ = os.Remove(filepath.Join(c.dir, name))
		}
	}
}

func (c *Cache) metadataPath() string {
	return filepath.Join(c.dir, metadataMirror)
}

// imageName is the content-addressed file name of a slogan image.
func imageName(digest string) string {
	return imagePrefix + digest + imageSuffix
}

// parseImageName returns the digest a content-addressed image name carries. A
// name with a directory separator, a wrong length or a non-hex digest is not one.
func parseImageName(name string) (string, bool) {
	if !strings.HasPrefix(name, imagePrefix) || !strings.HasSuffix(name, imageSuffix) {
		return "", false
	}
	digest := strings.TrimSuffix(strings.TrimPrefix(name, imagePrefix), imageSuffix)
	if len(digest) != md5.Size*2 {
		return "", false
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", false
	}
	return strings.ToLower(digest), true
}

// stampOrZero stores a timestamp in a NOT NULL column, where zero means unknown.
func stampOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().UnixNano()
}

func validateDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("%w: cache directory is empty", errInvalidRecord)
	}
	if !utf8.ValidString(dir) || strings.ContainsRune(dir, 0) || len(dir) > 32<<10 {
		return fmt.Errorf("%w: cache directory is not a usable path", errInvalidRecord)
	}
	clean := filepath.Clean(dir)
	if !filepath.IsAbs(clean) || filepath.Dir(clean) == clean {
		return fmt.Errorf("%w: cache directory must be an absolute path below a volume root", errInvalidRecord)
	}
	return nil
}

func validateRecord(record Record) error {
	if len(record.Image) == 0 || len(record.Image) > maxImageBytes {
		return fmt.Errorf("%w: image byte limit exceeded", errInvalidRecord)
	}
	config, _, err := imagecheck.Check(record.Image, imagecheck.Limits{
		MaxBytes:  maxImageBytes,
		MaxSide:   maxImageSide,
		MaxPixels: maxImagePixels,
		Formats:   []string{"png", "jpeg"},
	})
	if err != nil {
		return fmt.Errorf("%w: %v", errInvalidRecord, err)
	}
	m := record.Metadata
	for _, required := range []string{m.CoverCode, m.URL, m.MD5, m.VersionNumber, m.VersionName, m.ArticleURL} {
		if required == "" {
			return fmt.Errorf("%w: metadata is incomplete", errInvalidRecord)
		}
	}
	for _, value := range []string{m.CoverCode, m.URL, m.ETag, m.MD5, m.VersionNumber, m.VersionName, m.ArticleURL} {
		if !utf8.ValidString(value) {
			return fmt.Errorf("%w: metadata is not UTF-8", errInvalidRecord)
		}
	}
	if m.Bytes != len(record.Image) || m.Width != config.Width || m.Height != config.Height {
		return fmt.Errorf("%w: metadata dimensions or byte count do not match image", errInvalidRecord)
	}
	if len(m.MD5) != md5.Size*2 {
		return fmt.Errorf("%w: invalid md5", errInvalidRecord)
	}
	if _, err := hex.DecodeString(m.MD5); err != nil {
		return fmt.Errorf("%w: invalid md5: %v", errInvalidRecord, err)
	}
	sum := md5.Sum(record.Image)
	if !strings.EqualFold(m.MD5, hex.EncodeToString(sum[:])) {
		return fmt.Errorf("%w: md5 mismatch", errInvalidRecord)
	}
	return nil
}

func readImage(path string) ([]byte, error) {
	file, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return imagecheck.ReadBounded(file, maxImageBytes)
}
