// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package broker

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/nerv-base/Oddward/internal/cnlogin/cnproto"
	"github.com/nerv-base/Oddward/internal/process"
)

// JSON base64 expands the SDK's maximum 64 KiB packets to almost 88 KiB.
const maxFrameSize = 96 << 10
const protocolVersion = 1

var errProtocol = errors.New("cnlogin/broker: invalid protocol")
var errRemote = errors.New("cnlogin/broker: host operation failed")

type wireFrame struct {
	Version  int             `json:"version"`
	Kind     string          `json:"kind"`
	ID       uint64          `json:"id,omitempty"`
	Deadline int64           `json:"deadline,omitempty"`
	Payload  json.RawMessage `json:"payload"`
}

func (f *wireFrame) zero()                     { clear(f.Payload); f.Payload = nil }
func (f wireFrame) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "broker frame{redacted}") }

func decode(data []byte, out any) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errProtocol
	}
	if !objectShape(trimmed, reflect.TypeOf(out).Elem()) {
		return errProtocol
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return errProtocol
	}
	if d.Decode(new(any)) != io.EOF {
		return errProtocol
	}
	return nil
}

// encoding/json accepts duplicate keys, case aliases and missing scalar fields.
// Require the exact DTO shape so an absent/null exit code cannot become exit 0.
func objectShape(data []byte, t reflect.Type) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	fields := make(map[string]reflect.StructField, t.NumField())
	required := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		fields[name] = f
		if opts != "omitempty" {
			required[name] = true
		}
	}
	seen := make(map[string]bool, len(fields))
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return false
		}
		name, ok := key.(string)
		field, known := fields[name]
		if !ok || !known || seen[name] {
			return false
		}
		seen[name] = true
		delete(required, name)
		var raw json.RawMessage
		err = d.Decode(&raw)
		if err != nil {
			clear(raw)
			return false
		}
		valid := true
		if field.Type.Kind() != reflect.Slice && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			valid = false
		}
		if field.Type.Kind() == reflect.Struct {
			valid = objectShape(raw, field.Type)
		}
		clear(raw)
		if !valid {
			return false
		}
	}
	token, err = d.Token()
	return err == nil && token == json.Delim('}') && len(required) == 0
}
func readFrame(r io.Reader) (wireFrame, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return wireFrame{}, err
	}
	n := binary.LittleEndian.Uint32(header[:])
	if n == 0 || n > maxFrameSize {
		return wireFrame{}, errProtocol
	}
	b := make([]byte, int(n))
	defer clear(b)
	if _, err := io.ReadFull(r, b); err != nil {
		return wireFrame{}, err
	}
	var f wireFrame
	if decode(b, &f) != nil || f.Version != protocolVersion || len(f.Payload) == 0 {
		f.zero()
		return wireFrame{}, errProtocol
	}
	return f, nil
}
func makeFrame(kind string, id uint64, payload any) (wireFrame, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		clear(b)
		return wireFrame{}, errProtocol
	}
	return wireFrame{Version: protocolVersion, Kind: kind, ID: id, Payload: b}, nil
}
func writeFrame(w io.Writer, f wireFrame) error {
	b, err := json.Marshal(f)
	if err != nil {
		clear(b)
		return errProtocol
	}
	defer clear(b)
	if len(b) == 0 || len(b) > maxFrameSize {
		return errProtocol
	}
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(b)))
	for _, part := range [][]byte{header[:], b} {
		for len(part) > 0 {
			n, err := w.Write(part)
			if err != nil {
				return err
			}
			if n <= 0 || n > len(part) {
				return io.ErrShortWrite
			}
			part = part[n:]
		}
	}
	return nil
}

type startPayload struct {
	Root string `json:"root"`
}
type startedPayload struct {
	Identity         process.Identity `json:"identity"`
	InstalledVersion string           `json:"installed_version"`
}
type exitPayload struct {
	Code uint32 `json:"code"`
}
type replyPayload struct {
	ErrorCode string `json:"error_code"`
}
type registrationPayload struct {
	ConfigKey   []byte `json:"config_key"`
	PreLoginKey []byte `json:"pre_login_key"`
}
type queryPayload struct {
	Type      int32 `json:"type"`
	ContextID int32 `json:"context_id"`
}
type loginPayload struct {
	GameUID      []byte `json:"game_uid"`
	GameToken    []byte `json:"game_token"`
	ChannelUID   []byte `json:"channel_uid"`
	ChannelToken []byte `json:"channel_token"`
	Info         []byte `json:"info"`
}

func loginFrom(id cnproto.LoginIdentity) loginPayload {
	return loginPayload{id.GameUID, id.GameToken, id.ChannelUID, id.ChannelToken, id.Info}
}
func (p loginPayload) identity() cnproto.LoginIdentity {
	return cnproto.LoginIdentity{GameUID: p.GameUID, GameToken: p.GameToken, ChannelUID: p.ChannelUID, ChannelToken: p.ChannelToken, Info: p.Info}
}
func (p loginPayload) zero() { id := p.identity(); id.Zero() }
func (p loginPayload) valid() bool {
	for _, f := range []struct {
		b   []byte
		max int
	}{{p.GameUID, 63}, {p.ChannelUID, 63}, {p.GameToken, 127}, {p.ChannelToken, 127}} {
		if len(f.b) == 0 || len(f.b) > f.max || bytes.IndexByte(f.b, 0) >= 0 {
			return false
		}
	}
	return len(p.Info) <= cnproto.MaxExtraInfo && bytes.IndexByte(p.Info, 0) < 0
}

type preLoginPayload struct {
	Code      int32  `json:"code"`
	ContextID int32  `json:"context_id"`
	Message   []byte `json:"message"`
	Body      []byte `json:"body"`
}

func (p preLoginPayload) zero() { clear(p.Message); clear(p.Body) }
func (p preLoginPayload) valid() bool {
	return len(p.Body) > 0 && len(p.Body) <= cnproto.MaxPreLoginBody && bytes.IndexByte(p.Body, 0) < 0 && utf8.Valid(p.Message) && bytes.IndexByte(p.Message, 0) < 0 && len(utf16.Encode([]rune(string(p.Message)))) <= 511
}
func (p loginPayload) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "broker login{redacted}") }
func (p preLoginPayload) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "broker reply{redacted}")
}
func (p registrationPayload) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "broker registration{redacted}")
}
func (p queryPayload) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "broker query{redacted}") }
