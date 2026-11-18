// SPDX-License-Identifier: GPL-3.0-or-later

package news

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Response size caps. They are generous for the real pages (the home page is
// ~39 KiB, the data file ~9 KiB, an announcement ~22 KiB) and small enough
// that a misbehaving host cannot exhaust memory.
const (
	maxMainHTML = 128 << 10
	maxFeed     = 128 << 10
	maxArticle  = 512 << 10

	maxRedirects = 3
	httpTimeout  = 30 * time.Second
	defaultGap   = time.Second
)

// OfficialOrigin is the public home of the game's official site. Every URL
// the client reads lives under it.
const OfficialOrigin = "https://yh.wanmei.com"

// LauncherPath is the official launcher's news page. It carries the same three
// channels as the public site, one block each, and is what the shipped
// launcher itself reads.
const LauncherPath = "/launcher/launcher_ob2.html?expand=0"

// BannerScriptURL is the official carousel data script. It is read as data and
// never executed.
const BannerScriptURL = "https://static.games.wanmei.com/public/commonData/gamesData/gameSwiper/yh-gameSwiper.js"

// OfficialHosts are the only hosts this client will talk to.
func OfficialHosts() []string {
	return []string{"yh.wanmei.com", "www.yh.wanmei.com"}
}

// OfficialDataHosts are the hosts that serve the launcher's static data.
func OfficialDataHosts() []string {
	return []string{"static.games.wanmei.com"}
}

// OfficialImageHosts are the hosts that serve the launcher's artwork.
func OfficialImageHosts() []string {
	return []string{"yh.wanmei.com", "yhvmg.wmupd.com"}
}

// Client reads the official public announcements. The zero value is not
// usable; NewClient sets the official origin, the HTTPS host allowlists and a
// polite minimum gap between requests.
type Client struct {
	// HTTP performs the requests. It must not carry credentials.
	HTTP *http.Client
	// Base is the official site origin, e.g. "https://yh.wanmei.com".
	Base string
	// Hosts is the allowlist of official hosts a URL may point at.
	Hosts []string
	// DataHosts is the allowlist of hosts serving the launcher's static data.
	DataHosts []string
	// ImageHosts is the allowlist of hosts serving official artwork.
	ImageHosts []string
	// BannerScript is the absolute URL of the carousel data script. Empty
	// means the official one.
	BannerScript string
	// MinGap is the minimum delay between two requests to the same origin.
	MinGap time.Duration

	mu   sync.Mutex
	last time.Time
}

// NewClient returns a client for the official public site. It keeps no
// cookies and sends no credentials of any kind.
func NewClient() *Client {
	return &Client{
		HTTP: &http.Client{
			Timeout: httpTimeout,
			// Redirects are followed by get itself, which re-checks the
			// allowlist on every hop.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		Base:         OfficialOrigin,
		Hosts:        OfficialHosts(),
		DataHosts:    OfficialDataHosts(),
		ImageHosts:   OfficialImageHosts(),
		BannerScript: BannerScriptURL,
		MinGap:       defaultGap,
	}
}

// Feed reads the official launcher's news page and its carousel. Only when the
// launcher page itself cannot be read does it fall back to the public site
// feed, and the returned board says which upstream answered.
func (c *Client) Feed(ctx context.Context) (Feed, error) {
	board, launcherErr := c.launcherFeed(ctx)
	if launcherErr == nil {
		return board, nil
	}
	if ctx.Err() != nil {
		return Feed{}, ctx.Err()
	}
	board, siteErr := c.siteFeed(ctx)
	if siteErr == nil {
		return board, nil
	}
	return Feed{}, fmt.Errorf("news: the launcher page failed (%w) and so did the fallback (%w)", launcherErr, siteErr)
}

// siteFeed is the public web site's own announcement feed: home page, then the
// data file it advertises, then the entries.
func (c *Client) siteFeed(ctx context.Context) (Feed, error) {
	page, err := c.get(ctx, c.origin()+"/main.html", maxMainHTML)
	if err != nil {
		return Feed{}, err
	}
	path, err := dataPath(page)
	if err != nil {
		return Feed{}, err
	}
	data, err := c.get(ctx, c.origin()+path, maxFeed)
	if err != nil {
		return Feed{}, err
	}
	feed, err := parseFeed(data, c.origin())
	if err != nil {
		return Feed{}, err
	}
	feed.Source = SourceSiteFallback
	return feed, nil
}

// Detail reads one announcement page and returns its readable text. The URL
// must already be an official one: nothing else is fetched.
func (c *Client) Detail(ctx context.Context, url string) (Detail, error) {
	page, err := c.get(ctx, url, maxArticle)
	if err != nil {
		return Detail{}, err
	}
	return parseArticle(page, url)
}

func (c *Client) origin() string {
	base := strings.TrimRight(strings.TrimSpace(c.Base), "/")
	if base == "" {
		return OfficialOrigin
	}
	return base
}

func allowedBy(raw string, hosts []string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("news: %q is not a URL: %w", raw, err)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("news: %q: only public HTTPS sources are read", raw)
	}
	if parsed.User != nil {
		return fmt.Errorf("news: %q: URLs may not carry credentials", raw)
	}
	if hostAllowed(parsed.Host, hosts) {
		return nil
	}
	return fmt.Errorf("news: %q: %s is not an official source", raw, parsed.Host)
}

// get performs one bounded, allowlisted GET and follows redirects itself so
// that every hop stays on an official host.
func (c *Client) get(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	return c.getWith(ctx, rawURL, limit, c.Hosts)
}

// getWith is get against a specific host allowlist: the launcher's data files
// live on a different official host than the pages do.
func (c *Client) getWith(ctx context.Context, rawURL string, limit int64, hosts []string) ([]byte, error) {
	current := rawURL
	for hop := 0; hop <= maxRedirects; hop++ {
		if err := allowedBy(current, hosts); err != nil {
			return nil, err
		}
		if err := c.pause(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			return nil, fmt.Errorf("news: %w", err)
		}
		req.Header.Set("User-Agent", "oddward-launcher (read-only announcement reader)")
		req.Header.Set("Accept", "text/html,application/javascript;q=0.9,*/*;q=0.1")
		resp, err := c.httpClient().Do(req)
		if err != nil {
			return nil, fmt.Errorf("news: %w", err)
		}
		body, readErr := readBounded(resp.Body, limit)
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			return body, nil
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			location := resp.Header.Get("Location")
			if location == "" {
				return nil, fmt.Errorf("news: %s: status %d without a location", current, resp.StatusCode)
			}
			next, err := resolveURL(current, location)
			if err != nil {
				return nil, err
			}
			current = next
		default:
			return nil, fmt.Errorf("news: %s: status %d", current, resp.StatusCode)
		}
	}
	return nil, fmt.Errorf("news: %s: too many redirects", rawURL)
}

func (c *Client) httpClient() *http.Client {
	client := http.Client{Timeout: httpTimeout}
	if c.HTTP != nil {
		client = *c.HTTP
	}
	// Redirect safety belongs to this reader, not the injected client's policy.
	// Copy its configuration so every hop returns to our allowlist checks without
	// mutating a client that may also be used by the caller elsewhere.
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &client
}

// pause keeps the client polite: at most one request per MinGap.
func (c *Client) pause(ctx context.Context) error {
	gap := c.MinGap
	if gap <= 0 {
		return nil
	}
	c.mu.Lock()
	wait := time.Until(c.last.Add(gap))
	c.mu.Unlock()
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	c.mu.Lock()
	c.last = time.Now()
	c.mu.Unlock()
	return nil
}

func resolveURL(base, location string) (string, error) {
	from, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("news: %w", err)
	}
	to, err := url.Parse(strings.TrimSpace(location))
	if err != nil {
		return "", fmt.Errorf("news: %w", err)
	}
	return from.ResolveReference(to).String(), nil
}

func readBounded(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("news: read: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("news: response exceeds %d bytes", limit)
	}
	return data, nil
}
