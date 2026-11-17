// SPDX-License-Identifier: GPL-3.0-or-later

package cnproto

import (
	"encoding/binary"
	"unicode/utf16"
)

// Synthetic values only; no real application key appears in this package.
const (
	testAppKey      = "ABCDEFGHIJKLMNOPQRSTUVWXYZ012345"
	testGamePID     = uint32(0x1234AB)
	testHostPID     = uint32(0x00C0FFEE)
	testCallbackWnd = uint32(0x00020F3C)
)

// putWide writes s as UTF-16LE at b[off:] without a terminator.
func putWide(b []byte, off int, s string) {
	for i, u := range utf16.Encode([]rune(s)) {
		binary.LittleEndian.PutUint16(b[off+2*i:], u)
	}
}

// readWideAt decodes a NUL-terminated UTF-16LE string from a fixed field.
func readWideAt(b []byte, off, size int) string {
	var units []uint16
	for i := off; i+1 < off+size; i += 2 {
		u := binary.LittleEndian.Uint16(b[i:])
		if u == 0 {
			break
		}
		units = append(units, u)
	}
	return string(utf16.Decode(units))
}

func header(op uint32, size int) []byte {
	b := make([]byte, size)
	binary.LittleEndian.PutUint32(b, Magic)
	binary.LittleEndian.PutUint32(b[4:], op)
	binary.LittleEndian.PutUint32(b[8:], uint32(size))
	return b
}

func xorFrom12(b []byte, pid uint32) {
	for i := HeaderSize; i < len(b); i++ {
		b[i] ^= byte(pid)
	}
}

func envInitFrame(pid uint32, abbr string) []byte {
	b := header(OpEnvInit, EnvInitSize)
	binary.LittleEndian.PutUint32(b[12:], pid)
	putWide(b, 18, abbr)
	return b
}

// regFields describes a synthetic op 0 registration before XOR obfuscation.
type regFields struct {
	hwnd    uint32
	app     string
	key     string
	config  string
	version string
	// keyUnits, when set, replaces key with raw UTF-16 units.
	keyUnits []uint16
	// noKeyTerminator writes a non-zero unit right after the 32-unit key.
	noKeyTerminator bool
}

func validReg(version string) regFields {
	return regFields{hwnd: testCallbackWnd, app: "1289", key: testAppKey, config: "oneSdkConfig.config", version: version}
}

func registrationFrame(pid uint32, f regFields) []byte {
	b := header(OpRegister, RegistrationSize)
	body := b[HeaderSize:]
	binary.LittleEndian.PutUint32(body, f.hwnd)
	putWide(body, 0x004, f.app)
	if f.keyUnits != nil {
		for i, u := range f.keyUnits {
			binary.LittleEndian.PutUint16(body[0x104+2*i:], u)
		}
	} else {
		putWide(body, 0x104, f.key)
	}
	if f.noKeyTerminator {
		binary.LittleEndian.PutUint16(body[0x104+64:], 'X')
	}
	putWide(body, 0x204, f.config)
	putWide(body, 0x40C, f.version)
	xorFrom12(b, pid)
	return b
}

func queryFrame(pid uint32, queryType, contextID int32, payload string) []byte {
	b := header(OpQuery, QuerySize)
	body := b[HeaderSize:]
	binary.LittleEndian.PutUint32(body, uint32(queryType))
	copy(body[4:], payload)
	binary.LittleEndian.PutUint32(body[0x204:], uint32(contextID))
	xorFrom12(b, pid)
	return b
}

// plain returns a copy of an obfuscated host packet with its body decoded.
func plain(packet []byte, pid uint32) []byte {
	b := append([]byte(nil), packet...)
	xorFrom12(b, pid)
	return b
}

func cstr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
