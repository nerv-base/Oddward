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
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

// sloganFixture encodes a solid-colour PNG of the given size.
func sloganFixture(t *testing.T, width, height int, shade uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	fill := color.RGBA{R: shade, G: shade, B: shade, A: 0xff}
	for y := range height {
		for x := range width {
			img.SetRGBA(x, y, fill)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// recordFor is the record that describes image exactly: the metadata has to agree
// with the pixels or Save refuses it.
func recordFor(t *testing.T, image []byte, width, height int) Record {
	t.Helper()
	sum := md5.Sum(image)
	return Record{
		Image: image,
		Metadata: Metadata{
			CoverCode:     "cover123456",
			URL:           "https://yh.wanmei.com/images/cover123456/slogan.png",
			ETag:          `"v1"`,
			MD5:           hex.EncodeToString(sum[:]),
			Bytes:         len(image),
			Width:         width,
			Height:        height,
			LastModified:  time.Unix(0, 1700000000000000000).UTC(),
			VersionNumber: "1.2",
			VersionName:   "星海回响",
			ArticleURL:    "https://yh.wanmei.com/news/gamebroad/20261119/slogan.html",
			FetchedAt:     time.Unix(0, 1700000001000000000).UTC(),
		},
	}
}

func openCache(t *testing.T) (*Cache, *database.DB, string) {
	t.Helper()
	db := dbtest.Open(t)
	dir := filepath.Join(t.TempDir(), "branding")
	cache, err := Open(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	return cache, db, dir
}

func sameMetadata(a, b Metadata) bool {
	return a.CoverCode == b.CoverCode && a.URL == b.URL && a.ETag == b.ETag && a.MD5 == b.MD5 &&
		a.Bytes == b.Bytes && a.Width == b.Width && a.Height == b.Height &&
		a.VersionNumber == b.VersionNumber && a.VersionName == b.VersionName &&
		a.ArticleURL == b.ArticleURL && a.LastModified.Equal(b.LastModified) && a.FetchedAt.Equal(b.FetchedAt)
}

func requireRecord(t *testing.T, got Record, err error, want Record) {
	t.Helper()
	if err != nil {
		t.Fatalf("load = %v", err)
	}
	if !bytes.Equal(got.Image, want.Image) || !sameMetadata(got.Metadata, want.Metadata) {
		t.Fatalf("loaded record differs from the saved one: %+v", got.Metadata)
	}
}

func metadataJSON(t *testing.T, metadata Metadata) []byte {
	t.Helper()
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSaveThenLoadReturnsTheSameRecord(t *testing.T) {
	cache, db, dir := openCache(t)
	ctx := context.Background()
	want := recordFor(t, sloganFixture(t, 8, 6, 0x40), 8, 6)
	if err := cache.Save(ctx, want); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Load(ctx)
	requireRecord(t, got, err, want)

	name := imageName(want.Metadata.MD5)
	if info, err := os.Lstat(filepath.Join(dir, name)); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("the image must live in the content-addressed file: %v", err)
	}
	// The mirror is the pair a rolled-back build reads, so it has to be exact.
	mirror := filepath.Join(dir, metadataMirror)
	if data, err := os.ReadFile(mirror); err != nil || !bytes.Equal(data, metadataJSON(t, want.Metadata)) {
		t.Fatalf("metadata mirror = %v", err)
	}
	if stale, err := db.MirrorStale(ctx, Domain); err != nil || stale {
		t.Fatalf("the written mirror must be recorded: stale=%v, %v", stale, err)
	}
}

func TestOpenRequiresADatabaseAndAnAbsoluteDirectory(t *testing.T) {
	db := dbtest.Open(t)
	if _, err := Open(nil, filepath.Join(t.TempDir(), "branding")); err == nil {
		t.Fatal("a cache without a database must not open")
	}
	if _, err := Open(db, filepath.Join("relative", "branding")); err == nil {
		t.Fatal("a relative directory must not open")
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(db, blocked); err == nil {
		t.Fatal("a regular file must not be used as the cache directory")
	}
}

func TestLoadWithoutARowReportsNoCache(t *testing.T) {
	cache, _, dir := openCache(t)
	ctx := context.Background()
	if _, err := cache.Load(ctx); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty cache = %v, want os.ErrNotExist", err)
	}
	// A legacy slogan.png on its own describes nothing; it is not a record.
	if err := os.WriteFile(filepath.Join(dir, legacyImage), sloganFixture(t, 4, 4, 0x10), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Load(ctx); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an unadopted legacy image = %v, want os.ErrNotExist", err)
	}
}

func TestLoadTreatsAMissingImageAsNoCache(t *testing.T) {
	cache, db, dir := openCache(t)
	ctx := context.Background()
	want := recordFor(t, sloganFixture(t, 8, 6, 0x22), 8, 6)
	if err := cache.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	name := imageName(want.Metadata.MD5)
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
	// The launcher draws its built-in artwork instead of describing an image that
	// is not there.
	if _, err := cache.Load(ctx); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a row whose image is gone = %v, want os.ErrNotExist", err)
	}

	// A file that does not match the record is reported, not served.
	other := sloganFixture(t, 8, 6, 0x33)
	if err := os.WriteFile(filepath.Join(dir, name), other, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Load(ctx); !errors.Is(err, errInvalidRecord) {
		t.Fatalf("mismatched pixels = %v, want an invalid record", err)
	}

	// A row that names a file outside the naming scheme is not trusted either.
	if _, err := db.Exec(ctx, `UPDATE branding_record SET image_path = 'slogan.png'`); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Load(ctx); !errors.Is(err, errInvalidRecord) {
		t.Fatalf("a row naming an unchecked file = %v, want an invalid record", err)
	}
}

func TestInterruptedSaveLeavesThePreviousRecordLoadable(t *testing.T) {
	cache, db, dir := openCache(t)
	ctx := context.Background()
	previous := recordFor(t, sloganFixture(t, 8, 6, 0x44), 8, 6)
	if err := cache.Save(ctx, previous); err != nil {
		t.Fatal(err)
	}

	// A read-only handle can publish the image but cannot commit the row, which is
	// exactly the window between the two steps of a save.
	readOnly, err := database.Open(ctx, database.Options{Path: db.Path(), ReadOnly: true, NoBackup: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { readOnly.Close() })
	broken, err := Open(readOnly, dir)
	if err != nil {
		t.Fatal(err)
	}
	next := recordFor(t, sloganFixture(t, 4, 4, 0x55), 4, 4)
	if err := broken.Save(ctx, next); err == nil {
		t.Fatal("a save that cannot commit must fail")
	}
	if _, err := os.Lstat(filepath.Join(dir, imageName(next.Metadata.MD5))); err != nil {
		t.Fatalf("the published image should be on disk before the commit: %v", err)
	}
	got, err := cache.Load(ctx)
	requireRecord(t, got, err, previous)

	// The unreferenced image is swept once a row that does not name it is read.
	if _, err := os.Lstat(filepath.Join(dir, imageName(next.Metadata.MD5))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the orphaned image should be swept: %v", err)
	}
}

func TestCancellationDoesNotWrite(t *testing.T) {
	cache, db, dir := openCache(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	record := recordFor(t, sloganFixture(t, 8, 6, 0x66), 8, 6)
	if err := cache.Save(ctx, record); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled save = %v", err)
	}
	if _, err := cache.Load(context.Background()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a canceled save stored a record: %v", err)
	}
	var rows int
	if err := db.QueryRow(context.Background(), `SELECT COUNT(*) FROM branding_record`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rows = %d, %v", rows, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a canceled save left %d files", len(entries))
	}
}

func TestInvalidReplacementPreservesTheOldRecord(t *testing.T) {
	cache, _, _ := openCache(t)
	ctx := context.Background()
	previous := recordFor(t, sloganFixture(t, 8, 6, 0x77), 8, 6)
	if err := cache.Save(ctx, previous); err != nil {
		t.Fatal(err)
	}
	image := sloganFixture(t, 8, 6, 0x88)
	cases := map[string]func() Record{
		"wrong checksum": func() Record {
			r := recordFor(t, image, 8, 6)
			r.Metadata.MD5 = strings.Repeat("0", 32)
			return r
		},
		"wrong byte count": func() Record {
			r := recordFor(t, image, 8, 6)
			r.Metadata.Bytes++
			return r
		},
		"wrong dimensions": func() Record {
			r := recordFor(t, image, 8, 6)
			r.Metadata.Width, r.Metadata.Height = 9, 6
			return r
		},
		"incomplete metadata": func() Record {
			r := recordFor(t, image, 8, 6)
			r.Metadata.VersionName = ""
			return r
		},
		"not an image": func() Record {
			r := recordFor(t, image, 8, 6)
			r.Image = []byte("not an image at all")
			r.Metadata.Bytes = len(r.Image)
			return r
		},
	}
	for label, build := range cases {
		t.Run(label, func(t *testing.T) {
			if err := cache.Save(ctx, build()); !errors.Is(err, errInvalidRecord) {
				t.Fatalf("invalid record = %v, want an invalid record", err)
			}
			got, err := cache.Load(ctx)
			requireRecord(t, got, err, previous)
		})
	}
}

func TestSweepRemovesOnlyUnreferencedImages(t *testing.T) {
	cache, _, dir := openCache(t)
	ctx := context.Background()
	kept := recordFor(t, sloganFixture(t, 8, 6, 0x99), 8, 6)
	if err := cache.Save(ctx, kept); err != nil {
		t.Fatal(err)
	}
	// The leftovers an interrupted save can leave behind, plus the files a
	// rolled-back build owns.
	orphan := filepath.Join(dir, imageName(strings.Repeat("ab", 16)))
	stage := filepath.Join(dir, stagePrefix+"orphan")
	legacy := []string{
		filepath.Join(dir, legacyImage),
		filepath.Join(dir, metadataMirror),
		filepath.Join(dir, "transaction.json"),
	}
	for _, path := range append([]string{orphan, stage}, legacy...) {
		if err := os.WriteFile(path, []byte("left behind"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got, err := cache.Load(ctx)
	requireRecord(t, got, err, kept)
	for _, path := range []string{orphan, stage} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s should be swept: %v", filepath.Base(path), err)
		}
	}
	for _, path := range append([]string{filepath.Join(dir, imageName(kept.Metadata.MD5))}, legacy...) {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("%s must survive the sweep: %v", filepath.Base(path), err)
		}
	}
}

func TestImportLegacyAdoptsThePair(t *testing.T) {
	cache, db, dir := openCache(t)
	ctx := context.Background()
	want := recordFor(t, sloganFixture(t, 8, 6, 0xAA), 8, 6)
	if err := os.WriteFile(filepath.Join(dir, legacyImage), want.Image, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, metadataMirror), metadataJSON(t, want.Metadata), 0o600); err != nil {
		t.Fatal(err)
	}

	ran, err := db.RunImport(ctx, Domain, ImportVersion, cache.ImportLegacy)
	if err != nil || !ran {
		t.Fatalf("import = %v, %v", ran, err)
	}
	got, err := cache.Load(ctx)
	requireRecord(t, got, err, want)
	if stale, err := db.MirrorStale(ctx, Domain); err != nil || stale {
		t.Fatalf("the adopted mirror must be recorded: stale=%v, %v", stale, err)
	}
	if ran, err := db.RunImport(ctx, Domain, ImportVersion, cache.ImportLegacy); err != nil || ran {
		t.Fatalf("second import = %v, %v; want a no-op", ran, err)
	}
}

func TestImportLegacyRejectsCorruptSources(t *testing.T) {
	cache, db, dir := openCache(t)
	ctx := context.Background()
	kept := recordFor(t, sloganFixture(t, 8, 6, 0xBB), 8, 6)
	if err := cache.Save(ctx, kept); err != nil {
		t.Fatal(err)
	}
	image := sloganFixture(t, 8, 6, 0xCC)
	valid := recordFor(t, image, 8, 6)
	validJSON := metadataJSON(t, valid.Metadata)
	duplicate := []byte(`{"cover_code":"cover123456","md5":"` + valid.Metadata.MD5 + `","md5":"` + valid.Metadata.MD5 + `"}`)
	unknown := append(validJSON[:len(validJSON)-1], []byte(`,"extra":1}`)...)
	mismatched := recordFor(t, image, 8, 6)
	mismatched.Metadata.MD5 = strings.Repeat("d", 32)

	cases := map[string]struct {
		metadata []byte
		image    []byte
		wantFile string
	}{
		"unknown field":     {unknown, image, metadataMirror},
		"duplicate field":   {duplicate, image, metadataMirror},
		"checksum mismatch": {metadataJSON(t, mismatched.Metadata), image, metadataMirror},
		"missing image":     {validJSON, nil, legacyImage},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(dir, metadataMirror), tc.metadata, 0o600); err != nil {
				t.Fatal(err)
			}
			imagePath := filepath.Join(dir, legacyImage)
			if tc.image != nil {
				if err := os.WriteFile(imagePath, tc.image, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				_ = os.Remove(imagePath)
			}
			_, err := db.RunImport(ctx, Domain, ImportVersion, cache.ImportLegacy)
			if err == nil || !strings.Contains(err.Error(), tc.wantFile) {
				t.Fatalf("corrupt source = %v, want an error naming %s", err, tc.wantFile)
			}
			if after, err := os.ReadFile(filepath.Join(dir, metadataMirror)); err != nil || !bytes.Equal(after, tc.metadata) {
				t.Fatalf("a rejected import must not rewrite the metadata: %v", err)
			}
			got, err := cache.Load(ctx)
			requireRecord(t, got, err, kept)
		})
	}
}

func TestImportLegacyWithoutASourceIsANoOp(t *testing.T) {
	ctx := context.Background()
	absent := filepath.Join(t.TempDir(), "branding")
	db := dbtest.Open(t)
	cache, err := Open(db, absent)
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := db.RunImport(ctx, Domain, ImportVersion, cache.ImportLegacy); err != nil || !ran {
		t.Fatalf("import without a directory = %v, %v", ran, err)
	}
	if _, err := cache.Load(ctx); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("import without a source stored a record: %v", err)
	}

	// An image without its metadata is a half pair from an interrupted write; it is
	// reported as no cache rather than adopted on its own.
	orphan, db2, dir := openCache(t)
	if err := os.WriteFile(filepath.Join(dir, legacyImage), sloganFixture(t, 8, 6, 0xDD), 0o600); err != nil {
		t.Fatal(err)
	}
	if ran, err := db2.RunImport(ctx, Domain, ImportVersion, orphan.ImportLegacy); err != nil || !ran {
		t.Fatalf("import of a lone image = %v, %v", ran, err)
	}
	if _, err := orphan.Load(ctx); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a lone legacy image = %v, want no cache", err)
	}
	var rows int
	if err := db2.QueryRow(ctx, `SELECT COUNT(*) FROM branding_record`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rows = %d, %v", rows, err)
	}
}

// decodeRowName guards the assumption the cache relies on: the row names the file
// by the digest it also stores.
func TestRowNamesTheFileItDescribes(t *testing.T) {
	cache, db, _ := openCache(t)
	ctx := context.Background()
	record := recordFor(t, sloganFixture(t, 8, 6, 0xEE), 8, 6)
	if err := cache.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	var name, digest string
	var stored sql.NullInt64
	err := db.QueryRow(ctx, `SELECT image_path, md5, last_modified FROM branding_record WHERE id = 1`).
		Scan(&name, &digest, &stored)
	if err != nil {
		t.Fatal(err)
	}
	parsed, ok := parseImageName(name)
	if !ok || parsed != strings.ToLower(digest) {
		t.Fatalf("row names %q for md5 %q", name, digest)
	}
	if got := database.StampTime(stored); !got.Equal(record.Metadata.LastModified) {
		t.Fatalf("last modified = %v, want %v", got, record.Metadata.LastModified)
	}
}
