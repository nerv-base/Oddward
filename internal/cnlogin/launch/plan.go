// SPDX-License-Identifier: GPL-3.0-or-later
package launch

import (
	"errors"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

const RequiredArguments = "/Game/LoginAndCreate/Map/Updater/Updater_P -SAVEWINPOS=1"

// PatcherPaths is either entirely empty or a validated public patcher layout.
type PatcherPaths struct{ Config, UserData, Log string }

// BuildOptions supplies public paths and inherited environment to the pure builder.
// Building a plan does not authorize starting a process; Prepare enforces the gate.
type BuildOptions struct {
	Root, Bridge, PipeName, LogDirectory, AdditionalArguments string
	ParentEnvironment                                         []string
	Patcher                                                   PatcherPaths
}

// LaunchPlan describes the CreateProcess inputs. CommandLine is arguments-only.
type LaunchPlan struct {
	Image, CommandLine, Directory string
	Environment                   []string
}

func BuildPlan(o BuildOptions) (LaunchPlan, error) {
	bad := errors.New("launch: invalid launch options")
	for _, s := range []string{o.Root, o.Bridge, o.PipeName, o.LogDirectory, o.AdditionalArguments, o.Patcher.Config, o.Patcher.UserData, o.Patcher.Log} {
		if strings.ContainsRune(s, 0) {
			return LaunchPlan{}, bad
		}
	}
	if !filepath.IsAbs(o.Root) || !filepath.IsAbs(o.Bridge) || !filepath.IsAbs(o.LogDirectory) || !strings.EqualFold(filepath.Base(o.Bridge), "WPGameClientSDK_64.dll") || len(utf16.Encode([]rune(o.AdditionalArguments))) > 8192 {
		return LaunchPlan{}, bad
	}
	root := filepath.Clean(o.Root)
	pipe := strings.TrimPrefix(o.PipeName, `\\.\pipe\`)
	if pipe == "" || len(pipe) > 256 || strings.ContainsAny(pipe, `\/`) {
		return LaunchPlan{}, bad
	}
	env := map[string]string{}
	set := func(k, v string) { env[strings.ToUpper(k)] = k + "=" + v }
	for _, entry := range o.ParentEnvironment {
		k, v, ok := splitEnvironment(entry)
		if !ok {
			return LaunchPlan{}, bad
		}
		set(k, v)
	}
	for _, k := range []string{"ArcGameAppId", "GameVersion", "_PATCHER_RES_VERSION_", "_PATCHER_CONFIG_PATH_", "_PATCHER_USERDATA_PATH_", "_PATCHER_LOG_PATH_"} {
		delete(env, strings.ToUpper(k))
	}
	set("ArcGameP", pipe)
	set("ArcGameSDKSharePath", o.Bridge[:len(o.Bridge)-len("_64.dll")])
	set("_PATCHER_RES_PATH_", root)
	// The mixed separators are part of the upstream launch contract.
	set("_PATCHER_SDK_PATH_", filepath.Join(root, "NTELauncher")+`\UserData/Patcher/PatcherSDK`)
	if o.Patcher.Config != "" && o.Patcher.UserData != "" && o.Patcher.Log != "" {
		set("_PATCHER_CONFIG_PATH_", o.Patcher.Config)
		set("_PATCHER_USERDATA_PATH_", o.Patcher.UserData)
		set("_PATCHER_LOG_PATH_", o.Patcher.Log)
	}
	for _, k := range []string{"wmgplaunch", "ArcGameMobileSdkType", "ArcGameMsgSkipFlag"} {
		set(k, "1")
	}
	set("GamePlatformPath", filepath.Join(root, "NTELauncher"))
	set("sdkLogPath", strings.TrimRight(filepath.Clean(o.LogDirectory), `\/`)+`\`)
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	entries := make([]string, 0, len(keys))
	for _, k := range keys {
		entries = append(entries, env[k])
	}
	args := RequiredArguments
	if o.AdditionalArguments != "" {
		args += " " + o.AdditionalArguments
	}
	return LaunchPlan{Image: filepath.Join(root, `Client\WindowsNoEditor\HT\Binaries\Win64\HTGame.exe`), CommandLine: args, Directory: filepath.Join(root, "Client"), Environment: entries}, nil
}

// EnvironmentBlock creates the sorted, double-NUL terminated UTF-16 Windows block.
// Duplicate keys use the last inherited value, with case-insensitive matching.
func EnvironmentBlock(env []string) ([]uint16, error) {
	entries := map[string]string{}
	for _, s := range env {
		k, _, ok := splitEnvironment(s)
		if !ok {
			return nil, errors.New("launch: invalid environment entry")
		}
		entries[strings.ToUpper(k)] = s
	}
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	block := []uint16{}
	for _, k := range keys {
		block = append(block, utf16.Encode([]rune(entries[k]))...)
		block = append(block, 0)
	}
	if len(block) == 0 {
		block = append(block, 0)
	}
	return append(block, 0), nil
}
func splitEnvironment(s string) (string, string, bool) {
	if strings.ContainsRune(s, 0) || len(s) < 2 {
		return "", "", false
	}
	start := 0
	if s[0] == '=' {
		start = 1
	}
	i := strings.IndexByte(s[start:], '=')
	if i < 0 {
		return "", "", false
	}
	i += start
	if i == 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// NormalizeVersion applies System.Version's 2–4 nonnegative Int32 components,
// excluding the all-zero version. It neither reads nor guesses installed metadata.
func NormalizeVersion(s string) (string, error) {
	parts := strings.Split(s, ".")
	bad := errors.New("launch: invalid installed version")
	if len(parts) < 2 || len(parts) > 4 {
		return "", bad
	}
	nonzero := false
	for i, p := range parts {
		if p == "" {
			return "", bad
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return "", bad
			}
		}
		v, err := strconv.ParseUint(p, 10, 31)
		if err != nil {
			return "", bad
		}
		nonzero = nonzero || v != 0
		parts[i] = strconv.FormatUint(v, 10)
	}
	if !nonzero {
		return "", bad
	}
	return strings.Join(parts, "."), nil
}
