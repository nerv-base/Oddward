// SPDX-License-Identifier: GPL-3.0-or-later

package gacha

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
)

// legacyArchive is one per-role mirror file read by the importer.
type legacyArchive struct {
	roleID  string
	path    string
	archive Archive
	data    []byte
}

// ImportLegacy brings the database level with the legacy files. The first start
// imports every per-role archive and the name catalog once; a later start
// re-runs the archive import only when a mirror file no longer matches what this
// build wrote, which is how data an older build changed after a rollback is
// reconciled. The database stays authoritative between those passes.
func (s *Store) ImportLegacy(ctx context.Context) error {
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	if _, err := s.db.RunImport(ctx, Domain, ImportVersion, s.importArchives); err != nil {
		return err
	}
	changed, err := s.mirrorsChanged(ctx)
	if err != nil {
		return err
	}
	if changed {
		// The migration is already recorded as done, so this pass runs the
		// importer directly: the whole directory is the only consistent source.
		if _, _, err := s.importArchives(ctx); err != nil {
			return err
		}
	}
	_, err = s.db.RunImport(ctx, catalogDomain, catalogImportVersion, s.importCatalog)
	return err
}

// importArchives replaces every stored archive with the archive files that are
// present. It is the database.ImportFunc for Domain: idempotent, doing nothing
// when there is no legacy source, and recording the digest of the exact bytes it
// read without rewriting a file. One unreadable file fails the whole import, so
// neither any file nor any row changes.
func (s *Store) importArchives(ctx context.Context) (source, detail string, err error) {
	files, present, err := s.scanArchives(ctx)
	if err != nil {
		return s.dir, "", err
	}
	if !present {
		// A missing directory is a fresh profile (or a state root that was moved
		// away), not an instruction to drop archives the database already holds.
		return s.dir, "no legacy archives", nil
	}
	now := time.Now().UTC()
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM gacha_archives`); err != nil {
			return fmt.Errorf("replace gacha archives: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM compat_mirrors WHERE domain LIKE ?`, Domain+"/%"); err != nil {
			return fmt.Errorf("replace gacha mirror records: %w", err)
		}
		for _, file := range files {
			if err := putArchive(ctx, tx, file.archive, sourceLegacy, now); err != nil {
				return err
			}
			if err := s.db.RecordMirror(ctx, tx, archiveDomain(file.roleID), file.path, database.Digest(file.data)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return s.dir, "", err
	}
	return s.dir, fmt.Sprintf("%d archives", len(files)), nil
}

// scanArchives reads every archive file. Non-archive entries are ignored like
// the file store ignored them, but an archive-shaped file that cannot be decoded
// fails the scan with an error naming that file.
func (s *Store) scanArchives(ctx context.Context) ([]legacyArchive, bool, error) {
	if err := checkLocalPath(ctx, s.dir, true); err != nil {
		return nil, false, err
	}
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	files := []legacyArchive{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") || !entry.Type().IsRegular() {
			continue
		}
		roleID := strings.TrimSuffix(name, ".json")
		if validateRole(roleID) != nil {
			continue
		}
		path := filepath.Join(s.dir, name)
		archive, data, err := readArchiveFile(ctx, path, roleID)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", path, err)
		}
		files = append(files, legacyArchive{roleID: roleID, path: path, archive: archive, data: data})
	}
	return files, true, ctx.Err()
}

// mirrorsChanged reports whether a recorded archive mirror no longer matches its
// file, or an archive file exists that this build never recorded. Both mean an
// older build wrote the directory after the last reconcile.
func (s *Store) mirrorsChanged(ctx context.Context) (bool, error) {
	rows, err := s.db.Query(ctx, `SELECT path, digest FROM compat_mirrors WHERE domain LIKE ?`, Domain+"/%")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	recorded := map[string]bool{}
	for rows.Next() {
		var path, digest string
		if err := rows.Scan(&path, &digest); err != nil {
			return false, err
		}
		recorded[path] = true
		data, err := os.ReadFile(path)
		if err != nil {
			// A recorded file that is gone or unreadable is a change too; the
			// importer reports the details and leaves the rows alone on failure.
			return true, nil
		}
		if database.Digest(data) != digest {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") || !entry.Type().IsRegular() {
			continue
		}
		if validateRole(strings.TrimSuffix(name, ".json")) != nil {
			continue
		}
		if !recorded[filepath.Join(s.dir, name)] {
			return true, nil
		}
	}
	return false, nil
}

// readLegacyArchive decodes the mirror file of one role. It is what reports an
// unreadable legacy archive as ErrCorrupt for a role the database does not hold.
func (s *Store) readLegacyArchive(ctx context.Context, roleID string) (Archive, []byte, error) {
	name, err := s.filename(roleID)
	if err != nil {
		return Archive{}, nil, err
	}
	return readArchiveFile(ctx, name, roleID)
}

// readArchiveFile decodes one per-role archive document with the same strict
// decoder the file store used.
func readArchiveFile(ctx context.Context, path, roleID string) (Archive, []byte, error) {
	data, err := readLegacyFile(ctx, path, MaxDocumentBytes)
	if err != nil {
		return Archive{}, nil, err
	}
	imported, err := DecodeImport(data)
	if err != nil {
		return Archive{}, nil, fmt.Errorf("%w: %w", ErrCorrupt, err)
	}
	if imported.Format != "oddward-gacha" || imported.Archive.RoleID != roleID {
		return Archive{}, nil, fmt.Errorf("%w: archive format or role differs from filename", ErrCorrupt)
	}
	return imported.Archive, data, nil
}

// readLegacyFile reads one bounded legacy file. Links and other irregular files
// are refused before reading, because this build later replaces the path.
func readLegacyFile(ctx context.Context, path string, limit int64) ([]byte, error) {
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
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("%w: expected a regular file no larger than %d bytes", ErrCorrupt, limit)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: file exceeds %d bytes", ErrCorrupt, limit)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}
