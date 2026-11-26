// SPDX-License-Identifier: GPL-3.0-or-later

package gallery

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// MaxVideoBytes caps indexed/archived video files at 2 GiB (real observed
	// CN 1080p in-game recordings range from ~5 MiB to ~240+ MiB).
	MaxVideoBytes int64 = 2 << 30

	maxMP4MoovBytes = 16 << 20
	maxMP4Boxes     = 50_000
)

// IsVideoExtension reports whether ext (including leading dot) is a supported
// local video container extension.
func IsVideoExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".mp4", ".mov":
		return true
	default:
		return false
	}
}

// IsVideoMIME reports whether mime is a supported video MIME type.
func IsVideoMIME(mime string) bool {
	switch strings.ToLower(mime) {
	case "video/mp4", "video/quicktime":
		return true
	default:
		return false
	}
}

// InspectMP4File validates an MP4/MOV file's container structure, seeking past
// mdat to parse moov headers without reading video frames into memory.
func InspectMP4File(path string) (MediaInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return MediaInfo{}, err
	}
	defer f.Close()
	st, err := statOpenFile(f)
	if err != nil {
		return MediaInfo{}, err
	}
	if st.Reparse || st.IsDir {
		return MediaInfo{}, ErrUnsafePath
	}
	return InspectMP4Reader(f, st.Size)
}

// InspectMP4Reader validates an ISO BMFF (MP4/MOV) stream via seeking and
// extracts real verified dimensions, duration and codec names. It never
// fabricates capture timestamps, locations, or character names.
func InspectMP4Reader(rs io.ReadSeeker, byteSize int64) (MediaInfo, error) {
	if byteSize <= 16 {
		return MediaInfo{}, fmt.Errorf("%w: truncated video", ErrInvalidMedia)
	}
	if byteSize > MaxVideoBytes {
		return MediaInfo{}, fmt.Errorf("%w: %d bytes exceeds video limit", ErrTooLarge, byteSize)
	}

	var pos int64
	var sawFtyp bool
	var mimeType string
	var moovData []byte
	var hdr [16]byte

	for boxes := 0; pos+8 <= byteSize && boxes < maxMP4Boxes; boxes++ {
		if _, err := rs.Seek(pos, io.SeekStart); err != nil {
			return MediaInfo{}, err
		}
		n, err := io.ReadFull(rs, hdr[:8])
		if err != nil || n < 8 {
			break
		}
		boxSize := int64(binary.BigEndian.Uint32(hdr[0:4]))
		boxType := string(hdr[4:8])
		hdrLen := int64(8)
		switch boxSize {
		case 0:
			boxSize = byteSize - pos
		case 1:
			if pos+16 > byteSize {
				return MediaInfo{}, fmt.Errorf("%w: truncated extended mp4 box", ErrInvalidMedia)
			}
			if _, err := io.ReadFull(rs, hdr[8:16]); err != nil {
				return MediaInfo{}, fmt.Errorf("%w: read extended mp4 box: %v", ErrInvalidMedia, err)
			}
			u64 := binary.BigEndian.Uint64(hdr[8:16])
			if u64 > math.MaxInt64 {
				return MediaInfo{}, fmt.Errorf("%w: mp4 box size overflow", ErrInvalidMedia)
			}
			boxSize = int64(u64)
			hdrLen = 16
		}
		if boxSize < hdrLen || pos+boxSize > byteSize {
			return MediaInfo{}, fmt.Errorf("%w: invalid mp4 box %q size %d", ErrInvalidMedia, boxType, boxSize)
		}

		switch boxType {
		case "ftyp":
			if boxSize-hdrLen < 4 || boxSize-hdrLen > 4096 {
				return MediaInfo{}, fmt.Errorf("%w: invalid ftyp box", ErrInvalidMedia)
			}
			buf := make([]byte, boxSize-hdrLen)
			if _, err := io.ReadFull(rs, buf); err != nil {
				return MediaInfo{}, fmt.Errorf("%w: read ftyp: %v", ErrInvalidMedia, err)
			}
			brand := string(buf[0:4])
			mime, ok := classifyMP4Brand(brand, buf)
			if !ok {
				return MediaInfo{}, fmt.Errorf("%w: unsupported mp4 brand %q", ErrInvalidMedia, brand)
			}
			sawFtyp = true
			mimeType = mime
		case "moov":
			payloadLen := boxSize - hdrLen
			if payloadLen <= 0 || payloadLen > maxMP4MoovBytes {
				return MediaInfo{}, fmt.Errorf("%w: moov size %d out of bounds", ErrInvalidMedia, payloadLen)
			}
			moovData = make([]byte, payloadLen)
			if _, err := io.ReadFull(rs, moovData); err != nil {
				return MediaInfo{}, fmt.Errorf("%w: read moov: %v", ErrInvalidMedia, err)
			}
		}
		pos += boxSize
	}

	if !sawFtyp || len(moovData) == 0 {
		return MediaInfo{}, fmt.Errorf("%w: missing ftyp or moov box", ErrInvalidMedia)
	}

	parsed, err := parseMP4Moov(moovData)
	if err != nil {
		return MediaInfo{}, err
	}
	if parsed.width <= 0 || parsed.height <= 0 || parsed.width > MaxImageSide || parsed.height > MaxImageSide {
		return MediaInfo{}, fmt.Errorf("%w: invalid video dimensions %dx%d", ErrInvalidMedia, parsed.width, parsed.height)
	}
	return MediaInfo{
		MediaType:  MediaVideo,
		MIMEType:   mimeType,
		Width:      parsed.width,
		Height:     parsed.height,
		ByteSize:   byteSize,
		DurationMS: parsed.durationMS,
		VideoCodec: parsed.videoCodec,
		AudioCodec: parsed.audioCodec,
	}, nil
}

func classifyMP4Brand(major string, ftypPayload []byte) (string, bool) {
	check := func(b string) (string, bool) {
		switch b {
		case "isom", "iso2", "iso5", "iso6", "mp41", "mp42", "avc1", "M4V ", "dash":
			return "video/mp4", true
		case "qt  ":
			return "video/quicktime", true
		}
		return "", false
	}
	if mime, ok := check(major); ok {
		return mime, true
	}
	// Check compatible brands list starting at offset 8.
	for i := 8; i+4 <= len(ftypPayload); i += 4 {
		if mime, ok := check(string(ftypPayload[i : i+4])); ok {
			return mime, true
		}
	}
	return "", false
}

type mp4MoovInfo struct {
	durationMS int64
	width      int
	height     int
	videoCodec string
	audioCodec string
}

func parseMP4Moov(data []byte) (mp4MoovInfo, error) {
	var info mp4MoovInfo
	walkMP4Children(data, func(boxType string, body []byte) {
		switch boxType {
		case "mvhd":
			if ms := parseMP4Mvhd(body); ms > 0 {
				info.durationMS = ms
			}
		case "trak":
			t := parseMP4Trak(body)
			switch t.handler {
			case "vide":
				if t.width > 0 && t.height > 0 && info.width == 0 {
					info.width, info.height = t.width, t.height
				}
				if t.codec != "" && info.videoCodec == "" {
					info.videoCodec = t.codec
				}
			case "soun":
				if t.codec != "" && info.audioCodec == "" {
					info.audioCodec = t.codec
				}
			}
		}
	})
	if info.width == 0 || info.height == 0 {
		return mp4MoovInfo{}, fmt.Errorf("%w: no video track in moov", ErrInvalidMedia)
	}
	return info, nil
}

func walkMP4Children(data []byte, fn func(boxType string, body []byte)) {
	for i, steps := 0, 0; i+8 <= len(data) && steps < 4096; steps++ {
		sz := int64(binary.BigEndian.Uint32(data[i : i+4]))
		typ := string(data[i+4 : i+8])
		hdrLen := int64(8)
		if sz == 0 {
			sz = int64(len(data) - i)
		} else if sz == 1 {
			if i+16 > len(data) {
				return
			}
			u64 := binary.BigEndian.Uint64(data[i+8 : i+16])
			if u64 > math.MaxInt64 {
				return
			}
			sz = int64(u64)
			hdrLen = 16
		}
		if sz < hdrLen || int64(i)+sz > int64(len(data)) {
			return
		}
		fn(typ, data[i+int(hdrLen):i+int(sz)])
		i += int(sz)
	}
}

func parseMP4Mvhd(body []byte) int64 {
	if len(body) < 20 {
		return 0
	}
	ver := body[0]
	if ver == 0 && len(body) >= 20 {
		ts := binary.BigEndian.Uint32(body[12:16])
		dur := binary.BigEndian.Uint32(body[16:20])
		if ts > 0 {
			return int64(dur) * 1000 / int64(ts)
		}
	} else if ver == 1 && len(body) >= 32 {
		ts := binary.BigEndian.Uint32(body[20:24])
		dur := binary.BigEndian.Uint64(body[24:32])
		if ts > 0 {
			return int64(dur/uint64(ts)*1000 + (dur%uint64(ts))*1000/uint64(ts))
		}
	}
	return 0
}

type mp4TrakInfo struct {
	handler string
	width   int
	height  int
	codec   string
}

func parseMP4Trak(body []byte) mp4TrakInfo {
	var t mp4TrakInfo
	walkMP4Children(body, func(typ string, sub []byte) {
		switch typ {
		case "tkhd":
			if len(sub) < 84 {
				return
			}
			ver := sub[0]
			off := 76
			if ver == 1 {
				off = 88
			}
			if off+8 <= len(sub) {
				w := int(binary.BigEndian.Uint32(sub[off:off+4]) >> 16)
				h := int(binary.BigEndian.Uint32(sub[off+4:off+8]) >> 16)
				if w > 0 && h > 0 {
					t.width, t.height = w, h
				}
			}
		case "mdia":
			walkMP4Children(sub, func(mtyp string, msub []byte) {
				switch mtyp {
				case "hdlr":
					if len(msub) >= 12 {
						t.handler = string(msub[8:12])
					}
				case "minf":
					walkMP4Children(msub, func(ntyp string, nsub []byte) {
						if ntyp != "stbl" {
							return
						}
						walkMP4Children(nsub, func(styp string, ssub []byte) {
							if styp == "stsd" && len(ssub) >= 16 {
								// Skip 4-byte version/flags + 4-byte entry count.
								walkMP4Children(ssub[8:], func(entryType string, _ []byte) {
									if t.codec == "" {
										t.codec = friendlyMP4Codec(entryType)
									}
								})
							}
						})
					})
				}
			})
		}
	})
	return t
}

func friendlyMP4Codec(fourCC string) string {
	switch fourCC {
	case "avc1", "avc3":
		return "H.264"
	case "hvc1", "hev1":
		return "HEVC"
	case "vp09":
		return "VP9"
	case "av01":
		return "AV1"
	case "mp4a":
		return "AAC"
	case "Opus":
		return "Opus"
	default:
		return ""
	}
}

// FindVideoKeyFrame locates the real CN in-game companion *_KeyFrame.jpg
// thumbnail sitting beside an FFMpeg MP4 recording without modifying files.
// Observed CN naming:
//   - Video:    YY_MM_DD_HH_MM_<32hex>.mp4
//   - Keyframe: YY_MM_DD_HH_MM_SS_KeyFrame.jpg
func FindVideoKeyFrame(mp4Path string, mp4Mod time.Time, durationMS int64) string {
	dir := filepath.Dir(mp4Path)
	base := filepath.Base(mp4Path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))

	// Exact <stem>_KeyFrame.jpg match first.
	exact := filepath.Join(dir, stem+"_KeyFrame.jpg")
	if st, err := statRegularFile(exact); err == nil && !st.Reparse && !st.IsDir && st.Size > 0 && st.Size <= MaxImageBytes {
		return exact
	}

	// Prefix match on YY_MM_DD_HH_MM_ (first 15 chars, e.g. "26_11_24_16_49_").
	prefix := videoMinutePrefix(stem)
	if prefix == "" {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	targetStart := mp4Mod
	if durationMS > 0 && !mp4Mod.IsZero() {
		targetStart = mp4Mod.Add(-time.Duration(durationMS) * time.Millisecond)
	}
	bestPath := ""
	bestDelta := time.Duration(math.MaxInt64)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, "_keyframe.jpg") && !strings.HasSuffix(lower, "_keyframe.jpeg") && !strings.HasSuffix(lower, "_keyframe.png") {
			continue
		}
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		cand := filepath.Join(dir, name)
		st, serr := statRegularFile(cand)
		if serr != nil || st.Reparse || st.IsDir || st.Size <= 0 || st.Size > MaxImageBytes {
			continue
		}
		delta := st.ModifiedAt.Sub(targetStart)
		if delta < 0 {
			delta = -delta
		}
		if bestPath == "" || delta < bestDelta {
			bestPath = cand
			bestDelta = delta
		}
	}
	return bestPath
}

func videoMinutePrefix(stem string) string {
	// Match "DD_DD_DD_DD_DD_" (15 bytes).
	if len(stem) < 15 {
		return ""
	}
	p := stem[:15]
	for _, idx := range []int{2, 5, 8, 11, 14} {
		if p[idx] != '_' {
			return ""
		}
	}
	for _, idx := range []int{0, 1, 3, 4, 6, 7, 9, 10, 12, 13} {
		if p[idx] < '0' || p[idx] > '9' {
			return ""
		}
	}
	return p
}
