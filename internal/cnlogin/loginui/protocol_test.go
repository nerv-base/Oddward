// SPDX-License-Identifier: GPL-3.0-or-later
package loginui

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestFrameRoundTripAndLengthBound(t *testing.T) {
	var b bytes.Buffer
	want := Event{Kind: "login_phone", Phone: "13800000000", Captcha: "123456", Remember: true}
	if err := writeFrame(&b, envelope{Version: 1, Type: "event", Event: &want}); err != nil {
		t.Fatal(err)
	}
	got, err := readFrame(&b)
	if err != nil || got.Event == nil || *got.Event != want {
		t.Fatalf("synthetic event round trip failed: %v", err)
	}
	for _, n := range []uint32{0, MaxFrame + 1, 0xffffffff} {
		var header [4]byte
		binary.LittleEndian.PutUint32(header[:], n)
		if _, err := readFrame(bytes.NewReader(header[:])); !errors.Is(err, ErrProtocol) {
			t.Fatalf("frame size %d accepted", n)
		}
	}
}

func TestRejectMalformedFramesWithoutEchoingContent(t *testing.T) {
	for _, s := range []string{`{"version":2,"type":"event","event":{"kind":"cancel"}}`, `{"version":1,"type":"event","event":{"kind":"login_phone","token":"synthetic-secret"}}`, `{"version":1,"type":"event","event":{"kind":"unknown"}}`, `{"version":1,"type":"event","event":{"kind":"cancel","phone":"synthetic-secret"}}`, `{"version":1,"type":"event","event":{"kind":"cancel"}} trailing`, `null`} {
		var b bytes.Buffer
		_ = binary.Write(&b, binary.LittleEndian, uint32(len(s)))
		b.WriteString(s)
		_, err := readFrame(&b)
		if !errors.Is(err, ErrProtocol) || strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatal("malformed frame must produce generic protocol error")
		}
	}
	if _, err := readFrame(bytes.NewReader(nil)); !errors.Is(err, io.EOF) {
		t.Fatal("clean EOF must remain EOF")
	}
	var b bytes.Buffer
	if err := writeFrame(&b, envelope{Version: 1, Type: "model", Model: &Model{Screen: "phone", Status: strings.Repeat("x", MaxFrame)}}); !errors.Is(err, ErrProtocol) || b.Len() != 0 {
		t.Fatal("oversized output was not rejected before write")
	}
}

func TestModelValidation(t *testing.T) {
	valid := Model{Screen: "notice", Notice: []Section{{Title: "Purpose", Body: "Synthetic notice"}}}
	if err := validateModel(valid); err != nil {
		t.Fatal(err)
	}
	for _, m := range []Model{{Screen: "unknown"}, {Screen: "accounts", Accounts: make([]Account, 17)}, {Screen: "phone", Status: strings.Repeat("s", 4097)}} {
		if !errors.Is(validateModel(m), ErrProtocol) {
			t.Fatal("unbounded/unknown model accepted")
		}
	}
}

func TestWelcomeBannerRoundTripAndValidation(t *testing.T) {
	welcome := Model{Screen: "welcome", Banner: "夜航星，欢迎登录", GameProcessID: 42, GameCreationTime: "133000000000000000"}
	if err := validateModel(welcome); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := writeFrame(&b, envelope{Version: 1, Type: "model", Model: &welcome}); err != nil {
		t.Fatal(err)
	}
	got, err := readFrame(&b)
	if err != nil || got.Model == nil || got.Model.Screen != welcome.Screen || got.Model.Banner != welcome.Banner || got.Model.GameProcessID != welcome.GameProcessID || got.Model.GameCreationTime != welcome.GameCreationTime {
		t.Fatalf("welcome model round trip failed: %v", err)
	}
	for _, m := range []Model{
		{Screen: "welcome"},
		{Screen: "welcome", Banner: strings.Repeat("b", 257)},
		{Screen: "phone", Banner: "夜航星，欢迎登录"},
		{Screen: "accounts", Banner: "夜航星，欢迎登录"},
		{Screen: "notice", Banner: "夜航星，欢迎登录"},
	} {
		if !errors.Is(validateModel(m), ErrProtocol) {
			t.Fatalf("banner model accepted: screen=%q banner length=%d", m.Screen, len(m.Banner))
		}
	}
}
