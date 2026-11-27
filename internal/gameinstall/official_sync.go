// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/nerv-base/Oddward/internal/gameres"
)

type officialRawElement struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Inner   []byte     `xml:",innerxml"`
}

func (e officialRawElement) writeTo(w *bytes.Buffer, indent string) error {
	w.WriteString(indent)
	w.WriteByte('<')
	w.WriteString(e.XMLName.Local)
	for _, a := range e.Attrs {
		w.WriteByte(' ')
		w.WriteString(a.Name.Local)
		w.WriteString(`="`)
		if err := xml.EscapeText(w, []byte(a.Value)); err != nil {
			return err
		}
		w.WriteByte('"')
	}
	if len(bytes.TrimSpace(e.Inner)) == 0 {
		w.WriteString(" />\n")
		return nil
	}
	w.WriteByte('>')
	w.Write(e.Inner)
	w.WriteString("</")
	w.WriteString(e.XMLName.Local)
	w.WriteString(">\n")
	return nil
}

type officialConfigPreserved struct {
	localBranch      string
	appVersion       string
	updateResVersion string
	speed            string
	baseVerson       *officialRawElement
}

func parseOfficialConfigPreserved(data []byte) (officialConfigPreserved, error) {
	out := officialConfigPreserved{
		appVersion:       "0.0",
		updateResVersion: "0.0",
		speed:            "50",
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	var rootFound bool
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if !rootFound {
			if se.Name.Local != "config" {
				return out, gameres.ErrMalformedManifest
			}
			rootFound = true
			continue
		}
		switch se.Name.Local {
		case "LocalBranch":
			var s string
			if err := dec.DecodeElement(&s, &se); err != nil {
				return out, err
			}
			out.localBranch = strings.TrimSpace(s)
		case "AppVersion":
			var s string
			if err := dec.DecodeElement(&s, &se); err != nil {
				return out, err
			}
			if v := strings.TrimSpace(s); v != "" {
				out.appVersion = v
			}
		case "UpdateResVersion":
			var s string
			if err := dec.DecodeElement(&s, &se); err != nil {
				return out, err
			}
			if v := strings.TrimSpace(s); v != "" {
				out.updateResVersion = v
			}
		case "BaseVerson":
			var elem officialRawElement
			if err := dec.DecodeElement(&elem, &se); err != nil {
				return out, err
			}
			out.baseVerson = &elem
		case "Extra":
			var extra struct {
				Speed string `xml:"speed"`
			}
			if err := dec.DecodeElement(&extra, &se); err != nil {
				return out, err
			}
			if v := strings.TrimSpace(extra.Speed); v != "" {
				out.speed = v
			}
		default:
			if err := dec.Skip(); err != nil {
				return out, err
			}
		}
	}
	if !rootFound {
		return out, gameres.ErrMalformedManifest
	}
	return out, nil
}

type officialResListPreserved struct {
	baseVersions []officialRawElement
	removed      *officialRawElement
}

func parseOfficialResListPreserved(data []byte) (officialResListPreserved, error) {
	plain := data
	if bytes.HasPrefix(data, []byte("PatcherXML0")) {
		var err error
		plain, err = gameres.OpenContainer(data)
		if err != nil {
			return officialResListPreserved{}, err
		}
	}
	var out officialResListPreserved
	dec := xml.NewDecoder(bytes.NewReader(plain))
	var rootFound bool
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if !rootFound {
			if se.Name.Local != "ResList" {
				return out, gameres.ErrMalformedManifest
			}
			rootFound = true
			continue
		}
		switch se.Name.Local {
		case "BaseVersion":
			var elem officialRawElement
			if err := dec.DecodeElement(&elem, &se); err != nil {
				return out, err
			}
			out.baseVersions = append(out.baseVersions, elem)
		case "Removed":
			var elem officialRawElement
			if err := dec.DecodeElement(&elem, &se); err != nil {
				return out, err
			}
			out.removed = &elem
		default:
			if err := dec.Skip(); err != nil {
				return out, err
			}
		}
	}
	if !rootFound {
		return out, gameres.ErrMalformedManifest
	}
	return out, nil
}

type officialSyncEntry struct {
	path      string
	md5       string
	size      int64
	timestamp int64
	blocks    []gameres.Block
}

// SyncOfficialMetadata updates coexisting official PatcherSDK metadata files
// (NTELauncher/UserData/Patcher/PatcherSDK/ResList.xml and config.xml) after a
// published Oddward update so launching the official NTELauncher later does not
// see a stale ResVersion/ResList and delete updated game files.
//
// If official config.xml is absent (for example, a fresh Oddward-only install),
// SyncOfficialMetadata is a no-op and never creates NTELauncher directories.
// Preserved tag-resource metadata (<BaseVerson>, <BaseVersion>, <Removed>) and
// speed settings are kept verbatim, and every target file is verified against
// disk size before any official file is replaced atomically.
func SyncOfficialMetadata(ctx context.Context, root string, cfg gameres.Config, manifest gameres.ResList, branch string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cfg.ResVersion == "" || cfg.ListHash == "" || manifest.Version == "" {
		return ErrInvalidState
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()

	cfgRel := adoptOfficialDir + "config.xml"
	if err := txnSafe(r, cfgRel, false); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	existingCfgBytes, err := stateReadBounded(r, cfgRel)
	if err != nil {
		return err
	}
	preservedCfg, err := parseOfficialConfigPreserved(existingCfgBytes)
	if err != nil {
		return err
	}

	var preservedRes officialResListPreserved
	resRel := adoptOfficialDir + "ResList.xml"
	if err := txnSafe(r, resRel, false); err == nil {
		existingResBytes, err := stateReadBounded(r, resRel)
		if err != nil {
			return err
		}
		preservedRes, err = parseOfficialResListPreserved(existingResBytes)
		if err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	// Build normalized flat baseTag entries and confirm every file exists on disk
	// with its exact published size before mutating official metadata.
	seen := map[string]bool{}
	var entries []officialSyncEntry
	var resSize int64
	addTarget := func(rawPath, hash string, size int64, blocks []gameres.Block) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		normalized, err := gameres.NormalizePath(rawPath)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(strings.ToLower(normalized), "client/") {
			normalized = "Client/WindowsNoEditor/" + normalized
		}
		if !strings.HasPrefix(strings.ToLower(normalized), "client/windowsnoeditor/") {
			return gameres.ErrPathUnsafe
		}
		key := strings.ToUpper(normalized)
		if seen[key] {
			return nil
		}
		seen[key] = true
		if err := txnSafe(r, normalized, false); err != nil {
			return err
		}
		st, err := r.Stat(normalized)
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() || st.Size() != size {
			return fmt.Errorf("official sync target mismatch: %s", normalized)
		}
		entries = append(entries, officialSyncEntry{
			path:      normalized,
			md5:       strings.ToLower(hash),
			size:      size,
			timestamp: st.ModTime().Unix(),
			blocks:    blocks,
		})
		resSize += size
		return nil
	}
	for _, f := range manifest.Files {
		if err := addTarget(f.Path, f.MD5, f.Size, f.Blocks); err != nil {
			return err
		}
	}
	for _, p := range manifest.Packages {
		for _, e := range p.Entries {
			if err := addTarget(e.Path, e.MD5, e.Size, nil); err != nil {
				return err
			}
		}
	}
	if len(entries) == 0 {
		return ErrInvalidState
	}

	tag := manifest.Tag
	if tag == "" {
		tag = "baseTag"
	}
	baseTag := cfg.BaseTag
	if baseTag == "" {
		baseTag = tag
	}
	if branch == "" {
		branch = preservedCfg.localBranch
	}

	resListPlain, err := formatOfficialResListXML(manifest.Version, tag, preservedRes, entries)
	if err != nil {
		return err
	}
	if _, err := gameres.ParseResList(resListPlain); err != nil {
		return fmt.Errorf("validate synced ResList.xml: %w", err)
	}
	resListSealed, err := gameres.SealContainer(resListPlain)
	if err != nil {
		return err
	}
	if check, err := gameres.OpenContainer(resListSealed); err != nil || !bytes.Equal(check, resListPlain) {
		return fmt.Errorf("verify sealed ResList.xml: %w", err)
	}

	configXML, err := formatOfficialConfigXML(branch, cfg, preservedCfg, tag, baseTag, resSize, len(entries))
	if err != nil {
		return err
	}
	if _, err := gameres.ParseConfig(configXML); err != nil {
		return fmt.Errorf("validate synced config.xml: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	dirRel := strings.TrimSuffix(adoptOfficialDir, "/")
	if err := txnSafe(r, dirRel, false); err != nil {
		return err
	}
	dirRoot, err := r.OpenRoot(dirRel)
	if err != nil {
		return err
	}
	defer dirRoot.Close()

	// Write ResList.xml before config.xml so config.xml only advertises the new
	// version once the matching per-file manifest is already durable.
	if err := officialAtomicWrite(dirRoot, "ResList.xml", resListSealed); err != nil {
		return err
	}
	return officialAtomicWrite(dirRoot, "config.xml", configXML)
}

func writeXMLAttr(w *bytes.Buffer, name, value string) error {
	w.WriteByte(' ')
	w.WriteString(name)
	w.WriteString(`="`)
	if err := xml.EscapeText(w, []byte(value)); err != nil {
		return err
	}
	w.WriteByte('"')
	return nil
}

func writeXMLElem(w *bytes.Buffer, indent, name, value string) error {
	w.WriteString(indent)
	w.WriteByte('<')
	w.WriteString(name)
	w.WriteByte('>')
	if err := xml.EscapeText(w, []byte(value)); err != nil {
		return err
	}
	w.WriteString("</")
	w.WriteString(name)
	w.WriteString(">\n")
	return nil
}

func formatOfficialResListXML(version, tag string, preserved officialResListPreserved, entries []officialSyncEntry) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("<?xml version=\"1.0\" encoding=\"utf-8\" ?>\n<ResList")
	if err := writeXMLAttr(&b, "version", version); err != nil {
		return nil, err
	}
	if err := writeXMLAttr(&b, "tag", tag); err != nil {
		return nil, err
	}
	b.WriteString(">\n")
	for _, bv := range preserved.baseVersions {
		if err := bv.writeTo(&b, "    "); err != nil {
			return nil, err
		}
	}
	if preserved.removed != nil {
		if err := preserved.removed.writeTo(&b, "    "); err != nil {
			return nil, err
		}
	}
	for _, e := range entries {
		b.WriteString("    <Res")
		if err := writeXMLAttr(&b, "filename", e.path); err != nil {
			return nil, err
		}
		if err := writeXMLAttr(&b, "md5", e.md5); err != nil {
			return nil, err
		}
		if err := writeXMLAttr(&b, "timestamp", strconv.FormatInt(e.timestamp, 10)); err != nil {
			return nil, err
		}
		if err := writeXMLAttr(&b, "filesize", strconv.FormatInt(e.size, 10)); err != nil {
			return nil, err
		}
		if len(e.blocks) == 0 {
			b.WriteString(" />\n")
			continue
		}
		if err := writeXMLAttr(&b, "blockSize", strconv.Itoa(len(e.blocks))); err != nil {
			return nil, err
		}
		b.WriteString(">\n")
		for _, blk := range e.blocks {
			b.WriteString("        <Block")
			if err := writeXMLAttr(&b, "index", strconv.Itoa(blk.Index)); err != nil {
				return nil, err
			}
			if err := writeXMLAttr(&b, "start", strconv.FormatInt(blk.Start, 10)); err != nil {
				return nil, err
			}
			if err := writeXMLAttr(&b, "size", strconv.FormatInt(blk.Size, 10)); err != nil {
				return nil, err
			}
			if err := writeXMLAttr(&b, "md5", strings.ToLower(blk.MD5)); err != nil {
				return nil, err
			}
			b.WriteString(" />\n")
		}
		b.WriteString("    </Res>\n")
	}
	b.WriteString("</ResList>\n")
	return b.Bytes(), nil
}

func formatOfficialConfigXML(branch string, cfg gameres.Config, preserved officialConfigPreserved, tag, baseTag string, resSize int64, resCount int) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("<?xml version=\"1.0\" encoding=\"utf-8\" ?>\n<config>\n")
	if branch != "" {
		if err := writeXMLElem(&b, "    ", "LocalBranch", branch); err != nil {
			return nil, err
		}
	}
	if err := writeXMLElem(&b, "    ", "ResVersion", cfg.ResVersion); err != nil {
		return nil, err
	}
	if err := writeXMLElem(&b, "    ", "AppVersion", preserved.appVersion); err != nil {
		return nil, err
	}
	if err := writeXMLElem(&b, "    ", "UpdateResVersion", preserved.updateResVersion); err != nil {
		return nil, err
	}
	if err := writeXMLElem(&b, "    ", "Tag", tag); err != nil {
		return nil, err
	}
	if err := writeXMLElem(&b, "    ", "ResSize", strconv.FormatInt(resSize, 10)); err != nil {
		return nil, err
	}
	if err := writeXMLElem(&b, "    ", "ResCount", strconv.Itoa(resCount)); err != nil {
		return nil, err
	}
	if preserved.baseVerson != nil {
		if err := preserved.baseVerson.writeTo(&b, "    "); err != nil {
			return nil, err
		}
	}
	b.WriteString("    <Extra>\n")
	if cfg.DiffHash != "" {
		if err := writeXMLElem(&b, "        ", "diffHash", strings.ToLower(cfg.DiffHash)); err != nil {
			return nil, err
		}
	}
	if err := writeXMLElem(&b, "        ", "listHash", strings.ToLower(cfg.ListHash)); err != nil {
		return nil, err
	}
	b.WriteString("        <BaseTag>\n            <item")
	if err := writeXMLAttr(&b, "name", baseTag); err != nil {
		return nil, err
	}
	b.WriteString(" />\n        </BaseTag>\n")
	if err := writeXMLElem(&b, "        ", "speed", preserved.speed); err != nil {
		return nil, err
	}
	b.WriteString("    </Extra>\n")
	if cfg.CheckHash != "" {
		if err := writeXMLElem(&b, "    ", "Hash", cfg.CheckHash); err != nil {
			return nil, err
		}
	}
	b.WriteString("</config>\n")
	return b.Bytes(), nil
}

func officialAtomicWrite(dir *os.Root, name string, data []byte) (resultErr error) {
	if err := txnSafe(dir, name, true); err != nil {
		return err
	}
	tmp := ".oddward-sync-" + rand.Text() + ".tmp"
	f, err := dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if resultErr != nil {
			_ = dir.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return dir.Rename(tmp, name)
}
