// SPDX-License-Identifier: GPL-3.0-or-later

package news

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// maxLauncherPage caps the launcher page read. The real page is ~12 KiB.
const maxLauncherPage = 128 << 10

// dateSpanPattern is the only text shape accepted as a row date: the page shows
// 月/日 and nothing else.
var dateSpanPattern = regexp.MustCompile(`^([0-9]{1,2})/([0-9]{1,2})$`)

var launcherLogoURL = regexp.MustCompile(`^https://yh\.wanmei\.com/images/launcher[0-9]{6}/logo\.png$`)

// LogoAsset reads the original logo referenced by the official launcher page.
func (c *Client) LogoAsset(ctx context.Context) ([]byte, error) {
	page, err := c.get(ctx, c.origin()+LauncherPath, maxLauncherPage)
	if err != nil {
		return nil, err
	}
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return nil, err
	}
	for node := range doc.Descendants() {
		if isElement(node, "img") && hasClass(node, "logo") {
			if src := attr(node, "src"); launcherLogoURL.MatchString(src) {
				return c.Image(ctx, src)
			}
		}
	}
	return nil, fmt.Errorf("%w: official launcher page has no logo", ErrImage)
}

// launcherFeed reads the official launcher's own news page and, independently,
// its carousel. A carousel failure leaves Banners empty and preserves stories,
// unless the caller canceled the whole operation.
func (c *Client) launcherFeed(ctx context.Context) (Feed, error) {
	page, err := c.get(ctx, c.origin()+LauncherPath, maxLauncherPage)
	if err != nil {
		return Feed{}, err
	}
	feed, err := parseLauncherPage(page, c.origin(), c.Hosts)
	if err != nil {
		return Feed{}, err
	}
	feed.Source = SourceLauncher
	banners, bannerErr := c.Banners(ctx)
	// An optional carousel failure may leave stories usable, but cancellation
	// ends the whole caller operation and must never turn into a successful feed.
	if err := ctx.Err(); err != nil {
		return Feed{}, err
	}
	if bannerErr == nil {
		feed.Banners = banners
	}
	return feed, nil
}

// parseLauncherPage reads the launcher page's news blocks. Rows are filed by the
// channel their own article URL names, because the page's block order
// (资讯, 公告, 活动) is not the board's tab order (公告, 活动, 资讯).
//
// A page without any news block has changed shape and is an error. Three blocks
// that hold no row are an official empty board and are not an error; rows that
// all fail validation are reported instead of being shown as empty.
func parseLauncherPage(page []byte, base string, hosts []string) (Feed, error) {
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return Feed{}, fmt.Errorf("%w: %v", ErrLauncherPage, err)
	}
	blocks := newsBlocks(doc)
	if len(blocks) == 0 {
		return Feed{}, ErrLauncherPage
	}
	var items []Item
	seenURL := make(map[string]bool)
	rows := 0
	for _, block := range blocks {
		for _, row := range blockRows(block) {
			rows++
			item, ok := launcherRow(row, base, hosts)
			if !ok {
				continue
			}
			if seenURL[item.URL] {
				continue
			}
			seenURL[item.URL] = true
			items = append(items, item)
		}
	}
	if rows > 0 && len(items) == 0 {
		return Feed{}, ErrEntryShape
	}
	sortItems(items)
	return Feed{Items: items}, nil
}

// newsBlocks returns the div.news-cont blocks in document order.
func newsBlocks(root *html.Node) []*html.Node {
	var blocks []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if isElement(n, "div") && hasClass(n, "news-cont") {
			blocks = append(blocks, n)
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return blocks
}

// blockRows returns the top-level list rows of one block. A list nested inside a
// row is part of that row, not a row of its own.
func blockRows(block *html.Node) []*html.Node {
	var rows []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if isElement(n, "li") {
			rows = append(rows, n)
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(block)
	return rows
}

// launcherRow turns one row into an entry, or reports that the row is not an
// official article of one of the three channels.
func launcherRow(row *html.Node, base string, hosts []string) (Item, bool) {
	// The row's own anchor: the tooltip div carries anchors and spans too, and
	// they never name the row.
	anchor := directChild(row, "a")
	if anchor == nil {
		return Item{}, false
	}
	title := nodeText(anchor)
	if title == "" {
		return Item{}, false
	}
	rawURL, category, published, ok := officialArticleURL(attr(anchor, "href"), base, hosts)
	if !ok {
		return Item{}, false
	}
	// The row shows 月/日 only; the year comes from the article URL. The two may
	// disagree upstream, and what the row displays wins. No time of day is ever
	// invented, and an unreadable date stays empty rather than being guessed.
	date := time.Time{}
	if shown := rowDateText(row); shown != "" {
		date = displayedDate(shown, published.Year())
	}
	return Item{
		Title:    title,
		URL:      rawURL,
		Date:     date,
		Category: category,
		Channel:  category.Channel(),
	}, true
}

// officialArticleURL resolves a row link and accepts it only when it names an
// official article of one of the three channels.
func officialArticleURL(href, base string, hosts []string) (string, Category, time.Time, bool) {
	href = strings.TrimSpace(href)
	if href == "" {
		return "", 0, time.Time{}, false
	}
	from, err := url.Parse(base)
	if err != nil || from.Host == "" {
		return "", 0, time.Time{}, false
	}
	ref, err := url.Parse(href)
	if err != nil {
		return "", 0, time.Time{}, false
	}
	resolved := from.ResolveReference(ref)
	if resolved.Scheme != "https" || resolved.Host == "" || resolved.User != nil {
		return "", 0, time.Time{}, false
	}
	if !hostAllowed(resolved.Host, hosts) {
		return "", 0, time.Time{}, false
	}
	path := resolved.Path
	if strings.Contains(path, "..") || strings.Contains(path, "//") {
		return "", 0, time.Time{}, false
	}
	match := articleURLShape.FindStringSubmatch(path)
	if match == nil {
		return "", 0, time.Time{}, false
	}
	category, ok := CategoryFor(match[1])
	if !ok {
		return "", 0, time.Time{}, false
	}
	published, err := time.Parse("20060102", match[2])
	if err != nil {
		return "", 0, time.Time{}, false
	}
	return resolved.String(), category, published, true
}

// rowDateText is the last direct span of the row that reads as 月/日. Reading the
// last one keeps a leading tag span ("置顶") out of the date.
func rowDateText(row *html.Node) string {
	shown := ""
	for child := row.FirstChild; child != nil; child = child.NextSibling {
		if !isElement(child, "span") {
			continue
		}
		text := nodeText(child)
		if dateSpanPattern.MatchString(text) {
			shown = text
		}
	}
	return shown
}

// displayedDate combines the displayed 月/日 with the year from the article URL
// at 00:00 UTC. An impossible month or day yields a zero date instead of a
// silently normalised one.
func displayedDate(shown string, year int) time.Time {
	match := dateSpanPattern.FindStringSubmatch(strings.TrimSpace(shown))
	if match == nil || year <= 0 {
		return time.Time{}
	}
	month, err := strconv.Atoi(match[1])
	if err != nil {
		return time.Time{}
	}
	day, err := strconv.Atoi(match[2])
	if err != nil {
		return time.Time{}
	}
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}
	}
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if date.Month() != time.Month(month) || date.Day() != day {
		return time.Time{}
	}
	return date
}

// sortItems orders the board newest first; rows without a readable date follow.
func sortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i].Date, items[j].Date
		if left.IsZero() != right.IsZero() {
			return right.IsZero()
		}
		return left.After(right)
	})
}

func hostAllowed(host string, hosts []string) bool {
	for _, allowed := range hosts {
		if strings.EqualFold(allowed, host) {
			return true
		}
	}
	return false
}

func isElement(n *html.Node, tag string) bool {
	return n.Type == html.ElementNode && n.Data == tag
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	for _, field := range strings.Fields(attr(n, "class")) {
		if field == class {
			return true
		}
	}
	return false
}

// directChild returns the first direct element child with the given tag.
func directChild(n *html.Node, tag string) *html.Node {
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if isElement(child, tag) {
			return child
		}
	}
	return nil
}

// nodeText is the readable text of one node, whitespace collapsed.
func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}
