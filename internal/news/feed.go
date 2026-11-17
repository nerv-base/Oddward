// SPDX-License-Identifier: GPL-3.0-or-later

package news

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The data file is a script, not JSON: it wraps one object in an assignment.
// Its name carries a datestamp that changes whenever the site republishes, so
// the path is discovered from the public home page.
var (
	dataFilePattern = regexp.MustCompile(`include/(newsData[0-9]{8}\.js)`)
	articleURLShape = regexp.MustCompile(`^/news/([a-z]+)/([0-9]{8})/([A-Za-z0-9_-]+)\.html$`)
)

// dataPath finds the data file the public home page advertises.
func dataPath(page []byte) (string, error) {
	match := dataFilePattern.FindSubmatch(page)
	if match == nil {
		return "", ErrNoFeedLink
	}
	return "/include/" + string(match[1]), nil
}

// rawEntry is one entry as the official data file writes it.
type rawEntry struct {
	Title         string `json:"title"`
	URL           string `json:"url"`
	Time          string `json:"time"`
	ChannelCnName string `json:"channelCnName"`
	ChannelName   string `json:"channelName"`
}

// parseFeed maps the official data file onto the board's categories. Only the
// desktop buckets of the three official channels are used: the mixed "news"
// bucket repeats entries, and the "m" buckets mirror the mobile site.
func parseFeed(data []byte, base string) (Feed, error) {
	payload := stripTrailingCommas(objectBody(data))
	if len(payload) == 0 {
		return Feed{}, ErrFeedFormat
	}
	var root struct {
		PC json.RawMessage `json:"pc"`
	}
	if err := json.Unmarshal(payload, &root); err != nil {
		return Feed{}, fmt.Errorf("%w: %v", ErrFeedFormat, err)
	}
	if len(root.PC) == 0 || string(root.PC) == "null" {
		return Feed{}, fmt.Errorf("%w: no desktop channels", ErrFeedFormat)
	}
	var channels map[string]json.RawMessage
	if err := json.Unmarshal(root.PC, &channels); err != nil {
		return Feed{}, fmt.Errorf("%w: %v", ErrFeedFormat, err)
	}

	base = strings.TrimRight(strings.TrimSpace(base), "/")
	feed := Feed{}
	seen := map[string]bool{}
	total, usable := 0, 0
	for _, category := range []Category{Announcements, Events, News} {
		raw, ok := channels[category.Channel()]
		if !ok {
			continue
		}
		var entries []rawEntry
		if err := json.Unmarshal(raw, &entries); err != nil {
			return Feed{}, fmt.Errorf("%w: %s: %v", ErrFeedFormat, category.Channel(), err)
		}
		for _, entry := range entries {
			total++
			item, ok := mapEntry(entry, category, base)
			if !ok || seen[item.URL] {
				continue
			}
			seen[item.URL] = true
			usable++
			feed.Items = append(feed.Items, item)
		}
	}
	if total == 0 {
		// An official feed that lists nothing is an empty board, not an error.
		return feed, nil
	}
	if usable == 0 {
		return Feed{}, ErrEntryShape
	}
	sort.SliceStable(feed.Items, func(i, j int) bool {
		return feed.Items[i].Date.After(feed.Items[j].Date)
	})
	return feed, nil
}

// objectBody trims the assignment the data file wraps its object in.
func objectBody(data []byte) []byte {
	start := strings.IndexByte(string(data), '{')
	if start < 0 {
		return nil
	}
	end := strings.LastIndexByte(string(data), '}')
	if end < start {
		return nil
	}
	return data[start : end+1]
}

// stripTrailingCommas drops the commas the script leaves directly before a
// closing brace or bracket: JavaScript allows them, JSON does not. Strings are
// copied byte for byte, so a title may contain any punctuation.
func stripTrailingCommas(payload []byte) []byte {
	out := make([]byte, 0, len(payload))
	inString, escaped := false, false
	for i := range payload {
		c := payload[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == ',' {
			next := i + 1
			for next < len(payload) && isJSONSpace(payload[next]) {
				next++
			}
			if next < len(payload) && (payload[next] == '}' || payload[next] == ']') {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

func isJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

// mapEntry accepts only entries that name an official article of the very
// channel they are listed under, with a readable date and title.
func mapEntry(entry rawEntry, category Category, base string) (Item, bool) {
	title := strings.TrimSpace(entry.Title)
	if title == "" {
		return Item{}, false
	}
	if channel := strings.TrimSpace(entry.ChannelName); channel != "" && channel != category.Channel() {
		return Item{}, false
	}
	published, err := time.Parse("2006-01-02", strings.TrimSpace(entry.Time))
	if err != nil {
		return Item{}, false
	}
	raw := strings.TrimSpace(entry.URL)
	if strings.Contains(raw, "..") || strings.Contains(raw, "//") {
		return Item{}, false
	}
	match := articleURLShape.FindStringSubmatch(raw)
	if match == nil {
		return Item{}, false
	}
	if match[1] != category.Channel() {
		return Item{}, false
	}
	if _, err := url.Parse(base + raw); err != nil {
		return Item{}, false
	}
	return Item{
		Title:    title,
		URL:      base + raw,
		Date:     published,
		Category: category,
		Channel:  category.Channel(),
	}, true
}
