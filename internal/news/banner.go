// SPDX-License-Identifier: GPL-3.0-or-later

package news

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// maxBannerScript caps the carousel read. The real script is ~1.3 KiB.
const maxBannerScript = 64 << 10

// carouselKey is the name the launcher's banner script assigns its data to.
// Only this name is looked for; the script is never executed.
const carouselKey = "yh_data_data"

// rawBanner is one slide as the script states it. mlink is the mobile mirror
// and is deliberately not read.
type rawBanner struct {
	Title  string `json:"title"`
	BigPic string `json:"bigpic"`
	Link   string `json:"link"`
}

type rawCarousel struct {
	Lb1 *[]rawBanner `json:"lb1"`
}

// Banners reads the official carousel on its own, so a caller can fill a
// carousel panel without re-reading the news page.
func (c *Client) Banners(ctx context.Context) ([]Banner, error) {
	target := strings.TrimSpace(c.BannerScript)
	if target == "" {
		target = BannerScriptURL
	}
	if err := allowedBy(target, c.DataHosts); err != nil {
		return nil, err
	}
	script, err := c.getWith(ctx, target, maxBannerScript, c.DataHosts)
	if err != nil {
		return nil, err
	}
	return parseBanners(script, c.origin(), c.Hosts, c.ImageHosts)
}

// parseBanners reads the carousel out of the banner script as data. The script
// is a JavaScript assignment, so the object literal is located by name and cut
// out by matching braces while respecting string literals; nothing is
// evaluated. Trailing commas, a trailing unrelated array and trailing code are
// all tolerated, exactly as they appear upstream.
//
// The carousel order is the site's order. A slide is kept only when its image
// is an official HTTPS image and its link stays on the official site; a
// carousel whose slides are all unusable is reported rather than shown empty.
func parseBanners(script []byte, base string, pageHosts, imageHosts []string) ([]Banner, error) {
	object, err := carouselObject(string(script))
	if err != nil {
		return nil, err
	}
	var carousel rawCarousel
	if err := json.Unmarshal([]byte(object), &carousel); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBannerScript, err)
	}
	if carousel.Lb1 == nil {
		return nil, fmt.Errorf("%w: the script states no lb1 carousel", ErrBannerScript)
	}
	slides := *carousel.Lb1
	if len(slides) == 0 {
		return nil, nil
	}
	var banners []Banner
	for _, slide := range slides {
		banner, ok := usableBanner(slide, base, pageHosts, imageHosts)
		if !ok {
			continue
		}
		banners = append(banners, banner)
	}
	if len(banners) == 0 {
		return nil, fmt.Errorf("%w: every slide names an unusable image or link", ErrBannerScript)
	}
	return banners, nil
}

func usableBanner(slide rawBanner, base string, pageHosts, imageHosts []string) (Banner, bool) {
	title := strings.Join(strings.Fields(slide.Title), " ")
	if title == "" {
		return Banner{}, false
	}
	link, ok := officialPageURL(slide.Link, base, pageHosts)
	if !ok {
		return Banner{}, false
	}
	image, ok := officialImageURL(slide.BigPic, imageHosts)
	if !ok {
		return Banner{}, false
	}
	return Banner{Title: title, URL: link, ImageURL: image}, true
}

// carouselObject finds the object literal the script assigns to yh_data_data and
// returns it as JSON text. Brace matching respects quotes, so a brace inside a
// title cannot end the object early.
func carouselObject(script string) (string, error) {
	for offset := 0; ; {
		found := strings.Index(script[offset:], carouselKey)
		if found < 0 {
			return "", fmt.Errorf("%w: the script assigns no %s", ErrBannerScript, carouselKey)
		}
		after := offset + found + len(carouselKey)
		rest := script[after:]
		trimmed := strings.TrimLeft(rest, " \t\r\n")
		skipped := len(rest) - len(trimmed)
		if strings.HasPrefix(trimmed, "=") {
			// Skip the assignment and any whitespace before the object.
			body := strings.TrimLeft(trimmed[1:], " \t\r\n")
			if !strings.HasPrefix(body, "{") {
				return "", fmt.Errorf("%w: %s is not assigned an object", ErrBannerScript, carouselKey)
			}
			return balancedObject(body)
		}
		// A mention that is not an assignment (a comment, a read of the
		// variable): keep looking for the real assignment.
		offset = after + skipped
		if offset >= len(script) {
			return "", fmt.Errorf("%w: the script assigns no %s", ErrBannerScript, carouselKey)
		}
	}
}

// balancedObject cuts the first balanced {...} out of text, respecting quoted
// strings and a single escape character.
func balancedObject(text string) (string, error) {
	depth := 0
	var quote byte
	escaped := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case escaped:
			escaped = false
		case quote != 0:
			if c == '\\' {
				escaped = true
			} else if c == quote {
				quote = 0
			}
		case c == '"':
			quote = c
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return string(stripTrailingCommas([]byte(text[:i+1]))), nil
			}
		}
	}
	return "", fmt.Errorf("%w: the carousel object is not closed", ErrBannerScript)
}

// officialPageURL resolves a carousel link and keeps it on the official site.
func officialPageURL(href, base string, hosts []string) (string, bool) {
	href = strings.TrimSpace(href)
	if href == "" {
		return "", false
	}
	from, err := url.Parse(base)
	if err != nil || from.Host == "" {
		return "", false
	}
	ref, err := url.Parse(href)
	if err != nil {
		return "", false
	}
	resolved := from.ResolveReference(ref)
	if resolved.Scheme != "https" || resolved.Host == "" || resolved.User != nil {
		return "", false
	}
	if !hostAllowed(resolved.Host, hosts) {
		return "", false
	}
	if strings.Contains(resolved.Path, "..") {
		return "", false
	}
	return resolved.String(), true
}

// officialImageURL keeps an image on the official HTTPS image hosts. Only these
// URLs are ever handed to the image reader.
func officialImageURL(raw string, hosts []string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return "", false
	}
	if !hostAllowed(parsed.Host, hosts) {
		return "", false
	}
	if strings.Contains(parsed.Path, "..") {
		return "", false
	}
	return parsed.String(), true
}
