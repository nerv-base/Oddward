// SPDX-License-Identifier: GPL-3.0-or-later
package laohu

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ChannelIdentity is a laohu identity suitable for encrypted account storage.
// Callers own its slices and must Zero them when finished.
type ChannelIdentity struct{ UID, Token []byte }

// GameIdentity is a short-lived identity; never persist it.
type GameIdentity struct{ UID, Token, ExtraOne []byte }

// PreLoginResult retains the exact HTTP bytes for the game's op 49 reply.
// Message and Body contain server data, never log or persist them.
type PreLoginResult struct {
	Code                         int
	Message                      string
	Status, RealUser, BannedType int
	Body                         []byte
}
type ValidationStatus string

const (
	Validated                   ValidationStatus = "validated"
	ServiceRejected             ValidationStatus = "service-rejected"
	RequiresOfficialInteraction ValidationStatus = "official-interaction"
	RequestRejected             ValidationStatus = "request-rejected"
	NetworkFailure              ValidationStatus = "network-failure"
	Unknown                     ValidationStatus = "unknown"
)

type ValidationResult struct {
	Status     ValidationStatus
	APICode    int
	HasAPICode bool
	HTTPStatus int
}

var errJSON = errors.New("laohu: invalid response shape")

// strictObject rejects duplicates at every nesting level, invalid UTF-8,
// trailing values and excessive depth before assigning any security fields.
func strictObject(raw []byte) (map[string]any, error) {
	if len(raw) == 0 || len(raw) > 1<<20 || !utf8.Valid(raw) {
		return nil, errJSON
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, err := strictValue(d, 0)
	if err != nil {
		return nil, errJSON
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errJSON
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errJSON
	}
	return obj, nil
}
func strictValue(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, errJSON
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		obj := map[string]any{}
		for d.More() {
			t, err := d.Token()
			if err != nil {
				return nil, err
			}
			key, ok := t.(string)
			if !ok {
				return nil, errJSON
			}
			if _, exists := obj[key]; exists {
				return nil, errJSON
			}
			v, err := strictValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			obj[key] = v
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, errJSON
		}
		return obj, nil
	case '[':
		values := []any{}
		for d.More() {
			v, err := strictValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			values = append(values, v)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, errJSON
		}
		return values, nil
	}
	return nil, errJSON
}
func object(v any) map[string]any { o, _ := v.(map[string]any); return o }
func scalarText(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return string(x), true
	}
	return "", false
}
func intValue(v any, allowString bool) (int, bool) {
	n, ok := v.(json.Number)
	if !ok && allowString {
		if s, yes := v.(string); yes {
			n = json.Number(s)
			ok = true
		}
	}
	if !ok {
		return 0, false
	}
	i, err := strconv.ParseInt(string(n), 10, 32)
	return int(i), err == nil
}

// ParseConfig consumes the decrypted boot config. No raw JSON is retained.
func ParseConfig(raw []byte) (Config, error) {
	root, err := strictObject(raw)
	if err != nil {
		return Config{}, errInvalidConfig
	}
	meta := object(root["meta"])
	channel := object(object(root["init"])["channel"])
	var c Config
	c.AppVersion, _ = scalarText(meta["version"])
	c.PackageName, _ = scalarText(meta["packageName"])
	c.ChannelID, _ = intValue(meta["channelId"], true)
	c.Platform, _ = intValue(meta["platform"], true)
	c.ChannelAppID, _ = scalarText(channel["channelAppId"])
	c.ChannelAppKey, _ = scalarText(channel["channelAppKey"])
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}
func validIdentityField(b []byte, max int) bool {
	if len(b) == 0 || len(b) > max || !utf8.Valid(b) || strings.TrimSpace(string(b)) == "" {
		return false
	}
	for _, r := range string(b) {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func identityUID(v any) ([]byte, bool) {
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case json.Number:
		n, err := strconv.ParseUint(string(x), 10, 64)
		if err != nil || n == 0 {
			return nil, false
		}
		s = strconv.FormatUint(n, 10)
	default:
		return nil, false
	}
	b := []byte(s)
	return b, validIdentityField(b, 63)
}
func identityToken(v any) ([]byte, bool) {
	s, ok := v.(string)
	if !ok {
		return nil, false
	}
	b := []byte(s)
	return b, validIdentityField(b, 127)
}
func parseIdentityRoot(raw []byte, phase Phase) (map[string]any, *Error) {
	root, err := strictObject(raw)
	if err != nil {
		return nil, &Error{Phase: phase}
	}
	code, ok := intValue(root["code"], false)
	if !ok || code != 0 {
		return nil, &Error{Phase: phase, APICode: code, HasAPICode: ok}
	}
	return root, nil
}
func ParseChannelIdentity(raw []byte) (ChannelIdentity, error) {
	root, e := parseIdentityRoot(raw, PhaseChannelLoginRejected)
	if e != nil {
		return ChannelIdentity{}, e
	}
	r := object(root["result"])
	uid, uok := identityUID(r["userId"])
	token, tok := identityToken(r["token"])
	if !uok || !tok {
		clear(uid)
		clear(token)
		return ChannelIdentity{}, &Error{Phase: PhaseChannelLoginRejected, HasAPICode: true}
	}
	return ChannelIdentity{UID: uid, Token: token}, nil
}
func ParseGameIdentity(raw []byte) (GameIdentity, error) {
	root, e := parseIdentityRoot(raw, PhaseGameLoginRejected)
	if e != nil {
		return GameIdentity{}, e
	}
	uid, uok := identityUID(root["uid"])
	token, tok := identityToken(root["token"])
	fail := func() (GameIdentity, error) {
		clear(uid)
		clear(token)
		return GameIdentity{}, &Error{Phase: PhaseGameLoginRejected, HasAPICode: true}
	}
	if !uok || !tok {
		return fail()
	}
	var info []byte
	if extra, exists := root["extraOne"]; exists {
		if object(extra) == nil {
			return fail()
		}
		info = marshalRelaxed(extra)
		if len(info) > 65519 {
			clear(info)
			return fail()
		}
	}
	return GameIdentity{UID: uid, Token: token, ExtraOne: info}, nil
}
func ParseValidation(raw []byte) ValidationResult {
	r := ValidationResult{Status: Unknown}
	root, err := strictObject(raw)
	if err != nil {
		return r
	}
	code, ok := intValue(root["code"], false)
	if !ok {
		return r
	}
	r.APICode = code
	r.HasAPICode = true
	if code != 0 {
		r.Status = ServiceRejected
		return r
	}
	result := root["result"]
	if result != nil {
		o := object(result)
		if o == nil {
			return r
		}
		if o["privacyAgreement"] != nil {
			r.Status = RequiresOfficialInteraction
			return r
		}
	}
	r.Status = Validated
	return r
}
func ParsePreLogin(raw, uid []byte) (PreLoginResult, error) {
	fail := func() (PreLoginResult, error) { return PreLoginResult{}, &Error{Phase: PhaseInvalidResponse} }
	if len(raw) > 64478 || bytes.IndexByte(raw, 0) >= 0 {
		return fail()
	}
	root, err := strictObject(raw)
	if err != nil {
		return fail()
	}
	code, ok := intValue(root["code"], false)
	if !ok {
		return fail()
	}
	r := PreLoginResult{Code: code}
	if value, exists := root["message"]; exists {
		s, ok := value.(string)
		if !ok || utf16Len(s) > 511 || strings.ContainsRune(s, 0) {
			return fail()
		}
		r.Message = s
	}
	if code == 0 {
		result := object(root["result"])
		if result == nil {
			return fail()
		}
		r.Status, ok = intValue(result["status"], true)
		if !ok {
			return fail()
		}
		r.RealUser, ok = intValue(result["realUser"], true)
		if !ok {
			return fail()
		}
		banned, present := result["bannedType"]
		if !present {
			return fail()
		}
		if banned != nil {
			r.BannedType, ok = intValue(banned, false)
			if !ok {
				return fail()
			}
		}
		appID, ok := intValue(result["appId"], true)
		if !ok || appID != 1289 {
			return fail()
		}
		user, ok := result["userId"].(string)
		if !ok || !validIdentityField(uid, 63) || subtle.ConstantTimeCompare([]byte(user), uid) != 1 {
			return fail()
		}
	}
	r.Body = bytes.Clone(raw)
	return r, nil
}
func (id *ChannelIdentity) Zero() { clear(id.UID); clear(id.Token); id.UID = nil; id.Token = nil }
func (id *GameIdentity) Zero() {
	clear(id.UID)
	clear(id.Token)
	clear(id.ExtraOne)
	id.UID = nil
	id.Token = nil
	id.ExtraOne = nil
}
func (r *PreLoginResult) Zero()                          { clear(r.Body); *r = PreLoginResult{} }
func (id ChannelIdentity) String() string                { return "laohu.ChannelIdentity{redacted}" }
func (id ChannelIdentity) GoString() string              { return id.String() }
func (id ChannelIdentity) Format(f fmt.State, verb rune) { io.WriteString(f, id.String()) }
func (id GameIdentity) String() string                   { return "laohu.GameIdentity{redacted}" }
func (id GameIdentity) GoString() string                 { return id.String() }
func (id GameIdentity) Format(f fmt.State, verb rune)    { io.WriteString(f, id.String()) }
func (r PreLoginResult) String() string                  { return "laohu.PreLoginResult{redacted}" }
func (r PreLoginResult) GoString() string                { return r.String() }
func (r PreLoginResult) Format(f fmt.State, verb rune)   { io.WriteString(f, r.String()) }
