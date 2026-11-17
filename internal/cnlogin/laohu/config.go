// SPDX-License-Identifier: GPL-3.0-or-later

package laohu

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"unicode/utf16"
)

// Fixed China-server protocol constants (spec §3, §5.3).
const (
	cnAppID       = "1289"
	cnChannelID   = 9
	cnPlatform    = 4
	sdkVersion    = "5.159.0"
	deviceLiteral = "WindowsPc"
)

var (
	channelAppIDPattern  = regexp.MustCompile(`\A[0-9]{1,10}\z`)
	channelAppKeyPattern = regexp.MustCompile(`\A[A-Za-z0-9]{32}\z`)
	versionPattern       = regexp.MustCompile(`\A[0-9]+(?:\.[0-9]+){1,3}\z`)
	deviceIDPattern      = regexp.MustCompile(`\A[0-9a-f]{32}\z`)
	phonePattern         = regexp.MustCompile(`\A1[0-9]{10}\z`)
	captchaPattern       = regexp.MustCompile(`\A[0-9]{4,8}\z`)
)

// Config holds the boot-config values the protocol needs. The bootcfg
// package fills it from the decrypted oneSdkConfig.config JSON; the JSON path
// of each field is given in its comment.
type Config struct {
	AppVersion    string // meta.version (raw text, 1..128 chars)
	PackageName   string // meta.packageName (raw text, 1..128 chars)
	ChannelID     int    // meta.channelId, must be 9
	Platform      int    // meta.platform, must be 4
	ChannelAppID  string // init.channel.channelAppId, 1..10 digits
	ChannelAppKey string // init.channel.channelAppKey, 32 alphanumerics (secret)
}

var errInvalidConfig = errors.New("laohu: unsupported boot configuration")

// Validate reports whether c has the verified PC channel shape.
func (c Config) Validate() error {
	if c.ChannelID != cnChannelID || c.Platform != cnPlatform ||
		!channelAppIDPattern.MatchString(c.ChannelAppID) || !channelAppKeyPattern.MatchString(c.ChannelAppKey) ||
		!metadataText(c.AppVersion) || !metadataText(c.PackageName) {
		return errInvalidConfig
	}
	return nil
}

// String omits every value; the channel app key is a secret.
func (c Config) String() string { return "laohu.Config{redacted}" }

// GoString omits every value; the channel app key is a secret.
func (c Config) GoString() string { return c.String() }

func metadataText(s string) bool {
	n := utf16Len(s)
	return n >= 1 && n <= 128
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
		if r == '\uFFFD' {
			n += 0 // invalid UTF-8 counts as one unit, like a replacement char
		}
	}
	return n
}

// AppKey holds the keys derived from the bridge's 32-character application
// key (op 0 registration): the OneLogin envelope key (its last 16 bytes) and
// the antifatigue pre-login signing key.
type AppKey struct {
	tail, preLogin [16]byte
	valid          bool
}

var errInvalidAppKey = errors.New("laohu: invalid application key")

// NewAppKey derives the protocol keys from the 32-byte printable-ASCII
// application key. raw is not retained.
func NewAppKey(raw []byte) (AppKey, error) {
	if len(raw) != 32 {
		return AppKey{}, errInvalidAppKey
	}
	for _, b := range raw {
		if b < 0x20 || b > 0x7e {
			return AppKey{}, errInvalidAppKey
		}
	}
	var k AppKey
	copy(k.tail[:], raw[16:32])
	n := copy(k.preLogin[:], raw[1:3])
	n += copy(k.preLogin[n:], raw[7:12])
	n += copy(k.preLogin[n:], raw[15:18])
	copy(k.preLogin[n:], raw[25:31])
	k.valid = true
	return k, nil
}

// NewDerivedAppKey accepts independently verified registration-derived keys.
// Each key is copied; callers must clear their own copies when finished.
func NewDerivedAppKey(tail, preLogin [16]byte) AppKey {
	return AppKey{tail: tail, preLogin: preLogin, valid: true}
}

// Zero clears this instance. Copies owned by a Client are cleared by Close.
func (k *AppKey) Zero()                        { clear(k.tail[:]); clear(k.preLogin[:]); k.valid = false }
func (k AppKey) Format(f fmt.State, verb rune) { io.WriteString(f, k.String()) }
func (c Config) Format(f fmt.State, verb rune) { io.WriteString(f, c.String()) }

// String omits the key material.
func (k AppKey) String() string { return "laohu.AppKey{redacted}" }

// GoString omits the key material.
func (k AppKey) GoString() string { return k.String() }

// NewDeviceID returns a fresh random device identifier (32 lowercase hex
// digits). Every new account gets its own; the official launcher's device id
// is never reused.
func NewDeviceID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// MaskPhone returns a display label such as 138****0000 for a mainland
// mobile number, or an error for anything else.
func MaskPhone(phone string) (string, error) {
	if !phonePattern.MatchString(phone) {
		return "", &Error{Phase: PhaseInvalidMainlandPhone}
	}
	return phone[:3] + "****" + phone[7:], nil
}
