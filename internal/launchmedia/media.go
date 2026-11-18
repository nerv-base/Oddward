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
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nerv-base/Oddward/internal/imagecheck"
)

const (
	cdnHost    = "yhcdn1.wmupd.com"
	cdnRoot    = "/hd/publish_ob/launcher/"
	versionURL = "https://" + cdnHost + cdnRoot + "Version.ini"
	maxPoster  = 16 << 20
	maxVideo   = 64 << 20
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

// Load checks the active manifest once. A failed refresh leaves the last
// verified files usable, including when only one of the two assets is available.
func (c *Client) Load(ctx context.Context) (video string, poster image.Image, err error) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	if err := os.MkdirAll(c.dir, 0700); err != nil {
		return "", nil, err
	}
	videoPath, posterPath := filepath.Join(c.dir, "bg.mp4"), filepath.Join(c.dir, "poster")
	previousVideo := readCache(videoPath, maxVideo)
	if !isMP4(previousVideo) {
		previousVideo = nil
	} else {
		video = videoPath
	}
	previousPoster := readCache(posterPath, maxPoster)
	if poster, err = decodePoster(previousPoster); err != nil {
		previousPoster = nil
	}
	manifest, still, motion, err := c.discover(ctx)
	if err != nil {
		return video, poster, err
	}
	var stillErr, motionErr error
	if still.Path != "" && !still.matches(previousPoster) {
		var data []byte
		data, stillErr = c.download(ctx, manifest, still, maxPoster)
		if stillErr == nil {
			var decoded image.Image
			decoded, stillErr = decodePoster(data)
			if stillErr == nil {
				stillErr = store(ctx, posterPath, data)
				if stillErr == nil {
					poster = decoded
				}
			}
		}
	}
	if motion.Path != "" && !motion.matches(previousVideo) {
		var data []byte
		data, motionErr = c.download(ctx, manifest, motion, maxVideo)
		if motionErr == nil {
			if !isMP4(data) {
				motionErr = errors.New("launcher media: invalid MP4 header")
			} else if motionErr = store(ctx, videoPath, data); motionErr == nil {
				video = videoPath
			}
		}
	}
	return video, poster, errors.Join(stillErr, motionErr)
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
	if len(data) < 16 || string(data[4:8]) != "ftyp" {
		return false
	}
	headerSize := uint64(binary.BigEndian.Uint32(data[:4]))
	return headerSize >= 16 && headerSize <= uint64(len(data))
}

func decodePoster(data []byte) (image.Image, error) {
	if _, _, err := imagecheck.Check(data, imagecheck.Limits{MaxBytes: maxPoster, MaxSide: 4096, MaxPixels: 4096 * 2304, Formats: []string{"png", "jpeg"}}); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
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
