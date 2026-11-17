// SPDX-License-Identifier: GPL-3.0-or-later

// Package netcfg routes every launcher HTTP request through the user's network
// choice: the Windows system proxy, a direct connection or a custom proxy, and
// optionally a GitHub download mirror. Settings can change while requests are
// in flight; each new request reads the current choice.
package netcfg

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// Mode selects how requests reach the internet.
type Mode string

const (
	System Mode = "system" // follow Windows Internet Settings (no PAC scripts)
	Direct Mode = "direct"
	Custom Mode = "custom" // ProxyURL, http:// or socks5://
)

// DefaultMirrorPrefix is prefilled for the GitHub mirror; it is off by
// default. Public mirrors come and go, so users may replace it.
const DefaultMirrorPrefix = "https://gh-proxy.com/"

const maxAddress = 512

// Settings is the user's persisted network choice. It never holds
// credentials: proxy URLs with user info are refused.
type Settings struct {
	Proxy        Mode
	ProxyURL     string
	Mirror       bool
	MirrorPrefix string
}

// Default follows the system proxy with the mirror off.
func Default() Settings { return Settings{Proxy: System, MirrorPrefix: DefaultMirrorPrefix} }

// Validate rejects unknown modes, credentials and addresses that could be
// read more than one way.
func (s Settings) Validate() error {
	if len(s.ProxyURL) > maxAddress || len(s.MirrorPrefix) > maxAddress {
		return errors.New("网络地址过长")
	}
	switch s.Proxy {
	case System, Direct:
	case Custom:
		if _, err := ParseProxyURL(s.ProxyURL); err != nil {
			return err
		}
	default:
		return errors.New("未知的代理模式")
	}
	if s.Mirror || s.MirrorPrefix != "" {
		if err := validMirror(s.MirrorPrefix); err != nil {
			return err
		}
	}
	return nil
}

// ParseProxyURL accepts http://, https://, socks5:// or socks5h:// with an
// explicit host and port and nothing else.
func ParseProxyURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Host == "" {
		return nil, errors.New("代理地址需形如 http://127.0.0.1:7890 或 socks5://127.0.0.1:7891")
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, errors.New("代理只支持 http、https 或 socks5")
	}
	if u.User != nil {
		return nil, errors.New("代理地址不能包含账号密码")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Port() == "" || u.Hostname() == "" {
		return nil, errors.New("代理地址只能包含协议、主机和端口")
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}, nil
}

func validMirror(prefix string) error {
	u, err := url.Parse(prefix)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || !strings.HasSuffix(prefix, "/") {
		return errors.New("镜像前缀需为以 / 结尾的 https 地址，例如 https://gh-proxy.com/")
	}
	return nil
}

// SystemProxy is a snapshot of HKCU Internet Settings.
type SystemProxy struct {
	Enabled  bool
	Server   string // ProxyServer
	Override string // ProxyOverride
}

// Network is an http.RoundTripper applying the current Settings.
type Network struct {
	current atomic.Pointer[Settings]
	system  func() SystemProxy
	base    http.RoundTripper
}

// New starts with s (Default() when invalid). system reads the Windows proxy;
// nil means ReadSystemProxy.
func New(s Settings, system func() SystemProxy) *Network {
	if s.Validate() != nil {
		s = Default()
	}
	if system == nil {
		system = ReadSystemProxy
	}
	n := &Network{system: system}
	n.current.Store(&s)
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = n.proxy
	n.base = t
	return n
}

// Set validates and applies s to every later request; on error the previous
// settings stay in force.
func (n *Network) Set(s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	n.current.Store(&s)
	return nil
}

// Settings returns the settings in force.
func (n *Network) Settings() Settings { return *n.current.Load() }

// Client returns a client using this network. Callers keep their own
// redirect and host policies on top.
func (n *Network) Client(timeout time.Duration) *http.Client {
	return &http.Client{Transport: n, Timeout: timeout}
}

// RoundTrip sends r, through the mirror when it is on and r targets a GitHub
// download host. The caller's request is never mutated.
func (n *Network) RoundTrip(r *http.Request) (*http.Response, error) {
	s := n.current.Load()
	if s.Mirror && mirrored(r.URL) {
		target, err := url.Parse(s.MirrorPrefix + r.URL.String())
		if err != nil {
			return nil, err
		}
		clone := r.Clone(r.Context())
		clone.URL, clone.Host = target, target.Host
		return n.base.RoundTrip(clone)
	}
	return n.base.RoundTrip(r)
}

func mirrored(u *url.URL) bool {
	if u == nil || u.Scheme != "https" {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "github.com", "codeload.github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com", "raw.githubusercontent.com":
		return true
	}
	return false
}

func (n *Network) proxy(r *http.Request) (*url.URL, error) {
	s := n.current.Load()
	switch s.Proxy {
	case Direct:
		return nil, nil
	case Custom:
		return ParseProxyURL(s.ProxyURL)
	}
	return systemRoute(n.system(), r.URL), nil
}

// systemRoute mirrors WinINet's static proxy rules closely enough for a
// launcher: per-scheme servers, a socks fallback and ProxyOverride bypasses.
func systemRoute(sys SystemProxy, target *url.URL) *url.URL {
	if !sys.Enabled || target == nil {
		return nil
	}
	host := strings.ToLower(target.Hostname())
	if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) || bypassed(sys.Override, host) {
		return nil
	}
	servers := map[string]string{}
	for _, part := range strings.Split(sys.Server, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if k, v, ok := strings.Cut(part, "="); ok && !strings.Contains(k, "/") {
			servers[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		} else {
			servers[""] = part
		}
	}
	pick := func(addr, scheme string) *url.URL {
		if addr == "" {
			return nil
		}
		if !strings.Contains(addr, "://") {
			addr = scheme + "://" + addr
		}
		u, err := ParseProxyURL(addr)
		if err != nil {
			return nil
		}
		return u
	}
	if u := pick(servers[target.Scheme], "http"); u != nil {
		return u
	}
	if u := pick(servers[""], "http"); u != nil {
		return u
	}
	return pick(servers["socks"], "socks5")
}

func bypassed(override, host string) bool {
	for _, rule := range strings.Split(override, ";") {
		rule = strings.ToLower(strings.TrimSpace(rule))
		switch {
		case rule == "":
		case rule == "<local>":
			if !strings.Contains(host, ".") {
				return true
			}
		case strings.Contains(rule, "*"):
			if wildcard(rule, host) {
				return true
			}
		case rule == host:
			return true
		}
	}
	return false
}

func wildcard(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for i, p := range parts[1:] {
		if i == len(parts)-2 {
			return strings.HasSuffix(s, p)
		}
		j := strings.Index(s, p)
		if j < 0 {
			return false
		}
		s = s[j+len(p):]
	}
	return true
}
