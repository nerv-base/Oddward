// SPDX-License-Identifier: GPL-3.0-or-later

package gameres

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
)

var (
	ErrContainer    = errors.New("malformed game metadata container")
	ErrManifestHash = errors.New("game manifest checksum mismatch")
)

const (
	patcherMagic      = "PatcherXML0\x00"
	patcherHeaderSize = len(patcherMagic) + 4
	// Windows CN resource format constants. These identify the wire format.
	patcherKey = "1289@Patcher0000"
	patcherIV  = "PatcherSDK000000"

	// Allow DEFLATE framing overhead, CBC padding, and the container header.
	maxContainerBytes = maxXMLBytes + 64<<10
	// A manifest ZIP carries up to two containers, plus ZIP framing overhead.
	maxManifestZIPBytes = 2*maxContainerBytes + 64<<10
)

// OpenContainer decodes one bounded Windows CN PatcherXML0 container. It leaves
// the input untouched and returns the exact plaintext for checksum verification.
func OpenContainer(data []byte) ([]byte, error) {
	if len(data) > maxContainerBytes {
		return nil, ErrTooLarge
	}
	if len(data) < patcherHeaderSize+aes.BlockSize || !bytes.HasPrefix(data, []byte(patcherMagic)) {
		return nil, ErrContainer
	}
	want := binary.LittleEndian.Uint32(data[len(patcherMagic):patcherHeaderSize])
	if want > maxXMLBytes {
		return nil, ErrTooLarge
	}
	body := data[patcherHeaderSize:]
	if want == 0 || len(body)%aes.BlockSize != 0 {
		return nil, ErrContainer
	}

	block, err := aes.NewCipher([]byte(patcherKey))
	if err != nil {
		return nil, ErrContainer
	}
	compressed := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, []byte(patcherIV)).CryptBlocks(compressed, body)
	padding := int(compressed[len(compressed)-1])
	if padding == 0 || padding > aes.BlockSize {
		return nil, ErrContainer
	}
	end := len(compressed) - padding
	for _, b := range compressed[end:] {
		if int(b) != padding {
			return nil, ErrContainer
		}
	}

	// bytes.Reader implements io.ByteReader, so zlib does not read past its
	// trailer. Check for both surplus plaintext and trailing compressed data.
	compressedInput := bytes.NewReader(compressed[:end])
	zr, err := zlib.NewReader(compressedInput)
	if err != nil {
		return nil, ErrContainer
	}
	defer zr.Close()
	plaintext := make([]byte, int(want))
	if _, err := io.ReadFull(zr, plaintext); err != nil {
		return nil, ErrContainer
	}
	var extra [1]byte
	if n, err := io.ReadFull(zr, extra[:]); n != 0 || err != io.EOF || compressedInput.Len() != 0 {
		return nil, ErrContainer
	}
	return plaintext, nil
}

// ManifestXML contains decoded members of a delivery archive. LastDiff is nil
// if the archive omits lastdiff.bin.
type ManifestXML struct {
	ResList, LastDiff []byte
}

// OpenManifestZIP decodes the exact root ResList.bin and optional lastdiff.bin
// members of a bounded archive. Use VerifyManifestHash before trusting bytes.
func OpenManifestZIP(data []byte) (ManifestXML, error) {
	if len(data) > maxManifestZIPBytes {
		return ManifestXML{}, ErrTooLarge
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(zr.File) == 0 || len(zr.File) > 2 {
		return ManifestXML{}, ErrContainer
	}
	var res, diff *zip.File
	for _, f := range zr.File {
		if !f.Mode().IsRegular() {
			return ManifestXML{}, ErrContainer
		}
		switch f.Name {
		case "ResList.bin":
			if res != nil {
				return ManifestXML{}, ErrContainer
			}
			res = f
		case "lastdiff.bin":
			if diff != nil {
				return ManifestXML{}, ErrContainer
			}
			diff = f
		default:
			return ManifestXML{}, ErrContainer
		}
	}
	if res == nil {
		return ManifestXML{}, ErrContainer
	}
	out := ManifestXML{}
	if out.ResList, err = openZIPMember(res); err != nil {
		return ManifestXML{}, err
	}
	if diff != nil {
		if out.LastDiff, err = openZIPMember(diff); err != nil {
			return ManifestXML{}, err
		}
	}
	return out, nil
}

func openZIPMember(f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > maxContainerBytes {
		return nil, ErrTooLarge
	}
	entry, err := f.Open()
	if err != nil {
		return nil, ErrContainer
	}
	defer entry.Close()
	data, err := io.ReadAll(io.LimitReader(entry, maxContainerBytes+1))
	if len(data) > maxContainerBytes {
		return nil, ErrTooLarge
	}
	if err != nil || uint64(len(data)) != f.UncompressedSize64 {
		return nil, ErrContainer
	}
	return OpenContainer(data)
}

// VerifyManifestHash checks the protocol MD5 over the exact plaintext bytes.
func VerifyManifestHash(data []byte, want string) error {
	if len(data) > maxXMLBytes {
		return ErrTooLarge
	}
	if !validMD5(want) {
		return ErrManifestHash
	}
	got := md5.Sum(data)
	if hex.EncodeToString(got[:]) != want {
		return ErrManifestHash
	}
	return nil
}
