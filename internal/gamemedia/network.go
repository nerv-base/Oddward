// SPDX-License-Identifier: GPL-3.0-or-later
package gamemedia

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nerv-base/Oddward/internal/imagecheck"
)

// The semaphore covers all Cache instances in this process.
var downloads = make(chan struct{}, 4)

type downloaded struct {
	status int
	header http.Header
	data   []byte
}

// Distinguish a broken connection from an intentionally bounded read: only
// transport failures qualify for returning an expired, previously valid icon.
type networkReader struct {
	io.Reader
	failed bool
}

func (r *networkReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err != nil && err != io.EOF {
		r.failed = true
	}
	return n, err
}
func allowedURL(raw string, hosts []string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("gamemedia: URL: %w", err)
	}
	if u.Scheme != "https" || u.User != nil || u.Host == "" || u.Opaque != "" {
		return errors.New("gamemedia: only credential-free HTTPS URLs are allowed")
	}
	for _, host := range hosts {
		if strings.EqualFold(u.Host, host) {
			return nil
		}
	}
	return errors.New("gamemedia: host is not allowlisted")
}
func (c *Cache) download(ctx context.Context, key Key, previous cachedIcon, hasOld bool) (downloaded, bool, error) {
	select {
	case downloads <- struct{}{}:
		defer func() { <-downloads }()
	case <-ctx.Done():
		return downloaded{}, true, ctx.Err()
	}
	current := c.base + key.remotePath()
	for hop := 0; hop <= 3; hop++ {
		if err := allowedURL(current, c.hosts); err != nil {
			return downloaded{}, false, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			return downloaded{}, false, err
		}
		req.Header.Set("User-Agent", "oddward-launcher (game record icons)")
		req.Header.Set("Accept", "image/png")
		if hasOld {
			if previous.etag != "" {
				req.Header.Set("If-None-Match", previous.etag)
			}
			if previous.lastModified != "" {
				req.Header.Set("If-Modified-Since", previous.lastModified)
			}
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return downloaded{}, true, err
		}
		result := downloaded{status: resp.StatusCode, header: resp.Header.Clone()}
		switch resp.StatusCode {
		case http.StatusOK:
			if resp.ContentLength > maxIconBytes {
				resp.Body.Close()
				return downloaded{}, false, errors.New("gamemedia: image exceeds 1 MiB")
			}
			reader := &networkReader{Reader: resp.Body}
			data, err := imagecheck.ReadBounded(reader, maxIconBytes)
			resp.Body.Close()
			if err != nil {
				return downloaded{}, reader.failed, err
			}
			if err = checkIcon(data); err != nil {
				return downloaded{}, false, err
			}
			result.data = data
			return result, false, nil
		case http.StatusNotFound:
			resp.Body.Close()
			return result, false, nil
		case http.StatusNotModified:
			resp.Body.Close()
			if !hasOld {
				return downloaded{}, false, errors.New("gamemedia: unexpected 304 without cached image")
			}
			return result, false, nil
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			location := resp.Header.Get("Location")
			resp.Body.Close()
			if location == "" {
				return downloaded{}, false, errors.New("gamemedia: redirect lacks Location")
			}
			base, err := url.Parse(current)
			if err != nil {
				return downloaded{}, false, err
			}
			to, err := url.Parse(location)
			if err != nil {
				return downloaded{}, false, err
			}
			current = base.ResolveReference(to).String()
		default:
			resp.Body.Close()
			return downloaded{}, false, fmt.Errorf("gamemedia: HTTP status %d", resp.StatusCode)
		}
	}
	return downloaded{}, false, errors.New("gamemedia: more than three redirects")
}
func checkIcon(data []byte) error {
	_, _, err := imagecheck.Check(data, imagecheck.Limits{MaxBytes: maxIconBytes, MaxSide: 1024, MaxPixels: 1024 * 1024, Formats: []string{"png"}})
	return err
}
func freshness(h http.Header) time.Duration {
	result := time.Hour
	for _, value := range h.Values("Cache-Control") {
		for _, part := range strings.Split(value, ",") {
			name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
			if !ok || !strings.EqualFold(name, "max-age") {
				continue
			}
			seconds, err := strconv.ParseInt(strings.Trim(value, "\""), 10, 64)
			if err != nil {
				continue
			}
			switch {
			case seconds < 3600:
				result = time.Hour
			case seconds > 7*24*3600:
				result = 7 * 24 * time.Hour
			default:
				result = time.Duration(seconds) * time.Second
			}
			return result
		}
	}
	return result
}
