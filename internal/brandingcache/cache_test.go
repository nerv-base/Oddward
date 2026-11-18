// SPDX-License-Identifier: GPL-3.0-or-later
package brandingcache

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sloganFixture(t *testing.T, width, height int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func validRecord(t *testing.T, imageBytes []byte) Record {
	t.Helper()
	sum := md5.Sum(imageBytes)
	return Record{
		Image: imageBytes,
		Metadata: Metadata{
			CoverCode:     "cover123456",
			URL:           "https://yh.wanmei.com/images/cover123456/slogan.png",
			ETag:          "\"v1\"",
			MD5:           hex.EncodeToString(sum[:]),
			Bytes:         len(imageBytes),
			Width:         8,
			Height:        6,
			LastModified:  time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC),
			VersionNumber: "1.2",
			VersionName:   "星海回响",
			ArticleURL:    "https://yh.wanmei.com/notice/1",
			FetchedAt:     time.Date(2026, 11, 18, 12, 1, 0, 0, time.UTC),
		},
	}
}

func openCache(t *testing.T) (*Cache, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "branding")
	c, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return c, dir
}

func TestRoundTrip(t *testing.T) {
	c, dir := openCache(t)
	data := sloganFixture(t, 8, 6)
	want := validRecord(t, data)
	if err := c.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := c.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Image, want.Image) || got.Metadata != want.Metadata {
		t.Fatalf("round trip changed record: got %+v", got.Metadata)
	}
	for _, name := range []string{"slogan.png", "metadata.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("%s is not a regular file", name)
		}
	}
}

func TestOpenRequiresAbsoluteDirectory(t *testing.T) {
	if _, err := Open("relative"); err == nil {
		t.Fatal("relative cache directory accepted")
	}
}

func TestMissingAndPartialPair(t *testing.T) {
	c, dir := openCache(t)
	if _, err := c.Load(context.Background()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing pair error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "slogan.png"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Load(context.Background()); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial pair accepted or reported missing: %v", err)
	}
}

func TestLoadRejectsCorruptOversizedAndMismatchedRecords(t *testing.T) {
	data := sloganFixture(t, 8, 6)
	cases := []struct {
		name string
		make func(t *testing.T, dir string, record Record)
	}{
		{"corrupt image", func(t *testing.T, dir string, record Record) {
			if err := os.WriteFile(filepath.Join(dir, "slogan.png"), []byte("broken"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"oversized image", func(t *testing.T, dir string, record Record) {
			if err := os.WriteFile(filepath.Join(dir, "slogan.png"), bytes.Repeat([]byte("x"), 4<<20+1), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"checksum mismatch", func(t *testing.T, dir string, record Record) {
			record.Metadata.MD5 = strings.Repeat("0", 32)
			writeMetadata(t, dir, record.Metadata)
		}},
		{"oversized metadata", func(t *testing.T, dir string, record Record) {
			writeMetadata(t, dir, record.Metadata)
			if err := os.WriteFile(filepath.Join(dir, "metadata.json"), bytes.Repeat([]byte("x"), 64<<10+1), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, dir := openCache(t)
			want := validRecord(t, data)
			if err := os.WriteFile(filepath.Join(dir, "slogan.png"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			writeMetadata(t, dir, want.Metadata)
			tc.make(t, dir, want)
			if _, err := c.Load(context.Background()); err == nil {
				t.Fatal("invalid pair accepted")
			}
		})
	}
}

func TestStrictMetadataSchema(t *testing.T) {
	c, dir := openCache(t)
	data := sloganFixture(t, 8, 6)
	want := validRecord(t, data)
	if err := os.WriteFile(filepath.Join(dir, "slogan.png"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(want.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.TrimSuffix(string(raw), "}") + `,"unknown":1}`,
		strings.TrimSuffix(string(raw), "}") + `,"md5":"x"}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Load(context.Background()); err == nil {
			t.Fatal("invalid metadata accepted")
		}
	}
}

func TestCancellationDoesNotWrite(t *testing.T) {
	c, dir := openCache(t)
	data := sloganFixture(t, 8, 6)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Save(ctx, validRecord(t, data)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Save cancellation = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "slogan.png")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled Save wrote image: %v", err)
	}
	if _, err := c.Load(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load cancellation = %v", err)
	}
}

func TestInvalidReplacementPreservesOldRecord(t *testing.T) {
	c, _ := openCache(t)
	oldData := sloganFixture(t, 8, 6)
	old := validRecord(t, oldData)
	if err := c.Save(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	bad := old
	bad.Image = sloganFixture(t, 2, 2)
	bad.Metadata.Bytes = len(bad.Image)
	bad.Metadata.Width = 2
	bad.Metadata.Height = 2
	bad.Metadata.MD5 = strings.Repeat("0", 32)
	if err := c.Save(context.Background(), bad); err == nil {
		t.Fatal("invalid replacement accepted")
	}
	got, err := c.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Image, old.Image) || got.Metadata != old.Metadata {
		t.Fatal("invalid replacement damaged old record")
	}
}

func writeMetadata(t *testing.T, dir string, metadata Metadata) {
	t.Helper()
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
