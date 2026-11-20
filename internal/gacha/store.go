// SPDX-License-Identifier: GPL-3.0-or-later

package gacha

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nerv-base/Oddward/internal/database"
)

// Domain is the archive domain in migration_state. The per-file mirror rows use
// one domain per archive instead: compat_mirrors keeps a single row per domain,
// while a directory of archives must be reconciled file by file.
const Domain = "gacha"

// ImportVersion is the importer generation recorded for Domain. Bump it when
// the importer learns to read a legacy document it did not read before.
// ImportVersion 2 honours the archive a rolled-back build deleted instead of
// treating an empty directory as "keep everything".
const ImportVersion = 2

// Where an archive row first came from. It is bookkeeping for tracing a
// migration; the archive itself is identical either way.
const (
	sourceSaved  = "oddward"
	sourceLegacy = "legacy"
)

// Store is the database-backed gacha archive repository. SQLite is
// authoritative; every archive is also written as the legacy oddward-gacha file
// under dir, because the updater can roll the launcher back to a build that
// cannot read the database. Use only one Store per database and directory in a
// process: operations serialize on one cancelable lock, and there is no
// cross-process lock. Path checks are conservative snapshots, not a hostile
// concurrent path-swap jail.
type Store struct {
	db         *database.DB
	dir        string
	operations chan struct{}
}

// NewStore binds the archive rows to the mirror directory dir. It validates the
// path syntax without creating directories or reading user data.
func NewStore(db *database.DB, dir string) (*Store, error) {
	if db == nil {
		return nil, errors.New("gacha store requires an open database")
	}
	if err := validPathSyntax(dir); err != nil {
		return nil, err
	}
	dir = filepath.Clean(dir)
	if filepath.Dir(dir) == dir {
		return nil, errors.New("gacha directory must not be a volume root")
	}
	s := &Store{db: db, dir: dir, operations: make(chan struct{}, 1)}
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

// filename is the legacy path of one archive. The bytes written there are the
// mirror a rolled-back build reads.
func (s *Store) filename(roleID string) (string, error) {
	if err := validateRole(roleID); err != nil {
		return "", err
	}
	return filepath.Join(s.dir, roleID+".json"), nil
}

// archiveDomain scopes the mirror row of one archive file.
func archiveDomain(roleID string) string { return Domain + "/" + roleID }

// List returns the role IDs stored in the database, lexicographically sorted. A
// role that only has a legacy file appears after the import has stored it.
func (s *Store) List(ctx context.Context) ([]string, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := s.db.Query(ctx, `SELECT role_id FROM gacha_archives ORDER BY role_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

// Load returns an os.ErrNotExist-compatible error for a role the database does
// not hold. An unreadable legacy file is reported as ErrCorrupt so the caller
// can quarantine it, exactly as the file store reported a corrupt archive; the
// file's contents never become authoritative.
func (s *Store) Load(ctx context.Context, roleID string) (Archive, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return Archive{}, err
	}
	defer release()
	return s.load(ctx, roleID)
}

func (s *Store) load(ctx context.Context, roleID string) (Archive, error) {
	archive, err := loadArchive(ctx, s.db.Query, s.db.QueryRow, roleID)
	if !errors.Is(err, os.ErrNotExist) {
		return archive, err
	}
	if _, _, legacyErr := s.readLegacyArchive(ctx, roleID); legacyErr != nil && !errors.Is(legacyErr, os.ErrNotExist) {
		return Archive{}, legacyErr
	}
	return Archive{}, err
}

// Save writes the archive as one transaction: its bookkeeping row, its missing
// records with the merge luck rule, and the complete snapshot set. The mirror
// bytes and their digest commit with the rows, and only then is the legacy file
// replaced. If that replacement fails the rows are kept: the database is
// authoritative and the recorded digest no longer matches the old file, which
// the next start reconciles.
func (s *Store) Save(ctx context.Context, a Archive) error {
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	canonical, err := normalizeArchive(a, false)
	if err != nil {
		return err
	}
	name, err := s.filename(canonical.RoleID)
	if err != nil {
		return err
	}
	if _, err := s.load(ctx, canonical.RoleID); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("previous gacha archive retained: %w", err)
	}
	// Checked before the transaction: an unsafe mirror path must never accompany
	// committed rows.
	if err := checkLocalPath(ctx, name, false); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	now := time.Now().UTC()
	previous, hadPrevious, err := database.MirrorSnapshot(name)
	if err != nil {
		return err
	}
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := putArchive(ctx, tx, canonical, sourceSaved, now); err != nil {
			return err
		}
		stored, err := loadArchive(ctx, tx.QueryContext, tx.QueryRowContext, canonical.RoleID)
		if err != nil {
			return err
		}
		// The mirror is serialized from the stored archive, not from the input:
		// records the database already held are part of the file a rollback build
		// has to see. It is written inside the transaction; a refused commit puts
		// the previous bytes back below, so a reported failure never reappears as
		// an import on the next start.
		mirror, err := ExportOddward(stored, now)
		if err != nil {
			return err
		}
		return s.db.MirrorInTx(ctx, tx, archiveDomain(canonical.RoleID), name, mirror)
	})
	if err != nil {
		if restoreErr := database.RestoreMirror(name, previous, hadPrevious); restoreErr != nil {
			return fmt.Errorf("%w (the gacha mirror could not be restored: %v)", err, restoreErr)
		}
		return err
	}
	return nil
}

// Delete removes one archive from the database and its mirror file, including a
// file the import never stored. A missing archive is os.ErrNotExist. Caller-owned
// UI confirmation is required outside this package.
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
	_, statErr := os.Lstat(name)
	hasFile := statErr == nil
	if !hasFile && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	stored, err := s.archiveExists(ctx, roleID)
	if err != nil {
		return err
	}
	if !hasFile && !stored {
		return fmt.Errorf("%w: gacha archive %s", os.ErrNotExist, roleID)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM gacha_archives WHERE role_id = ?`, roleID); err != nil {
			return fmt.Errorf("delete gacha archive: %w", err)
		}
		// The mirror row goes with the file: a recorded path whose file is gone
		// would make every later start reconcile it back.
		_, err := tx.ExecContext(ctx, `DELETE FROM compat_mirrors WHERE domain = ?`, archiveDomain(roleID))
		return err
	}); err != nil {
		return err
	}
	if !hasFile {
		return nil
	}
	return os.Remove(name)
}

// Quarantine renames an unreadable legacy archive without replacing any existing
// backup. A same-second name collision returns an error and retains the
// original, allowing a later explicit retry.
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
	if err := moveFile(name, destination); err != nil {
		return "", err
	}
	// The bytes now belong to the quarantine sibling, so the role has no mirror.
	if _, err := s.db.Exec(ctx, `DELETE FROM compat_mirrors WHERE domain = ?`, archiveDomain(roleID)); err != nil {
		return destination, fmt.Errorf("gacha archive quarantined, mirror record not cleared: %w", err)
	}
	return destination, nil
}

func (s *Store) archiveExists(ctx context.Context, roleID string) (bool, error) {
	var one int
	err := s.db.QueryRow(ctx, `SELECT 1 FROM gacha_archives WHERE role_id = ?`, roleID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// queryFunc and queryRowFunc let the same read run on the connection pool or
// inside the transaction that just wrote the rows.
type queryFunc func(context.Context, string, ...any) (*sql.Rows, error)
type queryRowFunc func(context.Context, string, ...any) *sql.Row

// loadArchive reads one complete archive. The rows are normalized in memory so
// the returned order is exactly the file store's order, independent of SQLite's
// collation.
func loadArchive(ctx context.Context, query queryFunc, queryRow queryRowFunc, roleID string) (Archive, error) {
	var one int
	err := queryRow(ctx, `SELECT 1 FROM gacha_archives WHERE role_id = ?`, roleID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return Archive{}, fmt.Errorf("%w: gacha archive %s", os.ErrNotExist, roleID)
	}
	if err != nil {
		return Archive{}, err
	}
	archive := Archive{RoleID: roleID, Records: []Record{}, Snapshots: []SnapshotMeta{}}
	records, err := query(ctx, `SELECT pool, item_id, at_ms, pity, luck FROM gacha_records WHERE role_id = ?`, roleID)
	if err != nil {
		return Archive{}, err
	}
	for records.Next() {
		var pool, itemID string
		var atMS int64
		var pity, luck int
		if err := records.Scan(&pool, &itemID, &atMS, &pity, &luck); err != nil {
			records.Close()
			return Archive{}, err
		}
		archive.Records = append(archive.Records, Record{Pool: Pool(pool), ItemID: itemID, At: time.UnixMilli(atMS).UTC(), Pity: pity, Luck: luck})
	}
	if err := records.Err(); err != nil {
		records.Close()
		return Archive{}, err
	}
	records.Close()
	type snapshotRow struct {
		id   int64
		meta SnapshotMeta
	}
	var snapshots []snapshotRow
	rows, err := query(ctx, `SELECT id, fetched_at, luck_type, luck_title FROM gacha_snapshots WHERE role_id = ?`, roleID)
	if err != nil {
		return Archive{}, err
	}
	for rows.Next() {
		var id, fetchedAt int64
		var luckType int
		var luckTitle string
		if err := rows.Scan(&id, &fetchedAt, &luckType, &luckTitle); err != nil {
			rows.Close()
			return Archive{}, err
		}
		snapshots = append(snapshots, snapshotRow{id: id, meta: SnapshotMeta{FetchedAt: database.StampInt(fetchedAt), LuckType: luckType, LuckTitle: luckTitle, Pools: []PoolMeta{}}})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Archive{}, err
	}
	rows.Close()
	for _, snapshot := range snapshots {
		pools, err := query(ctx, `SELECT pool, m, draw_count, rare_count, average, player_over, rule_mismatch FROM gacha_snapshot_pools WHERE snapshot_id = ?`, snapshot.id)
		if err != nil {
			return Archive{}, err
		}
		for pools.Next() {
			var pool, average, playerOver string
			var m, draws, rares, mismatch int
			if err := pools.Scan(&pool, &m, &draws, &rares, &average, &playerOver, &mismatch); err != nil {
				pools.Close()
				return Archive{}, err
			}
			snapshot.meta.Pools = append(snapshot.meta.Pools, PoolMeta{Pool: Pool(pool), M: m, DrawCount: draws, RareCount: rares, Average: average, PlayerOver: playerOver, RuleMismatch: mismatch == 1})
		}
		if err := pools.Err(); err != nil {
			pools.Close()
			return Archive{}, err
		}
		pools.Close()
		archive.Snapshots = append(archive.Snapshots, snapshot.meta)
	}
	return normalizeArchive(archive, false)
}

// putArchive writes one archive's rows inside tx. Records are upserted because
// an archive is accumulated, never truncated by a save; the luck update applies
// the same rule mergeArchives applies in memory, so a known tier is never
// replaced by a larger one. Snapshot rows are replaced because an archive
// carries its complete snapshot metadata set.
func putArchive(ctx context.Context, tx *sql.Tx, a Archive, source string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO gacha_archives(role_id, source, imported_at, updated_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(role_id) DO UPDATE SET updated_at = excluded.updated_at`,
		a.RoleID, source, database.Stamp(now), database.Stamp(now)); err != nil {
		return fmt.Errorf("store gacha archive: %w", err)
	}
	for _, r := range a.Records {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gacha_records(role_id, pool, item_id, at_ms, pity, luck, created_at)
			VALUES(?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(role_id, pool, item_id, at_ms, pity) DO UPDATE SET
				luck = CASE
					WHEN gacha_records.luck = -1 THEN excluded.luck
					WHEN excluded.luck = -1 THEN gacha_records.luck
					WHEN excluded.luck < gacha_records.luck THEN excluded.luck
					ELSE gacha_records.luck
				END`,
			a.RoleID, string(r.Pool), r.ItemID, r.At.UnixMilli(), r.Pity, r.Luck, database.Stamp(now)); err != nil {
			return fmt.Errorf("store gacha record: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM gacha_snapshots WHERE role_id = ?`, a.RoleID); err != nil {
		return fmt.Errorf("replace gacha snapshots: %w", err)
	}
	for _, m := range a.Snapshots {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO gacha_snapshots(role_id, meta_key, fetched_at, luck_type, luck_title, created_at)
			VALUES(?, ?, ?, ?, ?, ?)`,
			a.RoleID, metaKey(m), database.Stamp(m.FetchedAt), m.LuckType, m.LuckTitle, database.Stamp(now))
		if err != nil {
			return fmt.Errorf("store gacha snapshot: %w", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("store gacha snapshot: %w", err)
		}
		for _, p := range m.Pools {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO gacha_snapshot_pools(snapshot_id, pool, m, draw_count, rare_count, average, player_over, rule_mismatch)
				VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
				id, string(p.Pool), p.M, p.DrawCount, p.RareCount, p.Average, p.PlayerOver, flag(p.RuleMismatch)); err != nil {
				return fmt.Errorf("store gacha snapshot pool: %w", err)
			}
		}
	}
	return nil
}

func flag(value bool) int {
	if value {
		return 1
	}
	return 0
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
