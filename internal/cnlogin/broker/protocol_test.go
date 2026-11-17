// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package broker

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nerv-base/Oddward/internal/cnlogin/cnproto"
)

func framedJSON(s string) []byte {
	b := make([]byte, 4+len(s))
	binary.LittleEndian.PutUint32(b, uint32(len(s)))
	copy(b[4:], s)
	return b
}

func TestProtocolCarriesMaximumSDKPayloads(t *testing.T) {
	login := loginFrom(syntheticLogin())
	login.Info = bytes.Repeat([]byte{'a'}, cnproto.MaxExtraInfo)
	defer login.zero()
	reply := preLoginPayload{Message: []byte(strings.Repeat("界", 511)), Body: bytes.Repeat([]byte{'b'}, cnproto.MaxPreLoginBody)}
	defer reply.zero()
	if !login.valid() || !reply.valid() {
		t.Fatal("legal SDK boundary rejected")
	}
	for _, payload := range []any{login, reply} {
		f, err := makeFrame("sendlogin", 1, payload)
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		err = writeFrame(&b, f)
		f.zero()
		if err != nil {
			t.Fatalf("maximum SDK payload does not fit wire: %v", err)
		}
		if b.Len() <= 64<<10 || b.Len() > maxFrameSize+4 {
			t.Fatalf("unexpected bounded JSON size: %d", b.Len())
		}
		got, err := readFrame(&b)
		if err != nil {
			t.Fatal(err)
		}
		got.zero()
		clear(b.Bytes())
	}
	login.Info = append(login.Info, 'a')
	if login.valid() {
		t.Fatal("SDK login overflow accepted")
	}
	reply.Body = append(reply.Body, 'b')
	if reply.valid() {
		t.Fatal("SDK reply overflow accepted")
	}
}

func TestProtocolSensitiveDTOsAlwaysFormatRedacted(t *testing.T) {
	secret := []byte("synthetic-secret-canary")
	f, _ := makeFrame("query", 0, queryPayload{Type: 123, ContextID: 456})
	defer f.zero()
	for _, value := range []any{loginPayload{GameToken: secret}, preLoginPayload{Body: secret}, registrationPayload{ConfigKey: secret}, queryPayload{Type: 123, ContextID: 456}, f} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%x", "%q"} {
			text := fmt.Sprintf(verb, value)
			if !strings.Contains(text, "redacted") || strings.Contains(text, "synthetic-secret") || strings.Contains(text, "115 121") {
				t.Fatal("sensitive DTO leaked in formatting")
			}
		}
	}
	var bad loginPayload
	if err := decode([]byte(`{"synthetic-secret-canary":true}`), &bad); err == nil || strings.Contains(err.Error(), "canary") {
		t.Fatal("decode error echoed attacker content")
	}
}

func TestProtocolAcceptsVersionedStart(t *testing.T) {
	f, err := readFrame(bytes.NewReader(framedJSON(`{"version":1,"kind":"start","id":1,"payload":{"root":"C:\\synthetic"}}`)))
	if err != nil {
		t.Fatalf("valid start rejected: %v", err)
	}
	defer f.zero()
	if f.Kind != "start" || f.ID != 1 {
		t.Fatal("start metadata was lost")
	}
}

func TestProtocolRejectsAmbiguousObjectShapes(t *testing.T) {
	for _, s := range []string{
		`{"version":1,"version":1,"kind":"start","id":1,"payload":{}}`,
		`{"Version":1,"kind":"start","id":1,"payload":{}}`,
	} {
		if _, err := readFrame(bytes.NewReader(framedJSON(s))); !errors.Is(err, errProtocol) {
			t.Fatalf("ambiguous envelope accepted: %v", err)
		}
	}
	for _, s := range []string{`{}`, `{"code":null}`, `{"code":0,"code":9}`} {
		var p exitPayload
		if err := decode([]byte(s), &p); !errors.Is(err, errProtocol) {
			t.Fatalf("missing or ambiguous exit status accepted: %v", err)
		}
	}
}

func TestProtocolRejectsMalformedWithoutEcho(t *testing.T) {
	for _, s := range []string{
		`{"version":2,"kind":"start","id":1,"payload":{}}`,
		`{"version":1,"kind":"start","id":1,"secret-value":true,"payload":{}}`,
		`{"version":1,"kind":"start","id":1,"payload":{}} {}`,
		`{"version":1,"kind":"start","id":1,"payload":`,
	} {
		_, err := readFrame(bytes.NewReader(framedJSON(s)))
		if !errors.Is(err, errProtocol) {
			t.Fatalf("malformed frame accepted or error not sanitized: %v", err)
		}
	}
	oversized := make([]byte, 4)
	binary.LittleEndian.PutUint32(oversized, maxFrameSize+1)
	if _, err := readFrame(bytes.NewReader(oversized)); !errors.Is(err, errProtocol) {
		t.Fatalf("oversize: %v", err)
	}
}
