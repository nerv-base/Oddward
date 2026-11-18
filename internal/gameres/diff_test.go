// SPDX-License-Identifier: GPL-3.0-or-later

package gameres

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

const (
	diffOldMD5   = "11111111111111111111111111111111"
	diffNewMD5   = "22222222222222222222222222222222"
	diffBlobMD5  = "33333333333333333333333333333333"
	diffPatchXML = `<Patch oldfile="` + diffOldMD5 + `.9" newfile="` + diffNewMD5 + `.10" patch="` + diffBlobMD5 + `.5" v="aabbcc"/>`
)

func TestParseDiffListRejectsTooManyEdges(t *testing.T) {
	data := []byte(`<PatchList>` + strings.Repeat(diffPatchXML, 4097) + `</PatchList>`)
	if graph, err := ParseDiffList(data); graph != nil || !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized graph returned an error of %v, want ErrTooLarge", err)
	}
}

func TestParseDiffListRejectsInvalidPatches(t *testing.T) {
	without := func(attr string) string {
		start := strings.Index(diffPatchXML, ` `+attr+`="`)
		end := start + len(attr) + 3
		end += strings.IndexByte(diffPatchXML[end:], '"') + 1
		return diffPatchXML[:start] + diffPatchXML[end:]
	}
	for _, tt := range []struct{ name, xml string }{
		{"wrong root", `<Wrong>` + diffPatchXML + `</Wrong>`},
		{"trailing root", `<PatchList/> <PatchList/>`},
		{"DTD", `<!DOCTYPE PatchList><PatchList/>`},
		{"missing oldfile", `<PatchList>` + without("oldfile") + `</PatchList>`},
		{"missing newfile", `<PatchList>` + without("newfile") + `</PatchList>`},
		{"missing blob", `<PatchList>` + without("patch") + `</PatchList>`},
		{"missing version", `<PatchList>` + without("v") + `</PatchList>`},
		{"missing section identity", `<PatchList><Section>` + diffPatchXML + `</Section></PatchList>`},
		{"bad MD5", `<PatchList>` + strings.Replace(diffPatchXML, diffOldMD5, "ABCDEF1234567890ABCDEF1234567890", 1) + `</PatchList>`},
		{"nondigit size", `<PatchList>` + strings.Replace(diffPatchXML, `.9"`, `.+9"`, 1) + `</PatchList>`},
		{"zero size", `<PatchList>` + strings.Replace(diffPatchXML, `.9"`, `.0"`, 1) + `</PatchList>`},
		{"oversized object", `<PatchList>` + strings.Replace(diffPatchXML, `.9"`, `.34359738369"`, 1) + `</PatchList>`},
		{"overflowing size", `<PatchList><Section resversion="tag-a">` + strings.Replace(diffPatchXML, `.10"`, `.9223372036854775808"`, 1) + `</Section></PatchList>`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			graph, err := ParseDiffList([]byte(tt.xml))
			if graph != nil || !errors.Is(err, ErrMalformedManifest) {
				t.Fatalf("got (%v, %v), want (nil, ErrMalformedManifest)", graph, err)
			}
		})
	}
}

func TestParseDiffListValidatesBlobBlocks(t *testing.T) {
	withBlocks := func(blocks string) []byte {
		return []byte(`<PatchList>` + strings.TrimSuffix(diffPatchXML, `/>`) + ` ignored="future">` + blocks + `</Patch></PatchList>`)
	}
	const tail = `<Block index="1" start="0" size="2" md5="` + diffNewMD5 + `"/>`
	const head = `<Block index="0" start="2" size="3" md5="` + diffOldMD5 + `"/>`
	graph, err := ParseDiffList(withBlocks(tail + head))
	if err != nil {
		t.Fatal(err)
	}
	want := []Block{{Index: 0, Start: 0, Size: 3, MD5: diffOldMD5}, {Index: 1, Start: 3, Size: 2, MD5: diffNewMD5}}
	if got := graph.Edges[0].Blocks; !slices.Equal(got, want) {
		t.Errorf("blocks = %+v, want %+v", got, want)
	}
	for _, tt := range []struct{ name, blocks string }{
		{"duplicate index", head + head},
		{"missing start", strings.Replace(head, ` start="2"`, "", 1) + tail},
		{"wrong coverage", head},
		{"bad block hash", strings.Replace(head, diffOldMD5, "missing", 1) + tail},
	} {
		t.Run(tt.name, func(t *testing.T) {
			graph, err := ParseDiffList(withBlocks(tt.blocks))
			if graph != nil || !errors.Is(err, ErrMalformedManifest) {
				t.Fatalf("got (%v, %v), want (nil, ErrMalformedManifest)", graph, err)
			}
		})
	}
}

func TestParseDiffListKeepsSectionIdentityInDocumentOrder(t *testing.T) {
	data := []byte(`<PatchList>` + diffPatchXML + `<Section resversion="tag-a">` + diffPatchXML + `</Section>` + diffPatchXML + `</PatchList>`)
	graph, err := ParseDiffList(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Edges) != 3 {
		t.Fatalf("patch count = %d, want 3", len(graph.Edges))
	}
	for i, section := range []string{"", "tag-a", ""} {
		edge := graph.Edges[i]
		if edge.Section != section || edge.Version != "aabbcc" {
			t.Errorf("patch %d = %+v, want Section %q and Version aabbcc", i, edge, section)
		}
		if edge.Old != (Object{MD5: diffOldMD5, Size: 9}) ||
			edge.New != (Object{MD5: diffNewMD5, Size: 10}) ||
			edge.Blob != (Object{MD5: diffBlobMD5, Size: 5}) {
			t.Errorf("patch %d has wrong old, new, or blob identity: %+v", i, edge)
		}
	}
}
