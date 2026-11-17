// SPDX-License-Identifier: GPL-3.0-or-later

package cnproto

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// CN service constants.
const (
	// AppID is the CN application id.
	AppID = 1289
	// AppIDString is AppID as it appears in IPC strings and HTTP fields.
	AppIDString = "1289"
	// Platform is the PC platform id sent in op 2 and HTTP requests.
	Platform = 4
	// ChannelID is the CN channel id sent in op 2 and HTTP requests.
	ChannelID = 9
	// ServiceSDKVersion is the HTTP sdkVersion, independent of the bridge IPC version.
	ServiceSDKVersion = "5.159.0"
	// ConfigName is the configuration name the bridge registers in op 0.
	ConfigName = "oneSdkConfig.config"
	// CopyDataTag is COPYDATASTRUCT.dwData for every host-to-game packet ("wmgp").
	CopyDataTag = 0x70676D77
)

// wParam values of the registered "<class>.Notify" window message.
const (
	// NotifyLogin asks the host to log in.
	NotifyLogin = 1
	// NotifyLogout reports an in-game logout.
	NotifyLogout = 4
	// NotifyGeometry is a game window geometry hint (payload not interpreted).
	NotifyGeometry = 14
)

// Frame layout constants.
const (
	// Magic is the little-endian u32 at offset 0 of every frame ("1sdk").
	Magic = 0x6B647331
	// HeaderSize is the plain header (magic, op, length); bodies start here.
	HeaderSize = 12
	// MaxFrameSize bounds any frame received or sent.
	MaxFrameSize = 65536
)

// Operations.
const (
	// OpRegister is the client registration frame from the game.
	OpRegister = 0
	// OpLoginResult is the login identity packet sent to the game.
	OpLoginResult = 2
	// OpQuery is a game query (antifatigue) over the pipe.
	OpQuery = 40
	// OpQueryReply answers an OpQuery via WM_COPYDATA.
	OpQueryReply = 49
	// OpEnvInit is the environment initialization request from the game.
	OpEnvInit = 1000
	// OpEnvReply answers OpEnvInit on the pipe.
	OpEnvReply = 1001
	// OpExtraInfo carries the extended login information, sent before OpLoginResult.
	OpExtraInfo = 0x40D
)

// Fixed frame sizes.
const (
	// EnvInitSize is the size of an op 1000 request.
	EnvInitSize = 796
	// EnvReplySize is the size of an op 1001 reply.
	EnvReplySize = 0x1624
	// RegistrationSize is the size of an op 0 registration.
	RegistrationSize = 1244
	// QuerySize is the size of an op 40 query.
	QuerySize = 0x214
	// LoginResultSize is the size of an op 2 packet.
	LoginResultSize = 0x594
)

var (
	// ErrFrame reports a frame whose header or shape does not match the protocol.
	ErrFrame = errors.New("cnproto: unexpected SDK frame")
	// ErrValue reports an input value that does not fit its protocol field.
	ErrValue = errors.New("cnproto: value does not fit the SDK protocol")
)

// Header is the plain 12-byte frame header.
type Header struct {
	// Op is the operation code.
	Op uint32
	// Length is the total frame length including the header.
	Length int
}

// ParseHeader validates the generic header of a received frame: at least
// HeaderSize bytes, at most MaxFrameSize, Magic, and a length field equal to
// len(frame).
func ParseHeader(frame []byte) (Header, error) {
	if len(frame) < HeaderSize || len(frame) > MaxFrameSize {
		return Header{}, fmt.Errorf("%w: frame size", ErrFrame)
	}
	if binary.LittleEndian.Uint32(frame) != Magic {
		return Header{}, fmt.Errorf("%w: header magic", ErrFrame)
	}
	if binary.LittleEndian.Uint32(frame[8:]) != uint32(len(frame)) {
		return Header{}, fmt.Errorf("%w: header length", ErrFrame)
	}
	return Header{Op: binary.LittleEndian.Uint32(frame[4:]), Length: len(frame)}, nil
}

// expect checks the header plus an exact operation and size.
func expect(frame []byte, op uint32, size int) error {
	h, err := ParseHeader(frame)
	if err != nil {
		return err
	}
	if h.Op != op || h.Length != size {
		return fmt.Errorf("%w: operation %d with %d bytes", ErrFrame, h.Op, h.Length)
	}
	return nil
}

func putHeader(b []byte, op uint32) {
	binary.LittleEndian.PutUint32(b, Magic)
	binary.LittleEndian.PutUint32(b[4:], op)
	binary.LittleEndian.PutUint32(b[8:], uint32(len(b)))
}

// obfuscate XORs everything after the header with the PID's low byte.
func obfuscate(b []byte, pid uint32) {
	key := byte(pid)
	for i := HeaderSize; i < len(b); i++ {
		b[i] ^= key
	}
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
