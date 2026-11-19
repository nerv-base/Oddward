// SPDX-License-Identifier: GPL-3.0-or-later

// Package launchmedia reads and caches the official CN launcher's public backgrounds.
package launchmedia

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5" // Official CDN transfer checksums, not publisher signatures.
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	xdraw "golang.org/x/image/draw"

	"github.com/nerv-base/Oddward/internal/imagecheck"
)

const (
	cdnHost     = "yhcdn1.wmupd.com"
	cdnRoot     = "/hd/publish_ob/launcher/"
	versionURL  = "https://" + cdnHost + cdnRoot + "Version.ini"
	maxPoster   = 16 << 20
	maxVideo    = 64 << 20
	maxPreview  = 4 << 20
	videoName   = "bg.mp4"
	posterName  = "poster"
	previewName = "poster-preview.jpg"

	// A preview this size stays sharp on a 1440p window after the system scales
	// it, and decodes in a fraction of the full poster's time.
	previewWidth  = 1920
	previewHeight = 1080
)

var (
	manifestPath = regexp.MustCompile(`^` + cdnRoot + `[0-9][0-9._-]*/AllFiles\.xml$`)
	assetPath    = regexp.MustCompile(`^/ResFilesM/[0-9]+/bgimgs/(bg\.mp4|bg_0\.(png|jpe?g))$`)
)

type Client struct {
	dir  string
	http *http.Client
}

func New(dir string, transport http.RoundTripper) *Client {
	return &Client{dir: dir, http: &http.Client{
		Transport: transport, Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 3 || !official(req.URL) || req.URL.Path != via[0].URL.Path {
				return errors.New("launcher media: unexpected redirect")
			}
			return nil
		},
	}}
}

type asset struct {
	Path      string `xml:"Path,attr"`
	Size      int64  `xml:"Size,attr"`
	ZipSize   int64  `xml:"ZipSize,attr"`
	Digest    string `xml:"Checksum,attr"`
	ZipDigest string `xml:"ZipChecksum,attr"`
}

// CachedArtwork is the artwork already on disk. It is read without any
// network request, so a window can show it before the manifest is checked.
// Video stays in memory because Media Foundation would otherwise hold a file
// handle that refuses to be replaced while the old frame is still playing.
type CachedArtwork struct {
	Video  []byte
	Poster image.Image
}

// ArtworkUpdate is the artwork a refresh replaced. Unchanged fields stay zero:
// the caller already has the cached copy and must not decode or reopen it.
type ArtworkUpdate struct {
	Video         []byte
	Poster        image.Image
	VideoChanged  bool
	PosterChanged bool
}

// Cached reads the last verified files. A corrupted still is reported without
// hiding a usable video; an unreadable video is treated as absent. The small
// preview is preferred so a 4K poster does not decode on every launch.
func (c *Client) Cached() (CachedArtwork, error) {
	var cached CachedArtwork
	videoPath := filepath.Join(c.dir, videoName)
	if data := readCache(videoPath, maxVideo); data != nil && isMP4(data) {
		cached.Video = data
		removeLegacyVideos(c.dir)
	} else {
		cached.Video = promoteLegacyVideo(c.dir, videoPath)
	}
	if data := readCache(filepath.Join(c.dir, previewName), maxPreview); data != nil {
		if poster, err := decodePreview(data); err == nil {
			cached.Poster = poster
		}
	}
	if cached.Poster == nil {
		if data := readCache(filepath.Join(c.dir, posterName), maxPoster); data != nil {
			poster, err := decodePoster(data)
			if err != nil {
				return cached, fmt.Errorf("launcher media: cached poster: %w", err)
			}
			cached.Poster = poster
		}
	}
	return cached, nil
}

// Refresh checks the active manifest once and returns only the assets it
// replaced. A failed refresh leaves the verified cache usable, including when
// only one of the two assets changed.
func (c *Client) Refresh(ctx context.Context) (ArtworkUpdate, error) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	if err := os.MkdirAll(c.dir, 0700); err != nil {
		return ArtworkUpdate{}, err
	}
	videoPath, posterPath := filepath.Join(c.dir, videoName), filepath.Join(c.dir, posterName)
	previewPath := filepath.Join(c.dir, previewName)
	manifest, still, motion, err := c.discover(ctx)
	if err != nil {
		return ArtworkUpdate{}, err
	}
	var update ArtworkUpdate
	var stillErr, motionErr error
	if still.Path != "" && !still.matchesFile(posterPath) {
		var data []byte
		data, stillErr = c.download(ctx, manifest, still, maxPoster)
		if stillErr == nil {
			var poster image.Image
			poster, stillErr = decodePoster(data)
			if stillErr == nil {
				if stillErr = store(ctx, posterPath, data); stillErr == nil {
					update.Poster, update.PosterChanged = poster, true
					// A preview is an optimization, never a reason to fail the
					// verified poster that is already stored.
					if preview, previewData, err := previewPoster(poster); err == nil {
						if store(ctx, previewPath, previewData) == nil {
							update.Poster = preview
						}
					}
				}
			}
		}
	} else if still.Path != "" {
		// Upgrade the existing full poster once, so the next launch does not
		// pay for decoding it again.
		if _, err := os.Lstat(previewPath); err != nil {
			if data := readCache(posterPath, maxPoster); data != nil {
				if poster, err := decodePoster(data); err == nil {
					if preview, previewData, err := previewPoster(poster); err == nil {
						if store(ctx, previewPath, previewData) == nil {
							update.Poster, update.PosterChanged = preview, true
						}
					}
				}
			}
		}
	}
	if motion.Path != "" && !motion.matchesFile(videoPath) {
		var data []byte
		data, motionErr = c.download(ctx, manifest, motion, maxVideo)
		if motionErr == nil {
			if !isMP4(data) {
				motionErr = errors.New("launcher media: invalid MP4 header")
			} else if motionErr = store(ctx, videoPath, data); motionErr == nil {
				update.Video, update.VideoChanged = data, true
			}
		}
	}
	return update, errors.Join(stillErr, motionErr)
}

func (c *Client) discover(ctx context.Context) (*url.URL, asset, asset, error) {
	ini, err := c.get(ctx, versionURL, 32<<10)
	if err != nil {
		return nil, asset{}, asset{}, err
	}
	var manifest *url.URL
	for line := range strings.Lines(strings.TrimPrefix(string(ini), "\ufeff")) {
		name, value, ok := strings.Cut(line, "=")
		if ok && strings.EqualFold(strings.TrimSpace(name), "FileListURL") {
			manifest, err = url.Parse(strings.TrimSpace(value))
			break
		}
	}
	if err != nil || !official(manifest) || !manifestPath.MatchString(manifest.Path) || manifest.RawQuery != "" {
		return nil, asset{}, asset{}, errors.New("launcher media: invalid FileListURL")
	}
	data, err := c.get(ctx, manifest.String(), 1<<20)
	if err != nil {
		return nil, asset{}, asset{}, err
	}
	var list struct {
		XMLName xml.Name `xml:"All_Files"`
		Files   []asset  `xml:"File"`
	}
	if err := xml.Unmarshal(data, &list); err != nil {
		return nil, asset{}, asset{}, fmt.Errorf("launcher media: parse AllFiles.xml: %w", err)
	}
	var still, motion asset
	for _, file := range list.Files {
		if !assetPath.MatchString(file.Path) {
			continue
		}
		if path.Base(file.Path) == "bg.mp4" {
			if motion.Path == "" {
				motion = file
			}
		} else if still.Path == "" || path.Ext(file.Path) == ".png" {
			still = file
		}
	}
	if still.Path == "" && motion.Path == "" {
		return nil, asset{}, asset{}, errors.New("launcher media: manifest has no supported backgrounds")
	}
	return manifest, still, motion, nil
}

func (c *Client) download(ctx context.Context, manifest *url.URL, file asset, limit int64) ([]byte, error) {
	if file.Size <= 0 || file.Size > limit || file.ZipSize <= 0 || file.ZipSize > limit+(1<<20) || !validDigest(file.Digest) || !validDigest(file.ZipDigest) {
		return nil, errors.New("launcher media: invalid manifest size or checksum")
	}
	archive := manifest.ResolveReference(&url.URL{Path: strings.TrimPrefix(file.Path, "/") + ".zip"})
	data, err := c.get(ctx, archive.String(), file.ZipSize)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != file.ZipSize || !matchesDigest(data, file.ZipDigest) {
		return nil, errors.New("launcher media: archive checksum or size mismatch")
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("launcher media: read ZIP: %w", err)
	}
	if len(z.File) != 1 || z.File[0].Name != path.Base(file.Path) || !z.File[0].Mode().IsRegular() || z.File[0].UncompressedSize64 != uint64(file.Size) {
		return nil, errors.New("launcher media: unexpected archive entry")
	}
	entry, err := z.File[0].Open()
	if err != nil {
		return nil, err
	}
	defer entry.Close()
	payload, err := imagecheck.ReadBounded(entry, file.Size)
	if err != nil {
		return nil, err
	}
	if !file.matches(payload) {
		return nil, errors.New("launcher media: payload checksum or size mismatch")
	}
	return payload, ctx.Err()
}

func official(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.Host == cdnHost && u.User == nil && u.Opaque == "" && u.RawPath == "" && u.Fragment == "" && strings.HasPrefix(u.Path, cdnRoot)
}

func (c *Client) get(ctx context.Context, rawURL string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if !official(req.URL) {
		return nil, errors.New("launcher media: unexpected CDN URL")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("launcher media: unexpected response for %s (HTTP %d, %d bytes)", path.Base(req.URL.Path), resp.StatusCode, resp.ContentLength)
	}
	return imagecheck.ReadBounded(resp.Body, maxBytes)
}

func (a asset) matches(data []byte) bool {
	return len(data) != 0 && int64(len(data)) == a.Size && matchesDigest(data, a.Digest)
}

// matchesFile compares the manifest without loading a whole video into memory.
func (a asset) matchesFile(name string) bool {
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || a.Size <= 0 || info.Size() != a.Size {
		return false
	}
	file, err := os.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()
	digest := md5.New()
	n, err := io.Copy(digest, file)
	if err != nil || n != a.Size {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), a.Digest)
}

func validDigest(s string) bool {
	if len(s) != md5.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func matchesDigest(data []byte, s string) bool {
	digest := md5.Sum(data)
	return strings.EqualFold(hex.EncodeToString(digest[:]), s)
}

func isMP4(data []byte) bool {
	return isMP4Header(data, int64(len(data)))
}

func isMP4Header(header []byte, size int64) bool {
	if len(header) < 16 || size < 16 || string(header[4:8]) != "ftyp" {
		return false
	}
	headerSize := uint64(binary.BigEndian.Uint32(header[:4]))
	return headerSize >= 16 && headerSize <= uint64(size)
}

func decodePoster(data []byte) (image.Image, error) {
	if _, _, err := imagecheck.Check(data, imagecheck.Limits{MaxBytes: maxPoster, MaxSide: 4096, MaxPixels: 4096 * 2304, Formats: []string{"png", "jpeg"}}); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

// promoteLegacyVideo moves the newest verified versioned video to the fixed
// name once, so upgrading does not lose an offline cache. It returns the bytes
// it could read even if the rename fails.
func promoteLegacyVideo(dir, target string) []byte {
	legacy, _ := filepath.Glob(filepath.Join(dir, "bg-*.mp4"))
	var newest string
	var newestTime time.Time
	for _, name := range legacy {
		info, err := os.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxVideo {
			continue
		}
		if newest == "" || info.ModTime().After(newestTime) {
			newest, newestTime = name, info.ModTime()
		}
	}
	if newest == "" {
		return nil
	}
	data := readCache(newest, maxVideo)
	if data == nil || !isMP4(data) {
		return nil
	}
	if store(context.Background(), target, data) == nil {
		for _, name := range legacy {
			_ = os.Remove(name)
		}
	}
	return data
}

// removeLegacyVideos deletes the versioned names used before the switch to a
// single fixed bg.mp4. It runs on the cache-read path, before any player can
// hold one of those files.
func removeLegacyVideos(dir string) {
	legacy, _ := filepath.Glob(filepath.Join(dir, "bg-*.mp4"))
	for _, name := range legacy {
		_ = os.Remove(name)
	}
}

func readCache(name string, limit int64) []byte {
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
		return nil
	}
	file, err := os.Open(name)
	if err != nil {
		return nil
	}
	defer file.Close()
	data, err := imagecheck.ReadBounded(file, limit)
	if err != nil {
		return nil
	}
	return data
}

// previewPoster scales a verified poster down once, on the refresh path, so
// startup only decodes a small JPEG. The full poster remains on disk as the
// fallback if the preview is missing or damaged.
func previewPoster(poster image.Image) (image.Image, []byte, error) {
	bounds := poster.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, nil, errors.New("launcher media: empty poster")
	}
	scaledWidth, scaledHeight := width, height
	if width > previewWidth || height > previewHeight {
		scale := math.Min(float64(previewWidth)/float64(width), float64(previewHeight)/float64(height))
		scaledWidth = max(1, int(math.Round(float64(width)*scale)))
		scaledHeight = max(1, int(math.Round(float64(height)*scale)))
	}
	scaled := poster
	if scaledWidth != width || scaledHeight != height {
		dst := image.NewRGBA(image.Rect(0, 0, scaledWidth, scaledHeight))
		xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), poster, bounds, xdraw.Src, nil)
		scaled = dst
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: 85}); err != nil {
		return nil, nil, err
	}
	return scaled, buf.Bytes(), nil
}

func decodePreview(data []byte) (image.Image, error) {
	if _, _, err := imagecheck.Check(data, imagecheck.Limits{MaxBytes: maxPreview, MaxSide: previewWidth, MaxPixels: previewWidth * previewHeight, Formats: []string{"jpeg"}}); err != nil {
		return nil, err
	}
	poster, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if poster.Bounds().Dx() > previewWidth || poster.Bounds().Dy() > previewHeight {
		return nil, errors.New("launcher media: preview is larger than its limit")
	}
	return poster, nil
}

func store(ctx context.Context, target string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(target), ".media-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close(), ctx.Err()); err != nil {
		return err
	}
	return os.Rename(f.Name(), target)
}
