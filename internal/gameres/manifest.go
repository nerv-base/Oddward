// SPDX-License-Identifier: GPL-3.0-or-later

package gameres

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

type Block struct {
	Index       int
	Start, Size int64
	MD5         string
}

type File struct {
	Path   string
	MD5    string
	Size   int64
	Blocks []Block
}

type Package struct {
	Pak     Object
	Entries []Entry
}

type Entry struct {
	Path         string
	Pak          Object
	Offset, Size int64
	MD5          string
}

type TagGroup struct {
	Version  string
	Tag      string
	Files    []File
	Packages []Package
}

// ResList preserves tag groups separately from the baseTag targets. CheckHash
// comes from the matching config, since Patch.v is not a ResList attribute.
type ResList struct {
	Version   string
	Tag       string
	CheckHash string
	Files     []File
	Packages  []Package
	Tags      []TagGroup
	Warnings  []string
}

type xmlBlock struct {
	Index string `xml:"index,attr"`
	Start string `xml:"start,attr"`
	Size  string `xml:"size,attr"`
	MD5   string `xml:"md5,attr"`
}

type xmlFile struct {
	Path   string     `xml:"filename,attr"`
	MD5    string     `xml:"md5,attr"`
	Size   string     `xml:"filesize,attr"`
	Blocks []xmlBlock `xml:"Block"`
}

type xmlEntry struct {
	Path   string `xml:"name,attr"`
	MD5    string `xml:"md5,attr"`
	Size   string `xml:"size,attr"`
	Offset string `xml:"offset,attr"`
}

type xmlPak struct {
	MD5     string     `xml:"md5,attr"`
	Size    string     `xml:"filesize,attr"`
	Entries []xmlEntry `xml:"Entry"`
}

type xmlResources struct {
	Files []xmlFile `xml:"Res"`
	Paks  []xmlPak  `xml:"Package>Pak"`
}

type xmlTag struct {
	Version string `xml:"version,attr"`
	Tag     string `xml:"tag,attr"`
	xmlResources
	Nested xmlResources `xml:"ResList"`
}

type xmlResList struct {
	Version string `xml:"version,attr"`
	Tag     string `xml:"tag,attr"`
	xmlResources
	Tags []xmlTag `xml:"BaseVersion"`
}

// ParseResList parses the base and tag entries of a verified plaintext
// delivery manifest or an official installed manifest.
func ParseResList(data []byte) (ResList, error) {
	var raw xmlResList
	if err := decodeXML(data, "ResList", &raw); err != nil {
		return ResList{}, err
	}
	if raw.Version == "" || raw.Tag == "" {
		return ResList{}, fmt.Errorf("%w: missing resource version or tag", ErrMalformedManifest)
	}
	var tally manifestTally
	files, paks, err := decodeResources(raw.xmlResources, &tally)
	if err != nil {
		return ResList{}, err
	}
	if len(files) == 0 && len(paks) == 0 {
		return ResList{}, fmt.Errorf("%w: missing base targets", ErrMalformedManifest)
	}
	m := ResList{Version: raw.Version, Tag: raw.Tag, Files: files, Packages: paks}
	for _, t := range raw.Tags {
		if t.Version == "" {
			return ResList{}, fmt.Errorf("%w: missing tag version", ErrMalformedManifest)
		}
		t.Files = append(t.Files, t.Nested.Files...)
		t.Paks = append(t.Paks, t.Nested.Paks...)
		files, paks, err := decodeResources(t.xmlResources, &tally)
		if err != nil {
			return ResList{}, err
		}
		m.Tags = append(m.Tags, TagGroup{Version: t.Version, Tag: t.Tag, Files: files, Packages: paks})
	}
	m.Warnings = tally.warnings
	return m, nil
}

type manifestTally struct {
	resources, entries int
	warnings           []string
}

func decodeResources(raw xmlResources, tally *manifestTally) ([]File, []Package, error) {
	tally.resources += len(raw.Files)
	if tally.resources > maxTargets {
		return nil, nil, ErrTooLarge
	}
	var files []File
	var packages []Package
	targets := make(targetSet)
	for _, r := range raw.Files {
		path, err := NormalizePath(r.Path)
		if err != nil {
			return nil, nil, err
		}
		o := Object{MD5: r.MD5, Size: decimal(r.Size)}
		if !validObject(o, maxFileBytes) {
			return nil, nil, fmt.Errorf("%w: invalid resource identity", ErrMalformedManifest)
		}
		blocks, err := decodeBlocks(r.Blocks, o.Size)
		if err != nil {
			return nil, nil, err
		}
		if _, err := targets.add(path, o, false); err != nil {
			return nil, nil, err
		}
		files = append(files, File{Path: path, MD5: o.MD5, Size: o.Size, Blocks: blocks})
	}
	for _, p := range raw.Paks {
		pak := Object{MD5: p.MD5, Size: decimal(p.Size)}
		if !validObject(pak, maxPakBytes) {
			return nil, nil, fmt.Errorf("%w: invalid package identity", ErrMalformedManifest)
		}
		tally.entries += len(p.Entries)
		if tally.entries > maxEntries {
			return nil, nil, ErrTooLarge
		}
		var entries []Entry
		for _, e := range p.Entries {
			path, err := NormalizePath(e.Path)
			if err != nil {
				return nil, nil, err
			}
			o := Object{MD5: e.MD5, Size: decimal(e.Size)}
			offset := decimal(e.Offset)
			if !validObject(o, maxFileBytes) || offset < 0 || offset > pak.Size-o.Size {
				return nil, nil, fmt.Errorf("%w: invalid package range", ErrMalformedManifest)
			}
			dual, err := targets.add(path, o, true)
			if err != nil {
				return nil, nil, err
			}
			if dual {
				tally.warnings = append(tally.warnings, "DualSource: "+path)
				continue
			}
			entries = append(entries, Entry{Path: path, Pak: pak, Offset: offset, Size: o.Size, MD5: o.MD5})
		}
		if len(entries) > 0 {
			packages = append(packages, Package{Pak: pak, Entries: entries})
		}
	}
	if err := targets.validate(); err != nil {
		return nil, nil, err
	}
	return files, packages, nil
}

// decimal uses a negative sentinel for missing, signed or invalid attributes.
func decimal(s string) int64 {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return -1
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return -1
	}
	return v
}

func decodeBlocks(raw []xmlBlock, size int64) ([]Block, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > maxBlocks {
		return nil, ErrTooLarge
	}
	blocks := make([]Block, len(raw))
	for i, b := range raw {
		index, length := decimal(b.Index), decimal(b.Size)
		if index < 0 || index >= int64(len(raw)) || decimal(b.Start) < 0 || length <= 0 || !validMD5(b.MD5) {
			return nil, fmt.Errorf("%w: invalid block", ErrMalformedManifest)
		}
		blocks[i] = Block{Index: int(index), Size: length, MD5: b.MD5}
	}
	slices.SortFunc(blocks, func(a, b Block) int { return a.Index - b.Index })
	var offset int64
	// Some official snapshots report a nonzero start for index 0. Index
	// and size define relative offsets in the blob being validated.
	for i := range blocks {
		if blocks[i].Index != i || blocks[i].Size > size-offset {
			return nil, fmt.Errorf("%w: discontinuous block table", ErrMalformedManifest)
		}
		blocks[i].Start = offset
		offset += blocks[i].Size
	}
	if offset != size {
		return nil, fmt.Errorf("%w: incomplete block table", ErrMalformedManifest)
	}
	return blocks, nil
}

type seenTarget struct {
	object  Object
	isEntry bool
}

type targetSet map[string]seenTarget

func (ts targetSet) add(path string, object Object, isEntry bool) (bool, error) {
	key := pathKey(path)
	if previous, exists := ts[key]; exists {
		if !isEntry || previous.isEntry || previous.object != object {
			return false, fmt.Errorf("%w: duplicate resource path %q", ErrMalformedManifest, path)
		}
		ts[key] = seenTarget{object, true}
		return true, nil
	}
	ts[key] = seenTarget{object, isEntry}
	if len(ts) > maxTargets {
		return false, ErrTooLarge
	}
	return false, nil
}

func (ts targetSet) validate() error {
	var total int64
	for key, target := range ts {
		total += target.object.Size
		if total > maxTargetBytes {
			return ErrTooLarge
		}
		for i := strings.LastIndexByte(key, '/'); i >= 0; i = strings.LastIndexByte(key, '/') {
			key = key[:i]
			if _, exists := ts[key]; exists {
				return fmt.Errorf("%w: file is also a resource directory", ErrMalformedManifest)
			}
		}
	}
	return nil
}
