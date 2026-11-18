// SPDX-License-Identifier: GPL-3.0-or-later

// Package launchsettings persists only non-secret launcher choices. Consumers
// must revalidate an installation and compare RiskVersion before launching it.
package launchsettings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Settings stores launcher preferences; account state is stored separately.
// RiskVersion is an opaque, bounded UI consent revision, not authentication.
// SkippedVersion is an update the user chose to skip. Network is stored as
// bounded text here; its meaning is validated by the network layer.
type Settings struct {
	GameRoot       string
	RiskVersion    string
	SkippedVersion string
	Network        Network
	Downloads      Downloads
}

// Downloads holds the transfer settings. The zero value selects defaults.
type Downloads struct {
	FilesInFlight      int  `json:"filesInFlight"`
	ConnectionsPerFile int  `json:"connectionsPerFile"`
	RateLimitKiB       int  `json:"rateLimitKiB"`
	AutoResume         bool `json:"autoResume"`
	CacheLimitGiB      int  `json:"cacheLimitGiB"`
	PatchApply         bool `json:"patchApply"`
}

func DefaultDownloads() Downloads {
	return Downloads{FilesInFlight: 4, ConnectionsPerFile: 4, AutoResume: true, PatchApply: true}
}

// Network is the persisted network choice. It never carries credentials.
type Network struct {
	Proxy        string `json:"proxy"`
	ProxyURL     string `json:"proxyURL"`
	Mirror       bool   `json:"mirror"`
	MirrorPrefix string `json:"mirrorPrefix"`
}

// Store is an app-owned, file-backed settings store. Use one Store per path:
// operations on that instance serialize, but there is no cross-process lock.
// Path checks are conservative snapshots, not hostile path-swap protection.
type Store struct {
	file       string
	operations chan struct{}
}

// NewStore selects an absolute app-owned settings file, not a game file. It
// validates syntax without creating directories or reading user configuration.
func NewStore(absoluteFile string) (*Store, error) {
	if err := validPathSyntax(absoluteFile); err != nil {
		return nil, err
	}
	file := filepath.Clean(absoluteFile)
	if filepath.Dir(file) == file {
		return nil, errors.New("launch settings require a file, not a volume root")
	}
	s := &Store{file: file, operations: make(chan struct{}, 1)}
	s.operations <- struct{}{}
	return s, nil
}

func (s *Store) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.operations:
		if err := ctx.Err(); err != nil {
			s.operations <- struct{}{}
			return nil, err
		}
		return func() { s.operations <- struct{}{} }, nil
	}
}

// Load returns zero Settings when the selected file is missing. Invalid,
// oversized or unsupported files return an error and are never reset.
func (s *Store) Load(ctx context.Context) (Settings, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return Settings{}, err
	}
	defer release()
	return s.load(ctx)
}

func (s *Store) load(ctx context.Context) (Settings, error) {
	if err := checkLocalPath(ctx, s.file, false); err != nil {
		return Settings{}, err
	}
	file, err := os.Open(s.file)
	if errors.Is(err, os.ErrNotExist) {
		return Settings{}, ctx.Err()
	}
	if err != nil {
		return Settings{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Settings{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxDocumentBytes {
		return Settings{}, errors.New("launch settings must be a regular file no larger than 16 KiB")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxDocumentBytes+1))
	if err != nil {
		return Settings{}, err
	}
	if len(data) > maxDocumentBytes {
		return Settings{}, errors.New("launch settings exceed 16 KiB")
	}
	settings, err := decode(data)
	if err != nil {
		return Settings{}, err
	}
	if err := validateSettings(ctx, &settings); err != nil {
		return Settings{}, err
	}
	if err := ctx.Err(); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

const maxDocumentBytes = 16 * 1024

func decode(data []byte) (Settings, error) {
	if !utf8.Valid(data) {
		return Settings{}, errors.New("launch settings must be UTF-8")
	}
	fields, err := decodeObject(data)
	if err != nil {
		return Settings{}, err
	}
	invalid := errors.New("invalid or unsupported launch settings schema")
	var version int
	var root, risk *string
	if json.Unmarshal(fields["version"], &version) != nil ||
		json.Unmarshal(fields["gameRoot"], &root) != nil || root == nil ||
		json.Unmarshal(fields["riskVersion"], &risk) != nil || risk == nil {
		return Settings{}, invalid
	}
	settings := Settings{GameRoot: *root, RiskVersion: *risk, Downloads: DefaultDownloads()}
	if version == 1 && len(fields) == 3 {
		return settings, nil
	}
	if !(version == 2 && len(fields) == 5 || version == 3 && len(fields) == 6) {
		return Settings{}, invalid
	}
	var skipped *string
	if json.Unmarshal(fields["skippedVersion"], &skipped) != nil || skipped == nil {
		return Settings{}, invalid
	}
	network := json.NewDecoder(bytes.NewReader(fields["network"]))
	network.DisallowUnknownFields()
	var n *Network
	if network.Decode(&n) != nil || n == nil {
		return Settings{}, invalid
	}
	settings.SkippedVersion, settings.Network = *skipped, *n
	if version == 3 {
		settings.Downloads, err = decodeDownloads(fields["downloads"])
		if err != nil {
			return Settings{}, err
		}
	}
	return settings, nil
}

func decodeObject(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, errors.New("expected a launch settings JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := key.(string)
		if !ok {
			return nil, errors.New("invalid launch settings field")
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, fmt.Errorf("duplicate launch settings field %q", name)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing launch settings data")
	}
	return fields, nil
}

func decodeDownloads(data []byte) (Downloads, error) {
	fields, err := decodeObject(data)
	if err != nil {
		return Downloads{}, err
	}
	invalid := errors.New("invalid downloads settings")
	d := DefaultDownloads()
	for name, dest := range map[string]*int{
		"filesInFlight": &d.FilesInFlight, "connectionsPerFile": &d.ConnectionsPerFile, "rateLimitKiB": &d.RateLimitKiB,
	} {
		var n *int
		if json.Unmarshal(fields[name], &n) != nil || n == nil {
			return Downloads{}, invalid
		}
		*dest = *n
		delete(fields, name)
	}
	if raw, ok := fields["cacheLimitGiB"]; ok {
		var limit *int
		if json.Unmarshal(raw, &limit) != nil || limit == nil {
			return Downloads{}, invalid
		}
		d.CacheLimitGiB = *limit
		delete(fields, "cacheLimitGiB")
	}
	for name, value := range map[string]*bool{"autoResume": &d.AutoResume, "patchApply": &d.PatchApply} {
		if raw, ok := fields[name]; ok {
			var enabled *bool
			if json.Unmarshal(raw, &enabled) != nil || enabled == nil {
				return Downloads{}, invalid
			}
			*value = *enabled
			delete(fields, name)
		}
	}
	if len(fields) != 0 || !validDownloads(d) {
		return Downloads{}, invalid
	}
	return d, nil
}

func validDownloads(d Downloads) bool {
	return d.FilesInFlight >= 1 && d.FilesInFlight <= 8 && d.ConnectionsPerFile >= 1 && d.ConnectionsPerFile <= 16 &&
		d.RateLimitKiB >= 0 && d.RateLimitKiB <= 1048576 && d.CacheLimitGiB >= 0 && d.CacheLimitGiB <= 1<<30
}

type document struct {
	Version        int       `json:"version"`
	GameRoot       string    `json:"gameRoot"`
	RiskVersion    string    `json:"riskVersion"`
	SkippedVersion string    `json:"skippedVersion"`
	Network        Network   `json:"network"`
	Downloads      Downloads `json:"downloads"`
}

// Save stages and syncs a bounded document, then replaces the directory entry
// without first deleting the previous file. A canceled context is checked before
// committing; cancellation concurrent with the rename cannot undo that commit.
// Unknown/corrupt old files are retained. This is not a power-loss transaction.
func (s *Store) Save(ctx context.Context, settings Settings) error {
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	if _, err := s.load(ctx); err != nil {
		return fmt.Errorf("previous launch settings retained: %w", err)
	}
	return s.save(ctx, settings)
}

// Update loads the current settings, lets change edit them and saves the
// result as one serialized step, so independent features never overwrite each
// other's fields. Nothing is written when the edited settings are invalid.
func (s *Store) Update(ctx context.Context, change func(*Settings)) error {
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	current, err := s.load(ctx)
	if err != nil {
		return fmt.Errorf("previous launch settings retained: %w", err)
	}
	change(&current)
	return s.save(ctx, current)
}

func (s *Store) save(ctx context.Context, settings Settings) (result error) {
	if err := validateSettings(ctx, &settings); err != nil {
		return err
	}
	data, err := json.Marshal(document{Version: 3, GameRoot: settings.GameRoot, RiskVersion: settings.RiskVersion, SkippedVersion: settings.SkippedVersion, Network: settings.Network, Downloads: settings.Downloads})
	if err != nil {
		return err
	}
	if len(data) > maxDocumentBytes {
		return errors.New("launch settings exceed 16 KiB")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	parent := filepath.Dir(s.file)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	if err := checkLocalPath(ctx, s.file, false); err != nil {
		return err
	}
	stage, err := os.CreateTemp(parent, ".launch-*.tmp")
	if err != nil {
		return err
	}
	stagedName := stage.Name()
	committed := false
	defer func() {
		_ = stage.Close()
		if !committed {
			if err := os.Remove(stagedName); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, fmt.Errorf("remove owned launch settings stage: %w", err))
			}
		}
	}()
	if _, err := stage.Write(data); err != nil {
		return err
	}
	if err := stage.Sync(); err != nil {
		return err
	}
	if err := stage.Close(); err != nil {
		return err
	}
	if err := checkLocalPath(ctx, s.file, false); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := replaceFile(stagedName, s.file); err != nil {
		return err
	}
	committed = true
	return nil
}

func validateSettings(ctx context.Context, settings *Settings) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if settings.Downloads == (Downloads{}) {
		settings.Downloads = DefaultDownloads()
	}
	if !validDownloads(settings.Downloads) {
		return errors.New("download preferences are out of range")
	}
	if !boundedText(settings.RiskVersion, 128) {
		return errors.New("risk version must be at most 128 UTF-8 bytes without control characters")
	}
	n := settings.Network
	if !boundedText(settings.SkippedVersion, 64) || !boundedText(n.Proxy, 16) || !boundedText(n.ProxyURL, 512) || !boundedText(n.MirrorPrefix, 512) {
		return errors.New("update and network choices must be short UTF-8 text without control characters")
	}
	if settings.GameRoot == "" {
		if settings.RiskVersion != "" {
			return errors.New("risk consent requires a game root")
		}
		return nil
	}
	if err := validPathSyntax(settings.GameRoot); err != nil {
		return err
	}
	return checkLocalPath(ctx, filepath.Clean(settings.GameRoot), true)
}

func boundedText(s string, limit int) bool {
	return utf8.ValidString(s) && len(s) <= limit && strings.IndexFunc(s, unicode.IsControl) < 0
}

func validPathSyntax(path string) error {
	path = filepath.FromSlash(path)
	if len(path) < 3 || len(path) > 32768 || !utf8.ValidString(path) || path[1] != ':' || path[2] != '\\' ||
		!((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) {
		return errors.New("expected an ordinary absolute local Windows path")
	}
	for _, part := range strings.Split(path[3:], `\`) {
		if strings.ContainsAny(part, ":*?<>|\"") || strings.IndexFunc(part, unicode.IsControl) >= 0 || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return errors.New("unsupported launch settings path component")
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		letters := []rune(base)
		portDevice := len(letters) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && strings.ContainsRune("123456789¹²³", letters[3])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || base == "CONIN$" || base == "CONOUT$" || portDevice {
			return errors.New("reserved Windows device component")
		}
	}
	return nil
}
