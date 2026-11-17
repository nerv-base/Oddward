// SPDX-License-Identifier: GPL-3.0-or-later
package loginui

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func readWindowEvent(raw string) error {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(raw)))
	b.WriteString(raw)
	_, err := readFrame(&b)
	return err
}

func TestOverlayBindingEventIsStrictAndLossless(t *testing.T) {
	for _, kind := range []string{"bind_overlay", "embed_overlay"} {
		t.Run(kind, func(t *testing.T) { testOverlayBindingEvent(t, kind) })
	}
}

func testOverlayBindingEvent(t *testing.T, kind string) {
	if err := readWindowEvent(`{"version":1,"type":"event","event":{"kind":"` + kind + `","window":"9007199254740993","gameWindow":"42","requestId":"1"}}`); err != nil {
		t.Fatalf("valid native window identity rejected: %v", err)
	}
	for _, fields := range []string{
		`"window":"0","gameWindow":"42"`,
		`"window":"23"`,
		`"window":"23","gameWindow":""`,
		`"window":"+23","gameWindow":"42"`,
		`"window":"023","gameWindow":"42"`,
		`"window":"18446744073709551616","gameWindow":"42"`,
		`"window":23,"gameWindow":"42"`,
		`"window":"23","gameWindow":"42","phone":"synthetic-secret"`,
		`"window":"23","gameWindow":"42","remember":true`,
	} {
		if err := readWindowEvent(`{"version":1,"type":"event","event":{"kind":"` + kind + `","requestId":"1",` + fields + `}}`); !errors.Is(err, ErrProtocol) {
			t.Fatalf("invalid binding accepted: %s", fields)
		}
	}
	for _, kind := range []string{"ready", "cancel", "send_sms", "login_phone", "acknowledge"} {
		if err := readWindowEvent(`{"version":1,"type":"event","event":{"kind":"` + kind + `","window":"23","gameWindow":"42"}}`); !errors.Is(err, ErrProtocol) {
			t.Fatalf("window fields accepted on %s", kind)
		}
	}
}
