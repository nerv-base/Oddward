// SPDX-License-Identifier: GPL-3.0-or-later
package tajiduo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nerv-base/Oddward/internal/cnlogin/laohu"
)

const maxResponseBytes = 8 << 20

func (c *Client) request(ctx context.Context, isLaohu bool, method, path string, fields map[string]string, a Access) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	base := c.base
	if isLaohu {
		base = c.laohuBase
	}
	body := ""
	if fields != nil {
		body = form(fields)
	}
	target := base + path
	if method == http.MethodGet && body != "" {
		target += "?" + body
		body = ""
	}
	req, e := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
	if e != nil {
		return nil, ErrInvalidInput
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	if fields != nil && method != http.MethodGet {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if isLaohu {
		req.Header.Set("User-Agent", laohuUserAgent)
	} else {
		n, e := c.nonce()
		if e != nil {
			return nil, e
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("platform", platform)
		req.Header.Set("deviceid", a.DeviceID)
		req.Header.Set("appversion", appVersion)
		req.Header.Set("uid", clientUID)
		req.Header.Set("authorization", a.Token)
		req.Header.Set("ds", ds(c.now(), n, appVersion))
	}
	resp, e := c.http.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrNetwork
	}
	defer resp.Body.Close()
	if expired(resp.StatusCode) {
		return nil, ErrAuthExpired
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrChallengeRequired
	}
	if resp.ContentLength > maxResponseBytes {
		return nil, ErrProtocolChanged
	}
	if enc := resp.Header.Get("Content-Encoding"); enc != "" && enc != "identity" {
		return nil, ErrProtocolChanged
	}
	raw, e := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	defer clear(raw)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrNetwork
	}
	if len(raw) > maxResponseBytes {
		return nil, ErrProtocolChanged
	}
	root, e := decodeObject(raw)
	if e != nil {
		return nil, ErrProtocolChanged
	}
	code, ok := codeValue(root["code"])
	if ok && expired(code) {
		return nil, ErrAuthExpired
	}
	if challenge(root) {
		return nil, ErrChallengeRequired
	}
	if !ok {
		return nil, ErrProtocolChanged
	}
	if code != 0 || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, ErrProtocolChanged
	}
	key := "data"
	if isLaohu {
		key = "result"
	}
	v, exists := root[key]
	if !exists || v == nil {
		// SMS and write APIs can succeed without a data object.
		if path == "/m/newApi/sendPhoneCaptchaWithOutLogin" {
			return nil, nil
		}
		return nil, ErrProtocolChanged
	}
	return v, nil
}
func form(p map[string]string) string     { return laohu.FormParameters(p) }
func expired(code int) bool               { return code == 401 || code == 402 || code == 403 }
func object(v any) (map[string]any, bool) { m, ok := v.(map[string]any); return m, ok && m != nil }
func text(v any) (string, bool)           { s, ok := v.(string); return s, ok }
func identifier(v any) (string, bool) {
	s, ok := v.(string)
	if !ok {
		n, number := v.(json.Number)
		if !number {
			return "", false
		}
		s = string(n)
	}
	return s, digits(s) && s != "0"
}
func codeValue(v any) (int, bool) {
	switch x := v.(type) {
	case json.Number:
		n, e := x.Int64()
		return int(n), e == nil && int64(int(n)) == n
	case string:
		n, e := json.Number(x).Int64()
		return int(n), e == nil && int64(int(n)) == n
	}
	return 0, false
}
func integer(v any) (int, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, e := n.Int64()
	return int(i), e == nil && int64(int(i)) == i
}
func challenge(m map[string]any) bool {
	for _, key := range []string{"captchaUrl", "captchaId", "challenge", "challengeRequired", "needCaptcha", "needVerify", "verifyUrl", "riskControl"} {
		if v, exists := m[key]; exists && v != nil && v != false && v != "" {
			return true
		}
	}
	for _, key := range []string{"message", "msg"} {
		if s, ok := text(m[key]); ok {
			s = strings.ToLower(s)
			for _, hint := range []string{"滑块", "人机", "图形验证", "风控", "captcha", "challenge", "risk control"} {
				if strings.Contains(s, hint) {
					return true
				}
			}
		}
	}
	for _, key := range []string{"data", "result"} {
		if child, ok := object(m[key]); ok && challenge(child) {
			return true
		}
	}
	return false
}

// Decode known fields strictly while tolerating additive fields. Duplicate keys,
// null roots, trailing values, and unreasonable nesting are protocol changes.
func decodeObject(raw []byte) (map[string]any, error) {
	if !utf8.Valid(raw) {
		return nil, ErrProtocolChanged
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	v, e := decodeValue(d, 0)
	if e != nil {
		return nil, e
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, ErrProtocolChanged
	}
	m, ok := object(v)
	if !ok {
		return nil, ErrProtocolChanged
	}
	return m, nil
}
func decodeValue(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, ErrProtocolChanged
	}
	t, e := d.Token()
	if e != nil {
		return nil, e
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return t, nil
	}
	switch delim {
	case '{':
		m := map[string]any{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return nil, e
			}
			k, ok := key.(string)
			if !ok {
				return nil, ErrProtocolChanged
			}
			if _, exists := m[k]; exists {
				return nil, ErrProtocolChanged
			}
			v, e := decodeValue(d, depth+1)
			if e != nil {
				return nil, e
			}
			m[k] = v
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return nil, ErrProtocolChanged
		}
		return m, nil
	case '[':
		a := []any{}
		for d.More() {
			v, e := decodeValue(d, depth+1)
			if e != nil {
				return nil, e
			}
			a = append(a, v)
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return nil, ErrProtocolChanged
		}
		return a, nil
	}
	return nil, errors.New("invalid JSON delimiter")
}
