// SPDX-License-Identifier: GPL-3.0-or-later

package gameres

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

const sampleHash = "0123456789abcdef0123456789abcdef"

func manifestXML(contents string) []byte {
	return []byte(`<ResList version="1.2.3" tag="baseTag">` + contents + `</ResList>`)
}

func resXML(path string, size int64, inner string) string {
	return fmt.Sprintf(`<Res filename="%s" filesize="%d" md5="%s">%s</Res>`, path, size, sampleHash, inner)
}

func packXML(entries string) string {
	return `<Package><Pak md5="fedcba9876543210fedcba9876543210" filesize="900000000">` + entries + `</Pak></Package>`
}

func entryXML(path string, offset, size int64) string {
	return fmt.Sprintf(`<Entry name="%s" offset="%d" size="%d" md5="%s"/>`, path, offset, size, sampleHash)
}

func TestParseDeliveryAndInstalledManifestShapes(t *testing.T) {
	base := resXML("Client/File.bin", 5, "")
	delivery := packXML(entryXML("Client/Packed.bin", 4, 3)) +
		`<BaseVersion version="0.1.3" tag="bonus"><ResList>` + resXML("Client/File.bin", 10, "") +
		packXML(entryXML("Client/Bonus.bin", 15, 4)) + `</ResList></BaseVersion>`
	m, err := ParseResList(manifestXML(base + delivery))
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "1.2.3" || m.Tag != "baseTag" || len(m.Files) != 1 || len(m.Packages) != 1 || len(m.Tags) != 1 {
		t.Fatalf("unexpected manifest shape: %+v", m)
	}
	if entry := m.Packages[0].Entries[0]; entry.Path != "Client/Packed.bin" || entry.Offset != 4 || entry.Size != 3 || entry.Pak.Size != 900000000 {
		t.Fatalf("unexpected package entry: %+v", entry)
	}
	if tag := m.Tags[0]; tag.Version != "0.1.3" || tag.Files[0].Size != 10 || tag.Packages[0].Entries[0].Path != "Client/Bonus.bin" {
		t.Fatalf("unexpected tag: %+v", tag)
	}
	installed, err := ParseResList(manifestXML(base + resXML("Client/Packed.bin", 3, "") +
		`<BaseVersion version="0.1.3"><ResList>` + resXML("Client/Bonus.bin", 4, "") + `</ResList></BaseVersion>`))
	if err != nil || len(installed.Files) != 2 || len(installed.Packages) != 0 || len(installed.Tags[0].Files) != 1 {
		t.Fatalf("installed manifest = %+v, %v", installed, err)
	}
}

func TestParseManifestReconstructsOffsetsFromBlockIndex(t *testing.T) {
	blocks := `<Block index="1" start="0" size="2" md5="` + sampleHash + `"/>` +
		`<Block index="0" start="536870912" size="3" md5="fedcba9876543210fedcba9876543210"/>`
	m, err := ParseResList(manifestXML(resXML(`Client\Content\File.pak`, 5, blocks)))
	if err != nil {
		t.Fatal(err)
	}
	f := m.Files[0]
	if f.Path != "Client/Content/File.pak" || len(f.Blocks) != 2 || f.Blocks[0].Start != 0 || f.Blocks[0].Size != 3 || f.Blocks[1].Start != 3 || f.Blocks[1].Size != 2 {
		t.Fatalf("unexpected resource file: %+v", f)
	}
}

func TestParseManifestRejectsUnsafePathsAndAliases(t *testing.T) {
	for _, path := range []string{`/root.bin`, `C:/root.bin`, `\\host\dir\root.bin`, `Client/../a`, `Client/./a`, `Client//a`,
		`Client/root.bin:stream`, `Client/NUL.txt`, `Client/COM¹`, `Client/aux .txt`, `Client/a.`, `Client/a `, `.oddward/a`,
		`Client/x&#x7F;y`, `Client/a?b`, `Client/` + strings.Repeat("x", 240), `Client/` + strings.Repeat("𐐀", 117)} {
		t.Run(path, func(t *testing.T) {
			_, err := ParseResList(manifestXML(resXML(path, 5, "")))
			if !errors.Is(err, ErrPathUnsafe) {
				t.Fatalf("error = %v, want ErrPathUnsafe", err)
			}
		})
	}
	for _, body := range []string{
		resXML("Client/One", 5, "") + resXML(`client\one`, 5, ""),
		resXML("Client/σ", 5, "") + resXML("Client/ς", 5, ""),
		resXML("Client/σ", 5, "") + resXML("Client/ς/child", 5, ""),
		resXML("Client/One", 5, "") + packXML(entryXML("client/one/child", 1, 1)),
		resXML("Client/One", 5, "") + packXML(entryXML("client/one", 1, 4)),
		packXML(entryXML("Client/One", 0, 4) + entryXML("client/one", 9, 4)),
	} {
		if _, err := ParseResList(manifestXML(body)); !errors.Is(err, ErrMalformedManifest) {
			t.Fatalf("alias error = %v", err)
		}
	}
}

func TestParseManifestKeepsOnlyResForIdenticalDualSource(t *testing.T) {
	m, err := ParseResList(manifestXML(resXML("Client/One", 5, "") + packXML(entryXML("client/one", 1, 5))))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 1 || len(m.Packages) != 0 || len(m.Warnings) != 1 || !strings.Contains(m.Warnings[0], "DualSource") {
		t.Fatalf("unexpected dual source projection: %+v", m)
	}
}

func TestParseManifestRejectsInvalidRangesAndIdentities(t *testing.T) {
	for _, body := range []string{
		resXML("Client/a", 0, ""), resXML("Client/a", 32<<30+1, ""),
		`<Res filename="Client/a" filesize="9223372036854775808" md5="` + sampleHash + `"/>`,
		strings.ReplaceAll(resXML("Client/a", 9, ""), sampleHash, strings.ToUpper(sampleHash)),
		packXML(entryXML("Client/a", 9223372036854775807, 3)),
		packXML(entryXML("Client/a", 899999999, 2)),
		packXML(`<Entry name="Client/a" size="2" md5="` + sampleHash + `"/>`),
		resXML("Client/a", 5, `<Block index="1" start="0" size="5" md5="`+sampleHash+`"/>`),
		resXML("Client/a", 5, `<Block index="0" start="0" size="4" md5="`+sampleHash+`"/>`),
	} {
		if _, err := ParseResList(manifestXML(body)); !errors.Is(err, ErrMalformedManifest) {
			t.Fatalf("invalid range/identity error = %v for %s", err, body)
		}
	}
}

func TestParseManifestRejectsUntrustedXML(t *testing.T) {
	for _, raw := range []string{
		`<!DOCTYPE ResList [<!ENTITY x SYSTEM "file:///C:/a">]>` + string(manifestXML(resXML("Client/a", 1, ""))),
		string(manifestXML(resXML("Client/a", 1, ""))) + `<Trailing/>`,
		`binary` + string(manifestXML(resXML("Client/a", 1, ""))),
		`<ResList version="1" version="2" tag="baseTag"/>`,
	} {
		if _, err := ParseResList([]byte(raw)); !errors.Is(err, ErrMalformedManifest) {
			t.Fatalf("untrusted XML error = %v", err)
		}
	}
	var noisy strings.Builder
	noisy.WriteString(`<ResList version="1" tag="baseTag"`)
	for i := range 128 {
		fmt.Fprintf(&noisy, ` x%d="v"`, i)
	}
	noisy.WriteString(">" + resXML("Client/a", 1, "") + "</ResList>")
	if _, err := ParseResList([]byte(noisy.String())); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("excess attributes error = %v", err)
	}
	if _, err := ParseResList([]byte(strings.Repeat(" ", (8<<20)+1))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized XML error = %v", err)
	}
}

func TestSyntheticManifestMatchesBaseTagByteAndFileCounts(t *testing.T) {
	var files, entries strings.Builder
	for i := range 23 {
		size := int64(3_600_000_000)
		if i == 22 {
			size = 5_295_113_229
		}
		fmt.Fprint(&files, resXML(fmt.Sprintf("Client/Base%02d.bin", i), size, ""))
	}
	for i := range 311 {
		fmt.Fprint(&entries, entryXML(fmt.Sprintf("Client/Entry%03d.bin", i), int64(i)*3, 3))
	}
	m, err := ParseResList(manifestXML(files.String() + packXML(entries.String()) +
		`<BaseVersion version="0.1.0"><ResList>` + resXML("Client/Bonus", 7, "") + `</ResList></BaseVersion>`))
	if err != nil {
		t.Fatal(err)
	}
	count, sum := len(m.Files), int64(0)
	for _, f := range m.Files {
		sum += f.Size
	}
	for _, p := range m.Packages {
		for _, e := range p.Entries {
			count++
			sum += e.Size
		}
	}
	if count != 334 || sum != 84_495_114_162 {
		t.Fatalf("baseTag has %d targets and %d bytes", count, sum)
	}
}
