// SPDX-License-Identifier: GPL-3.0-or-later
package loginui

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func gameModelFrame(extra string) *bytes.Buffer {
	body := `{"version":1,"type":"model","model":{"screen":"phone"` + extra + `}}`
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(body)))
	b.WriteString(body)
	return &b
}

func TestGameIdentitySurvivesModelWire(t *testing.T) {
	// FILETIME is above JavaScript's exact integer range; keep decimal string bytes.
	const identity = `,"gameProcessId":42,"gameCreationTime":"134393594660123457"`
	frame, err := readFrame(gameModelFrame(identity))
	if err != nil {
		t.Fatalf("valid game identity rejected: %v", err)
	}
	var encoded bytes.Buffer
	if err := writeFrame(&encoded, frame); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded.Bytes(), []byte(`"gameProcessId":42`)) || !bytes.Contains(encoded.Bytes(), []byte(`"gameCreationTime":"134393594660123457"`)) {
		t.Fatal("game identity lost or creation time rounded during model round trip")
	}
}

func TestRejectPartialOrAmbiguousGameIdentity(t *testing.T) {
	for _, extra := range []string{
		`,"gameProcessId":42`,
		`,"gameCreationTime":"134393594660123457"`,
		`,"gameProcessId":42,"gameCreationTime":"0"`,
		`,"gameProcessId":42,"gameCreationTime":"01"`,
		`,"gameProcessId":42,"gameCreationTime":"+1"`,
		`,"gameProcessId":42,"gameCreationTime":" 1"`,
		`,"gameProcessId":42,"gameCreationTime":"18446744073709551616"`,
		`,"gameProcessId":42,"gameCreationTime":134393594660123457`,
		`,"gameProcessId":4294967296,"gameCreationTime":"1"`,
	} {
		if _, err := readFrame(gameModelFrame(extra)); !errors.Is(err, ErrProtocol) {
			t.Fatalf("invalid identity accepted: %s", extra)
		}
	}
	if _, err := readFrame(gameModelFrame("")); err != nil {
		t.Fatalf("legacy preview rejected: %v", err)
	}
}
