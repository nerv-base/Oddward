// SPDX-License-Identifier: GPL-3.0-or-later

package database_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

// BenchmarkOpenEmpty measures the first launch of a build on a machine that has
// no database yet: file creation plus the whole embedded migration set.
func BenchmarkOpenEmpty(b *testing.B) {
	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		dir := b.TempDir()
		b.StartTimer()
		db, err := database.Open(ctx, database.Options{Path: filepath.Join(dir, database.FileName), NoBackup: true})
		if err != nil {
			b.Fatalf("open empty: %v", err)
		}
		b.StopTimer()
		if err := db.Close(); err != nil {
			b.Fatalf("close: %v", err)
		}
	}
}

// BenchmarkOpenMigrated measures a normal launch: the file already carries the
// schema this build writes.
func BenchmarkOpenMigrated(b *testing.B) {
	ctx := context.Background()
	db := dbtest.Open(b)
	path := db.Path()
	if err := db.Close(); err != nil {
		b.Fatalf("close: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		opened, err := database.Open(ctx, database.Options{Path: path, NoBackup: true})
		if err != nil {
			b.Fatalf("open migrated: %v", err)
		}
		b.StopTimer()
		if err := opened.Close(); err != nil {
			b.Fatalf("close: %v", err)
		}
		b.StartTimer()
	}
}

// BenchmarkInsertRecords measures one write transaction carrying a gacha-sized
// batch, which is the shape of an archive import or a merge.
func BenchmarkInsertRecords(b *testing.B) {
	db := dbtest.Open(b)
	ctx := context.Background()
	if _, err := db.Exec(ctx, "INSERT INTO gacha_archives(role_id, source, imported_at, updated_at) VALUES('100000001', 'bench', 1, 1)"); err != nil {
		b.Fatalf("seed archive: %v", err)
	}
	const batch = 1000
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		if _, err := db.Exec(ctx, "DELETE FROM gacha_records"); err != nil {
			b.Fatalf("clear records: %v", err)
		}
		b.StartTimer()
		err := db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			stmt, err := tx.PrepareContext(ctx, `INSERT INTO gacha_records(role_id, pool, item_id, at_ms, pity, luck, created_at)
				VALUES('100000001', 'limited', ?, ?, ?, 0, 0)`)
			if err != nil {
				return err
			}
			defer stmt.Close()
			for j := 0; j < batch; j++ {
				if _, err := stmt.ExecContext(ctx, "item"+strconv.Itoa(j%40), int64(1700000000000)+int64(j), j%90); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			b.Fatalf("insert batch: %v", err)
		}
	}
}

// BenchmarkProgressUpdate measures the throttled download path: one small
// UPDATE per progress report instead of rewriting the whole task document.
func BenchmarkProgressUpdate(b *testing.B) {
	db := dbtest.Open(b)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO download_tasks(id, schema, install_key, purpose, cache_root, branch, target_version, list_hash, kind, state, err, network_bytes, written_bytes, reused_bytes, cached_bytes, created_at, updated_at)
		VALUES('t1', 2, 'key', 'install', '', 'prod', '1.0', 'hash', 1, 2, '', 0, 0, 0, 0, 1, 1)`); err != nil {
		b.Fatalf("seed task: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO download_transfers(task_id, ordinal, object_md5, object_size, object_key, source_kind, source_md5, source_size, source_offset, source_length, chunk_size, bytes_done, state, patch_blocks_trusted, blocks, marks, updated_at)
		VALUES('t1', 0, 'd41d8cd98f00b204e9800998ecf8427e', 1048576, 'd41d8cd98f00b204e9800998ecf8427e.1048576', 1, 'd41d8cd98f00b204e9800998ecf8427e', 1048576, 0, 0, 1048576, 0, 2, 0, '[]', '[]', 1)`); err != nil {
		b.Fatalf("seed transfer: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := db.Exec(ctx, "UPDATE download_transfers SET bytes_done = ?, updated_at = ? WHERE task_id = 't1' AND ordinal = 0", (i*4096)%1048576, i); err != nil {
			b.Fatalf("progress update: %v", err)
		}
	}
}

// BenchmarkConcurrentReadsWhileWriting is the UI-reads/background-writes shape:
// four readers keep querying while one writer commits.
func BenchmarkConcurrentReadsWhileWriting(b *testing.B) {
	db := dbtest.Open(b)
	ctx := context.Background()
	if _, err := db.Exec(ctx, "INSERT INTO app_meta(key, value) VALUES('bench', '0')"); err != nil {
		b.Fatalf("seed meta: %v", err)
	}
	done := make(chan struct{})
	b.ResetTimer()
	for i := 0; i < 4; i++ {
		go func() {
			for {
				select {
				case <-done:
					return
				default:
					var value string
					if err := db.QueryRow(ctx, "SELECT value FROM app_meta WHERE key = 'bench'").Scan(&value); err != nil {
						b.Errorf("read: %v", err)
						return
					}
				}
			}
		}()
	}
	for i := 0; i < b.N; i++ {
		if _, err := db.Exec(ctx, "UPDATE app_meta SET value = ? WHERE key = 'bench'", strconv.Itoa(i)); err != nil {
			b.Fatalf("write: %v", err)
		}
	}
	close(done)
}

// BenchmarkCheckpoint and BenchmarkBackup cover the shutdown and upgrade paths.
func BenchmarkCheckpoint(b *testing.B) {
	db := dbtest.Open(b)
	ctx := context.Background()
	for i := 0; i < 500; i++ {
		if _, err := db.Exec(ctx, "INSERT INTO app_meta(key, value) VALUES(?, '1')", "k"+strconv.Itoa(i)); err != nil {
			b.Fatalf("insert: %v", err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := db.Checkpoint(ctx); err != nil {
			b.Fatalf("checkpoint: %v", err)
		}
	}
}

func BenchmarkBackup(b *testing.B) {
	db := dbtest.Open(b)
	ctx := context.Background()
	for i := 0; i < 1000; i++ {
		if _, err := db.Exec(ctx, "INSERT INTO app_meta(key, value) VALUES(?, '1')", "k"+strconv.Itoa(i)); err != nil {
			b.Fatalf("insert: %v", err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		dest := filepath.Join(b.TempDir(), "copy.db")
		b.StartTimer()
		if err := db.Backup(ctx, dest); err != nil {
			b.Fatalf("backup: %v", err)
		}
	}
}
