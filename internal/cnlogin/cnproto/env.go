// SPDX-License-Identifier: GPL-3.0-or-later

package cnproto

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// op 1000 / op 1001 layout.
const (
	envInitPIDOffset  = 12
	envInitAbbrOffset = 18
	envInitAbbrBytes  = 64
	envPayloadOffset  = HeaderSize
	envPayloadLimit   = 4096
	maxPathUnits      = 259 // excluding the terminator
)

var envExeOffsets = [3]int{0x100C, 0x1214, 0x141C}

// EnvInit is the content of an op 1000 environment initialization request.
type EnvInit struct {
	// Abbreviation is the application abbreviation (CN: "1289").
	Abbreviation string
}

// ParseEnvInit validates an op 1000 request from the game whose PID is
// gamePID. The frame is not XOR-obfuscated: its u32 at offset 12 must equal
// gamePID and the abbreviation must match [A-Za-z0-9_.-]{1,31}.
func ParseEnvInit(frame []byte, gamePID uint32) (EnvInit, error) {
	if gamePID == 0 {
		return EnvInit{}, fmt.Errorf("%w: client process id", ErrFrame)
	}
	if err := expect(frame, OpEnvInit, EnvInitSize); err != nil {
		return EnvInit{}, err
	}
	if binary.LittleEndian.Uint32(frame[envInitPIDOffset:]) != gamePID {
		return EnvInit{}, fmt.Errorf("%w: environment request from another process", ErrFrame)
	}
	units := wideField(frame, envInitAbbrOffset, envInitAbbrBytes)
	abbr := string(utf16.Decode(units))
	if !validAbbreviation(abbr) {
		return EnvInit{}, fmt.Errorf("%w: application abbreviation", ErrFrame)
	}
	return EnvInit{Abbreviation: abbr}, nil
}

// EnvValues are the host-side values sent in the op 1001 reply. Every field
// is supplied by the caller (the Windows host owns the objects they name).
type EnvValues struct {
	// HostPID is the host's own process id (ArcProcessId).
	HostPID uint32
	// Abbreviation echoes the op 1000 abbreviation (ArcGameAppAbbr).
	Abbreviation string
	// NotifyMessage is RegisterWindowMessageW("<class>.Notify") (ArcMoblieNotify).
	NotifyMessage uint32
	// AnchorW is AnchorW(host window HWND).
	AnchorW int32
	// AnchorV is AnchorV(host window title).
	AnchorV int32
	// MapName is the shared-memory section name (ArcMId).
	MapName string
	// MutexName is the mutex name (ArcMMId).
	MutexName string
	// PlatformExecutables are the three platform executable paths written as
	// UTF-16LE at 0x100C, 0x1214 and 0x141C (each < 260 units).
	PlatformExecutables [3]string
}

// BuildEnvReply encodes the 0x1624-byte op 1001 reply (plain, not XOR-ed).
// The payload is "k|=|v|;|k|=|v..." in the upstream key order; it must be
// printable ASCII without '|' in values and shorter than 4096 bytes.
func BuildEnvReply(v EnvValues) ([]byte, error) {
	if v.HostPID == 0 {
		return nil, fmt.Errorf("%w: host process id", ErrValue)
	}
	if !validAbbreviation(v.Abbreviation) {
		return nil, fmt.Errorf("%w: application abbreviation", ErrValue)
	}
	pairs := [][2]string{
		{"ArcProcessId", strconv.FormatUint(uint64(v.HostPID), 10)},
		{"ArcGameAppId", AppIDString},
		{"ArcGameAppAbbr", v.Abbreviation},
		{"ArcMoblieNotify", strconv.FormatUint(uint64(v.NotifyMessage), 10)},
		{"anchor_w", strconv.FormatInt(int64(v.AnchorW), 10)},
		{"anchor_v", strconv.FormatInt(int64(v.AnchorV), 10)},
		{"ArcMId", v.MapName},
		{"ArcMMId", v.MutexName},
	}
	var sb strings.Builder
	for i, p := range pairs {
		if !validEnvValue(p[1]) {
			return nil, fmt.Errorf("%w: environment value %s", ErrValue, p[0])
		}
		if i > 0 {
			sb.WriteString("|;|")
		}
		sb.WriteString(p[0])
		sb.WriteString("|=|")
		sb.WriteString(p[1])
	}
	payload := sb.String()
	if len(payload) >= envPayloadLimit {
		return nil, fmt.Errorf("%w: environment payload exceeds %d bytes", ErrValue, envPayloadLimit)
	}
	exes := make([][]uint16, len(v.PlatformExecutables))
	for i, path := range v.PlatformExecutables {
		units, err := pathUnits(path)
		if err != nil {
			return nil, fmt.Errorf("%w: platform executable %d", err, i)
		}
		exes[i] = units
	}
	reply := make([]byte, EnvReplySize)
	putHeader(reply, OpEnvReply)
	copy(reply[envPayloadOffset:], payload)
	for i, units := range exes {
		putUnits(reply, envExeOffsets[i], units)
	}
	return reply, nil
}

// AnchorW returns the op 1001 anchor_w value: the HWND's low 32 bits as a
// signed int32, XOR AppID.
func AnchorW(hwnd uintptr) int32 {
	return int32(uint32(hwnd)) ^ AppID
}

// TitleChecksum returns the XOR of all UTF-16 code units of a window title.
func TitleChecksum(title string) int32 {
	var sum int32
	for _, u := range utf16.Encode([]rune(title)) {
		sum ^= int32(u)
	}
	return sum
}

// AnchorV returns the op 1001 anchor_v value: 1 - (TitleChecksum(title) XOR AppID).
func AnchorV(title string) int32 {
	return 1 - (TitleChecksum(title) ^ AppID)
}

func validAbbreviation(s string) bool {
	if len(s) == 0 || len(s) > 31 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-'
		if !ok {
			return false
		}
	}
	return true
}

// validEnvValue accepts non-empty printable ASCII without the '|' separator byte.
func validEnvValue(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c > 0x7E || c == '|' {
			return false
		}
	}
	return true
}

// pathUnits encodes a non-empty path of at most maxPathUnits UTF-16 units.
func pathUnits(path string) ([]uint16, error) {
	if path == "" || !utf8.ValidString(path) || strings.IndexByte(path, 0) >= 0 {
		return nil, ErrValue
	}
	units := utf16.Encode([]rune(path))
	if len(units) > maxPathUnits {
		return nil, ErrValue
	}
	return units, nil
}

// wideField returns the UTF-16LE units of a fixed field up to the first NUL.
func wideField(b []byte, off, size int) []uint16 {
	units := make([]uint16, 0, size/2)
	for i := off; i+1 < off+size; i += 2 {
		u := binary.LittleEndian.Uint16(b[i:])
		if u == 0 {
			break
		}
		units = append(units, u)
	}
	return units
}

// putUnits writes UTF-16LE units at b[off:]; the following NUL is implied by
// the zero-filled buffer.
func putUnits(b []byte, off int, units []uint16) {
	for i, u := range units {
		binary.LittleEndian.PutUint16(b[off+2*i:], u)
	}
}
