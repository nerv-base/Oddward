// SPDX-License-Identifier: GPL-3.0-or-later
package launch

import (
	"context"
	"errors"
	"github.com/nerv-base/Oddward/internal/cnlogin/cnproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func syntheticProfile(launcher, bridge string) (cnproto.Profile, error) {
	if launcher != sha256Upper("launcher-placeholder") || bridge != sha256Upper("bridge-placeholder") {
		return cnproto.Profile{}, cnproto.ErrUnsupportedSDK
	}
	return cnproto.Profile{Name: "synthetic", SDKVersion: "2.0.9.0", LauncherSHA256: launcher, BridgeSHA256: bridge}, nil
}
func TestInspectReadOnlyPathsAndDigestGate(t *testing.T) {
	f := newFakeInstall(t)
	before, err := os.ReadFile(f.pointer)
	if err != nil {
		t.Fatal(err)
	}
	got, err := inspect(context.Background(), f.root, syntheticProfile)
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != f.root || got.GameExecutable != f.game || got.Bridge != f.bridge || got.LauncherDirectory != filepath.Dir(f.launcher) || got.ConfigFile != filepath.Join(f.root, `NTELauncher\Config\Config.ini`) || got.Profile.Name != "synthetic" {
		t.Fatalf("info %+v", got)
	}
	after, _ := os.ReadFile(f.pointer)
	if string(before) != string(after) {
		t.Fatal("pointer mutated")
	}
	if _, err := Inspect(context.Background(), f.root); !errors.Is(err, ErrUnsupportedSDK) {
		t.Fatalf("production gate accepted synthetic hashes: %v", err)
	}
}
func TestInspectReadsOptionalInstalledVersion(t *testing.T) {
	f := newFakeInstall(t)
	configXML := filepath.Join(f.root, `NTELauncher\UserData\Patcher\PatcherSDK\config.xml`)
	writeFile(t, configXML, "<Configuration><ResVersion>1.2.3</ResVersion></Configuration>")
	got, err := inspect(context.Background(), f.root, syntheticProfile)
	if err != nil {
		t.Fatal(err)
	}
	if got.InstalledVersion != "1.2.3" {
		t.Fatalf("installed version %q", got.InstalledVersion)
	}
	for _, content := range []string{"", "<Configuration><ResVersion>1.2.3.4.5</ResVersion></Configuration>", "<Configuration><ResVersion>v1.2</ResVersion></Configuration>"} {
		writeFile(t, configXML, content)
		got, err := inspect(context.Background(), f.root, syntheticProfile)
		if err != nil {
			t.Fatal(err)
		}
		if got.InstalledVersion != "" {
			t.Fatalf("accepted %q as %q", content, got.InstalledVersion)
		}
	}
}

func TestInstalledVersionFallbackOrder(t *testing.T) {
	f := newFakeInstall(t)
	resList := filepath.Join(f.root, `.collapse-nte\installed\1289\ResList.xml`)
	writeFile(t, resList, `<?xml version="1.0"?><ResList version="2.3.4"><File/></ResList>`)
	got, err := inspect(context.Background(), f.root, syntheticProfile)
	if err != nil {
		t.Fatal(err)
	}
	if got.InstalledVersion != "2.3.4" {
		t.Fatalf("ResList version %q", got.InstalledVersion)
	}
	if err := os.Remove(resList); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.root, `.collapse_nte_config`), "AppId=1289\nGameVersion=3.4.5\n")
	got, err = inspect(context.Background(), f.root, syntheticProfile)
	if err != nil {
		t.Fatal(err)
	}
	if got.InstalledVersion != "3.4.5" {
		t.Fatalf("flat config version %q", got.InstalledVersion)
	}
	writeFile(t, filepath.Join(f.root, `.collapse_nte_config`), "GameVersion=broken\n")
	got, err = inspect(context.Background(), f.root, syntheticProfile)
	if err != nil {
		t.Fatal(err)
	}
	if got.InstalledVersion != "" {
		t.Fatalf("accepted broken version %q", got.InstalledVersion)
	}
}
func TestInspectMissingInvalidPointerAndCancellation(t *testing.T) {
	configPath := func(f fakeInstall) string { return filepath.Join(f.root, `NTELauncher\Config\Config.ini`) }
	for name, mutate := range map[string]func(fakeInstall){"missing game": func(f fakeInstall) { _ = os.Remove(f.game) }, "oversized pointer": func(f fakeInstall) { writeFile(t, f.pointer, strings.Repeat(" ", 32769)) }, "wrong DLL": func(f fakeInstall) { writeFile(t, f.pointer, f.launcher) }, "missing bridge": func(f fakeInstall) { _ = os.Remove(f.bridge) }, "relative pointer": func(f fakeInstall) { writeFile(t, f.pointer, "WPGameClientSDK_64.dll") }, "directory launcher": func(f fakeInstall) { _ = os.Remove(f.launcher); _ = os.Mkdir(f.launcher, 0700) }, "missing config": func(f fakeInstall) { _ = os.Remove(configPath(f)) }, "wrong app id": func(f fakeInstall) { writeFile(t, configPath(f), "[Main]\r\nOneAppId=9\r\n") }} {
		t.Run(name, func(t *testing.T) {
			f := newFakeInstall(t)
			mutate(f)
			if _, err := inspect(context.Background(), f.root, syntheticProfile); !errors.Is(err, ErrFilesMissing) {
				t.Fatalf("wanted missing files got %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Inspect(ctx, newFakeInstall(t).root); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}
func TestPatcherConfigurationReadOnlyAndBounded(t *testing.T) {
	f := newFakeInstall(t)
	for _, config := range []string{`{"appId":1289}`, `{"appId":"1289"}`} {
		addPatcher(t, f.root, config)
		p := readPatcher(f.root)
		if p.Config != filepath.Join(f.root, `NTELauncher\ResFilesM\1289\PatcherConfig`)+`\` || p.UserData != filepath.Join(f.root, `NTELauncher\UserData`)+`\` || p.Log == "" {
			t.Fatalf("patcher %+v", p)
		}
	}
	for _, config := range []string{`{"appId":9}`, `{"appId":1289.0}`, `[]`, `{"appId":1289,"appId":9}`, strings.Repeat(" ", 1<<20+1)} {
		addPatcher(t, f.root, config)
		if p := readPatcher(f.root); p != (PatcherPaths{}) {
			t.Fatalf("accepted bad patcher %q", config[:min(len(config), 40)])
		}
	}
}
