// SPDX-License-Identifier: GPL-3.0-or-later

package gacha

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Store serializes operations using one cancelable lock per directory. Use only
// one Store per directory in a process; there is no cross-process lock. Path
// checks are conservative snapshots, not a hostile concurrent path-swap jail.
type Store struct {
	dir        string
	operations chan struct{}
}

// NewStore validates an ordinary absolute local Windows directory without I/O.
// The directory must be app-owned, separate from the user's game installation.
func NewStore(dir string) (*Store, error) {
	if err := validPathSyntax(dir); err != nil {
		return nil, err
	}
	dir = filepath.Clean(dir)
	if filepath.Dir(dir) == dir {
		return nil, errors.New("gacha directory must not be a volume root")
	}
	s := &Store{dir: dir, operations: make(chan struct{}, 1)}
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

func (s *Store) filename(roleID string) (string, error) {
	if err := validateRole(roleID); err != nil {
		return "", err
	}
	return filepath.Join(s.dir, roleID+".json"), nil
}

// List returns lexicographically sorted role IDs without decoding file contents.
// A missing directory is an empty list. Non-archive and unsafe entries are ignored.
func (s *Store) List(ctx context.Context) ([]string, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := checkLocalPath(ctx, s.dir, true); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if validateRole(id) != nil || !entry.Type().IsRegular() {
			continue
		}
		if err := checkLocalPath(ctx, filepath.Join(s.dir, name), false); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, ctx.Err()
}

// Load returns an os.ErrNotExist-compatible error for missing files. Decode and
// size failures wrap ErrCorrupt and never change the existing bytes.
func (s *Store) Load(ctx context.Context, roleID string) (Archive, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return Archive{}, err
	}
	defer release()
	return s.load(ctx, roleID)
}

func (s *Store) load(ctx context.Context, roleID string) (Archive, error) {
	name, err := s.filename(roleID)
	if err != nil {
		return Archive{}, err
	}
	if err := checkLocalPath(ctx, name, false); err != nil {
		return Archive{}, err
	}
	file, err := os.Open(name)
	if err != nil {
		return Archive{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Archive{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxDocumentBytes {
		return Archive{}, fmt.Errorf("%w: expected regular archive no larger than 8 MiB", ErrCorrupt)
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxDocumentBytes+1))
	if err != nil {
		return Archive{}, err
	}
	if err := ctx.Err(); err != nil {
		return Archive{}, err
	}
	imported, err := DecodeImport(data)
	if err != nil {
		return Archive{}, fmt.Errorf("%w: %w", ErrCorrupt, err)
	}
	if imported.Format != "oddward-gacha" || imported.Archive.RoleID != roleID {
		return Archive{}, fmt.Errorf("%w: archive format or role differs from filename", ErrCorrupt)
	}
	return imported.Archive, nil
}

// Save validates and serializes before staging, and refuses to replace corrupt
// prior archives. Sync plus same-directory MoveFileEx is not a guarantee of
// complete power-loss recovery. Cancellation concurrent with commit cannot undo it.
func (s *Store) Save(ctx context.Context, a Archive) (result error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	data, err := ExportOddward(a, time.Now().UTC())
	if err != nil {
		return err
	}
	name, err := s.filename(a.RoleID)
	if err != nil {
		return err
	}
	if _, err := s.load(ctx, a.RoleID); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("previous gacha archive retained: %w", err)
	}
	if err := checkLocalPath(ctx, name, false); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	if err := checkLocalPath(ctx, name, false); err != nil {
		return err
	}
	stage, err := os.CreateTemp(s.dir, ".gacha-*.tmp")
	if err != nil {
		return err
	}
	stagedName := stage.Name()
	committed := false
	defer func() {
		_ = stage.Close()
		if !committed {
			if err := os.Remove(stagedName); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, fmt.Errorf("remove owned gacha stage: %w", err))
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
	if err := checkLocalPath(ctx, name, false); err != nil {
		return err
	}
	if err := checkLocalPath(ctx, stagedName, false); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := replaceFile(stagedName, name); err != nil {
		return err
	}
	committed = true
	return nil
}

// Delete removes only the selected local role archive, including corrupt files.
// Caller-owned UI confirmation is required outside this package.
func (s *Store) Delete(ctx context.Context, roleID string) error {
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	name, err := s.filename(roleID)
	if err != nil {
		return err
	}
	if err := checkLocalPath(ctx, name, false); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Remove(name)
}

// Quarantine renames a corrupt archive without replacing any existing backup.
// A healthy or missing archive is not quarantined. A same-second name collision
// returns an error and retains the original, allowing a later explicit retry.
func (s *Store) Quarantine(ctx context.Context, roleID string) (string, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	if _, err := s.load(ctx, roleID); !errors.Is(err, ErrCorrupt) {
		if err == nil {
			err = errors.New("gacha archive is not corrupt")
		}
		return "", err
	}
	name, err := s.filename(roleID)
	if err != nil {
		return "", err
	}
	destination := name + ".corrupt-" + time.Now().UTC().Format("20060102T150405Z")
	if err := checkLocalPath(ctx, name, false); err != nil {
		return "", err
	}
	if err := checkLocalPath(ctx, destination, false); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := moveFile(name, destination, false); err != nil {
		return "", err
	}
	return destination, nil
}

func validPathSyntax(path string) error {
	path = filepath.FromSlash(path)
	if len(path) < 3 || len(path) > 32768 || !utf8.ValidString(path) || path[1] != ':' || path[2] != '\\' || !((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) {
		return errors.New("expected an ordinary absolute local Windows path")
	}
	for _, part := range strings.Split(path[3:], `\`) {
		if strings.ContainsAny(part, ":*?<>|\"") || strings.IndexFunc(part, unicode.IsControl) >= 0 || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return errors.New("unsupported gacha path component")
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		letters := []rune(base)
		port := len(letters) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && strings.ContainsRune("123456789¹²³", letters[3])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || base == "CONIN$" || base == "CONOUT$" || port {
			return errors.New("reserved Windows device component")
		}
	}
	return nil
}
