// SPDX-License-Identifier: GPL-3.0-or-later

package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverRecognisesOnlyTheInstalledLayout(t *testing.T) {
	root, l := installed(t, "0.1.0")
	if l.Root != root || l.Version.String() != "0.1.0" || l.Dir != filepath.Join(root, "app-0.1.0") {
		t.Fatalf("layout = %+v", l)
	}
	portable := filepath.Join(t.TempDir(), "oddward.exe")
	if _, err := Discover(portable, "0.1.0"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("portable exe: %v", err)
	}
	if _, err := Discover(filepath.Join(root, "app-0.1.0", AppExe), "0.1.1"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("folder/version mismatch: %v", err)
	}
	if _, err := Discover(filepath.Join(root, "app-0.1.0", AppExe), ""); !errors.Is(err, ErrDevelopmentBuild) {
		t.Fatalf("unversioned build: %v", err)
	}
	os.Remove(filepath.Join(root, StubExe))
	if _, err := Discover(filepath.Join(root, "app-0.1.0", AppExe), "0.1.0"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("missing stub: %v", err)
	}
}

func TestAppsListsReadyFoldersNewestFirst(t *testing.T) {
	root, _ := installed(t, "0.1.0")
	writeApp(t, root, "0.3.0", appFiles("0.3.0"))
	writeApp(t, root, "0.2.0", appFiles("0.2.0"))
	os.MkdirAll(filepath.Join(root, "app-0.9.0"), 0o700) // no ready marker
	os.MkdirAll(filepath.Join(root, "app-0.8.0.partial-1"), 0o700)
	os.MkdirAll(filepath.Join(root, "app-latest"), 0o700)
	noExe := writeApp(t, root, "0.7.0", appFiles("0.7.0"))
	os.Remove(filepath.Join(noExe, AppExe))
	apps, err := Apps(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range apps {
		got = append(got, a.Version.String())
	}
	if len(got) != 3 || got[0] != "0.3.0" || got[1] != "0.2.0" || got[2] != "0.1.0" || !apps[2].Healthy || apps[0].Healthy {
		t.Fatalf("apps = %v %+v", got, apps)
	}
}

func TestChooseSkipsFailedVersions(t *testing.T) {
	root, _ := installed(t, "0.1.0")
	bad := writeApp(t, root, "0.2.0", appFiles("0.2.0"))
	if err := MarkFailed(bad); err != nil {
		t.Fatal(err)
	}
	apps, _ := Apps(root)
	pick, ok := Choose(apps)
	if !ok || pick.Version.String() != "0.1.0" {
		t.Fatalf("pick = %+v %v", pick, ok)
	}
	if _, ok := Choose(nil); ok {
		t.Fatal("chose from nothing")
	}
}

func TestPruneKeepsCurrentAndOnePreviousVersion(t *testing.T) {
	root, _ := installed(t, "0.1.0")
	for _, v := range []string{"0.2.0", "0.3.0", "0.5.0"} {
		MarkHealthy(writeApp(t, root, v, appFiles(v)))
	}
	current := writeApp(t, root, "0.4.0", appFiles("0.4.0"))
	failed := writeApp(t, root, "0.6.0", appFiles("0.6.0"))
	MarkFailed(failed)
	os.MkdirAll(filepath.Join(root, "app-0.7.0.partial-x"), 0o700)
	os.MkdirAll(filepath.Join(root, updatesDir), 0o700)
	os.WriteFile(filepath.Join(root, updatesDir, "abc.zip.partial-1"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(root, "user-note.txt"), []byte("keep me"), 0o600)
	if err := MarkHealthy(current); err != nil {
		t.Fatal(err)
	}
	if err := Prune(root, current); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{
		"app-0.1.0": false, "app-0.2.0": false, "app-0.3.0": true, "app-0.4.0": true,
		"app-0.5.0": true, "app-0.6.0": false, "app-0.7.0.partial-x": false,
		filepath.Join(updatesDir, "abc.zip.partial-1"): false, "user-note.txt": true, StubExe: true,
	} {
		_, err := os.Stat(filepath.Join(root, name))
		if (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", name, err == nil, want)
		}
	}
}
