// SPDX-License-Identifier: GPL-3.0-or-later

package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Installed layout, created by the installer and maintained by updates:
//
//	<root>\Oddward.exe                 stub: picks and starts an app folder
//	<root>\app-X.Y.Z\oddward-launcher.exe
//	<root>\app-X.Y.Z\.ready            written last, before the folder is renamed in
//	<root>\app-X.Y.Z\.healthy          the version opened its window once
//	<root>\app-X.Y.Z\.failed           the version exited before becoming healthy
//	<root>\.updates\                   downloaded packages
const (
	StubExe       = "Oddward.exe"
	AppExe        = "oddward-launcher.exe"
	appPrefix     = "app-"
	readyMarker   = ".ready"
	healthyMarker = ".healthy"
	failedMarker  = ".failed"
	updatesDir    = ".updates"
	partialInfix  = ".partial-"
)

var (
	// ErrDevelopmentBuild means the build carries no release version.
	ErrDevelopmentBuild = errors.New("开发构建不检查更新")
	// ErrNotInstalled means the exe was not started from an installed layout.
	ErrNotInstalled = errors.New("未通过安装器安装，不支持自动更新")
)

// Layout is the installation the running app belongs to.
type Layout struct {
	Root    string
	Dir     string
	Version Version
}

// Discover places executable in the installed layout for version.
func Discover(executable, version string) (Layout, error) {
	if version == "" {
		return Layout{}, ErrDevelopmentBuild
	}
	v, err := ParseVersion(version)
	if err != nil {
		return Layout{}, err
	}
	executable = filepath.Clean(executable)
	dir := filepath.Dir(executable)
	root := filepath.Dir(dir)
	if !strings.EqualFold(filepath.Base(executable), AppExe) || filepath.Base(dir) != appPrefix+v.String() {
		return Layout{}, ErrNotInstalled
	}
	if info, err := os.Lstat(filepath.Join(root, StubExe)); err != nil || !info.Mode().IsRegular() {
		return Layout{}, ErrNotInstalled
	}
	return Layout{Root: root, Dir: dir, Version: v}, nil
}

// App is one ready app folder.
type App struct {
	Version Version
	Dir     string
	Healthy bool
	Failed  bool
}

// Apps lists ready app folders under root, newest first. Folders without the
// ready marker or the launcher exe are ignored: they are unfinished.
func Apps(root string) ([]App, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var apps []App
	for _, e := range entries {
		v, ok := appVersion(e)
		if !ok {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if !regular(filepath.Join(dir, readyMarker)) || !regular(filepath.Join(dir, AppExe)) {
			continue
		}
		apps = append(apps, App{Version: v, Dir: dir, Healthy: regular(filepath.Join(dir, healthyMarker)), Failed: regular(filepath.Join(dir, failedMarker))})
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Version.Compare(apps[j].Version) > 0 })
	return apps, nil
}

func appVersion(e os.DirEntry) (Version, bool) {
	if !e.IsDir() || e.Type()&os.ModeSymlink != 0 || !strings.HasPrefix(e.Name(), appPrefix) {
		return Version{}, false
	}
	v, err := ParseVersion(strings.TrimPrefix(e.Name(), appPrefix))
	return v, err == nil
}

func regular(p string) bool {
	info, err := os.Lstat(p)
	return err == nil && info.Mode().IsRegular()
}

// Choose returns the newest app that has not failed.
func Choose(apps []App) (App, bool) {
	for _, a := range apps {
		if !a.Failed {
			return a, true
		}
	}
	return App{}, false
}

// MarkHealthy records that the app in dir opened its window.
func MarkHealthy(dir string) error { return touch(filepath.Join(dir, healthyMarker)) }

// MarkFailed records that the app in dir exited before becoming healthy.
func MarkFailed(dir string) error { return touch(filepath.Join(dir, failedMarker)) }

func touch(p string) error {
	if regular(p) {
		return nil
	}
	return os.WriteFile(p, nil, 0o600)
}

// Prune runs from the healthy app in current. It keeps current, the newest
// older healthy version as a fallback and any newer staged versions, and
// removes older versions, failed versions and unfinished downloads.
func Prune(root, current string) error {
	apps, err := Apps(root)
	if err != nil {
		return err
	}
	var cur *App
	for i := range apps {
		if filepath.Clean(apps[i].Dir) == filepath.Clean(current) {
			cur = &apps[i]
		}
	}
	if cur == nil || !cur.Healthy {
		return errors.New("selfupdate: prune must run from the healthy current version")
	}
	var errs []error
	keptFallback := false
	for _, a := range apps {
		switch c := a.Version.Compare(cur.Version); {
		case c == 0:
		case a.Failed:
			errs = append(errs, removeOwned(root, a.Dir))
		case c > 0:
		case a.Healthy && !keptFallback:
			keptFallback = true
		default:
			errs = append(errs, removeOwned(root, a.Dir))
		}
	}
	for _, parent := range []string{root, filepath.Join(root, updatesDir)} {
		entries, _ := os.ReadDir(parent)
		for _, e := range entries {
			if strings.Contains(e.Name(), partialInfix) && (parent != root || strings.HasPrefix(e.Name(), appPrefix)) {
				errs = append(errs, removeOwned(root, filepath.Join(parent, e.Name())))
			}
		}
	}
	return errors.Join(errs...)
}

// removeOwned deletes p only when it is a real entry inside root, never
// following a link or junction out of the installation.
func removeOwned(root, p string) error {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return errors.New("selfupdate: refusing to remove outside the installation")
	}
	info, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return errors.New("selfupdate: refusing to remove a link inside the installation")
	}
	return os.RemoveAll(p)
}
