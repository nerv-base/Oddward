// SPDX-License-Identifier: GPL-3.0-or-later

package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeStarts records starts; each version exits with the code in exits, or
// keeps running (and turns healthy) when absent.
type fakeStarts struct {
	started []string
	exits   map[string]error
	healthy bool
}

func (f *fakeStarts) start(exe string, args []string) (<-chan error, error) {
	dir := filepath.Dir(exe)
	f.started = append(f.started, filepath.Base(dir))
	done := make(chan error, 1)
	if err, ok := f.exits[filepath.Base(dir)]; ok {
		done <- err
		return done, nil
	}
	if f.healthy {
		MarkHealthy(dir)
	}
	return done, nil
}

func TestLaunchStartsTheNewestReadyVersion(t *testing.T) {
	root, _ := installed(t, "0.1.0")
	writeApp(t, root, "0.2.0", appFiles("0.2.0"))
	f := &fakeStarts{healthy: true}
	app, err := Launch(root, []string{"-demo"}, f.start, time.Second)
	if err != nil || app.Version.String() != "0.2.0" || len(f.started) != 1 {
		t.Fatalf("app %+v err %v started %v", app, err, f.started)
	}
}

func TestLaunchFallsBackWhenANewVersionDiesBeforeItsWindow(t *testing.T) {
	root, _ := installed(t, "0.1.0")
	bad := writeApp(t, root, "0.2.0", appFiles("0.2.0"))
	f := &fakeStarts{exits: map[string]error{"app-0.2.0": errors.New("exit status 2")}, healthy: true}
	app, err := Launch(root, nil, f.start, time.Second)
	if err != nil || app.Version.String() != "0.1.0" {
		t.Fatalf("app %+v err %v", app, err)
	}
	if !regular(filepath.Join(bad, failedMarker)) || len(f.started) != 2 {
		t.Fatalf("started %v", f.started)
	}
}

func TestLaunchDoesNotBlameACleanEarlyExit(t *testing.T) {
	root, _ := installed(t, "0.1.0")
	dir := writeApp(t, root, "0.2.0", appFiles("0.2.0"))
	f := &fakeStarts{exits: map[string]error{"app-0.2.0": nil}}
	if app, err := Launch(root, nil, f.start, time.Second); err != nil || app.Dir != dir || regular(filepath.Join(dir, failedMarker)) {
		t.Fatalf("app %+v err %v", app, err)
	}
}

func TestLaunchGivesUpWaitingWithoutBlame(t *testing.T) {
	root, _ := installed(t, "0.1.0")
	dir := writeApp(t, root, "0.2.0", appFiles("0.2.0"))
	f := &fakeStarts{}
	if app, err := Launch(root, nil, f.start, 50*time.Millisecond); err != nil || app.Dir != dir || regular(filepath.Join(dir, failedMarker)) {
		t.Fatalf("app %+v err %v", app, err)
	}
}

func TestLaunchReportsABrokenInstallation(t *testing.T) {
	root, _ := installed(t, "0.1.0")
	os.RemoveAll(filepath.Join(root, "app-0.1.0"))
	if _, err := Launch(root, nil, (&fakeStarts{}).start, time.Second); !errors.Is(err, ErrNoApp) {
		t.Fatalf("err = %v", err)
	}
	writeApp(t, root, "0.2.0", appFiles("0.2.0"))
	f := &fakeStarts{exits: map[string]error{"app-0.2.0": errors.New("exit status 1")}}
	if _, err := Launch(root, nil, f.start, time.Second); !errors.Is(err, ErrNoApp) {
		t.Fatalf("all failed: %v", err)
	}
}

func TestLaunchTrustsAVersionThatWasHealthyBefore(t *testing.T) {
	root, _ := installed(t, "0.1.0")
	f := &fakeStarts{exits: map[string]error{"app-0.1.0": errors.New("exit status 1")}}
	app, err := Launch(root, nil, f.start, time.Second)
	if err != nil || regular(filepath.Join(app.Dir, failedMarker)) {
		t.Fatalf("a once-healthy version was blamed: %+v %v", app, err)
	}
}
