// SPDX-License-Identifier: GPL-3.0-or-later

package cnproto

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"unicode/utf16"
)

// op 0 registration body offsets (relative to body start, frame offset +12).
const (
	regCallbackOffset = 0x000
	regAppOffset      = 0x004
	regAppBytes       = 256
	regKeyOffset      = 0x104
	regKeyUnits       = 32
	regConfigOffset   = 0x204
	regConfigBytes    = 520
	regVersionOffset  = 0x40C
	regVersionBytes   = 64
)

var (
	// ErrRegistration reports an op 0 registration whose metadata does not
	// match the owned client, the selected profile, or the key encoding. Its
	// message never contains key material.
	ErrRegistration = errors.New("cnproto: unverified CN registration")
	// ErrZeroed reports key access after the registration body was zeroed.
	ErrZeroed = errors.New("cnproto: registration key material already zeroed")
)

// Registration is a validated op 0 client registration. It holds the 32-byte
// application key, which never appears in any formatted representation.
type Registration struct {
	// CallbackWindow is the game's callback HWND (low 32 bits), used as the
	// WM_COPYDATA target.
	CallbackWindow uint32

	profile Profile
	appKey  [regKeyUnits]byte
	zeroed  bool
}

// ParseRegistration validates an op 0 frame from the game whose process id is
// gamePID, against profile p, without modifying frame. The body is XOR-ed with
// the low byte of gamePID; the application id, config name and version must
// match the owned CN client and p; the 32-unit key must be printable ASCII
// with a NUL terminator. Errors never include key material.
func ParseRegistration(frame []byte, gamePID uint32, p Profile) (*Registration, error) {
	if gamePID == 0 {
		return nil, fmt.Errorf("%w: client process id", ErrFrame)
	}
	if err := expect(frame, OpRegister, RegistrationSize); err != nil {
		return nil, err
	}
	if p.SDKVersion == "" {
		return nil, fmt.Errorf("%w: no compatibility profile", ErrRegistration)
	}

	// Work on a copy: the caller's frame must stay untouched, and the copy
	// (which holds the plain key) is wiped before returning.
	body := make([]byte, RegistrationSize-HeaderSize)
	copy(body, frame[HeaderSize:])
	k := byte(gamePID)
	for i := range body {
		body[i] ^= k
	}
	defer zero(body)

	if app := string(utf16.Decode(wideField(body, regAppOffset, regAppBytes))); app != AppIDString {
		return nil, fmt.Errorf("%w: application", ErrRegistration)
	}
	if cfg := string(utf16.Decode(wideField(body, regConfigOffset, regConfigBytes))); cfg != ConfigName {
		return nil, fmt.Errorf("%w: config name", ErrRegistration)
	}
	if version := string(utf16.Decode(wideField(body, regVersionOffset, regVersionBytes))); version != p.SDKVersion {
		return nil, fmt.Errorf("%w: SDK protocol version", ErrRegistration)
	}

	reg := &Registration{
		CallbackWindow: binary.LittleEndian.Uint32(body[regCallbackOffset:]),
		profile:        p,
	}
	if err := reg.decodeKey(body); err != nil {
		return nil, err
	}
	return reg, nil
}

// decodeKey reads the 32-unit application key: each unit must be 0x20..0x7E and
// a u16 0 terminator must follow the 32nd unit.
func (r *Registration) decodeKey(body []byte) error {
	for i := 0; i < regKeyUnits; i++ {
		u := binary.LittleEndian.Uint16(body[regKeyOffset+2*i:])
		if u < 0x20 || u > 0x7E {
			return fmt.Errorf("%w: application key encoding", ErrRegistration)
		}
		r.appKey[i] = byte(u)
	}
	if binary.LittleEndian.Uint16(body[regKeyOffset+2*regKeyUnits:]) != 0 {
		return fmt.Errorf("%w: application key encoding", ErrRegistration)
	}
	return nil
}

// AppKeyTail returns appKey[16:32], the AES-128 key for the boot config and the
// OneLogin envelope (§3.1).
func (r *Registration) AppKeyTail() ([16]byte, error) {
	var out [16]byte
	if r.zeroed {
		return out, ErrZeroed
	}
	copy(out[:], r.appKey[16:32])
	return out, nil
}

// PreLoginKey returns appKey[1:3] ‖ appKey[7:12] ‖ appKey[15:18] ‖ appKey[25:31]
// (2+5+3+6 = 16 bytes), the antifatigue preLogin signing key (§3.9).
func (r *Registration) PreLoginKey() ([16]byte, error) {
	var out [16]byte
	if r.zeroed {
		return out, ErrZeroed
	}
	n := 0
	for _, seg := range [][2]int{{1, 3}, {7, 12}, {15, 18}, {25, 31}} {
		n += copy(out[n:], r.appKey[seg[0]:seg[1]])
	}
	return out, nil
}

// Zero wipes the stored application key. Key access afterward returns ErrZeroed.
func (r *Registration) Zero() {
	for i := range r.appKey {
		r.appKey[i] = 0
	}
	r.zeroed = true
}

// safeString is the only textual form of a Registration: public profile and
// callback window, never key material.
func (r Registration) safeString() string {
	state := "live"
	if r.zeroed {
		state = "zeroed"
	}
	return fmt.Sprintf("Registration{profile:%s/%s callback:%#x key:%s}", r.profile.Name, r.profile.SDKVersion, r.CallbackWindow, state)
}

// String, GoString and Format all route through safeString so no verb (%v,
// %+v, %#v, %s, %x, %q, …) can expose the key, for value or pointer operands.
func (r Registration) String() string   { return r.safeString() }
func (r Registration) GoString() string { return r.safeString() }

func (r Registration) Format(f fmt.State, verb rune) {
	switch verb {
	case 'q':
		fmt.Fprintf(f, "%q", r.safeString())
	default:
		io.WriteString(f, r.safeString())
	}
}
