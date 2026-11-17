// SPDX-License-Identifier: GPL-3.0-or-later

package news

import (
	"context"
	"fmt"
	"net/http"

	"github.com/nerv-base/Oddward/internal/imagecheck"
)

// Image caps: a banner is a still picture shown behind the news list, not a
// payload. The byte cap bounds the transfer; the side and pixel caps bound what
// the UI would allocate when it decodes.
const (
	maxImage       = 4 << 20
	maxImageSide   = 4096
	maxImagePixels = 16 << 20
)

// Image reads one official banner image into memory and returns its bytes.
//
// It accepts exactly one kind of target: an HTTPS URL whose host is on the
// official image allowlist, with no credentials, on every redirect hop. It
// never writes to disk, never executes anything, and refuses local files and
// non-image payloads. The bytes are only ever returned; callers own them for as
// long as they display them.
func (c *Client) Image(ctx context.Context, rawURL string) ([]byte, error) {
	if err := allowedBy(rawURL, c.ImageHosts); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrImage, err)
	}
	data, err := c.imageBody(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	if err := checkImageBytes(data); err != nil {
		return nil, err
	}
	return data, nil
}

// imageBody fetches the bytes, re-checking the image allowlist on every hop.
func (c *Client) imageBody(ctx context.Context, rawURL string) ([]byte, error) {
	current := rawURL
	for hop := 0; hop <= maxRedirects; hop++ {
		if err := allowedBy(current, c.ImageHosts); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrImage, err)
		}
		if err := c.pause(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrImage, err)
		}
		req.Header.Set("User-Agent", "oddward-launcher (read-only announcement reader)")
		req.Header.Set("Accept", "image/png,image/jpeg;q=0.9")
		resp, err := c.httpClient().Do(req)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrImage, err)
		}
		body, readErr := readBounded(resp.Body, maxImage)
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrImage, readErr)
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			return body, nil
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			location := resp.Header.Get("Location")
			if location == "" {
				return nil, fmt.Errorf("%w: %s: status %d without a location", ErrImage, current, resp.StatusCode)
			}
			next, err := resolveURL(current, location)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrImage, err)
			}
			current = next
		default:
			return nil, fmt.Errorf("%w: %s: status %d", ErrImage, current, resp.StatusCode)
		}
	}
	return nil, fmt.Errorf("%w: %s: too many redirects", ErrImage, rawURL)
}

// checkImageBytes accepts only a JPEG or PNG payload, and only one whose decoded
// size stays inside the budget.
func checkImageBytes(data []byte) error {
	_, _, err := imagecheck.Check(data, imagecheck.Limits{
		MaxBytes: maxImage, MaxSide: maxImageSide, MaxPixels: maxImagePixels,
		Formats: []string{"jpeg", "png"},
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrImage, err)
	}
	return nil
}
