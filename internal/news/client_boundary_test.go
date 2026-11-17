// SPDX-License-Identifier: GPL-3.0-or-later

package news

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// boundaryTransport exercises the real http.Client redirect machinery without
// opening a socket, resolving a hostname, or contacting a public service.
type boundaryTransport func(*http.Request) (*http.Response, error)

func (f boundaryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func boundaryResponse(r *http.Request, status int, body, location string) *http.Response {
	h := make(http.Header)
	if location != "" {
		h.Set("Location", location)
	}
	return &http.Response{
		StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)), Request: r,
	}
}

func TestClientRedirectPolicyCannotBeBypassedByHTTPInjection(t *testing.T) {
	const host = "allowed.news.test"
	const base = "https://" + host
	imageBody := string(syntheticPNG(t, 2, 2))
	for _, mode := range []string{"constructor", "injected", "nil", "permissive"} {
		for _, resource := range []string{"page", "data", "image"} {
			for _, location := range []string{
				"https://off.news.test/final",
				"http://" + host + "/final",
				"https://synthetic-user:synthetic-pass@" + host + "/final",
			} {
				t.Run(mode+"/"+resource+"/"+location, func(t *testing.T) {
					requests, forbidden, originalRedirects := 0, 0, 0
					transport := boundaryTransport(func(r *http.Request) (*http.Response, error) {
						requests++
						if r.URL.Host != host || r.URL.Scheme != "https" || r.URL.User != nil {
							forbidden++
							return boundaryResponse(r, http.StatusOK, imageBody, ""), nil
						}
						return boundaryResponse(r, http.StatusFound, "", location), nil
					})
					c := NewClient()
					c.Hosts, c.DataHosts, c.ImageHosts = []string{host}, []string{host}, []string{host}
					c.MinGap = 0
					switch mode {
					case "constructor":
						c.HTTP.Transport = transport
					case "injected":
						c.HTTP = &http.Client{Transport: transport, Timeout: 7 * time.Second}
					case "nil":
						c.HTTP = nil
						previous := http.DefaultTransport
						http.DefaultTransport = transport
						t.Cleanup(func() { http.DefaultTransport = previous })
					case "permissive":
						c.HTTP = &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
							originalRedirects++
							return nil
						}}
					}
					original := c.HTTP
					var err error
					switch resource {
					case "page":
						_, err = c.get(context.Background(), base+"/start", maxMainHTML)
					case "data":
						_, err = c.getWith(context.Background(), base+"/start", maxBannerScript, c.DataHosts)
					case "image":
						_, err = c.Image(context.Background(), base+"/start")
					}
					if forbidden != 0 {
						t.Errorf("redirect reached a forbidden destination before validation: requests=%d forbidden=%d", requests, forbidden)
					}
					if err == nil {
						t.Error("the forbidden redirect must be refused")
					}
					if requests != 1 {
						t.Errorf("only the initial allowlisted request may be sent, got %d", requests)
					}
					if c.HTTP != original {
						t.Error("the caller's HTTP client must not be replaced")
					}
					if mode == "injected" && (original.Timeout != 7*time.Second || original.CheckRedirect != nil) {
						t.Error("the caller's HTTP client configuration was mutated")
					}
					if mode == "permissive" {
						if originalRedirects != 0 {
							t.Errorf("the injected redirect callback must not control safety, called %d times", originalRedirects)
						}
						if original.CheckRedirect == nil {
							t.Fatal("the caller's callback was removed")
						}
						before := originalRedirects
						_ = original.CheckRedirect(&http.Request{}, nil)
						if originalRedirects != before+1 {
							t.Error("the caller's original callback was overwritten")
						}
					}
				})
			}
		}
	}
}

func TestClientRevalidatesAfterAnAllowedIntermediateRedirect(t *testing.T) {
	const host = "allowed.news.test"
	const base = "https://" + host
	for _, image := range []bool{false, true} {
		t.Run(fmt.Sprint("image=", image), func(t *testing.T) {
			var paths []string
			forbidden := 0
			c := NewClient()
			c.Hosts, c.ImageHosts, c.MinGap = []string{host}, []string{host}, 0
			c.HTTP = &http.Client{Transport: boundaryTransport(func(r *http.Request) (*http.Response, error) {
				paths = append(paths, r.URL.Path)
				if r.URL.Host != host {
					forbidden++
					return boundaryResponse(r, http.StatusOK, "payload", ""), nil
				}
				location := "/intermediate"
				if r.URL.Path == "/intermediate" {
					location = "https://off.news.test/final"
				}
				return boundaryResponse(r, http.StatusFound, "", location), nil
			})}
			var err error
			if image {
				_, err = c.Image(context.Background(), base+"/start")
			} else {
				_, err = c.get(context.Background(), base+"/start", 128)
			}
			if forbidden != 0 || strings.Join(paths, ",") != "/start,/intermediate" {
				t.Errorf("every redirect must be revalidated before sending: paths=%v forbidden=%d", paths, forbidden)
			}
			if err == nil {
				t.Error("the off-host second redirect must fail")
			}
		})
	}
}

func TestClientEnforcesThreeRedirectLimitWithInjectedHTTP(t *testing.T) {
	const host = "allowed.news.test"
	for _, image := range []bool{false, true} {
		t.Run(fmt.Sprint("image=", image), func(t *testing.T) {
			requests := 0
			c := NewClient()
			c.Hosts, c.ImageHosts, c.MinGap = []string{host}, []string{host}, 0
			c.HTTP = &http.Client{Transport: boundaryTransport(func(r *http.Request) (*http.Response, error) {
				requests++
				return boundaryResponse(r, http.StatusFound, "", "/again"), nil
			})}
			var err error
			if image {
				_, err = c.Image(context.Background(), "https://"+host+"/start")
			} else {
				_, err = c.get(context.Background(), "https://"+host+"/start", 128)
			}
			if err == nil || requests != maxRedirects+1 {
				t.Fatalf("three redirect limit must apply before another request: requests=%d err=%v", requests, err)
			}
		})
	}
}

func TestClientPropagatesCancellationDuringBannersWithoutFallback(t *testing.T) {
	const host = "allowed.news.test"
	const base = "https://" + host
	for _, empty := range []bool{false, true} {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("empty=%t/deadline=%t", empty, deadline), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				want := context.Canceled
				if deadline {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
					want = context.DeadlineExceeded
				}
				defer cancel()
				banners, fallback := 0, 0
				page := launcherPage
				if empty {
					page = `<html><body><div class="news-cont"><ul></ul></div><div class="news-cont"><ul></ul></div><div class="news-cont"><ul></ul></div></body></html>`
				}
				c := NewClient()
				c.Base, c.BannerScript, c.MinGap = base, base+"/banners.js", 0
				c.Hosts, c.DataHosts = []string{host}, []string{host}
				c.HTTP = &http.Client{Transport: boundaryTransport(func(r *http.Request) (*http.Response, error) {
					switch r.URL.Path {
					case "/launcher/launcher_ob2.html":
						return boundaryResponse(r, http.StatusOK, page, ""), nil
					case "/banners.js":
						banners++
						if !deadline {
							cancel()
						}
						<-r.Context().Done()
						return nil, r.Context().Err()
					default:
						fallback++
						return boundaryResponse(r, http.StatusInternalServerError, "unexpected fallback", ""), nil
					}
				})}
				feed, err := c.Feed(ctx)
				if !errors.Is(err, want) {
					t.Errorf("cancellation during carousel must remain an error, got source=%q rows=%d err=%v, want %v", feed.Source, len(feed.Items), err, want)
				}
				if banners != 1 {
					t.Errorf("the cancellation must happen in Banners, got %d banner requests", banners)
				}
				if fallback != 0 {
					t.Errorf("a cancelled carousel must not start website fallback, got %d requests", fallback)
				}
			})
		}
	}
}
