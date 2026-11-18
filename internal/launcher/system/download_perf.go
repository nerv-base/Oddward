// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"golang.org/x/sys/windows"
)

const (
	perfProfileVersion    = 1
	perfProfileMaxBytes   = 32 << 10
	perfProfileMaxVolumes = 16
	perfProfileMaxRate    = 1 << 40 // B/s; rejects corrupt or impossible measurements.
)

var ErrPerfProfile = errors.New("download performance profile unavailable or invalid")

// PerfRates are planning estimates in bytes/second, never UI speed measurements.
type PerfRates struct{ NetBytesPerSecond, HashBytesPerSecond, IOBytesPerSecond int64 }

// PerfSample contains actual counter deltas and the matching active phase wall
// durations. Network includes retry bytes. Rebuild I/O counts whole verified
// source reads and output writes to match the planner's estimated workload.
// Overlapping operations must use their aggregate bytes and shared wall duration,
// not sum per-worker times. Zero duration with zero bytes means absent.
type PerfSample struct {
	NetworkBytes    int64
	NetworkDuration time.Duration
	HashReadBytes   int64
	HashDuration    time.Duration
	RebuildIOBytes  int64
	RebuildDuration time.Duration
}

// PerfProfile stores global network history and independent volume disk history.
// Volume keys are lowercase SHA256 identifiers, never raw paths or URLs. Slice
// order is least-recently-updated first; at most 16 volumes are retained.
type PerfProfile struct {
	Version           int          `json:"version"`
	NetBytesPerSecond int64        `json:"netBytesPerSecond,omitempty"`
	Volumes           []PerfVolume `json:"volumes,omitempty"`
}
type PerfVolume struct {
	Key                string `json:"key"`
	HashBytesPerSecond int64  `json:"hashBytesPerSecond,omitempty"`
	IOBytesPerSecond   int64  `json:"ioBytesPerSecond,omitempty"`
}

func (p PerfProfile) Rates(volumeKey string) PerfRates {
	rates := PerfRates{900000, 367 << 20, 939 << 20}
	if p.NetBytesPerSecond > 0 && p.NetBytesPerSecond <= perfProfileMaxRate {
		rates.NetBytesPerSecond = p.NetBytesPerSecond
	}
	for _, v := range p.Volumes {
		if v.Key == volumeKey {
			if v.HashBytesPerSecond > 0 && v.HashBytesPerSecond <= perfProfileMaxRate {
				rates.HashBytesPerSecond = v.HashBytesPerSecond
			}
			if v.IOBytesPerSecond > 0 && v.IOBytesPerSecond <= perfProfileMaxRate {
				rates.IOBytesPerSecond = v.IOBytesPerSecond
			}
			break
		}
	}
	return rates
}

// LoadPerfProfile treats missing state as defaults. Invalid state returns an
// empty profile plus a path-free error; callers can continue using Rates.
func LoadPerfProfile(ctx context.Context, stateRoot string) (PerfProfile, error) {
	empty := PerfProfile{Version: perfProfileVersion}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if stateRoot == "" {
		return empty, nil
	}
	dir := filepath.Join(stateRoot, "download-perf")
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if _, err := downloadDirectory(dir, false); err != nil {
		return empty, ErrPerfProfile
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return empty, ErrPerfProfile
	}
	defer root.Close()
	p, err := loadPerfRoot(root)
	if err != nil {
		return empty, ErrPerfProfile
	}
	if err = ctx.Err(); err != nil {
		return empty, err
	}
	return p, nil
}
func loadPerfRoot(root *os.Root) (PerfProfile, error) {
	empty := PerfProfile{Version: perfProfileVersion}
	fi, err := root.Lstat("profile.json")
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil || !fi.Mode().IsRegular() || downloadReparse(fi) || fi.Size() > perfProfileMaxBytes {
		return empty, ErrPerfProfile
	}
	f, err := root.Open("profile.json")
	if err != nil {
		return empty, err
	}
	defer f.Close()
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil || info.NumberOfLinks != 1 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return empty, ErrPerfProfile
	}
	data, err := io.ReadAll(io.LimitReader(f, perfProfileMaxBytes+1))
	if err != nil || len(data) > perfProfileMaxBytes {
		return empty, ErrPerfProfile
	}
	if !perfUniqueJSON(json.NewDecoder(bytes.NewReader(data)), 0) {
		return empty, ErrPerfProfile
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var p PerfProfile
	if err = decoder.Decode(&p); err != nil {
		return empty, ErrPerfProfile
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return empty, ErrPerfProfile
	}
	if !validPerfProfile(p) {
		return empty, ErrPerfProfile
	}
	return p, nil
}

// Reject JSON duplicate names and nulls before decoding the bounded schema.
// encoding/json otherwise silently accepts both, which hides ambiguous state.
func perfUniqueJSON(d *json.Decoder, depth int) bool {
	if depth > 5 {
		return false
	}
	token, err := d.Token()
	if err != nil || token == nil {
		return false
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return true
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] || len(seen) >= 8 || !perfJSONField(depth, name) {
				return false
			}
			seen[name] = true
			if !perfUniqueJSON(d, depth+1) {
				return false
			}
		}
	case '[':
		count := 0
		for d.More() {
			count++
			if count > perfProfileMaxVolumes || !perfUniqueJSON(d, depth+1) {
				return false
			}
		}
	default:
		return false
	}
	end, err := d.Token()
	return err == nil && (delim == '{' && end == json.Delim('}') || delim == '[' && end == json.Delim(']'))
}

func perfJSONField(depth int, name string) bool {
	if depth == 0 {
		return name == "version" || name == "netBytesPerSecond" || name == "volumes"
	}
	if depth == 2 {
		return name == "key" || name == "hashBytesPerSecond" || name == "ioBytesPerSecond"
	}
	return false
}

func validPerfKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	for _, r := range key {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func validPerfRate(rate int64) bool { return rate >= 0 && rate <= perfProfileMaxRate }
func validPerfProfile(p PerfProfile) bool {
	if p.Version != perfProfileVersion || !validPerfRate(p.NetBytesPerSecond) || len(p.Volumes) > perfProfileMaxVolumes {
		return false
	}
	seen := map[string]bool{}
	for _, v := range p.Volumes {
		if !validPerfKey(v.Key) || seen[v.Key] || !validPerfRate(v.HashBytesPerSecond) || !validPerfRate(v.IOBytesPerSecond) {
			return false
		}
		seen[v.Key] = true
	}
	return true
}
func perfSampleRate(n int64, d time.Duration) (int64, error) {
	if n < 0 || d < 0 || d > 7*24*time.Hour || n > 1<<60 || n > 0 && d == 0 {
		return 0, ErrPerfProfile
	}
	if n == 0 || d < 20*time.Second {
		return 0, nil
	}
	rate := float64(n) / d.Seconds()
	if rate < 1 || rate > perfProfileMaxRate {
		return 0, ErrPerfProfile
	}
	return int64(rate), nil
}
func perfEMA(old, sample int64) int64 { return old + (sample-old)/4 }

// RecordPerfSample updates independent >=20-second phase samples with EMA
// alpha=1/4, seeded from defaults. A kernel lock serializes read/modify/replace
// across backend instances and processes. Cancellation never publishes a stage.
func RecordPerfSample(ctx context.Context, stateRoot, volumeKey string, sample PerfSample) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	net, err := perfSampleRate(sample.NetworkBytes, sample.NetworkDuration)
	if err != nil {
		return err
	}
	hash, err := perfSampleRate(sample.HashReadBytes, sample.HashDuration)
	if err != nil {
		return err
	}
	rebuild, err := perfSampleRate(sample.RebuildIOBytes, sample.RebuildDuration)
	if err != nil {
		return err
	}
	if net == 0 && hash == 0 && rebuild == 0 {
		return nil
	}
	if stateRoot == "" || !validPerfKey(volumeKey) {
		return ErrPerfProfile
	}
	if _, err = downloadDirectory(stateRoot, false); err != nil {
		return ErrPerfProfile
	}
	state, err := os.OpenRoot(stateRoot)
	if err != nil {
		return ErrPerfProfile
	}
	defer state.Close()
	if err = state.Mkdir("download-perf", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return ErrPerfProfile
	}
	dir := filepath.Join(stateRoot, "download-perf")
	if _, err = downloadDirectory(dir, false); err != nil {
		return ErrPerfProfile
	}
	var lease *gamedl.Lease
	for attempt := 0; attempt < 400; attempt++ {
		lease, err = gamedl.Acquire(dir)
		if err == nil {
			break
		}
		var taskErr *gamedl.TaskError
		if !errors.As(err, &taskErr) || taskErr.Kind != gamedl.ErrLocked {
			return ErrPerfProfile
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	if err != nil {
		return ErrPerfProfile
	}
	defer lease.Close()
	root, err := os.OpenRoot(dir)
	if err != nil {
		return ErrPerfProfile
	}
	defer root.Close()
	p, err := loadPerfRoot(root)
	if err != nil {
		return ErrPerfProfile
	}
	rates := p.Rates(volumeKey)
	if net > 0 {
		p.NetBytesPerSecond = perfEMA(rates.NetBytesPerSecond, net)
	}
	if hash > 0 || rebuild > 0 {
		v := PerfVolume{Key: volumeKey}
		for i, old := range p.Volumes {
			if old.Key == volumeKey {
				v = old
				p.Volumes = append(p.Volumes[:i], p.Volumes[i+1:]...)
				break
			}
		}
		if hash > 0 {
			v.HashBytesPerSecond = perfEMA(rates.HashBytesPerSecond, hash)
		}
		if rebuild > 0 {
			v.IOBytesPerSecond = perfEMA(rates.IOBytesPerSecond, rebuild)
		}
		if len(p.Volumes) == perfProfileMaxVolumes {
			p.Volumes = p.Volumes[1:]
		}
		p.Volumes = append(p.Volumes, v)
	}
	data, err := json.Marshal(p)
	if err != nil {
		return ErrPerfProfile
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return ErrPerfProfile
	}
	stage := "profile-" + hex.EncodeToString(nonce[:]) + ".tmp"
	f, err := root.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ErrPerfProfile
	}
	defer root.Remove(stage)
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if errors.Join(writeErr, syncErr, closeErr) != nil {
		return ErrPerfProfile
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	from, _ := windows.UTF16PtrFromString(filepath.Join(dir, stage))
	to, _ := windows.UTF16PtrFromString(filepath.Join(dir, "profile.json"))
	if err = windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return ErrPerfProfile
	}
	return nil
}

// downloadPerfVolumeKey binds disk measurements to the actual Windows volume
// GUID, so drive-letter changes do not transfer history to a different disk.
func downloadPerfVolumeKey(root string) (string, error) {
	if !filepath.IsAbs(root) || strings.ContainsAny(root, "\x00\r\n") {
		return "", ErrPerfProfile
	}
	path, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return "", ErrPerfProfile
	}
	mount := make([]uint16, windows.MAX_PATH+1)
	if err = windows.GetVolumePathName(path, &mount[0], uint32(len(mount))); err != nil {
		return "", ErrPerfProfile
	}
	guid := make([]uint16, windows.MAX_PATH+1)
	if err = windows.GetVolumeNameForVolumeMountPoint(&mount[0], &guid[0], uint32(len(guid))); err != nil {
		return "", ErrPerfProfile
	}
	sum := sha256.Sum256([]byte(strings.ToUpper(windows.UTF16ToString(guid))))
	return hex.EncodeToString(sum[:]), nil
}
func (b *GameDownloadBackend) downloadPerfRates(ctx context.Context, root string) (PerfRates, error) {
	p, err := LoadPerfProfile(ctx, b.StateRoot)
	key, keyErr := downloadPerfVolumeKey(root)
	if err != nil {
		return p.Rates(key), err
	}
	return p.Rates(key), keyErr
}
func (b *GameDownloadBackend) recordDownloadPerf(ctx context.Context, root string, sample PerfSample) error {
	key, err := downloadPerfVolumeKey(root)
	if err != nil {
		return err
	}
	return RecordPerfSample(ctx, b.StateRoot, key, sample)
}
