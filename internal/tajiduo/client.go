// SPDX-License-Identifier: GPL-3.0-or-later
// Package tajiduo implements the app protocol without persisting credentials.
// Only launcher/system may orchestrate it. Callers supply a netcfg HTTP client
// and obtain user consent before any authentication or role-binding operation.
package tajiduo

import (
	"context"
	"crypto/md5" // Protocol digest, not a password hash.
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nerv-base/Oddward/internal/cnlogin/laohu"
	"github.com/nerv-base/Oddward/internal/gacha"
)

var (
	ErrAuthExpired       = errors.New("tajiduo: authentication expired")
	ErrProtocolChanged   = errors.New("tajiduo: protocol changed")
	ErrNoBoundRole       = errors.New("tajiduo: no bound role")
	ErrChallengeRequired = errors.New("tajiduo: challenge required")
	ErrNetwork           = errors.New("tajiduo: network failure")
	ErrNeedsRelink       = errors.New("tajiduo: relink required")
	ErrIdentityMismatch  = errors.New("tajiduo: identity mismatch")
	ErrInvalidInput      = errors.New("tajiduo: invalid input")
	// ErrCredentialRejected means the user center refused the saved game
	// credential. It is safe to fall back to the official SMS login.
	ErrCredentialRejected = errors.New("tajiduo: saved game credential rejected")
	// ErrAlreadySigned is a business answer, not a transport failure: the
	// server says this account or role already signed today. Callers must not
	// treat it as a fresh reward and must not retry the write.
	ErrAlreadySigned = errors.New("tajiduo: already signed today")
)

// Grant contains only the fields suitable for an encrypted account attachment.
type Grant struct{ DeviceID, CenterUID, RefreshToken, LaohuUserID, RoleID string }

// Access is process-local. Its identity is copied from the validated grant.
type Access struct {
	Token, DeviceID, CenterUID, LaohuUserID, RoleID string
	ExpiresAt                                       time.Time
}

// Config accepts an HTTP client constructed by netcfg. No default transport is
// created here. Base URLs may use plain HTTP only on literal loopback addresses.
// Now and Rand support deterministic tests; nil selects time.Now and crypto/rand.
type Config struct {
	HTTPClient            *http.Client
	BaseURL, LaohuBaseURL string
	Now                   func() time.Time
	Rand                  io.Reader
}

// Client owns no persistent token state and never retries a request.
type Client struct {
	http            *http.Client
	base, laohuBase string
	now             func() time.Time
	random          io.Reader
	randomMu        sync.Mutex
}

// Role identifies a game role returned by the official user center.
type Role struct{ ID, Name string }
type RoleList struct {
	BoundRoleID string
	Roles       []Role
}

// Snapshot is a validated, deduplicated S-only snapshot ready for gacha.Merge.
type Snapshot = gacha.Snapshot

// Catalog contains only names returned by the official characters endpoint.
type Catalog struct {
	FetchedAt time.Time
	Items     map[string]gacha.Item
}

func NewClient(cfg Config) (*Client, error) {
	if cfg.HTTPClient == nil {
		return nil, ErrInvalidInput
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.LaohuBaseURL == "" {
		cfg.LaohuBaseURL = defaultLaohuBaseURL
	}
	if !validBase(cfg.BaseURL) || !validBase(cfg.LaohuBaseURL) {
		return nil, ErrInvalidInput
	}
	h := *cfg.HTTPClient
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	h.Jar = nil // Never let ambient cookies participate in app authentication.
	if h.Timeout <= 0 || h.Timeout > 20*time.Second {
		h.Timeout = 20 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.Reader
	}
	return &Client{http: &h, base: strings.TrimRight(cfg.BaseURL, "/"), laohuBase: strings.TrimRight(cfg.LaohuBaseURL, "/"), now: cfg.Now, random: cfg.Rand}, nil
}
func validBase(raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Host == "" || u.Opaque != "" || u.Path != "" && u.Path != "/" {
		return false
	}
	return u.Scheme == "https" || u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")
}
func ds(ts time.Time, nonce [8]byte, version string) string {
	return dsWithSalt(ts, nonce, version, dsSalt)
}
func dsWithSalt(ts time.Time, nonce [8]byte, version, salt string) string {
	stamp := fmt.Sprint(ts.Unix())
	n := string(nonce[:])
	sum := md5.Sum([]byte(stamp + n + version + salt))
	return stamp + "," + n + "," + hex.EncodeToString(sum[:])
}
func (c *Client) nonce() ([8]byte, error) {
	c.randomMu.Lock()
	defer c.randomMu.Unlock()
	var n [8]byte
	for i := range n {
		for {
			var b [1]byte
			if _, e := io.ReadFull(c.random, b[:]); e != nil {
				return n, ErrNetwork
			}
			if int(b[0]) < 256-256%len(nonceAlphabet) {
				n[i] = nonceAlphabet[int(b[0])%len(nonceAlphabet)]
				break
			}
		}
	}
	return n, nil
}

// NewDeviceID generates the persistent per-account uppercase device identifier.
func NewDeviceID() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", ErrNetwork
	}
	return strings.ToUpper(hex.EncodeToString(b[:])), nil
}
func validDevice(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, b := range s {
		if !(b >= '0' && b <= '9' || b >= 'A' && b <= 'F') {
			return false
		}
	}
	return true
}
func digits(s string) bool {
	if len(s) < 1 || len(s) > 20 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
func validGrant(g Grant) bool {
	return validDevice(g.DeviceID) && digits(g.CenterUID) && digits(g.LaohuUserID) && g.RefreshToken != "" && (g.RoleID == "" || digits(g.RoleID))
}
func validAccess(a Access) bool {
	return a.Token != "" && validDevice(a.DeviceID) && digits(a.CenterUID) && digits(a.LaohuUserID)
}
func (c *Client) common(device string, millis bool) map[string]string {
	p := map[string]string{"appId": laohuAppID, "channelId": laohuChannelID, "deviceId": device, "deviceType": deviceModel, "deviceModel": deviceModel, "deviceName": deviceModel, "deviceSys": deviceSystem, "adm": device, "idfa": "", "sdkVersion": sdkVersion, "bid": packageName}
	if millis {
		p["t"] = fmt.Sprint(c.now().UnixMilli())
		p["version"] = versionCode
		p["mac"] = ""
	} else {
		p["t"] = fmt.Sprint(c.now().Unix())
		p["versionCode"] = versionCode
		p["imei"] = ""
	}
	return p
}
func (c *Client) laohu(ctx context.Context, path string, p map[string]string, keepEmpty bool) (any, error) {
	p["sign"] = laohu.SignParameters(p, laohuAppKey)
	if !keepEmpty {
		for k, v := range p {
			if v == "" {
				delete(p, k)
			}
		}
	}
	return c.request(ctx, true, http.MethodPost, path, p, Access{})
}
func (c *Client) SendSMS(ctx context.Context, phone string) error {
	if _, e := laohu.MaskPhone(phone); e != nil {
		return ErrInvalidInput
	}
	// SMS sending has no grant yet. Use a random neutral device, never host data.
	device, e := NewDeviceID()
	if e != nil {
		return e
	}
	p := c.common(device, false)
	p["cellphone"] = phone
	p["areaCodeId"] = "1"
	p["type"] = "16"
	_, e = c.laohu(ctx, "/m/newApi/sendPhoneCaptchaWithOutLogin", p, false)
	return e
}
func (c *Client) LoginSMS(ctx context.Context, phone, code, deviceID string) (Grant, error) {
	if _, e := laohu.MaskPhone(phone); e != nil {
		return Grant{}, ErrInvalidInput
	}
	if !validDevice(deviceID) || len(code) < 4 || len(code) > 8 || !digits(code) {
		return Grant{}, ErrInvalidInput
	}
	p := c.common(deviceID, true)
	var e error
	p["cellphone"], e = laohu.EncryptField(phone, laohuAppKey)
	if e != nil {
		return Grant{}, ErrProtocolChanged
	}
	p["captcha"], e = laohu.EncryptField(code, laohuAppKey)
	if e != nil {
		return Grant{}, ErrProtocolChanged
	}
	p["areaCodeId"] = "1"
	p["type"] = "16"
	// Login validates the SMS itself; no extra check request or automatic retry.
	v, e := c.laohu(ctx, "/openApi/sms/new/login", p, true)
	if e != nil {
		return Grant{}, e
	}
	m, ok := object(v)
	if !ok {
		return Grant{}, ErrProtocolChanged
	}
	uid, ok := identifier(m["userId"])
	token, okToken := text(m["token"])
	if !ok || !okToken || token == "" {
		return Grant{}, ErrProtocolChanged
	}
	v, e = c.request(ctx, false, http.MethodPost, "/usercenter/api/login", map[string]string{"token": token, "userIdentity": uid, "appId": centerAppID}, Access{DeviceID: deviceID})
	if e != nil {
		return Grant{}, e
	}
	m, ok = object(v)
	if !ok {
		return Grant{}, ErrProtocolChanged
	}
	center, ok := identifier(m["uid"])
	refresh, okRefresh := text(m["refreshToken"])
	access, okAccess := text(m["accessToken"])
	if !ok || !okRefresh || !okAccess || refresh == "" || access == "" {
		return Grant{}, ErrProtocolChanged
	}
	return Grant{DeviceID: deviceID, CenterUID: center, RefreshToken: refresh, LaohuUserID: uid}, nil
}

// ExchangeGameCredential mints a user-center session from the long-lived
// credential already saved for the game login. It never sends SMS and never
// persists the grant; the caller stages and commits it.
func (c *Client) ExchangeGameCredential(ctx context.Context, channelToken, channelUID, deviceID string) (Grant, error) {
	if channelToken == "" || !digits(channelUID) || !validDevice(deviceID) {
		return Grant{}, ErrInvalidInput
	}
	root, status, e := c.exchange(ctx, false, http.MethodPost, "/usercenter/api/login", map[string]string{
		"token":        channelToken,
		"userIdentity": channelUID,
		"appId":        centerAppID,
	}, Access{DeviceID: deviceID})
	if e != nil {
		return Grant{}, e
	}
	code, ok := codeValue(root["code"])
	if !ok || status < 200 || status >= 300 {
		return Grant{}, ErrProtocolChanged
	}
	if expired(code) {
		return Grant{}, ErrAuthExpired
	}
	if code != 0 {
		return Grant{}, ErrCredentialRejected
	}
	data, ok := object(root["data"])
	if !ok {
		return Grant{}, ErrProtocolChanged
	}
	center, okCenter := identifier(data["uid"])
	refresh, okRefresh := text(data["refreshToken"])
	access, okAccess := text(data["accessToken"])
	if !okCenter || !okRefresh || !okAccess || refresh == "" || access == "" {
		return Grant{}, ErrProtocolChanged
	}
	g := Grant{DeviceID: deviceID, CenterUID: center, RefreshToken: refresh, LaohuUserID: channelUID}
	if !validGrant(g) {
		return Grant{}, ErrProtocolChanged
	}
	return g, nil
}

func (c *Client) Refresh(ctx context.Context, g Grant) (Grant, Access, error) {
	if !validGrant(g) {
		return Grant{}, Access{}, ErrInvalidInput
	}
	v, e := c.request(ctx, false, http.MethodPost, "/usercenter/api/refreshToken", nil, Access{Token: g.RefreshToken, DeviceID: g.DeviceID})
	if e != nil {
		return Grant{}, Access{}, e
	}
	m, ok := object(v)
	if !ok {
		return Grant{}, Access{}, ErrProtocolChanged
	}
	token, okToken := text(m["accessToken"])
	refresh, okRefresh := text(m["refreshToken"])
	if !okToken || !okRefresh || token == "" || refresh == "" || refresh == g.RefreshToken {
		return Grant{}, Access{}, ErrProtocolChanged
	}
	if uid, exists := m["uid"]; exists {
		center, valid := identifier(uid)
		if !valid || center != g.CenterUID {
			return Grant{}, Access{}, ErrIdentityMismatch
		}
	}
	g.RefreshToken = refresh
	return g, Access{Token: token, DeviceID: g.DeviceID, CenterUID: g.CenterUID, LaohuUserID: g.LaohuUserID, RoleID: g.RoleID, ExpiresAt: c.now().Add(time.Hour)}, nil
}

func (g Grant) String() string               { return "tajiduo.Grant{redacted}" }
func (g Grant) GoString() string             { return g.String() }
func (g Grant) Format(f fmt.State, v rune)   { io.WriteString(f, g.String()) }
func (g Grant) LogValue() slog.Value         { return slog.StringValue(g.String()) }
func (a Access) String() string              { return "tajiduo.Access{redacted}" }
func (a Access) GoString() string            { return a.String() }
func (a Access) Format(f fmt.State, v rune)  { io.WriteString(f, a.String()) }
func (a Access) LogValue() slog.Value        { return slog.StringValue(a.String()) }
func (c Config) String() string              { return "tajiduo.Config{redacted}" }
func (c Config) GoString() string            { return c.String() }
func (c Config) Format(f fmt.State, v rune)  { io.WriteString(f, c.String()) }
func (c Config) LogValue() slog.Value        { return slog.StringValue(c.String()) }
func (c *Client) String() string             { return "tajiduo.Client{redacted}" }
func (c *Client) GoString() string           { return c.String() }
func (c *Client) Format(f fmt.State, v rune) { io.WriteString(f, c.String()) }
func (c *Client) LogValue() slog.Value       { return slog.StringValue(c.String()) }
