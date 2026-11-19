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
	UI             UI
}

// UI holds launcher window and theming choices. Both default to on for new
// and older profiles.
type UI struct {
	CloseToTray  bool `json:"closeToTray"`
	AccentFollow bool `json:"accentFollow"`
	CheckUpdates bool `json:"checkUpdates"`
}

func DefaultUI() UI {
	return UI{CloseToTray: true, AccentFollow: true, CheckUpdates: true}
}

// Downloads holds the transfer settings the user can still choose. Parallel
// transfers, cache limits, automatic resume and patch application are fixed
// engine behavior now, so they are no longer stored.
type Downloads struct {
	RateLimitKiB int `json:"rateLimitKiB"`
}

func DefaultDownloads() Downloads {
	return Downloads{}
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
		// A fresh profile starts from the same defaults the UI shows.
		return Settings{Downloads: DefaultDownloads(), UI: DefaultUI()}, ctx.Err()
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
	settings := Settings{GameRoot: *root, RiskVersion: *risk, Downloads: DefaultDownloads(), UI: DefaultUI()}
	if version == 1 && len(fields) == 3 {
		return settings, nil
	}
	if !(version == 2 && len(fields) == 5 || version == 3 && len(fields) == 6 || version == 4 && len(fields) == 6 || version == 5 && len(fields) == 7 || version == 6 && len(fields) == 7) {
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
		settings.Downloads, err = decodeDownloadsV3(fields["downloads"])
		if err != nil {
			return Settings{}, err
		}
	}
	if version == 4 {
		settings.Downloads, err = decodeDownloads(fields["downloads"])
		if err != nil {
			return Settings{}, err
		}
	}
	if version >= 5 {
		settings.Downloads, err = decodeDownloads(fields["downloads"])
		if err != nil {
			return Settings{}, err
		}
		settings.UI, err = decodeUI(fields["ui"], version)
		if err != nil {
			return Settings{}, err
		}
	}
	return settings, nil
}

func decodeUI(data []byte, version int) (UI, error) {
	fields, err := decodeObject(data)
	if err != nil {
		return UI{}, err
	}
	invalid := errors.New("invalid launcher ui settings")
	var closeToTray, accentFollow *bool
	if json.Unmarshal(fields["closeToTray"], &closeToTray) != nil || closeToTray == nil {
		return UI{}, invalid
	}
	if json.Unmarshal(fields["accentFollow"], &accentFollow) != nil || accentFollow == nil {
		return UI{}, invalid
	}
	delete(fields, "closeToTray")
	delete(fields, "accentFollow")
	ui := UI{CloseToTray: *closeToTray, AccentFollow: *accentFollow, CheckUpdates: true}
	if version >= 6 {
		var checkUpdates *bool
		if json.Unmarshal(fields["checkUpdates"], &checkUpdates) != nil || checkUpdates == nil {
			return UI{}, invalid
		}
		delete(fields, "checkUpdates")
		ui.CheckUpdates = *checkUpdates
	}
	if len(fields) != 0 {
		return UI{}, invalid
	}
	return ui, nil
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
	var rate *int
	if json.Unmarshal(fields["rateLimitKiB"], &rate) != nil || rate == nil {
		return Downloads{}, invalid
	}
	d.RateLimitKiB = *rate
	delete(fields, "rateLimitKiB")
	if len(fields) != 0 || !validDownloads(d) {
		return Downloads{}, invalid
	}
	return d, nil
}

// decodeDownloadsV3 reads the pre-v4 document, which also stored parallel
// transfer counts, a cache limit and engine switches. Those values are fixed
// defaults now, so only the user's rate limit survives the migration.
func decodeDownloadsV3(data []byte) (Downloads, error) {
	fields, err := decodeObject(data)
	if err != nil {
		return Downloads{}, err
	}
	invalid := errors.New("invalid downloads settings")
	var files, connections, rate *int
	for name, dest := range map[string]**int{
		"filesInFlight": &files, "connectionsPerFile": &connections, "rateLimitKiB": &rate,
	} {
		if json.Unmarshal(fields[name], dest) != nil || *dest == nil {
			return Downloads{}, invalid
		}
		delete(fields, name)
	}
	if *files < 1 || *files > 8 || *connections < 1 || *connections > 16 {
		return Downloads{}, invalid
	}
	if raw, ok := fields["cacheLimitGiB"]; ok {
		var limit *int
		if json.Unmarshal(raw, &limit) != nil || limit == nil || *limit < 0 || *limit > 1<<30 {
			return Downloads{}, invalid
		}
		delete(fields, "cacheLimitGiB")
	}
	for name := range map[string]bool{"autoResume": true, "patchApply": true} {
		if raw, ok := fields[name]; ok {
			var enabled *bool
			if json.Unmarshal(raw, &enabled) != nil || enabled == nil {
				return Downloads{}, invalid
			}
			delete(fields, name)
		}
	}
	d := Downloads{RateLimitKiB: *rate}
	if len(fields) != 0 || !validDownloads(d) {
		return Downloads{}, invalid
	}
	return d, nil
}

func validDownloads(d Downloads) bool {
	return d.RateLimitKiB >= 0 && d.RateLimitKiB <= 1048576
}

type document struct {
	Version        int       `json:"version"`
	GameRoot       string    `json:"gameRoot"`
	RiskVersion    string    `json:"riskVersion"`
	SkippedVersion string    `json:"skippedVersion"`
	Network        Network   `json:"network"`
	Downloads      Downloads `json:"downloads"`
	UI             UI        `json:"ui"`
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
	data, err := json.Marshal(document{Version: 6, GameRoot: settings.GameRoot, RiskVersion: settings.RiskVersion, SkippedVersion: settings.SkippedVersion, Network: settings.Network, Downloads: settings.Downloads, UI: settings.UI})
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
