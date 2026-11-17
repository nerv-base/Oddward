// SPDX-License-Identifier: GPL-3.0-or-later

package laohu

import (
	"bytes"
	"crypto/md5" // Required by the channel protocol, not a password hash.
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/nerv-base/Oddward/internal/cnlogin/ecb"
)

type parameters map[string]string

func (p parameters) keys() []string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
func (p parameters) signInput(key string) string {
	var b strings.Builder
	for _, k := range p.keys() {
		if k != "sign" {
			b.WriteString(p[k])
		}
	}
	b.WriteString(key)
	return b.String()
}
func (p parameters) sign(key string) {
	digest := md5.Sum([]byte(p.signInput(key)))
	p["sign"] = hex.EncodeToString(digest[:])
}
func (p parameters) canonical() string {
	parts := make([]string, 0, len(p))
	for _, k := range p.keys() {
		parts = append(parts, k+"="+p[k])
	}
	return strings.Join(parts, "&")
}
func (p parameters) form() string {
	v := make(url.Values, len(p))
	for k, s := range p {
		v.Set(k, s)
	}
	return v.Encode()
}
func anonymousParams(c Config, now time.Time, device, osVersion string) parameters {
	p := parameters{"appId": c.ChannelAppID, "appVersion": c.AppVersion, "channelId": "9", "deviceId": device, "deviceModel": deviceLiteral, "deviceSys": deviceLiteral, "deviceType": osVersion + " 64bit", "packageName": c.PackageName, "sdkVersion": sdkVersion, "t": strconv.FormatInt(now.Unix(), 10)}
	p.sign(c.ChannelAppKey)
	return p
}
func deviceJSON(c Config, now time.Time, device, osVersion string) string {
	p := anonymousParams(c, now, device, osVersion)
	for _, k := range []string{"appId", "channelId", "t", "sign"} {
		delete(p, k)
	}
	// System.Text.Json's default encoder escapes non-ASCII and HTML-sensitive
	// characters (including apostrophe and plus) using uppercase UTF-16 units.
	raw := marshalRelaxed(p)
	var b strings.Builder
	for _, r := range string(raw) {
		if r > 0x7e || strings.ContainsRune("<>&'+", r) {
			for _, u := range utf16.Encode([]rune{r}) {
				fmt.Fprintf(&b, "\\u%04X", u)
			}
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func channelParams(c Config, version, device string, now time.Time) parameters {
	p := parameters{"appId": c.ChannelAppID, "bid": c.PackageName, "channelId": "9", "deviceId": device, "deviceModel": deviceLiteral, "deviceName": deviceLiteral, "deviceSys": deviceLiteral, "deviceType": deviceLiteral, "mac": "", "sdkVersion": sdkVersion, "t": strconv.FormatInt(now.UnixMilli(), 10), "version": version}
	p.sign(c.ChannelAppKey)
	return p
}
func encryptField(text, key string) (string, error) {
	if !channelAppKeyPattern.MatchString(key) {
		return "", errInvalidConfig
	}
	plain := []byte(text)
	defer clear(plain)
	cipher, err := ecb.Encrypt([]byte(key[16:]), plain)
	if err != nil {
		return "", err
	}
	defer clear(cipher)
	return base64.StdEncoding.EncodeToString(cipher), nil
}
func preLoginParams(version, device, uid string, now time.Time, key [16]byte) parameters {
	p := parameters{"appId": cnAppID, "appVersion": version, "channelId": "9", "deviceName": deviceLiteral, "ndid": device, "platformId": "4", "sdkVersion": sdkVersion, "timestamp": strconv.FormatInt(now.UnixMilli(), 10), "udid": device, "userId": uid, "version": "2"}
	h := sha256.New()
	h.Write(key[:])
	h.Write([]byte(p.canonical()))
	p["sign"] = base64.StdEncoding.EncodeToString(h.Sum(nil))
	return p
}
func oneLoginFields(uid, token, device string) map[string]string {
	return map[string]string{"appId": cnAppID, "channelId": "9", "channelToken": token, "channelUid": uid, "device": device, "extraJson": `{"channelId":"9","subchannelId":"9"}`, "platformId": "4", "version": "2"}
}
func marshalRelaxed(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if enc.Encode(v) != nil {
		return nil
	}
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'})
}

var errEnvelope = errors.New("laohu: malformed game envelope")

func encodeEnvelope(plain []byte, key [16]byte) ([]byte, error) {
	if len(plain) == 0 || len(plain) > 512<<10 {
		return nil, errEnvelope
	}
	cipher, err := ecb.Encrypt(key[:], plain)
	if err != nil {
		return nil, errEnvelope
	}
	defer clear(cipher)
	out := make([]byte, base64.StdEncoding.EncodedLen(len(cipher)))
	base64.StdEncoding.Encode(out, cipher)
	return out, nil
}
func decodeEnvelope(wire []byte, key [16]byte) ([]byte, error) {
	if len(wire) == 0 || len(wire) > 1<<20 {
		return nil, errEnvelope
	}
	for _, b := range wire {
		if b < 0x09 || b > 0x7e {
			return nil, errEnvelope
		}
	}
	// Convert.FromBase64String permits whitespace inside the envelope.
	compact := bytes.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, wire)
	defer clear(compact)
	cipher, err := base64.StdEncoding.Strict().DecodeString(string(compact))
	if err != nil {
		return nil, errEnvelope
	}
	defer clear(cipher)
	plain, err := ecb.Decrypt(key[:], cipher)
	if err != nil {
		return nil, errEnvelope
	}
	return plain, nil
}
