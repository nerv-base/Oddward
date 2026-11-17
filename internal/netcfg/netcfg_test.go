// SPDX-License-Identifier: GPL-3.0-or-later

package netcfg

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestDefaultFollowsSystemProxyWithMirrorOff(t *testing.T) {
	d := Default()
	if d.Proxy != System || d.Mirror || d.MirrorPrefix != DefaultMirrorPrefix || d.ProxyURL != "" {
		t.Fatalf("Default() = %+v", d)
	}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRefusesUnsafeOrAmbiguousAddresses(t *testing.T) {
	good := []Settings{
		{Proxy: Direct, MirrorPrefix: DefaultMirrorPrefix},
		{Proxy: Custom, ProxyURL: "http://127.0.0.1:7890", MirrorPrefix: DefaultMirrorPrefix},
		{Proxy: Custom, ProxyURL: "socks5://127.0.0.1:7891", MirrorPrefix: DefaultMirrorPrefix},
		{Proxy: System, Mirror: true, MirrorPrefix: "https://mirror.example/gh/"},
	}
	for _, s := range good {
		if err := s.Validate(); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
	bad := []Settings{
		{Proxy: "auto", MirrorPrefix: DefaultMirrorPrefix},
		{Proxy: Custom, MirrorPrefix: DefaultMirrorPrefix},
		{Proxy: Custom, ProxyURL: "127.0.0.1:7890", MirrorPrefix: DefaultMirrorPrefix},
		{Proxy: Custom, ProxyURL: "ftp://127.0.0.1:21", MirrorPrefix: DefaultMirrorPrefix},
		{Proxy: Custom, ProxyURL: "http://user:secret@127.0.0.1:7890", MirrorPrefix: DefaultMirrorPrefix},
		{Proxy: Custom, ProxyURL: "http://127.0.0.1", MirrorPrefix: DefaultMirrorPrefix},
		{Proxy: Custom, ProxyURL: "http://127.0.0.1:7890/path", MirrorPrefix: DefaultMirrorPrefix},
		{Proxy: System, Mirror: true, MirrorPrefix: "http://mirror.example/"},
		{Proxy: System, Mirror: true, MirrorPrefix: "https://mirror.example/gh"},
		{Proxy: System, Mirror: true, MirrorPrefix: "https://u:p@mirror.example/"},
		{Proxy: System, Mirror: true, MirrorPrefix: "https://mirror.example/?x=1"},
		{Proxy: System, Mirror: true, MirrorPrefix: ""},
		{Proxy: Direct, ProxyURL: strings.Repeat("a", 600), MirrorPrefix: DefaultMirrorPrefix},
	}
	for _, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("accepted %+v", s)
		}
	}
}

func TestSystemProxyFollowsWindowsInternetSettings(t *testing.T) {
	cases := []struct {
		name   string
		sys    SystemProxy
		target string
		want   string
	}{
		{"disabled", SystemProxy{Enabled: false, Server: "127.0.0.1:7890"}, "https://github.com/", ""},
		{"single server", SystemProxy{Enabled: true, Server: "127.0.0.1:7890"}, "https://github.com/", "http://127.0.0.1:7890"},
		{"explicit scheme", SystemProxy{Enabled: true, Server: "http://127.0.0.1:7890"}, "https://github.com/", "http://127.0.0.1:7890"},
		{"per protocol https", SystemProxy{Enabled: true, Server: "http=a.local:1;https=b.local:2"}, "https://github.com/", "http://b.local:2"},
		{"per protocol http", SystemProxy{Enabled: true, Server: "http=a.local:1;https=b.local:2"}, "http://example.com/", "http://a.local:1"},
		{"socks only", SystemProxy{Enabled: true, Server: "socks=c.local:3"}, "https://github.com/", "socks5://c.local:3"},
		{"bypass local", SystemProxy{Enabled: true, Server: "127.0.0.1:7890", Override: "<local>"}, "http://intranet/", ""},
		{"bypass wildcard", SystemProxy{Enabled: true, Server: "127.0.0.1:7890", Override: "*.example.com;10.*"}, "https://cdn.example.com/", ""},
		{"bypass ip wildcard", SystemProxy{Enabled: true, Server: "127.0.0.1:7890", Override: "*.example.com;10.*"}, "http://10.1.2.3/", ""},
		{"not bypassed", SystemProxy{Enabled: true, Server: "127.0.0.1:7890", Override: "*.example.com"}, "https://github.com/", "http://127.0.0.1:7890"},
		{"loopback never proxied", SystemProxy{Enabled: true, Server: "127.0.0.1:7890"}, "http://127.0.0.1:8080/", ""},
		{"garbage", SystemProxy{Enabled: true, Server: ";;=;"}, "https://github.com/", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := New(Settings{Proxy: System, MirrorPrefix: DefaultMirrorPrefix}, func() SystemProxy { return c.sys })
			got, err := n.proxy(mustRequest(t, c.target))
			if err != nil {
				t.Fatal(err)
			}
			if (got == nil) != (c.want == "") || (got != nil && got.String() != c.want) {
				t.Fatalf("proxy = %v, want %q", got, c.want)
			}
		})
	}
}

func TestProxyModesSelectTheConfiguredRoute(t *testing.T) {
	system := func() SystemProxy { return SystemProxy{Enabled: true, Server: "127.0.0.1:1"} }
	n := New(Settings{Proxy: Direct, MirrorPrefix: DefaultMirrorPrefix}, system)
	if p, _ := n.proxy(mustRequest(t, "https://github.com/")); p != nil {
		t.Fatalf("direct used %v", p)
	}
	if err := n.Set(Settings{Proxy: Custom, ProxyURL: "socks5://127.0.0.1:7891", MirrorPrefix: DefaultMirrorPrefix}); err != nil {
		t.Fatal(err)
	}
	if p, _ := n.proxy(mustRequest(t, "https://github.com/")); p == nil || p.String() != "socks5://127.0.0.1:7891" {
		t.Fatalf("custom = %v", p)
	}
	if err := n.Set(Settings{Proxy: Custom, MirrorPrefix: DefaultMirrorPrefix}); err == nil {
		t.Fatal("invalid settings applied")
	}
	if n.Settings().ProxyURL != "socks5://127.0.0.1:7891" {
		t.Fatal("rejected Set replaced the previous settings")
	}
}

func TestMirrorRewritesOnlyGitHubDownloads(t *testing.T) {
	var seen []string
	n := New(Settings{Proxy: Direct, Mirror: true, MirrorPrefix: "https://mirror.example/gh/"}, nil)
	n.base = roundTrip(func(r *http.Request) (*http.Response, error) {
		seen = append(seen, r.URL.String()+" host="+r.Host)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	for _, target := range []string{
		"https://github.com/nerv-base/oddward/releases/latest/download/update.json",
		"https://release-assets.githubusercontent.com/github-production-release-asset/1/x?sig=1",
		"https://yh.wanmei.com/news/",
	} {
		req := mustRequest(t, target)
		res, err := n.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if req.URL.String() != target {
			t.Fatal("RoundTrip mutated the caller's request")
		}
	}
	want := []string{
		"https://mirror.example/gh/https://github.com/nerv-base/oddward/releases/latest/download/update.json host=mirror.example",
		"https://mirror.example/gh/https://release-assets.githubusercontent.com/github-production-release-asset/1/x?sig=1 host=mirror.example",
		"https://yh.wanmei.com/news/ host=yh.wanmei.com",
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("seen:\n%s", strings.Join(seen, "\n"))
	}
	seen = nil
	_ = n.Set(Settings{Proxy: Direct, MirrorPrefix: "https://mirror.example/gh/"})
	res, _ := n.RoundTrip(mustRequest(t, "https://github.com/a"))
	res.Body.Close()
	if seen[0] != "https://github.com/a host=github.com" {
		t.Fatalf("mirror off still rewrote: %s", seen[0])
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func mustRequest(t *testing.T, raw string) *http.Request {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Request{Method: http.MethodGet, URL: u, Host: u.Host, Header: http.Header{}}
}
