// SPDX-License-Identifier: GPL-3.0-or-later

package gameres

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
)

const (
	maxXMLBytes    = 8 << 20
	maxFileBytes   = 32 << 30
	maxPakBytes    = 64 << 30
	maxTargetBytes = 1 << 40
	maxTargets     = 4096
	maxEntries     = 65536
	maxBlocks      = 128
	maxXMLDepth    = 32
)

var (
	ErrMalformedManifest = errors.New("malformed game metadata")
	ErrTooLarge          = errors.New("game metadata exceeds limit")
	ErrPathUnsafe        = errors.New("unsafe game resource path")
)

// Object identifies a content-addressed object by its lowercase MD5 and size.
type Object struct {
	MD5  string
	Size int64
}

func (o Object) String() string { return o.MD5 + "." + strconv.FormatInt(o.Size, 10) }

func validMD5(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validObject(o Object, limit int64) bool {
	return o.Size > 0 && o.Size <= limit && validMD5(o.MD5)
}

// A protocol document is a single unnamespaced XML tree. Check the envelope
// before unmarshalling so ignored fields and trailing input are checked too.
func decodeXML(data []byte, root string, dst any) error {
	if len(data) > maxXMLBytes {
		return ErrTooLarge
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	d := xml.NewDecoder(bytes.NewReader(data))
	seenRoot, depth := false, 0
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ErrMalformedManifest
		}
		switch tok := token.(type) {
		case xml.Directive:
			return ErrMalformedManifest
		case xml.StartElement:
			if depth == 0 {
				if seenRoot || tok.Name.Local != root {
					return ErrMalformedManifest
				}
				seenRoot = true
			}
			depth++
			if depth > maxXMLDepth || tok.Name.Space != "" {
				return ErrMalformedManifest
			}
			const maxAttributes = 64
			if len(tok.Attr) > maxAttributes {
				return ErrTooLarge
			}
			attributes := make(map[string]struct{}, len(tok.Attr))
			for _, attr := range tok.Attr {
				name := attr.Name.Local
				if _, duplicate := attributes[name]; duplicate || attr.Name.Space != "" || name == "xmlns" {
					return ErrMalformedManifest
				}
				attributes[name] = struct{}{}
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && len(bytes.Trim(tok, " \t\r\n")) != 0 {
				return ErrMalformedManifest
			}
		}
	}
	if !seenRoot || depth != 0 || xml.Unmarshal(data, dst) != nil {
		return ErrMalformedManifest
	}
	return nil
}
