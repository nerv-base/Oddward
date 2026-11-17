// SPDX-License-Identifier: GPL-3.0-or-later

package bootcfg

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

// resourceWithSiblingFixture adds a separate resource beside the target config.
// Every byte is synthetic, including the encrypted configuration from the base fixture.
func resourceWithSiblingFixture(t *testing.T, plain []byte, name string, flags uint16, data []byte) []byte {
	t.Helper()
	base := resourceFixture(t, plain, false)
	tree := int(binary.BigEndian.Uint32(base[8:]))
	names := int(binary.BigEndian.Uint32(base[16:]))
	payload := int(binary.BigEndian.Uint32(base[12:]))
	units := utf16.Encode([]rune(name))
	nameRecord := make([]byte, 6+2*len(units))
	binary.BigEndian.PutUint16(nameRecord, uint16(len(units)))
	for i, unit := range units {
		binary.BigEndian.PutUint16(nameRecord[6+2*i:], unit)
	}
	newNames := names + 22
	newPayload := payload + 22 + len(nameRecord)
	out := make([]byte, len(base)+22+len(nameRecord)+4+len(data))
	copy(out, base[:names])
	binary.BigEndian.PutUint32(out[16:], uint32(newNames))
	binary.BigEndian.PutUint32(out[12:], uint32(newPayload))
	copy(out[newNames:], base[names:payload])
	copy(out[newNames+payload-names:], nameRecord)
	copy(out[newPayload:], base[payload:])
	binary.BigEndian.PutUint32(out[tree+2*22+6:], 2)
	sibling := out[tree+4*22:]
	binary.BigEndian.PutUint32(sibling, uint32(payload-names))
	binary.BigEndian.PutUint16(sibling[4:], flags)
	binary.BigEndian.PutUint32(sibling[10:], uint32(len(base)-payload))
	blob := newPayload + len(base) - payload
	binary.BigEndian.PutUint32(out[blob:], uint32(len(data)))
	copy(out[blob+4:], data)
	return out
}
