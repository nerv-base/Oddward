// SPDX-License-Identifier: GPL-3.0-or-later

package gameres

import (
	"encoding/xml"
	"strconv"
	"strings"
)

// Config is the read-only projection of an official remote or local config.xml.
// Optional fields are zero-valued when absent. Pre-release fields report the
// advertisement verbatim, without inferring that a download is available.
type Config struct {
	ResVersion, UpdateResVersion, Section, UpdateBranch, LocalBranch  string
	ResSize                                                           int64
	CheckHash, Tag, BaseTag, ListHash, DiffHash                       string
	MaxThreadCnt, TagTaskThreadCnt                                    int
	BaseVersions                                                      []string
	PreReleaseStatus, PreReleaseBranch, PreReleaseVersion             string
	PreReleaseTestStatus, PreReleaseTestBranch, PreReleaseTestVersion string
	UnknownFields                                                     int
}

// configText holds one scalar as supplied, including explicit empty elements.
// A repeated field or markup in a scalar has ambiguous meaning.
type configText struct {
	value   string
	present bool
}

func (v *configText) UnmarshalXML(d *xml.Decoder, _ xml.StartElement) error {
	if v.present {
		return ErrMalformedManifest
	}
	var text strings.Builder
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch tok := tok.(type) {
		case xml.CharData:
			text.Write(tok)
		case xml.EndElement:
			*v = configText{value: strings.TrimSpace(text.String()), present: true}
			return nil
		case xml.StartElement:
			return ErrMalformedManifest
		}
	}
}

type unexpectedConfigField struct{ XMLName xml.Name }

// The remote metadata names its base tag in an item attribute.
type configBaseTag struct {
	Text  string `xml:",chardata"`
	Items []struct {
		Name string `xml:"name,attr"`
	} `xml:"item"`
}

type configDocument struct {
	ResVersion            configText
	UpdateResVersion      configText
	Section               configText
	UpdateBranchName      configText
	LocalBranch           configText
	ResSize               configText
	Hash                  configText
	Tag                   configText
	BaseTag               configText
	PreReleaseStatus      configText
	PreReleaseBranch      configText
	PreReleaseVersion     configText
	PreReleaseTestStatus  configText
	PreReleaseTestBranch  configText
	PreReleaseTestVersion configText
	BaseVerson            []struct {
		Refs []struct {
			Version string `xml:"version,attr"`
		} `xml:"Res"`
		Other []struct{} `xml:",any"`
	}
	Extra []struct {
		ListHash         configText `xml:"listHash"`
		DiffHash         configText `xml:"diffHash"`
		MaxThreadCnt     configText `xml:"maxThreadCnt"`
		TagTaskThreadCnt configText `xml:"tagTaskThreadCnt"`
		ResSize          configText
		BaseTag          []configBaseTag
		Other            []unexpectedConfigField `xml:",any"`
	}
	Other []unexpectedConfigField `xml:",any"`
}

// ParseConfig reads a bounded configuration and requires the version and the
// plaintext manifest checksum. Unknown elements are ignored and counted.
func ParseConfig(data []byte) (Config, error) {
	var doc configDocument
	if err := decodeXML(data, "config", &doc); err != nil {
		return Config{}, err
	}
	if len(doc.Extra) != 1 || len(doc.BaseVerson) > 1 || !validURLComponent(doc.ResVersion.value) {
		return Config{}, ErrMalformedManifest
	}
	extra := doc.Extra[0]
	if !validMD5(extra.ListHash.value) || extra.DiffHash.value != "" && !validMD5(extra.DiffHash.value) {
		return Config{}, ErrMalformedManifest
	}
	resSize, err := configUint(doc.ResSize.value, 64)
	if err != nil {
		return Config{}, err
	}
	if extra.ResSize.present {
		extraSize, err := configUint(extra.ResSize.value, 64)
		if err != nil || doc.ResSize.present && resSize != extraSize {
			return Config{}, ErrMalformedManifest
		}
		resSize = extraSize
	}
	baseTag := doc.BaseTag.value
	if len(extra.BaseTag) > 1 {
		return Config{}, ErrMalformedManifest
	}
	if len(extra.BaseTag) == 1 {
		wireTag := extra.BaseTag[0]
		name := strings.TrimSpace(wireTag.Text)
		if len(wireTag.Items) != 0 {
			if len(wireTag.Items) != 1 || name != "" {
				return Config{}, ErrMalformedManifest
			}
			name = strings.TrimSpace(wireTag.Items[0].Name)
		}
		if doc.BaseTag.present && baseTag != name {
			return Config{}, ErrMalformedManifest
		}
		baseTag = name
	}
	maxThreadCnt, err := configUint(extra.MaxThreadCnt.value, strconv.IntSize)
	if err != nil {
		return Config{}, err
	}
	tagTaskThreadCnt, err := configUint(extra.TagTaskThreadCnt.value, strconv.IntSize)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		ResVersion: doc.ResVersion.value, UpdateResVersion: doc.UpdateResVersion.value,
		Section: doc.Section.value, UpdateBranch: doc.UpdateBranchName.value, LocalBranch: doc.LocalBranch.value,
		ResSize: resSize, CheckHash: doc.Hash.value, Tag: doc.Tag.value, BaseTag: baseTag,
		ListHash: extra.ListHash.value, DiffHash: extra.DiffHash.value,
		MaxThreadCnt: int(maxThreadCnt), TagTaskThreadCnt: int(tagTaskThreadCnt),
		PreReleaseStatus: doc.PreReleaseStatus.value, PreReleaseBranch: doc.PreReleaseBranch.value,
		PreReleaseVersion: doc.PreReleaseVersion.value, PreReleaseTestStatus: doc.PreReleaseTestStatus.value,
		PreReleaseTestBranch: doc.PreReleaseTestBranch.value, PreReleaseTestVersion: doc.PreReleaseTestVersion.value,
		UnknownFields: countUnknownConfigFields(doc.Other) + countUnknownConfigFields(extra.Other),
	}
	if len(doc.BaseVerson) != 0 {
		base := doc.BaseVerson[0]
		cfg.UnknownFields += len(base.Other)
		for _, ref := range base.Refs {
			if version := strings.TrimSpace(ref.Version); version != "" {
				cfg.BaseVersions = append(cfg.BaseVersions, version)
			}
		}
	}
	return cfg, nil
}

func configUint(s string, bits int) (int64, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, bits)
	if err != nil || n < 0 {
		return 0, ErrMalformedManifest
	}
	return n, nil
}

func countUnknownConfigFields(fields []unexpectedConfigField) int {
	var count int
	for _, field := range fields {
		switch field.XMLName.Local {
		case "ResCount", "RemoteContent", "Compressed", "Encrypt", "speed", "playAssetsTimeout",
			"Devices", "Server", "InternalInfo", "ResCheck", "FixCallback", "cdnOrder", "smartDownload",
			"PreInstall", "SpaceCheck", "Revision":
			// Recognized fields with no role in the download protocol projection.
		default:
			count++
		}
	}
	return count
}
