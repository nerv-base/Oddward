// SPDX-License-Identifier: GPL-3.0-or-later

// Package launchsettings persists only non-secret launcher choices. Consumers
// must revalidate an installation and compare RiskVersion before launching it.
//
// The launch_settings row is authoritative. The legacy launcher-v1.json file is
// kept only so a build that the updater rolled back to still finds the choices
// this one accepted: every write rewrites it as a mirror, and the first start of
// a newer build reconciles the file back into the row before using it.
package launchsettings

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nerv-base/Oddward/internal/database"
)

// Domain is this package's domain in migration_state and compat_mirrors.
const Domain = "settings"

// ImportVersion is the importer generation recorded for Domain. Bump it when
// ImportLegacy learns to read a legacy document it did not read before.
const ImportVersion = 1

// currentVersion is the document version this build writes to the mirror and
// records as source_version for its own rows.
const currentVersion = 6

// maxDocumentBytes bounds the legacy document in both directions: a rolled-back
// build wrote it with the same buffer, so an oversized file is refused instead of
// being truncated or silently half-migrated.
const maxDocumentBytes = 16 * 1024

// settingsID is the only launch_settings row: settings are one document whose
// sections independent panels edit, so they share one row.
const settingsID = 1

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

// UI holds launcher window and theming choices. All three default to on for new
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

// Store owns the launch_settings row and the legacy file that mirrors it. Use one
// Store per database: operations on that instance serialize, but a second
// instance opened on the same database can still lose a read-modify-write,
// because only this instance's Update serializes its own read with its write.
// Path checks are conservative snapshots, not hostile path-swap protection.
type Store struct {
	db         *database.DB
	file       string
	operations chan struct{}
}

// NewStore binds the settings row to the legacy mirror file at absoluteFile. It
// validates the path syntax without creating directories or reading user data.
func NewStore(db *database.DB, absoluteFile string) (*Store, error) {
	if db == nil {
		return nil, errors.New("launch settings require an open database")
	}
	if err := validPathSyntax(absoluteFile); err != nil {
		return nil, err
	}
	file := filepath.Clean(absoluteFile)
	if filepath.Dir(file) == file {
		return nil, errors.New("launch settings require a file, not a volume root")
	}
	s := &Store{db: db, file: file, operations: make(chan struct{}, 1)}
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

// Load returns the stored settings. A database without a row yet is a fresh
// profile and yields the same defaults the UI shows. A row that cannot be read
// or validated is reported and never replaced by defaults.
func (s *Store) Load(ctx context.Context) (Settings, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return Settings{}, err
	}
	defer release()
	return s.load(ctx)
}

func (s *Store) load(ctx context.Context) (Settings, error) {
	settings, _, found, err := scanSettings(s.db.QueryRow(ctx, selectSettings, settingsID))
	if err != nil {
		return Settings{}, err
	}
	if !found {
		// A fresh profile starts from the same defaults the UI shows.
		return Settings{Downloads: DefaultDownloads(), UI: DefaultUI()}, nil
	}
	if err := validateSettings(ctx, &settings); err != nil {
		return Settings{}, err
	}
	if err := ctx.Err(); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

// selectSettings reads the one settings row. Import reads the same columns back
// inside its transaction, so the insert and the parity check cannot drift apart.
const selectSettings = `
	SELECT source_version, game_root, risk_version, skipped_version, proxy,
	       proxy_url, mirror, mirror_prefix, rate_limit_kib,
	       ui_close_to_tray, ui_accent_follow, ui_check_updates
	FROM launch_settings WHERE id = ?`

// scanSettings turns one row into Settings plus the legacy version that produced
// it. It reports found = false for the absent row of a fresh profile.
func scanSettings(row *sql.Row) (settings Settings, version int, found bool, err error) {
	var mirror, closeToTray, accentFollow, checkUpdates int
	err = row.Scan(&version, &settings.GameRoot, &settings.RiskVersion,
		&settings.SkippedVersion, &settings.Network.Proxy, &settings.Network.ProxyURL,
		&mirror, &settings.Network.MirrorPrefix, &settings.Downloads.RateLimitKiB,
		&closeToTray, &accentFollow, &checkUpdates)
	if errors.Is(err, sql.ErrNoRows) {
		return Settings{}, 0, false, nil
	}
	if err != nil {
		return Settings{}, 0, false, err
	}
	// The schema constrains every flag column to 0 or 1.
	settings.Network.Mirror = mirror == 1
	settings.UI = UI{CloseToTray: closeToTray == 1, AccentFollow: accentFollow == 1, CheckUpdates: checkUpdates == 1}
	return settings, version, true, nil
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

// encode produces the canonical version 6 document, with the same field order and
// encoding the file build wrote, so a rolled-back build reads exactly what it
// wrote itself.
func encode(settings Settings) ([]byte, error) {
	data, err := json.Marshal(document{Version: currentVersion, GameRoot: settings.GameRoot, RiskVersion: settings.RiskVersion, SkippedVersion: settings.SkippedVersion, Network: settings.Network, Downloads: settings.Downloads, UI: settings.UI})
	if err != nil {
		return nil, err
	}
	if len(data) > maxDocumentBytes {
		return nil, errors.New("launch settings exceed 16 KiB")
	}
	return data, nil
}

// Save writes the whole settings document. The mirror file is replaced first and
// the row plus the mirror digest commit after it. When the mirror cannot be
// written nothing is accepted, so a rolled-back build never sees an older file
// and this build never keeps a value the older build would revert.
//
// A stored row that cannot be read or validated is never overwritten, so a
// damaged store keeps its data instead of silently becoming the defaults.
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

func (s *Store) save(ctx context.Context, settings Settings) error {
	if err := validateSettings(ctx, &settings); err != nil {
		return err
	}
	data, err := encode(settings)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// The mirror path is checked before the transaction so an unsafe path never
	// becomes the mirror of a committed row. The mirror itself is written before
	// the rows: if the file cannot be written nothing is accepted, so a later
	// start can never read an older file as an edit made by a rolled-back build.
	if err := checkLocalPath(ctx, s.file, false); err != nil {
		return err
	}
	return s.db.MirrorThenCommit(ctx, Domain, s.file, data, func(ctx context.Context, tx *sql.Tx) error {
		return putSettings(ctx, tx, settings, currentVersion, time.Now())
	})
}

// putSettings writes the single settings row. sourceVersion is the legacy
// document version the values were read from; this build writes currentVersion.
func putSettings(ctx context.Context, tx *sql.Tx, settings Settings, sourceVersion int, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO launch_settings(
			id, source_version, game_root, risk_version, skipped_version, proxy,
			proxy_url, mirror, mirror_prefix, rate_limit_kib,
			ui_close_to_tray, ui_accent_follow, ui_check_updates, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			source_version = excluded.source_version,
			game_root = excluded.game_root,
			risk_version = excluded.risk_version,
			skipped_version = excluded.skipped_version,
			proxy = excluded.proxy,
			proxy_url = excluded.proxy_url,
			mirror = excluded.mirror,
			mirror_prefix = excluded.mirror_prefix,
			rate_limit_kib = excluded.rate_limit_kib,
			ui_close_to_tray = excluded.ui_close_to_tray,
			ui_accent_follow = excluded.ui_accent_follow,
			ui_check_updates = excluded.ui_check_updates,
			updated_at = excluded.updated_at`,
		settingsID, sourceVersion, settings.GameRoot, settings.RiskVersion, settings.SkippedVersion,
		settings.Network.Proxy, settings.Network.ProxyURL, flag(settings.Network.Mirror), settings.Network.MirrorPrefix,
		settings.Downloads.RateLimitKiB, flag(settings.UI.CloseToTray), flag(settings.UI.AccentFollow), flag(settings.UI.CheckUpdates),
		database.Stamp(now))
	if err != nil {
		return fmt.Errorf("store launch settings: %w", err)
	}
	return nil
}

// flag converts a boolean to the 0/1 integer its column accepts.
func flag(value bool) int {
	if value {
		return 1
	}
	return 0
}

// ImportLegacy replaces the settings row with the legacy document's contents. It
// is the database.ImportFunc for Domain: idempotent, doing nothing when the file
// is absent, and recording the digest of the exact bytes it read without
// rewriting the file. A corrupt, unreadable or unsupported document fails the
// whole import, so neither the file nor the stored row changes.
func (s *Store) ImportLegacy(ctx context.Context) (string, string, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return s.file, "", err
	}
	defer release()
	detail, err := s.importLegacy(ctx)
	if err != nil {
		// The path names the file a rollback build changed; the migration record
		// keeps it, so a failed import can be traced back to that file.
		return s.file, "", fmt.Errorf("%s: %w", s.file, err)
	}
	return s.file, detail, nil
}

func (s *Store) importLegacy(ctx context.Context) (string, error) {
	data, err := readLegacy(ctx, s.file)
	if errors.Is(err, os.ErrNotExist) {
		return "no legacy launch settings file", nil
	}
	if err != nil {
		return "", err
	}
	settings, version, err := decode(data)
	if err != nil {
		return "", err
	}
	if err := validateSettings(ctx, &settings); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		// REPLACE semantics: the file is the whole domain, so whatever the rows
		// held before this import is stale by definition.
		if _, err := tx.ExecContext(ctx, "DELETE FROM launch_settings"); err != nil {
			return fmt.Errorf("replace launch settings: %w", err)
		}
		if err := putSettings(ctx, tx, settings, version, time.Now()); err != nil {
			return err
		}
		if err := s.db.RecordMirror(ctx, tx, Domain, s.file, database.Digest(data)); err != nil {
			return err
		}
		// Read the row back in the same transaction: a value the schema or the
		// scan lost rolls the import back instead of becoming authoritative.
		stored, storedVersion, found, err := scanSettings(tx.QueryRowContext(ctx, selectSettings, settingsID))
		if err != nil {
			return fmt.Errorf("read imported launch settings back: %w", err)
		}
		if !found || stored != settings || storedVersion != version {
			return errors.New("imported launch settings do not read back unchanged")
		}
		return nil
	}); err != nil {
		return "", err
	}
	return fmt.Sprintf("version %d document", version), nil
}

// readLegacy reads the bounded legacy document. Links, irregular files and hard
// links are refused before reading, because a later write replaces this path.
func readLegacy(ctx context.Context, path string) ([]byte, error) {
	if err := checkLocalPath(ctx, path, false); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxDocumentBytes {
		return nil, errors.New("launch settings must be a regular file no larger than 16 KiB")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDocumentBytes {
		return nil, errors.New("launch settings exceed 16 KiB")
	}
	return data, nil
}

// decode reads a legacy document of version 1 to 6 and reports which version it
// was, because the migrated row records the version that produced it.
func decode(data []byte) (Settings, int, error) {
	if !utf8.Valid(data) {
		return Settings{}, 0, errors.New("launch settings must be UTF-8")
	}
	fields, err := decodeObject(data)
	if err != nil {
		return Settings{}, 0, err
	}
	invalid := errors.New("invalid or unsupported launch settings schema")
	var version int
	var root, risk *string
	if json.Unmarshal(fields["version"], &version) != nil ||
		json.Unmarshal(fields["gameRoot"], &root) != nil || root == nil ||
		json.Unmarshal(fields["riskVersion"], &risk) != nil || risk == nil {
		return Settings{}, 0, invalid
	}
	settings := Settings{GameRoot: *root, RiskVersion: *risk, Downloads: DefaultDownloads(), UI: DefaultUI()}
	if version == 1 && len(fields) == 3 {
		return settings, version, nil
	}
	if !(version == 2 && len(fields) == 5 || version == 3 && len(fields) == 6 || version == 4 && len(fields) == 6 || version == 5 && len(fields) == 7 || version == 6 && len(fields) == 7) {
		return Settings{}, 0, invalid
	}
	var skipped *string
	if json.Unmarshal(fields["skippedVersion"], &skipped) != nil || skipped == nil {
		return Settings{}, 0, invalid
	}
	network := json.NewDecoder(bytes.NewReader(fields["network"]))
	network.DisallowUnknownFields()
	var n *Network
	if network.Decode(&n) != nil || n == nil {
		return Settings{}, 0, invalid
	}
	settings.SkippedVersion, settings.Network = *skipped, *n
	if version == 3 {
		settings.Downloads, err = decodeDownloadsV3(fields["downloads"])
		if err != nil {
			return Settings{}, 0, err
		}
	}
	if version == 4 {
		settings.Downloads, err = decodeDownloads(fields["downloads"])
		if err != nil {
			return Settings{}, 0, err
		}
	}
	if version >= 5 {
		settings.Downloads, err = decodeDownloads(fields["downloads"])
		if err != nil {
			return Settings{}, 0, err
		}
		settings.UI, err = decodeUI(fields["ui"], version)
		if err != nil {
			return Settings{}, 0, err
		}
	}
	return settings, version, nil
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
