// SPDX-License-Identifier: GPL-3.0-or-later

package selfupdate

import (
	"bytes"
	"crypto/ed25519"
	"strings"
	"testing"
)

func TestSignedManifestRoundTrips(t *testing.T) {
	m, data, sig, _ := release(t, "0.2.0", appFiles("0.2.0"))
	pub, _ := testKey()
	got, err := Verify(data, sig, pub, testRepo)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != m.Version || got.Package != m.Package || len(got.Files) != 2 || got.Notes != m.Notes {
		t.Fatalf("got %+v", got)
	}
}

func TestVerifyRefusesAnythingNotSignedForThisRepository(t *testing.T) {
	m, data, sig, _ := release(t, "0.2.0", appFiles("0.2.0"))
	pub, priv := testKey()
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32))
	resign := func(change func(*Manifest)) ([]byte, []byte) {
		c := m
		c.Files = append([]FileEntry(nil), m.Files...)
		change(&c)
		d, s, err := Sign(priv, c)
		if err != nil {
			// Sign refuses invalid manifests; sign raw bytes instead to prove Verify refuses them too.
			d = []byte(strings.Replace(string(data), `"0.2.0"`, `"`+c.Version+`"`, 1))
			s = signRaw(priv, d)
		}
		return d, s
	}
	tampered := bytes.Replace(data, []byte("修复"), []byte("植入"), 1)
	cases := map[string]struct{ data, sig []byte }{
		"no key":        {data, sig},
		"tampered":      {tampered, sig},
		"other key":     {data, signRaw(other, data)},
		"truncated sig": {data, sig[:10]},
		"empty":         {nil, nil},
		"raw ed25519":   {data, []byte(encodeSig(ed25519.Sign(priv, data)))},
		"other repo": func() struct{ data, sig []byte } {
			d, s := resign(func(c *Manifest) { c.Repository = "someone/else" })
			return struct{ data, sig []byte }{d, s}
		}(),
		"bad version": func() struct{ data, sig []byte } {
			d, s := resign(func(c *Manifest) { c.Version = "v0.2.0" })
			return struct{ data, sig []byte }{d, s}
		}(),
		"unknown field":  {[]byte(strings.Replace(string(data), `"schema": 1`, `"schema": 1, "exec": "calc.exe"`, 1)), nil},
		"trailing value": {append(append([]byte{}, data...), []byte(" {}")...), nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			key := pub
			if name == "no key" {
				key = nil
			}
			s := c.sig
			if s == nil && c.data != nil {
				s = signRaw(priv, c.data)
			}
			if _, err := Verify(c.data, s, key, testRepo); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestManifestRejectsUnsafeFileLists(t *testing.T) {
	_, priv := testKey()
	base, _, _, _ := release(t, "0.2.0", appFiles("0.2.0"))
	for name, change := range map[string]func(*Manifest){
		"parent path":    func(m *Manifest) { m.Files[1].Path = "../evil.dll" },
		"absolute":       func(m *Manifest) { m.Files[1].Path = "/evil.dll" },
		"backslash":      func(m *Manifest) { m.Files[1].Path = `licenses\x.txt` },
		"drive":          func(m *Manifest) { m.Files[1].Path = "C:/x.txt" },
		"stream":         func(m *Manifest) { m.Files[1].Path = "a.txt:ads" },
		"duplicate case": func(m *Manifest) { m.Files[1].Path = strings.ToUpper(AppExe) },
		"marker":         func(m *Manifest) { m.Files[1].Path = readyMarker },
		"no launcher":    func(m *Manifest) { m.Files = m.Files[1:] },
		"bad digest":     func(m *Manifest) { m.Files[0].SHA256 = "zz" },
		"negative size":  func(m *Manifest) { m.Files[0].Size = -1 },
		"package path":   func(m *Manifest) { m.Package.Name = "../x.zip" },
		"package ext":    func(m *Manifest) { m.Package.Name = "x.exe" },
		"schema":         func(m *Manifest) { m.Schema = 2 },
		"huge notes":     func(m *Manifest) { m.Notes = strings.Repeat("a", maxNotes+1) },
	} {
		t.Run(name, func(t *testing.T) {
			m := base
			m.Files = append([]FileEntry(nil), base.Files...)
			change(&m)
			if _, _, err := Sign(priv, m); err == nil {
				t.Fatal("signed an unsafe manifest")
			}
		})
	}
}
