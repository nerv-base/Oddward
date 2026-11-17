// SPDX-License-Identifier: GPL-3.0-or-later
package launch

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
)

func planFixture() BuildOptions {
	return BuildOptions{Root: `D:\Game Root\`, Bridge: `D:\SDK\WPGameClientSDK_64.dll`, PipeName: `\\.\pipe\Collapse.NTE.Login.0123456789abcdef0123456789abcdef`, LogDirectory: `C:\Private Logs\run`, AdditionalArguments: `-dx12 -name="two words"`, ParentEnvironment: []string{`Path=C:\Windows`, "KEEP=保留", "arcgameappid=bad", "GameVersion=bad", "_PATCHER_RES_VERSION_=bad", "_PATCHER_CONFIG_PATH_=bad", "_PATCHER_USERDATA_PATH_=bad", "_PATCHER_LOG_PATH_=bad", "ARCGAMEP=old", "=D:=D:\\work"}}
}

func TestBuildPlanArgumentsAndEnvironment(t *testing.T) {
	p, err := BuildPlan(planFixture())
	if err != nil {
		t.Fatal(err)
	}
	if p.Image != `D:\Game Root\Client\WindowsNoEditor\HT\Binaries\Win64\HTGame.exe` || p.Directory != `D:\Game Root\Client` {
		t.Fatalf("paths: %+v", p)
	}
	if p.CommandLine != `/Game/LoginAndCreate/Map/Updater/Updater_P -SAVEWINPOS=1 -dx12 -name="two words"` {
		t.Fatalf("arguments: %q", p.CommandLine)
	}
	env := map[string]string{}
	for _, entry := range p.Environment {
		k, v, ok := splitEnvironment(entry)
		if !ok {
			t.Fatalf("bad entry %q", entry)
		}
		key := strings.ToUpper(k)
		if _, exists := env[key]; exists {
			t.Fatalf("duplicate %s", k)
		}
		env[key] = v
	}
	want := map[string]string{"PATH": `C:\Windows`, "KEEP": "保留", "=D:": `D:\work`, "ARCGAMEP": "Collapse.NTE.Login.0123456789abcdef0123456789abcdef", "ARCGAMESDKSHAREPATH": `D:\SDK\WPGameClientSDK`, "_PATCHER_RES_PATH_": `D:\Game Root`, "_PATCHER_SDK_PATH_": `D:\Game Root\NTELauncher\UserData/Patcher/PatcherSDK`, "WMGPLAUNCH": "1", "ARCGAMEMOBILESDKTYPE": "1", "ARCGAMEMSGSKIPFLAG": "1", "GAMEPLATFORMPATH": `D:\Game Root\NTELauncher`, "SDKLOGPATH": `C:\Private Logs\run\`}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("environment got %#v want %#v", env, want)
	}
	block, err := EnvironmentBlock(p.Environment)
	if err != nil {
		t.Fatal(err)
	}
	if len(block) < 2 || block[len(block)-1] != 0 || block[len(block)-2] != 0 {
		t.Fatal("missing double NUL")
	}
	if !strings.Contains(string(utf16.Decode(block)), "KEEP=保留\x00") {
		t.Fatal("UTF-16 environment lost unicode")
	}
}

func TestBuildPlanPatcherAndInputBounds(t *testing.T) {
	o := planFixture()
	o.Patcher = PatcherPaths{Config: `D:\config\`, UserData: `D:\data\`, Log: `D:\data\Log`}
	p, err := BuildPlan(o)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(p.Environment, `_PATCHER_CONFIG_PATH_=D:\config\`) {
		t.Fatal("patcher override missing")
	}
	for name, mutate := range map[string]func(*BuildOptions){"relative root": func(o *BuildOptions) { o.Root = "relative" }, "NUL arguments": func(o *BuildOptions) { o.AdditionalArguments = "bad\x00arg" }, "oversize args": func(o *BuildOptions) { o.AdditionalArguments = strings.Repeat("a", 8193) }, "bad pipe": func(o *BuildOptions) { o.PipeName = "bad\\pipe" }, "bad environment": func(o *BuildOptions) { o.ParentEnvironment = []string{"X=bad\x00value"} }, "bad bridge": func(o *BuildOptions) { o.Bridge = `D:\SDK\other.dll` }} {
		t.Run(name, func(t *testing.T) {
			o := planFixture()
			mutate(&o)
			if _, err := BuildPlan(o); err == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
	if b, err := EnvironmentBlock(nil); err != nil || !reflect.DeepEqual(b, []uint16{0, 0}) {
		t.Fatalf("empty block %v %v", b, err)
	}
}
func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func TestNormalizeVersion(t *testing.T) {
	for _, s := range []string{"1.2", "01.02.003.4", "0.0.1"} {
		got, err := NormalizeVersion(s)
		if err != nil || strings.HasPrefix(got, "01") {
			t.Fatalf("%q => %q, %v", s, got, err)
		}
	}
	for _, s := range []string{"1", "0.0", "1.2.3.4.5", "1.-2", "1.2147483648", "1. 2", " 1.2", "1.2\x00"} {
		if _, err := NormalizeVersion(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
}
