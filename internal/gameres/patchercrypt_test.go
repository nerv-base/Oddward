// SPDX-License-Identifier: GPL-3.0-or-later

package gameres

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"
)

// Independent .NET zlib/AES-CBC encodings of synthetic empty manifest roots.
const (
	resListVectorHex  = "50617463686572584d4c30000a000000cd692d187e65794e1cb2cf0fbc741407ae8da71659f9545fba4a14c6f409fa82"
	lastDiffVectorHex = "50617463686572584d4c30000c0000002a8c33c498acd1a36a799f3cd4c7d9d2cdd00901bca1860f954be6e9a7508fc0"
)

func containerVector(t *testing.T, encoded string) []byte {
	t.Helper()
	data, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestOpenContainerDecodesKnownVector(t *testing.T) {
	data := containerVector(t, resListVectorHex)
	before := bytes.Clone(data)
	got, err := OpenContainer(data)
	if err != nil || string(got) != "<ResList/>" {
		t.Fatalf("OpenContainer() = %q, %v; want synthetic ResList XML", got, err)
	}
	if !bytes.Equal(data, before) {
		t.Fatal("OpenContainer mutated its input")
	}
}

func TestSealContainerRoundTripsAndValidatesBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		xml  string
	}{
		{"small", "<ResList/>"},
		{"manifest", `<?xml version="1.0" encoding="utf-8" ?><ResList version="1.5.13" tag="baseTag"><Res filename="Client/WindowsNoEditor/HT/Binaries/Win64/HTGame.exe" md5="e36acf2ebb03eedfebc778f2f7ad4f38" timestamp="1795777328" filesize="282326624" /></ResList>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sealed, err := SealContainer([]byte(tc.xml))
			if err != nil {
				t.Fatalf("SealContainer() err = %v", err)
			}
			if !bytes.HasPrefix(sealed, []byte(patcherMagic)) {
				t.Fatalf("missing PatcherXML0 magic: %x", sealed[:min(len(sealed), 16)])
			}
			if gotLen := binary.LittleEndian.Uint32(sealed[12:16]); int(gotLen) != len(tc.xml) {
				t.Fatalf("declared length = %d, want %d", gotLen, len(tc.xml))
			}
			if (len(sealed)-patcherHeaderSize)%16 != 0 {
				t.Fatalf("ciphertext not block aligned: %d", len(sealed)-patcherHeaderSize)
			}
			opened, err := OpenContainer(sealed)
			if err != nil {
				t.Fatalf("OpenContainer(SealContainer()) err = %v", err)
			}
			if string(opened) != tc.xml {
				t.Fatalf("round-trip mismatch:\ngot:  %q\nwant: %q", opened, tc.xml)
			}
		})
	}
	if _, err := SealContainer(nil); !errors.Is(err, ErrContainer) {
		t.Fatalf("SealContainer(nil) = %v, want %v", err, ErrContainer)
	}
	if _, err := SealContainer(make([]byte, maxXMLBytes+1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("SealContainer(oversized) = %v, want %v", err, ErrTooLarge)
	}
}

func TestOpenContainerRejectsMalformedOrOversizedData(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func([]byte) []byte
		want   error
	}{
		{"magic", func(data []byte) []byte { data[0] ^= 1; return data }, ErrContainer},
		{"magic terminator", func(data []byte) []byte { data[11] = 1; return data }, ErrContainer},
		{"short header", func(data []byte) []byte { return data[:15] }, ErrContainer},
		{"empty ciphertext", func(data []byte) []byte { return data[:16] }, ErrContainer},
		{"truncated ciphertext", func(data []byte) []byte { return data[:len(data)-1] }, ErrContainer},
		{"padding", func(data []byte) []byte { data[31] ^= 1; return data }, ErrContainer},
		{"zlib", func(data []byte) []byte { data[16] ^= 1; return data }, ErrContainer},
		{"zero declared size", func(data []byte) []byte { binary.LittleEndian.PutUint32(data[12:], 0); return data }, ErrContainer},
		{"short declared size", func(data []byte) []byte { data[12]--; return data }, ErrContainer},
		{"long declared size", func(data []byte) []byte { data[12]++; return data }, ErrContainer},
		{"oversized declared length", func(data []byte) []byte { binary.LittleEndian.PutUint32(data[12:], maxXMLBytes+1); return data }, ErrTooLarge},
		{"oversized input", func(data []byte) []byte { return append(data, make([]byte, maxXMLBytes+1<<20)...) }, ErrTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := OpenContainer(tc.modify(containerVector(t, resListVectorHex)))
			if got != nil || !errors.Is(err, tc.want) {
				t.Fatalf("OpenContainer() returned plaintext=%t, error=%v; want %v", got != nil, err, tc.want)
			}
		})
	}
}

func TestVerifyManifestHashUsesPlaintextBytes(t *testing.T) {
	const want = "3892c4b3d09d60da3456f12ad2fcf0de"
	if err := VerifyManifestHash([]byte("<ResList/>"), want); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		data []byte
		hash string
	}{
		{[]byte("<ResList/>\n"), want},
		{containerVector(t, resListVectorHex), want},
		{[]byte("<ResList/>"), ""},
		{[]byte("<ResList/>"), "3892C4B3D09D60DA3456F12AD2FCF0DE"},
	} {
		if err := VerifyManifestHash(tc.data, tc.hash); !errors.Is(err, ErrManifestHash) {
			t.Fatalf("VerifyManifestHash() = %v; want %v", err, ErrManifestHash)
		}
	}
	if err := VerifyManifestHash(make([]byte, maxXMLBytes+1), want); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("VerifyManifestHash() = %v; want %v", err, ErrTooLarge)
	}
}

func TestOpenManifestZIPDecodesExpectedMembers(t *testing.T) {
	res := zipFixtureMember{"ResList.bin", containerVector(t, resListVectorHex)}
	diff := zipFixtureMember{"lastdiff.bin", containerVector(t, lastDiffVectorHex)}
	for _, tc := range []struct {
		name    string
		members []zipFixtureMember
		diff    string
	}{
		{"both", []zipFixtureMember{diff, res}, "<PatchList/>"},
		{"no diff", []zipFixtureMember{res}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := OpenManifestZIP(manifestZIPFixture(t, tc.members...))
			if err != nil || string(got.ResList) != "<ResList/>" || string(got.LastDiff) != tc.diff {
				t.Fatalf("OpenManifestZIP() = (%q, %q), %v", got.ResList, got.LastDiff, err)
			}
		})
	}
}

func TestOpenManifestZIPRejectsMalformedOrOversizedData(t *testing.T) {
	res := zipFixtureMember{"ResList.bin", containerVector(t, resListVectorHex)}
	diff := zipFixtureMember{"lastdiff.bin", containerVector(t, lastDiffVectorHex)}
	for _, tc := range []struct {
		name    string
		members []zipFixtureMember
		want    error
	}{
		{"missing list", []zipFixtureMember{diff}, ErrContainer},
		{"duplicate list", []zipFixtureMember{res, res}, ErrContainer},
		{"duplicate diff", []zipFixtureMember{res, diff, diff}, ErrContainer},
		{"path alias", []zipFixtureMember{{"nested/ResList.bin", res.data}}, ErrContainer},
		{"path traversal", []zipFixtureMember{res, {"../ResList.bin", res.data}}, ErrContainer},
		{"unexpected member", []zipFixtureMember{res, {"other.txt", res.data}}, ErrContainer},
		{"unencrypted list", []zipFixtureMember{{"ResList.bin", []byte("<ResList/>")}}, ErrContainer},
		{"malformed diff", []zipFixtureMember{res, {"lastdiff.bin", []byte("<PatchList/>")}}, ErrContainer},
		{"oversized member", []zipFixtureMember{{"ResList.bin", make([]byte, maxXMLBytes+1<<20)}}, ErrTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := OpenManifestZIP(manifestZIPFixture(t, tc.members...))
			if got.ResList != nil || got.LastDiff != nil || !errors.Is(err, tc.want) {
				t.Fatalf("OpenManifestZIP() returned list=%t, diff=%t, error=%v; want %v", got.ResList != nil, got.LastDiff != nil, err, tc.want)
			}
		})
	}
	for name, data := range map[string][]byte{
		"invalid ZIP": []byte("not a zip"),
		"bad archive CRC": func() []byte {
			data := manifestZIPFixture(t, res)
			i := bytes.Index(data, []byte("PK\x01\x02"))
			data[i+16] ^= 1
			return data
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := OpenManifestZIP(data); got.ResList != nil || got.LastDiff != nil || !errors.Is(err, ErrContainer) {
				t.Fatalf("OpenManifestZIP() = %#v, %v; want %v", got, err, ErrContainer)
			}
		})
	}
}

type zipFixtureMember struct {
	name string
	data []byte
}

func manifestZIPFixture(t *testing.T, members ...zipFixtureMember) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, member := range members {
		entry, err := w.Create(member.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(member.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
