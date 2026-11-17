// SPDX-License-Identifier: GPL-3.0-or-later

package cnproto

import (
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func TestParseHeaderAcceptsConsistentFrame(t *testing.T) {
	h, err := ParseHeader(header(OpQuery, QuerySize))
	if err != nil {
		t.Fatal(err)
	}
	if h.Op != OpQuery || h.Length != QuerySize {
		t.Fatalf("header = %+v", h)
	}
}

func TestParseHeaderRejectsMalformedFrames(t *testing.T) {
	badMagic := header(OpQuery, 32)
	badMagic[0] ^= 1
	badLength := header(OpQuery, 32)
	binary.LittleEndian.PutUint32(badLength[8:], 33)
	cases := map[string][]byte{
		"short":      make([]byte, HeaderSize-1),
		"magic":      badMagic,
		"length":     badLength,
		"oversize":   header(OpQuery, MaxFrameSize+1),
		"empty-body": nil,
	}
	for name, frame := range cases {
		if _, err := ParseHeader(frame); !errors.Is(err, ErrFrame) {
			t.Errorf("%s: err = %v, want ErrFrame", name, err)
		}
	}
}

func TestParseEnvInit(t *testing.T) {
	got, err := ParseEnvInit(envInitFrame(testGamePID, "1289"), testGamePID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Abbreviation != "1289" {
		t.Fatalf("abbreviation = %q", got.Abbreviation)
	}
}

func TestParseEnvInitIsNotObfuscated(t *testing.T) {
	// The PID and abbreviation are read plain; an XOR-ed frame must fail.
	frame := envInitFrame(testGamePID, "1289")
	xorFrom12(frame, testGamePID)
	if _, err := ParseEnvInit(frame, testGamePID); err == nil {
		t.Fatal("obfuscated op 1000 accepted")
	}
}

func TestParseEnvInitRejects(t *testing.T) {
	wrongOp := envInitFrame(testGamePID, "1289")
	binary.LittleEndian.PutUint32(wrongOp[4:], OpEnvReply)
	cases := map[string]struct {
		frame []byte
		pid   uint32
	}{
		"other-pid":    {envInitFrame(testGamePID+1, "1289"), testGamePID},
		"wrong-op":     {wrongOp, testGamePID},
		"wrong-size":   {header(OpEnvInit, EnvInitSize-2), testGamePID},
		"empty-abbr":   {envInitFrame(testGamePID, ""), testGamePID},
		"unsafe-abbr":  {envInitFrame(testGamePID, "12|89"), testGamePID},
		"too-long":     {envInitFrame(testGamePID, strings.Repeat("a", 32)), testGamePID},
		"non-ascii":    {envInitFrame(testGamePID, "异环"), testGamePID},
		"zero-pid-arg": {envInitFrame(0, "1289"), 0},
	}
	for name, c := range cases {
		if _, err := ParseEnvInit(c.frame, c.pid); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func validEnvValues() EnvValues {
	return EnvValues{
		HostPID:       4242,
		Abbreviation:  "1289",
		NotifyMessage: 0xC123,
		AnchorW:       69557,
		AnchorV:       -1386,
		MapName:       `Local\Oddward.Map.0123`,
		MutexName:     `Local\Oddward.Mutex.0123`,
		PlatformExecutables: [3]string{
			`C:\Oddward\a.exe`, `C:\Oddward\b.exe`, `C:\Oddward\c.exe`,
		},
	}
}

func TestBuildEnvReplyLayout(t *testing.T) {
	reply, err := BuildEnvReply(validEnvValues())
	if err != nil {
		t.Fatal(err)
	}
	if len(reply) != EnvReplySize || EnvReplySize != 0x1624 {
		t.Fatalf("len = %#x", len(reply))
	}
	if binary.LittleEndian.Uint32(reply) != Magic || binary.LittleEndian.Uint32(reply[4:]) != OpEnvReply ||
		binary.LittleEndian.Uint32(reply[8:]) != EnvReplySize {
		t.Fatalf("header = % x", reply[:12])
	}
	want := `ArcProcessId|=|4242|;|ArcGameAppId|=|1289|;|ArcGameAppAbbr|=|1289|;|ArcMoblieNotify|=|49443` +
		`|;|anchor_w|=|69557|;|anchor_v|=|-1386|;|ArcMId|=|Local\Oddward.Map.0123|;|ArcMMId|=|Local\Oddward.Mutex.0123`
	if got := cstr(reply[12:0x100C]); got != want {
		t.Fatalf("payload\n got %s\nwant %s", got, want)
	}
	if !allZero(reply[12+len(want) : 0x100C]) {
		t.Fatal("payload area not NUL-padded")
	}
	for i, off := range []int{0x100C, 0x1214, 0x141C} {
		if got := readWideAt(reply, off, 520); got != validEnvValues().PlatformExecutables[i] {
			t.Fatalf("exe[%d] = %q", i, got)
		}
	}
	end := 0x141C + 2*(len(validEnvValues().PlatformExecutables[2])+1)
	if !allZero(reply[end:]) {
		t.Fatal("tail not zero-filled")
	}
}

func TestBuildEnvReplyRejectsOutOfBoundValues(t *testing.T) {
	mutate := map[string]func(*EnvValues){
		"payload-4096": func(v *EnvValues) { v.MapName = strings.Repeat("m", 4000) },
		"separator":    func(v *EnvValues) { v.MutexName = "a|;|b" },
		"non-ascii":    func(v *EnvValues) { v.MapName = "Local\\映射" },
		"bad-abbr":     func(v *EnvValues) { v.Abbreviation = "" },
		"path-260":     func(v *EnvValues) { v.PlatformExecutables[1] = strings.Repeat("p", 260) },
		"empty-path":   func(v *EnvValues) { v.PlatformExecutables[0] = "" },
		"nul-in-path":  func(v *EnvValues) { v.PlatformExecutables[2] = "C:\\a\x00b" },
		"zero-pid":     func(v *EnvValues) { v.HostPID = 0 },
	}
	for name, m := range mutate {
		v := validEnvValues()
		m(&v)
		if _, err := BuildEnvReply(v); !errors.Is(err, ErrValue) {
			t.Errorf("%s: err = %v, want ErrValue", name, err)
		}
	}
	// 259 UTF-16 units plus the terminator still fit.
	v := validEnvValues()
	v.PlatformExecutables[0] = strings.Repeat("p", 259)
	if _, err := BuildEnvReply(v); err != nil {
		t.Fatalf("259-char path rejected: %v", err)
	}
}

func TestAnchorVectors(t *testing.T) {
	// spec §8.1 synthetic vectors.
	if got := AnchorW(0x00010ABC); got != 69557 {
		t.Fatalf("AnchorW = %d", got)
	}
	title := "Collapse NTE SDK Probe 00000000000000000000000000000000"
	if got := TitleChecksum(title); got != 98 {
		t.Fatalf("TitleChecksum = %d", got)
	}
	if got := AnchorV(title); got != -1386 {
		t.Fatalf("AnchorV = %d", got)
	}
	// HWND values are truncated to their low 32 bits, then XOR-ed as int32.
	if got := AnchorW(0xFFFF_FFFF_8000_0000); got != int32(-0x80000000^1289) {
		t.Fatalf("AnchorW(high) = %d", got)
	}
}
