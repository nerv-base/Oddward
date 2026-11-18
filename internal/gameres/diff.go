// SPDX-License-Identifier: GPL-3.0-or-later

package gameres

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// PatchEntry maps an old file identity to a new identity using a patch blob.
type PatchEntry struct {
	Old, New Object
	Blob     Object
	Version  string  // Hash of the manifest that owns New.
	Section  string  // Section resversion, empty for a top-level baseTag patch.
	Blocks   []Block // Layout metadata only. Whole-blob verification is still required.
}

type xmlDiffPatch struct {
	Old     string     `xml:"oldfile,attr"`
	New     string     `xml:"newfile,attr"`
	Blob    string     `xml:"patch,attr"`
	Version string     `xml:"v,attr"`
	Blocks  []xmlBlock `xml:"Block"`
}

type xmlDiffChild struct {
	XMLName xml.Name
	xmlDiffPatch
	Section string         `xml:"resversion,attr"`
	Patches []xmlDiffPatch `xml:"Patch"`
}

type xmlDiffList struct {
	XMLName  xml.Name       `xml:"PatchList"`
	Children []xmlDiffChild `xml:",any"`
}

// ParseDiffList parses a decoded lastdiff manifest and keeps both baseTag and
// Section edges in document order.
func ParseDiffList(data []byte) (*PatchGraph, error) {
	var raw xmlDiffList
	if err := decodeXML(data, "PatchList", &raw); err != nil {
		return nil, err
	}
	graph := &PatchGraph{}
	for _, child := range raw.Children {
		var patches []xmlDiffPatch
		var section string
		switch child.XMLName.Local {
		case "Patch":
			patches = []xmlDiffPatch{child.xmlDiffPatch}
		case "Section":
			section = child.Section
			if strings.TrimSpace(section) == "" {
				return nil, fmt.Errorf("%w: missing patch section version", ErrMalformedManifest)
			}
			patches = child.Patches
		}
		if len(patches) > maxPatchEdges-len(graph.Edges) {
			return nil, ErrTooLarge
		}
		for _, patch := range patches {
			edge, err := patch.decode(section)
			if err != nil {
				return nil, fmt.Errorf("patch %d: %w", len(graph.Edges), err)
			}
			graph.Edges = append(graph.Edges, edge)
		}
	}
	graph.byOld = indexPatchEdges(graph.Edges)
	return graph, nil
}

func (p xmlDiffPatch) decode(section string) (PatchEntry, error) {
	edge := PatchEntry{
		Old: diffObject(p.Old), New: diffObject(p.New), Blob: diffObject(p.Blob),
		Version: p.Version, Section: section,
	}
	if !validObject(edge.Old, maxFileBytes) || !validObject(edge.New, maxFileBytes) ||
		!validObject(edge.Blob, maxFileBytes) || strings.TrimSpace(edge.Version) == "" {
		return PatchEntry{}, fmt.Errorf("%w: invalid patch identity or version", ErrMalformedManifest)
	}
	blocks, err := decodeBlocks(p.Blocks, edge.Blob.Size)
	if err != nil {
		return PatchEntry{}, err
	}
	edge.Blocks = blocks
	return edge, nil
}

func diffObject(s string) Object {
	md5, size, _ := strings.Cut(s, ".")
	return Object{MD5: md5, Size: decimal(size)}
}
