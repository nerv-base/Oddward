// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/launchsettings"
	"github.com/nerv-base/Oddward/internal/selfupdate"
)

// TestRollbackKeepsBothBuildsData is the release-level contract of the SQLite
// move. A new build migrates the user's files, writes the same files back as
// mirrors, and then crashes before it becomes healthy. The stub falls back to
// the previous build, which keeps editing those files. The next start must pick
// that edit up instead of silently dropping it.
func TestRollbackKeepsBothBuildsData(t *testing.T) {
	root := t.TempDir()
	settingsFile := filepath.Join(root, "launcher-v1.json")
	dbPath := filepath.Join(root, database.FileName)
	ctx := context.Background()

	// The previous build is installed, started once and healthy. Its data is the
	// JSON file: there is no database yet.
	oldApp := installedApp(t, root, "0.1.0")
	if err := os.WriteFile(settingsFile, []byte(`{"version":6,"gameRoot":"C:\\Games\\old","riskVersion":"","skippedVersion":"","network":{"proxy":"","proxyURL":"","mirror":false,"mirrorPrefix":""},"downloads":{"rateLimitKiB":0},"ui":{"closeToTray":true,"accentFollow":true,"checkUpdates":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := selfupdate.MarkHealthy(oldApp); err != nil {
		t.Fatal(err)
	}
	// The new build was staged next to it.
	newApp := installedApp(t, root, "0.2.0")

	// The new build starts: it opens the database, imports the file, and then the
	// user changes the game path, which rewrites the file as a mirror.
	newBuild, err := database.Open(ctx, database.Options{Path: dbPath, NoBackup: true})
	if err != nil {
		t.Fatalf("new build database: %v", err)
	}
	store, err := launchsettings.NewStore(newBuild, settingsFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newBuild.RunImport(ctx, launchsettings.Domain, launchsettings.ImportVersion, store.ImportLegacy); err != nil {
		t.Fatalf("import: %v", err)
	}
	imported, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("load imported: %v", err)
	}
	if imported.GameRoot != `C:\Games\old` {
		t.Fatalf("imported game root = %q", imported.GameRoot)
	}
	if err := store.Update(ctx, func(s *launchsettings.Settings) { s.GameRoot = `C:\Games\new`; s.UI.CheckUpdates = false }); err != nil {
		t.Fatalf("update: %v", err)
	}

	// The new build never becomes healthy and exits with an error: the window
	// never opened, so update.go never wrote the marker. The stub must fall back
	// to the healthy previous build.
	started := make([]string, 0, 2)
	stub := selfupdate.Starter(func(exe string, args []string) (<-chan error, error) {
		started = append(started, exe)
		exited := make(chan error, 1)
		if strings.Contains(exe, "app-0.2.0") {
			// A build whose database cannot be opened exits non-zero before it
			// ever opens a window, so no healthy marker is written.
			exited <- errors.New("exit status 1")
		}
		return exited, nil
	})
	if err := newBuild.Close(); err != nil {
		t.Fatalf("close crashed build: %v", err)
	}
	app, err := selfupdate.Launch(root, nil, stub, 2*time.Second)
	if err != nil {
		t.Fatalf("stub launch: %v", err)
	}
	if app.Dir != oldApp {
		t.Fatalf("stub started %q, want the healthy fallback %q", app.Dir, oldApp)
	}
	if len(started) < 2 || !strings.Contains(started[0], "app-0.2.0") {
		t.Fatalf("stub tried %v before the fallback", started)
	}
	if _, err := os.Lstat(filepath.Join(newApp, ".failed")); err != nil {
		t.Fatalf("crashed version not marked failed: %v", err)
	}

	// The previous build is trusted, so the stub does not wait on it. What the
	// user saw before rolling back is still in the file it writes.
	rolledBack, err := os.ReadFile(settingsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rolledBack), `C:\\Games\\new`) || !strings.Contains(string(rolledBack), `"checkUpdates":false`) {
		t.Fatalf("rolled-back build cannot see the new choices: %s", rolledBack)
	}

	// The user changes the game path again while running the old build: that
	// build rewrites only the JSON file.
	edited := strings.Replace(string(rolledBack), `C:\\Games\\new`, `C:\\Games\\edited`, 1)
	if err := os.WriteFile(settingsFile, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}

	// The fixed build starts again. The database already has the migration done,
	// so it must notice the file was rewritten by the other build and import it.
	next, err := database.Open(ctx, database.Options{Path: dbPath, NoBackup: true})
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	defer next.Close()
	stale, err := next.MirrorStale(ctx, launchsettings.Domain)
	if err != nil {
		t.Fatalf("mirror stale: %v", err)
	}
	if !stale {
		t.Fatal("a file edited by the rolled-back build must be seen as an edit to import")
	}
	nextStore, err := launchsettings.NewStore(next, settingsFile)
	if err != nil {
		t.Fatal(err)
	}
	importedAgain, err := next.RunImport(ctx, launchsettings.Domain, launchsettings.ImportVersion, nextStore.ImportLegacy)
	if err != nil {
		t.Fatalf("reconcile import: %v", err)
	}
	if !importedAgain {
		t.Fatal("reconcile import was skipped")
	}
	after, err := nextStore.Load(ctx)
	if err != nil {
		t.Fatalf("load reconciled: %v", err)
	}
	if after.GameRoot != `C:\Games\edited` {
		t.Fatalf("reconciled game root = %q, want the rolled-back build's edit", after.GameRoot)
	}
	if after.UI.CheckUpdates {
		t.Fatal("reconcile dropped the last choice the new build made")
	}
}

// installedApp creates a ready, not yet healthy app folder under root.
func installedApp(t *testing.T, root, version string) string {
	t.Helper()
	dir := filepath.Join(root, "app-"+version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{selfupdate.AppExe, ".ready"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("MZ"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, selfupdate.StubExe), []byte("MZ"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}
