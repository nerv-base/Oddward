// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// QuickCheck runs PRAGMA quick_check, the cheap structural check used at open.
// It finds a truncated or overwritten file without scanning every page.
func (db *DB) QuickCheck(ctx context.Context) error {
	problems, err := db.pragmaLines(ctx, "PRAGMA quick_check")
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: quick_check reported %s", ErrCorrupt, strings.Join(problems, "; "))
	}
	return nil
}

// IntegrityCheck runs the full PRAGMA integrity_check plus PRAGMA
// foreign_key_check. Both must be clean; a violation is reported as ErrCorrupt so
// no caller can mistake a damaged database for an empty one.
func (db *DB) IntegrityCheck(ctx context.Context) error {
	problems, err := db.pragmaLines(ctx, "PRAGMA integrity_check")
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: integrity_check reported %s", ErrCorrupt, strings.Join(problems, "; "))
	}
	violations, err := db.ForeignKeyViolations(ctx)
	if err != nil {
		return err
	}
	if len(violations) > 0 {
		return fmt.Errorf("%w: foreign_key_check reported %s", ErrCorrupt, strings.Join(violations, "; "))
	}
	return nil
}

// ForeignKeyViolations returns "table rowid parent" descriptions of rows that
// break a declared reference.
func (db *DB) ForeignKeyViolations(ctx context.Context) ([]string, error) {
	rows, err := db.write.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return nil, classify(err, "foreign key check")
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, classify(err, "foreign key check")
	}
	var out []string
	for rows.Next() {
		values := make([]any, len(columns))
		for i := range values {
			values[i] = new(sql.RawBytes)
		}
		if err := rows.Scan(values...); err != nil {
			return nil, classify(err, "foreign key check")
		}
		parts := make([]string, len(columns))
		for i, value := range values {
			parts[i] = string(*value.(*sql.RawBytes))
		}
		out = append(out, strings.Join(parts, " "))
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, "foreign key check")
	}
	return out, nil
}

// pragmaLines runs a PRAGMA that returns diagnostic lines and drops the "ok"
// row SQLite prints for a healthy database.
func (db *DB) pragmaLines(ctx context.Context, statement string) ([]string, error) {
	rows, err := db.write.QueryContext(ctx, statement)
	if err != nil {
		return nil, classify(err, strings.ToLower(statement))
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, classify(err, strings.ToLower(statement))
		}
		if line == "ok" {
			continue
		}
		problems = append(problems, line)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, strings.ToLower(statement))
	}
	return problems, nil
}
