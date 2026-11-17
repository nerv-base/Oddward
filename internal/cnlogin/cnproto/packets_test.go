// SPDX-License-Identifier: GPL-3.0-or-later

package cnproto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestParseQueryPreLogin(t *testing.T) {
	q, err := ParseQuery(queryFrame(testGamePID, QueryPreLogin, 77, "{}\x00"), testGamePID)
	if err != nil {
		t.Fatal(err)
	}
	if q.Type != QueryPreLogin || q.ContextID != 77 {
		t.Fatalf("query = %+v", q)
	}
}

func TestParseQuerySubscriptionRequiresMinusOne(t *testing.T) {
	q, err := ParseQuery(queryFrame(testGamePID, QuerySubscription, -1, "{}"), testGamePID)
	if err != nil || q.Type != QuerySubscription || q.ContextID != -1 {
		t.Fatalf("query = %+v, %v", q, err)
	}
	if _, err := ParseQuery(queryFrame(testGamePID, QuerySubscription, 5, "{}"), testGamePID); !errors.Is(err, ErrFrame) {
		t.Fatalf("subscription with context 5: %v", err)
	}
}

func TestParseQueryUnknownTypeIsReportedNotValidated(t *testing.T) {
	q, err := ParseQuery(queryFrame(testGamePID, 3, 12, "garbage"), testGamePID)
	if err != nil || q.Type != 3 {
		t.Fatalf("query = %+v, %v", q, err)
	}
}

func TestParseQueryRejects(t *testing.T) {
	wrongOp := queryFrame(testGamePID, QueryPreLogin, 1, "{}")
	binary.LittleEndian.PutUint32(wrongOp[4:], OpRegister)
	cases := map[string]struct {
		frame []byte
		pid   uint32
	}{
		"payload":       {queryFrame(testGamePID, QueryPreLogin, 1, "{x}"), testGamePID},
		"trailing-data": {queryFrame(testGamePID, QueryPreLogin, 1, "{}\x00z"), testGamePID},
		"size":          {header(OpQuery, QuerySize+4), testGamePID},
		"op":            {wrongOp, testGamePID},
		"zero-pid":      {queryFrame(0, QueryPreLogin, 1, "{}"), 0},
	}
	for name, c := range cases {
		if _, err := ParseQuery(c.frame, c.pid); !errors.Is(err, ErrFrame) {
			t.Errorf("%s: err = %v, want ErrFrame", name, err)
		}
	}
	// Decoding with another PID byte must never yield a pre-login query.
	q, err := ParseQuery(queryFrame(testGamePID, QueryPreLogin, 1, "{}"), testGamePID+1)
	if err == nil && q.Type == QueryPreLogin {
		t.Fatal("query decoded with the wrong client PID")
	}
}

func testIdentity(info string) LoginIdentity {
	return LoginIdentity{
		GameUID:      []byte("123456789"),
		GameToken:    []byte("game-token-synthetic"),
		ChannelUID:   []byte("42"),
		ChannelToken: []byte("channel-token-synthetic"),
		Info:         []byte(info),
	}
}

func TestBuildLoginPacketsLayout(t *testing.T) {
	info := `{"a":1,"b":"异"}`
	extra, login, err := BuildLoginPackets(testIdentity(info), testHostPID)
	if err != nil {
		t.Fatal(err)
	}
	if len(extra) != len(info)+17 {
		t.Fatalf("extra len = %d", len(extra))
	}
	if binary.LittleEndian.Uint32(extra) != Magic || binary.LittleEndian.Uint32(extra[4:]) != OpExtraInfo ||
		binary.LittleEndian.Uint32(extra[8:]) != uint32(len(extra)) {
		t.Fatalf("extra header = % x", extra[:12])
	}
	e := plain(extra, testHostPID)
	if binary.LittleEndian.Uint32(e[12:]) != uint32(len(info)) || string(e[16:16+len(info)]) != info || e[len(e)-1] != 0 {
		t.Fatalf("extra body = % x", e[12:])
	}

	if len(login) != LoginResultSize || LoginResultSize != 0x594 {
		t.Fatalf("login len = %#x", len(login))
	}
	if binary.LittleEndian.Uint32(login) != Magic || binary.LittleEndian.Uint32(login[4:]) != OpLoginResult ||
		binary.LittleEndian.Uint32(login[8:]) != LoginResultSize {
		t.Fatalf("login header = % x", login[:12])
	}
	l := plain(login, testHostPID)
	checks := []struct {
		off, size int
		want      string
	}{
		{0x00C, 64, "123456789"},
		{0x04C, 128, "game-token-synthetic"},
		{0x0D4, 64, "42"},
		{0x114, 128, "channel-token-synthetic"},
		{0x194, 1024, info},
	}
	for _, c := range checks {
		field := l[c.off : c.off+c.size]
		if cstr(field) != c.want || !allZero(field[len(c.want):]) {
			t.Errorf("field %#x = %q", c.off, cstr(field))
		}
	}
	if int32(binary.LittleEndian.Uint32(l[0xCC:])) != Platform || int32(binary.LittleEndian.Uint32(l[0xD0:])) != ChannelID {
		t.Fatalf("platform/channel = % x", l[0xCC:0xD4])
	}
}

func TestBuildLoginPacketsLegacyInfoField(t *testing.T) {
	fits := strings.Repeat("x", 1023)
	_, login, err := BuildLoginPackets(testIdentity(fits), testHostPID)
	if err != nil {
		t.Fatal(err)
	}
	if got := plain(login, testHostPID)[0x194:]; string(got[:1023]) != fits || got[1023] != 0 {
		t.Fatal("1023-byte info not copied")
	}
	big := strings.Repeat("y", 1024)
	extra, login, err := BuildLoginPackets(testIdentity(big), testHostPID)
	if err != nil {
		t.Fatal(err)
	}
	if !allZero(plain(login, testHostPID)[0x194:]) {
		t.Fatal("legacy info field not cleared for 1024-byte info")
	}
	if string(plain(extra, testHostPID)[16:16+1024]) != big {
		t.Fatal("extra packet lost the full info")
	}
	extra, _, err = BuildLoginPackets(testIdentity(""), testHostPID)
	if err != nil || len(extra) != 17 || !allZero(plain(extra, testHostPID)[12:]) {
		t.Fatalf("empty info: len %d, %v", len(extra), err)
	}
}

func TestBuildLoginPacketsBounds(t *testing.T) {
	if _, _, err := BuildLoginPackets(testIdentity(strings.Repeat("z", 65519)), testHostPID); err != nil {
		t.Fatalf("65519-byte info rejected: %v", err)
	}
	mutate := map[string]func(*LoginIdentity){
		"info-65520":    func(id *LoginIdentity) { id.Info = bytes.Repeat([]byte("z"), 65520) },
		"empty-uid":     func(id *LoginIdentity) { id.GameUID = nil },
		"uid-64":        func(id *LoginIdentity) { id.GameUID = bytes.Repeat([]byte("1"), 64) },
		"token-128":     func(id *LoginIdentity) { id.GameToken = bytes.Repeat([]byte("t"), 128) },
		"channel-uid":   func(id *LoginIdentity) { id.ChannelUID = []byte("4\x002") },
		"channel-token": func(id *LoginIdentity) { id.ChannelToken = nil },
	}
	for name, m := range mutate {
		id := testIdentity("{}")
		m(&id)
		if _, _, err := BuildLoginPackets(id, testHostPID); !errors.Is(err, ErrValue) {
			t.Errorf("%s: err = %v, want ErrValue", name, err)
		}
	}
	if _, _, err := BuildLoginPackets(testIdentity("{}"), 0); !errors.Is(err, ErrValue) {
		t.Errorf("host pid 0: %v", err)
	}
	// 63 / 127 bytes are the largest values that keep a terminator.
	id := testIdentity("{}")
	id.GameUID, id.GameToken = bytes.Repeat([]byte("1"), 63), bytes.Repeat([]byte("t"), 127)
	if _, _, err := BuildLoginPackets(id, testHostPID); err != nil {
		t.Fatalf("maximum fields rejected: %v", err)
	}
}

func TestLoginIdentityFormattingRedacts(t *testing.T) {
	id := testIdentity(`{"secret-info":1}`)
	out := fmt.Sprintf("%v|%+v|%#v|%s|%x", id, id, id, id, &id)
	for _, s := range []string{"game-token", "channel-token", "secret-info", "123456789", "67616d65"} {
		if strings.Contains(out, s) {
			t.Fatalf("formatted identity leaks %q: %s", s, out)
		}
	}
}

func TestBuildPreLoginReplyLayout(t *testing.T) {
	body := []byte(`{"code":0,"result":{}}`)
	msg := "ok 异环"
	frame, err := BuildPreLoginReply(PreLoginReply{Code: -5, ContextID: 9, Message: msg, Body: body}, testHostPID)
	if err != nil {
		t.Fatal(err)
	}
	if len(frame) != len(body)+0x422 {
		t.Fatalf("len = %d", len(frame))
	}
	if binary.LittleEndian.Uint32(frame) != Magic || binary.LittleEndian.Uint32(frame[4:]) != OpQueryReply ||
		binary.LittleEndian.Uint32(frame[8:]) != uint32(len(frame)) {
		t.Fatalf("header = % x", frame[:12])
	}
	p := plain(frame, testHostPID)
	ints := []struct {
		off  int
		want int32
	}{{12, int32(len(body) + 0x411)}, {16, QueryPreLogin}, {20, -5}, {24, 9}}
	for _, c := range ints {
		if got := int32(binary.LittleEndian.Uint32(p[c.off:])); got != c.want {
			t.Errorf("i32 @%d = %d, want %d", c.off, got, c.want)
		}
	}
	if got := readWideAt(p, 28, 1024); got != msg {
		t.Fatalf("message = %q", got)
	}
	if !allZero(p[28+2*len([]rune(msg)) : 0x41C]) {
		t.Fatal("message field not NUL-padded")
	}
	if !bytes.Equal(p[0x41C:0x41C+len(body)], body) || !allZero(p[0x41C+len(body):]) || len(p)-0x41C-len(body) != 6 {
		t.Fatal("body or trailer wrong")
	}
}

func TestBuildPreLoginReplyBounds(t *testing.T) {
	max := bytes.Repeat([]byte("a"), MaxPreLoginBody)
	frame, err := BuildPreLoginReply(PreLoginReply{Body: max}, testHostPID)
	if err != nil || len(frame) != MaxFrameSize {
		t.Fatalf("max body: len %d, %v", len(frame), err)
	}
	cases := map[string]PreLoginReply{
		"empty":       {},
		"oversize":    {Body: append(max, 'a')},
		"nul-body":    {Body: []byte("{\x00}")},
		"long-msg":    {Body: []byte("{}"), Message: strings.Repeat("m", 512)},
		"nul-msg":     {Body: []byte("{}"), Message: "a\x00b"},
		"invalid-utf": {Body: []byte("{}"), Message: "\xff"},
	}
	for name, r := range cases {
		if _, err := BuildPreLoginReply(r, testHostPID); !errors.Is(err, ErrValue) {
			t.Errorf("%s: err = %v, want ErrValue", name, err)
		}
	}
	if _, err := BuildPreLoginReply(PreLoginReply{Body: []byte("{}"), Message: strings.Repeat("m", 511)}, testHostPID); err != nil {
		t.Fatalf("511-unit message rejected: %v", err)
	}
	if _, err := BuildPreLoginReply(PreLoginReply{Body: []byte("{}")}, 0); !errors.Is(err, ErrValue) {
		t.Fatalf("host pid 0: %v", err)
	}
}
