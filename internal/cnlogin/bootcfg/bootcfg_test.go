// SPDX-License-Identifier: GPL-3.0-or-later

package bootcfg

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/nerv-base/Oddward/internal/cnlogin/ecb"
)

var fixtureKey = [16]byte{'0', '1', '2', '3', '4', '5', '6', '7', '8', '9', 'A', 'B', 'C', 'D', 'E', 'F'}

func resourceFixture(t *testing.T, plain []byte, compressed bool) []byte {
	t.Helper()
	cipher, err := ecb.Encrypt(fixtureKey[:], plain)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(cipher)
	data := []byte(base64.StdEncoding.EncodeToString(cipher))
	if compressed {
		var b bytes.Buffer
		_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
		w := zlib.NewWriter(&b)
		_, _ = w.Write(data)
		_ = w.Close()
		data = b.Bytes()
	}
	var names bytes.Buffer
	offsets := make([]uint32, 4)
	for i, n := range []string{"", "1289", "main", "oneSdkConfig.config"} {
		offsets[i] = uint32(names.Len())
		u := utf16.Encode([]rune(n))
		_ = binary.Write(&names, binary.BigEndian, uint16(len(u)))
		_ = binary.Write(&names, binary.BigEndian, uint32(0))
		for _, c := range u {
			_ = binary.Write(&names, binary.BigEndian, c)
		}
	}
	tree := 24
	nameOff := tree + 4*22
	payloadOff := nameOff + names.Len()
	out := make([]byte, payloadOff+4+len(data))
	copy(out, "qres")
	for off, v := range map[int]uint32{4: 3, 8: uint32(tree), 12: uint32(payloadOff), 16: uint32(nameOff)} {
		binary.BigEndian.PutUint32(out[off:], v)
	}
	copy(out[nameOff:], names.Bytes())
	binary.BigEndian.PutUint32(out[payloadOff:], uint32(len(data)))
	copy(out[payloadOff+4:], data)
	for i := range 4 {
		node := out[tree+i*22:]
		binary.BigEndian.PutUint32(node, offsets[i])
		if i < 3 {
			binary.BigEndian.PutUint16(node[4:], 2)
			binary.BigEndian.PutUint32(node[6:], 1)
			binary.BigEndian.PutUint32(node[10:], uint32(i+1))
		} else if compressed {
			binary.BigEndian.PutUint16(node[4:], 1)
		}
	}
	return out
}

func TestDecodeRCCAndClearOwnedJSON(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		plain := []byte(`{"meta":{"channelId":9,"platform":4},"init":{}}`)
		resource := resourceFixture(t, plain, compressed)
		before := bytes.Clone(resource)
		got, err := Decode(resource, fixtureKey)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatal("wrong decoded JSON")
		}
		clear(got)
		if !bytes.Equal(before, resource) {
			t.Fatal("mutated caller resource")
		}
	}
}
func TestRejectMalformedResources(t *testing.T) {
	cases := map[string]func([]byte) []byte{
		"magic":          func(b []byte) []byte { b[0] = 0; return b },
		"version":        func(b []byte) []byte { b[7] = 2; return b },
		"truncated":      func(b []byte) []byte { return b[:23] },
		"cycle":          func(b []byte) []byte { binary.BigEndian.PutUint32(b[24+10:], 0); return b },
		"child overflow": func(b []byte) []byte { binary.BigEndian.PutUint32(b[24+6:], 0xffffffff); return b },
		"bad file flags": func(b []byte) []byte { binary.BigEndian.PutUint16(b[24+3*22+4:], 4); return b },
		"bad payload":    func(b []byte) []byte { binary.BigEndian.PutUint32(b[24+3*22+10:], 0xffffffff); return b },
		"missing":        func(b []byte) []byte { b[24+4*22+6+6+1] = 'X'; return b },
		"name traversal": func(b []byte) []byte {
			o := 24 + 4*22 + 6
			binary.BigEndian.PutUint16(b[o:], 2)
			binary.BigEndian.PutUint16(b[o+6:], '.')
			binary.BigEndian.PutUint16(b[o+8:], '.')
			return b
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Decode(mutate(resourceFixture(t, []byte(`{}`), false)), fixtureKey)
			if err == nil || got != nil {
				t.Fatal("accepted malformed resource")
			}
		})
	}
	for _, plain := range []string{`[]`, `null`, `{"a":`, "\xff", `true`} {
		if got, err := Decode(resourceFixture(t, []byte(plain), false), fixtureKey); err == nil || got != nil {
			t.Fatal("accepted nonobject JSON")
		}
	}
	b := resourceFixture(t, []byte(`{}`), false)
	wrong := fixtureKey
	wrong[0] = 'X'
	if _, err := Decode(b, wrong); err == nil {
		t.Fatal("accepted wrong key")
	}
	if _, err := Decode(make([]byte, 64*1024*1024+1), fixtureKey); err == nil {
		t.Fatal("accepted huge resource")
	}
}
func TestDecodeNamedRoot(t *testing.T) {
	plain := []byte(`{"fixture":"named root"}`)
	for _, sharedName := range []string{"1289", "res"} {
		t.Run(sharedName, func(t *testing.T) {
			resource := resourceWithSiblingFixture(t, plain, "res", 0, []byte("fixture"))
			tree := int(binary.BigEndian.Uint32(resource[8:]))
			nameNode := 1
			if sharedName == "res" {
				nameNode = 4
			}
			// RCC's root name offset may share an ordinary name-table record.
			copy(resource[tree:tree+4], resource[tree+nameNode*22:tree+nameNode*22+4])
			got, err := Decode(resource, fixtureKey)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, plain) {
				t.Fatal("wrong decoded JSON")
			}
			clear(got)
		})
	}
}

func TestDecodeIgnoresUnrelatedResourceEncoding(t *testing.T) {
	plain := []byte(`{"fixture":"unrelated resource"}`)
	var compressed bytes.Buffer
	_ = binary.Write(&compressed, binary.BigEndian, uint32(70000))
	w := zlib.NewWriter(&compressed)
	_, _ = w.Write(bytes.Repeat([]byte("x"), 70000))
	_ = w.Close()
	for _, tc := range []struct {
		name  string
		flags uint16
		data  []byte
	}{
		{"large", 0, make([]byte, 65537)},
		{"empty", 0, nil},
		{"zlib with large expansion", 1, compressed.Bytes()},
		// Qt's zstd flag is irrelevant when this resource is not selected.
		{"zstd", 4, []byte{0x28, 0xb5, 0x2f, 0xfd, 0x20, 0, 1, 0, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource := resourceWithSiblingFixture(t, plain, "unrelated.bin", tc.flags, tc.data)
			got, err := Decode(resource, fixtureKey)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, plain) {
				t.Fatal("wrong decoded JSON")
			}
			clear(got)
		})
	}
}

func TestRejectInvalidSiblingResources(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]byte)
	}{
		{"payload outside section", func(b []byte) {
			tree := int(binary.BigEndian.Uint32(b[8:]))
			binary.BigEndian.PutUint32(b[tree+4*22+10:], 0xffffffff)
		}},
		{"length outside section", func(b []byte) {
			tree := int(binary.BigEndian.Uint32(b[8:]))
			payload := int(binary.BigEndian.Uint32(b[12:]))
			blob := payload + int(binary.BigEndian.Uint32(b[tree+4*22+10:]))
			binary.BigEndian.PutUint32(b[blob:], uint32(len(b)-blob))
		}},
		{"duplicate config path", func(b []byte) {
			tree := int(binary.BigEndian.Uint32(b[8:]))
			copy(b[tree+4*22:tree+4*22+4], b[tree+3*22:tree+3*22+4])
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource := resourceWithSiblingFixture(t, []byte(`{}`), "unrelated.bin", 0, []byte("fixture"))
			tc.mutate(resource)
			if got, err := Decode(resource, fixtureKey); err == nil || got != nil {
				t.Fatal("accepted invalid resource")
			}
		})
	}
}

func TestRejectTargetResourceBeyondLimit(t *testing.T) {
	if got, err := Decode(resourceFixture(t, []byte(`{"x":"`+strings.Repeat("x", 70000)+`"}`), false), fixtureKey); err == nil || got != nil {
		t.Fatal("accepted oversized target config")
	}
}

func TestRejectCompressedExpansionBeyondLimit(t *testing.T) {
	b := resourceFixture(t, []byte(`{"x":"`+strings.Repeat("x", 70000)+`"}`), true)
	if got, err := Decode(b, fixtureKey); err == nil || got != nil {
		t.Fatal("accepted expansion bomb")
	}
}
