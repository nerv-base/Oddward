// SPDX-License-Identifier: GPL-3.0-or-later

// Package bootcfg reads the bounded local CN SDK resource without loading any SDK code.
package bootcfg

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/nerv-base/Oddward/internal/cnlogin/ecb"
)

var ErrConfig = errors.New("invalid CN boot configuration")

const maxResource = 64 << 20
const maxConfig = 65536

// Decode returns owned plaintext JSON; the caller must clear it after parsing.
// Both compressed and uncompressed resources are bounded before allocation.
func Decode(resource []byte, key [16]byte) ([]byte, error) {
	defer clear(key[:])
	data, err := extract(resource)
	if err != nil {
		return nil, ErrConfig
	}
	defer clear(data)
	compact := make([]byte, 0, len(data))
	defer func() { clear(compact) }()
	for _, c := range data {
		switch c {
		case ' ', '\r', '\n', '\t':
			continue
		}
		if c > 127 {
			return nil, ErrConfig
		}
		compact = append(compact, c)
	}
	cipher := make([]byte, base64.StdEncoding.DecodedLen(len(compact)))
	defer clear(cipher)
	n, err := base64.StdEncoding.Strict().Decode(cipher, compact)
	if err != nil {
		return nil, ErrConfig
	}
	plain, err := ecb.Decrypt(key[:], cipher[:n])
	if err != nil {
		return nil, ErrConfig
	}
	trimmed := bytes.TrimSpace(plain)
	if len(plain) > maxConfig || !utf8.Valid(plain) || len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(plain) {
		clear(plain)
		return nil, ErrConfig
	}
	return plain, nil
}

type archive struct {
	b                                               []byte
	tree, names, payload, treeEnd, nameEnd, dataEnd int
	seen                                            []bool
	found                                           []byte
}

func sectionEnd(start int, offsets []int, length int) int {
	end := length
	for _, off := range offsets {
		if off > start && off < end {
			end = off
		}
	}
	return end
}
func extract(b []byte) ([]byte, error) {
	if len(b) < 24 || len(b) > maxResource || string(b[:4]) != "qres" || binary.BigEndian.Uint32(b[4:]) != 3 {
		return nil, ErrConfig
	}
	a := archive{b: b, tree: int(binary.BigEndian.Uint32(b[8:])), payload: int(binary.BigEndian.Uint32(b[12:])), names: int(binary.BigEndian.Uint32(b[16:]))}
	offsets := []int{a.tree, a.payload, a.names}
	for _, off := range offsets {
		if off < 24 || off >= len(b) {
			return nil, ErrConfig
		}
	}
	if a.tree == a.names || a.tree == a.payload || a.names == a.payload {
		return nil, ErrConfig
	}
	a.treeEnd = sectionEnd(a.tree, offsets, len(b))
	a.nameEnd = sectionEnd(a.names, offsets, len(b))
	a.dataEnd = sectionEnd(a.payload, offsets, len(b))
	count := (a.treeEnd - a.tree) / 22
	if count < 1 || count > 20000 {
		return nil, ErrConfig
	}
	a.seen = make([]bool, count)
	if err := a.walk(0, "", 0); err != nil {
		return nil, err
	}
	for _, seen := range a.seen {
		if !seen {
			return nil, ErrConfig
		}
	}
	if len(a.found) == 0 {
		return nil, ErrConfig
	}
	return a.found, nil
}
func (a *archive) name(off uint32) (string, error) {
	pos := a.names + int(off)
	if pos < a.names || pos > a.nameEnd-6 {
		return "", ErrConfig
	}
	n := int(binary.BigEndian.Uint16(a.b[pos:]))
	if n > 255 || n > (a.nameEnd-pos-6)/2 {
		return "", ErrConfig
	}
	u := make([]uint16, n)
	for i := range u {
		u[i] = binary.BigEndian.Uint16(a.b[pos+6+2*i:])
	}
	for i := 0; i < len(u); i++ {
		if u[i] >= 0xd800 && u[i] <= 0xdbff {
			if i+1 == len(u) || u[i+1] < 0xdc00 || u[i+1] > 0xdfff {
				return "", ErrConfig
			}
			i++
		} else if u[i] >= 0xdc00 && u[i] <= 0xdfff {
			return "", ErrConfig
		}
	}
	s := string(utf16.Decode(u))
	if s == "." || s == ".." || strings.ContainsAny(s, "/\\\x00:") {
		return "", ErrConfig
	}
	return s, nil
}
func (a *archive) walk(index int, parent string, depth int) error {
	if index < 0 || index >= len(a.seen) || a.seen[index] || depth > 32 {
		return ErrConfig
	}
	a.seen[index] = true
	node := a.b[a.tree+index*22:]
	name, err := a.name(binary.BigEndian.Uint32(node))
	if err != nil {
		return err
	}
	// Qt may reuse a nonempty name-table entry for the root node.
	if index != 0 && name == "" {
		return ErrConfig
	}
	path := parent + "/" + name
	flags := binary.BigEndian.Uint16(node[4:])
	if flags&2 != 0 {
		if flags != 2 {
			return ErrConfig
		}
		count, first := int(binary.BigEndian.Uint32(node[6:])), int(binary.BigEndian.Uint32(node[10:]))
		if first > len(a.seen) || count > len(a.seen)-first {
			return ErrConfig
		}
		for i := range count {
			if err := a.walk(first+i, path, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if index == 0 {
		return ErrConfig
	}
	pos := a.payload + int(binary.BigEndian.Uint32(node[10:]))
	if pos < a.payload || pos > a.dataEnd-4 {
		return ErrConfig
	}
	size := int(binary.BigEndian.Uint32(a.b[pos:]))
	if size > a.dataEnd-pos-4 {
		return ErrConfig
	}
	// Other resources may be large media, empty files, or use compression
	// that the SDK configuration does not need. Validate their bounds only.
	if !strings.HasSuffix(path, "/1289/main/oneSdkConfig.config") {
		return nil
	}
	if size < 1 || size > maxConfig || flags&^uint16(1) != 0 {
		return ErrConfig
	}
	if a.found != nil {
		clear(a.found)
		return ErrConfig
	}
	data := a.b[pos+4 : pos+4+size]
	if flags == 0 {
		a.found = bytes.Clone(data)
		return nil
	}
	if len(data) < 5 {
		return ErrConfig
	}
	expected := int(binary.BigEndian.Uint32(data))
	if expected < 1 || expected > maxConfig {
		return ErrConfig
	}
	source := bytes.NewReader(data[4:])
	r, err := zlib.NewReader(source)
	if err != nil {
		return ErrConfig
	}
	inflated, err := io.ReadAll(io.LimitReader(r, int64(expected)+1))
	closeErr := r.Close()
	if err != nil || closeErr != nil || len(inflated) != expected || source.Len() != 0 {
		clear(inflated)
		return ErrConfig
	}
	a.found = inflated
	return nil
}
