// SPDX-License-Identifier: GPL-3.0-or-later

package news

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nerv-base/Oddward/internal/imagecheck"
)

var (
	sloganURLPattern   = regexp.MustCompile(`https://yh\.wanmei\.com/images/(cover\d{6})/slogan\.png`)
	versionNamePattern = regexp.MustCompile(`^《异环》(\d+\.\d+)版本「([^」]+)」更新公告$`)
)

type sloganLink struct {
	coverCode string
	url       string
}

func sloganPath(page []byte) (sloganLink, error) {
	match := sloganURLPattern.FindSubmatch(page)
	if match == nil {
		return sloganLink{}, ErrNoSloganLink
	}
	return sloganLink{coverCode: string(match[1]), url: string(sloganURLPattern.Find(page))}, nil
}

type versionNameResult struct {
	number     string
	name       string
	articleURL string
}

type versionEntry struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Time        string `json:"time"`
	ChannelName string `json:"channelName"`
}

func parseVersionName(data []byte, base string) (versionNameResult, error) {
	payload := stripTrailingCommas(objectBody(data))
	if len(payload) == 0 {
		return versionNameResult{}, ErrFeedFormat
	}
	var root struct {
		PC json.RawMessage `json:"pc"`
	}
	if err := json.Unmarshal(payload, &root); err != nil {
		return versionNameResult{}, fmt.Errorf("%w: %v", ErrFeedFormat, err)
	}
	var channels map[string]json.RawMessage
	if len(root.PC) == 0 {
		return versionNameResult{}, ErrNoVersionName
	}
	if err := json.Unmarshal(root.PC, &channels); err != nil {
		return versionNameResult{}, ErrNoVersionName
	}
	parsedBase, err := url.Parse(strings.TrimRight(strings.TrimSpace(base), "/"))
	if err != nil || parsedBase.Scheme != "https" || parsedBase.Host == "" {
		return versionNameResult{}, ErrNoVersionName
	}
	var matches []versionCandidate
	for channel, raw := range channels {
		if channel != Announcements.Channel() {
			continue
		}
		var entries []versionEntry
		if json.Unmarshal(raw, &entries) != nil {
			continue
		}
		for _, entry := range entries {
			if strings.TrimSpace(entry.ChannelName) != "" && strings.TrimSpace(entry.ChannelName) != Announcements.Channel() {
				continue
			}
			match := versionNamePattern.FindStringSubmatch(strings.TrimSpace(entry.Title))
			if match == nil {
				continue
			}
			published, err := time.Parse("2006-01-02", strings.TrimSpace(entry.Time))
			if err != nil {
				continue
			}
			articleURL, ok := officialPageURL(entry.URL, parsedBase.String(), []string{parsedBase.Host})
			if !ok {
				continue
			}
			matches = append(matches, versionCandidate{
				versionNameResult: versionNameResult{number: match[1], name: match[2], articleURL: articleURL},
				date:              published,
			})
		}
	}
	if len(matches) == 0 {
		return versionNameResult{}, ErrNoVersionName
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].date.After(matches[j].date) })
	return matches[0].versionNameResult, nil
}

type versionCandidate struct {
	versionNameResult
	date time.Time
}

// Slogan is the official CN site's current campaign image metadata.
type Slogan struct {
	CoverCode    string
	URL          string
	ETag         string
	MD5          string
	Bytes        int
	Width        int
	Height       int
	LastModified time.Time
}

// VersionName is the version number and campaign name from a named update notice.
type VersionName struct {
	Number     string
	Name       string
	ArticleURL string
}

// Slogan reads the current CN campaign image metadata from the official cover
// page. The page supplies the image URL, so the client never guesses a cover
// date.
func (c *Client) Slogan(ctx context.Context) (Slogan, error) {
	_, result, err := c.SloganAsset(ctx, Slogan{})
	return result, err
}

// SloganAsset downloads the current CN campaign image and its metadata. When a
// prior verified record carries the same URL and ETag, a 304 response returns
// the prior metadata and no image bytes.
func (c *Client) SloganAsset(ctx context.Context, prior Slogan) ([]byte, Slogan, error) {
	page, err := c.get(ctx, c.origin()+"/", maxMainHTML)
	if err != nil {
		return nil, Slogan{}, err
	}
	link, err := sloganPath(page)
	if err != nil {
		return nil, Slogan{}, err
	}
	etag := ""
	if prior.URL == link.url {
		etag = prior.ETag
	}
	data, headers, status, err := c.imageFetch(ctx, link.url, etag)
	if err != nil {
		return nil, Slogan{}, err
	}
	if status == http.StatusNotModified {
		if prior.URL != link.url || prior.ETag == "" {
			return nil, Slogan{}, fmt.Errorf("%w: slogan returned 304 without a matching cache", ErrImage)
		}
		return nil, prior, nil
	}
	if status != http.StatusOK {
		return nil, Slogan{}, fmt.Errorf("%w: slogan returned status %d", ErrImage, status)
	}
	config, _, err := imagecheck.Check(data, imagecheck.Limits{MaxBytes: maxImage, MaxSide: maxImageSide, MaxPixels: maxImagePixels, Formats: []string{"jpeg", "png"}})
	if err != nil {
		return nil, Slogan{}, fmt.Errorf("%w: %w", ErrImage, err)
	}
	checksum := md5.Sum(data)
	result := Slogan{
		CoverCode: link.coverCode,
		URL:       link.url,
		ETag:      headers.Get("ETag"),
		MD5:       hex.EncodeToString(checksum[:]),
		Bytes:     len(data),
		Width:     config.Width,
		Height:    config.Height,
	}
	if value := headers.Get("Last-Modified"); value != "" {
		result.LastModified, _ = http.ParseTime(value)
	}
	return data, result, nil
}

// VersionName reads main.html, discovers its current data file, and selects the
// newest named CN version update announcement.
func (c *Client) VersionName(ctx context.Context) (number, name, articleURL string, err error) {
	page, err := c.get(ctx, c.origin()+"/main.html", maxMainHTML)
	if err != nil {
		return "", "", "", err
	}
	path, err := dataPath(page)
	if err != nil {
		return "", "", "", err
	}
	data, err := c.get(ctx, c.origin()+path, maxFeed)
	if err != nil {
		return "", "", "", err
	}
	result, err := parseVersionName(data, c.origin())
	if err != nil {
		return "", "", "", err
	}
	return result.number, result.name, result.articleURL, nil
}
