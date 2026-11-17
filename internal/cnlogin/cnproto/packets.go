// SPDX-License-Identifier: GPL-3.0-or-later

package cnproto

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"unicode/utf16"
	"unicode/utf8"
)

// op 40 query types (spec §2.8).
const (
	// QueryPreLogin is the antifatigue preLogin query; it is answered with an
	// op 49 reply.
	QueryPreLogin = 1
	// QuerySubscription is the info subscription. It always carries context
	// id -1 and must never be answered: the bridge re-arms it immediately and
	// a reply loops forever.
	QuerySubscription = 2
)

// op 40 body offsets (relative to body start, frame offset +12).
const (
	queryTypeOffset    = 0
	queryPayloadOffset = 4
	queryContextOffset = 0x204
)

// Query is a decoded op 40 game query.
type Query struct {
	// Type is the query type; types other than QueryPreLogin and
	// QuerySubscription are reported, not validated, and never answered.
	Type int32
	// ContextID must be echoed in the op 49 reply.
	ContextID int32
}

// ParseQuery decodes an op 40 frame from the game whose process id is
// gamePID. The body is XOR-ed with the low byte of gamePID. Known query types
// must carry a JSON payload followed only by zero padding; a subscription must
// carry context id -1.
func ParseQuery(frame []byte, gamePID uint32) (Query, error) {
	if gamePID == 0 {
		return Query{}, fmt.Errorf("%w: client process id", ErrFrame)
	}
	if err := expect(frame, OpQuery, QuerySize); err != nil {
		return Query{}, err
	}
	body := make([]byte, QuerySize-HeaderSize)
	copy(body, frame[HeaderSize:])
	k := byte(gamePID)
	for i := range body {
		body[i] ^= k
	}
	q := Query{
		Type:      int32(binary.LittleEndian.Uint32(body[queryTypeOffset:])),
		ContextID: int32(binary.LittleEndian.Uint32(body[queryContextOffset:])),
	}
	if q.Type != QueryPreLogin && q.Type != QuerySubscription {
		return q, nil
	}
	payload, ok := terminated(body[queryPayloadOffset:queryContextOffset])
	if !ok || !json.Valid(payload) {
		return Query{}, fmt.Errorf("%w: query payload", ErrFrame)
	}
	if q.Type == QuerySubscription && q.ContextID != -1 {
		return Query{}, fmt.Errorf("%w: subscription context", ErrFrame)
	}
	return q, nil
}

// terminated returns the bytes of field before its first NUL, requiring that
// everything after that NUL is zero padding. A field without a NUL is
// rejected.
func terminated(field []byte) ([]byte, bool) {
	n := bytes.IndexByte(field, 0)
	if n < 0 {
		return nil, false
	}
	for _, c := range field[n:] {
		if c != 0 {
			return nil, false
		}
	}
	return field[:n], true
}

// op 2 login result offsets (absolute frame offsets, spec §2.7).
const (
	loginGameUIDOffset      = 0x00C
	loginUIDBytes           = 64
	loginGameTokenOffset    = 0x04C
	loginTokenBytes         = 128
	loginPlatformOffset     = 0x0CC
	loginChannelOffset      = 0x0D0
	loginChannelUIDOffset   = 0x0D4
	loginChannelTokenOffset = 0x114
	loginInfoOffset         = 0x194
	loginInfoBytes          = 1024
)

// op 0x40D layout.
const (
	extraLengthOffset = 12
	extraInfoOffset   = 16
	// extraOverhead is header + length + NUL terminator.
	extraOverhead = extraInfoOffset + 1
	// MaxExtraInfo is the largest info payload that fits one 0x40D frame.
	MaxExtraInfo = MaxFrameSize - extraOverhead
)

// LoginIdentity is the identity delivered to the game after a successful
// login. Every field is secret: no formatting verb prints any of them.
type LoginIdentity struct {
	// GameUID and GameToken come from the sdkValidate response.
	GameUID   []byte
	GameToken []byte
	// ChannelUID and ChannelToken come from the OneLogin response.
	ChannelUID   []byte
	ChannelToken []byte
	// Info is the compact UTF-8 JSON of OneLogin extraOne (may be empty).
	Info []byte
}

// Zero wipes every field in place.
func (id *LoginIdentity) Zero() {
	for _, b := range [][]byte{id.GameUID, id.GameToken, id.ChannelUID, id.ChannelToken, id.Info} {
		zero(b)
	}
}

func (id LoginIdentity) safeString() string {
	return fmt.Sprintf("LoginIdentity{redacted fields:%d info:%dB}", 4, len(id.Info))
}

// String, GoString and Format keep every verb redacted, for value and pointer
// operands.
func (id LoginIdentity) String() string   { return id.safeString() }
func (id LoginIdentity) GoString() string { return id.safeString() }

func (id LoginIdentity) Format(f fmt.State, verb rune) {
	if verb == 'q' {
		fmt.Fprintf(f, "%q", id.safeString())
		return
	}
	io.WriteString(f, id.safeString())
}

// BuildLoginPackets encodes the op 0x40D extended info packet and the op 2
// login result for the host whose process id is hostPID, both XOR-obfuscated
// after the header. They must be delivered in that order. Field errors name
// the field, never its value.
func BuildLoginPackets(id LoginIdentity, hostPID uint32) (extra, login []byte, err error) {
	if hostPID == 0 {
		return nil, nil, fmt.Errorf("%w: host process id", ErrValue)
	}
	fields := []struct {
		name  string
		value []byte
		size  int
	}{
		{"game uid", id.GameUID, loginUIDBytes},
		{"game token", id.GameToken, loginTokenBytes},
		{"channel uid", id.ChannelUID, loginUIDBytes},
		{"channel token", id.ChannelToken, loginTokenBytes},
	}
	for _, f := range fields {
		if len(f.value) == 0 || len(f.value) >= f.size || bytes.IndexByte(f.value, 0) >= 0 {
			return nil, nil, fmt.Errorf("%w: %s", ErrValue, f.name)
		}
	}
	if len(id.Info) > MaxExtraInfo || bytes.IndexByte(id.Info, 0) >= 0 {
		return nil, nil, fmt.Errorf("%w: login info", ErrValue)
	}

	extra = make([]byte, len(id.Info)+extraOverhead)
	putHeader(extra, OpExtraInfo)
	binary.LittleEndian.PutUint32(extra[extraLengthOffset:], uint32(len(id.Info)))
	copy(extra[extraInfoOffset:], id.Info)
	obfuscate(extra, hostPID)

	login = make([]byte, LoginResultSize)
	putHeader(login, OpLoginResult)
	copy(login[loginGameUIDOffset:], id.GameUID)
	copy(login[loginGameTokenOffset:], id.GameToken)
	binary.LittleEndian.PutUint32(login[loginPlatformOffset:], Platform)
	binary.LittleEndian.PutUint32(login[loginChannelOffset:], ChannelID)
	copy(login[loginChannelUIDOffset:], id.ChannelUID)
	copy(login[loginChannelTokenOffset:], id.ChannelToken)
	// The legacy info field is a strcpy_s target: an info that does not fit
	// with its terminator leaves it empty.
	if len(id.Info) < loginInfoBytes {
		copy(login[loginInfoOffset:], id.Info)
	}
	obfuscate(login, hostPID)
	return extra, login, nil
}

// op 49 layout (absolute frame offsets, spec §2.7).
const (
	replyInnerOffset   = 12
	replyTypeOffset    = 16
	replyCodeOffset    = 20
	replyContextOffset = 24
	replyMessageOffset = 28
	replyMessageBytes  = 1024
	replyBodyOffset    = 0x41C
	replyTrailer       = 6
	replyOverhead      = replyBodyOffset + replyTrailer // 0x422
	replyInnerOverhead = 0x411
	maxReplyMessage    = replyMessageBytes/2 - 1 // 511 UTF-16 units
	// MaxPreLoginBody is the largest preLogin response body one op 49 frame
	// can carry.
	MaxPreLoginBody = MaxFrameSize - replyOverhead
)

// PreLoginReply is the op 49 answer to a QueryPreLogin query.
type PreLoginReply struct {
	// Code is the server's response code.
	Code int32
	// ContextID echoes the query's context id.
	ContextID int32
	// Message is the server's message (≤ 511 UTF-16 units, may be empty).
	Message string
	// Body is the raw preLogin HTTP response body, forwarded untouched.
	Body []byte
}

// BuildPreLoginReply encodes an op 49 frame for the host whose process id is
// hostPID, XOR-obfuscated after the header.
func BuildPreLoginReply(r PreLoginReply, hostPID uint32) ([]byte, error) {
	if hostPID == 0 {
		return nil, fmt.Errorf("%w: host process id", ErrValue)
	}
	if len(r.Body) == 0 || len(r.Body) > MaxPreLoginBody || bytes.IndexByte(r.Body, 0) >= 0 {
		return nil, fmt.Errorf("%w: preLogin body", ErrValue)
	}
	msg, err := replyMessage(r.Message)
	if err != nil {
		return nil, err
	}
	if len(r.Body)+replyOverhead > math.MaxInt32 {
		return nil, fmt.Errorf("%w: preLogin body", ErrValue)
	}
	frame := make([]byte, len(r.Body)+replyOverhead)
	putHeader(frame, OpQueryReply)
	binary.LittleEndian.PutUint32(frame[replyInnerOffset:], uint32(len(r.Body)+replyInnerOverhead))
	binary.LittleEndian.PutUint32(frame[replyTypeOffset:], QueryPreLogin)
	binary.LittleEndian.PutUint32(frame[replyCodeOffset:], uint32(r.Code))
	binary.LittleEndian.PutUint32(frame[replyContextOffset:], uint32(r.ContextID))
	putUnits(frame, replyMessageOffset, msg)
	copy(frame[replyBodyOffset:], r.Body)
	obfuscate(frame, hostPID)
	return frame, nil
}

// replyMessage encodes a valid UTF-8 message without NUL into at most
// maxReplyMessage UTF-16 units.
func replyMessage(s string) ([]uint16, error) {
	if !utf8.ValidString(s) || bytes.IndexByte([]byte(s), 0) >= 0 {
		return nil, fmt.Errorf("%w: preLogin message", ErrValue)
	}
	units := utf16.Encode([]rune(s))
	if len(units) > maxReplyMessage {
		return nil, fmt.Errorf("%w: preLogin message", ErrValue)
	}
	return units, nil
}
