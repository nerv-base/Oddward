// SPDX-License-Identifier: GPL-3.0-or-later
package launch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/nerv-base/Oddward/internal/cnlogin/cnproto"
)

var (
	ErrFilesMissing   = errors.New("CnFilesMissing")
	ErrUnsupportedSDK = cnproto.ErrUnsupportedSDK
	ErrClientRunning  = errors.New("CnClientRunning")
	ErrHostRunning    = errors.New("CnLoginHostRunning")
	ErrClosed         = errors.New("launch: closed")
	ErrGameRunning    = errors.New("launch: game is still running")
)

// Info is a copy of the read-only compatibility inspection. Mutating it cannot
// change the opaque Prepared authorization used by Start.
type Info struct {
	Root, GameExecutable, LauncherDirectory, LauncherExecutable, ConfigFile, Bridge string
	Profile                                                                         cnproto.Profile
	InstalledVersion                                                                string
	Patcher                                                                         PatcherPaths
}

// Inspect only reads the install and always uses the compiled compatibility table.
// Prepare additionally checks running processes and acquires the instance gate.
func Inspect(ctx context.Context, root string) (Info, error) {
	return inspect(ctx, root, cnproto.ResolveProfile)
}
func inspect(ctx context.Context, root string, resolve func(string, string) (cnproto.Profile, error)) (Info, error) {
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	if !filepath.IsAbs(root) || strings.ContainsRune(root, 0) {
		return Info{}, ErrFilesMissing
	}
	root = filepath.Clean(root)
	info := Info{Root: root, GameExecutable: filepath.Join(root, `Client\WindowsNoEditor\HT\Binaries\Win64\HTGame.exe`), LauncherDirectory: filepath.Join(root, "NTELauncher"), LauncherExecutable: filepath.Join(root, `NTELauncher\NTEGame.exe`), ConfigFile: filepath.Join(root, `NTELauncher\Config\Config.ini`)}
	for _, path := range []string{info.GameExecutable, info.LauncherExecutable} {
		s, err := os.Stat(path)
		if err != nil || !s.Mode().IsRegular() {
			return Info{}, ErrFilesMissing
		}
	}
	if err := checkOneAppID(info.ConfigFile); err != nil {
		return Info{}, err
	}
	data, err := readBounded(filepath.Join(filepath.Dir(info.GameExecutable), "GameLauncher.config"), 32768)
	if err != nil {
		return Info{}, ErrFilesMissing
	}
	pointer, err := pointerText(data)
	if err != nil {
		return Info{}, ErrFilesMissing
	}
	bridge := strings.TrimSpace(pointer)
	if !filepath.IsAbs(bridge) || strings.ContainsRune(bridge, 0) || !strings.EqualFold(filepath.Base(bridge), "WPGameClientSDK_64.dll") {
		return Info{}, ErrFilesMissing
	}
	info.Bridge = filepath.Clean(bridge)
	launcherHash, err := hashFile(ctx, info.LauncherExecutable)
	if err != nil {
		return Info{}, err
	}
	bridgeHash, err := hashFile(ctx, info.Bridge)
	if err != nil {
		return Info{}, err
	}
	info.Profile, err = resolve(launcherHash, bridgeHash)
	if err != nil {
		return Info{}, err
	}
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	info.Patcher = readPatcher(root)
	info.InstalledVersion = readInstalledVersion(root)
	return info, nil
}

var oneAppIDPattern = regexp.MustCompile(`(?m)^OneAppId=([0-9]+)\s*$`)

// checkOneAppID enforces the official public application metadata gate from
// the compatibility specification: Config.ini must name application 1289.
func checkOneAppID(path string) error {
	data, err := readBounded(path, 64<<10)
	if err != nil {
		return ErrFilesMissing
	}
	text, err := pointerText(data)
	if err != nil {
		return ErrFilesMissing
	}
	m := oneAppIDPattern.FindStringSubmatch(text)
	if m == nil {
		return ErrFilesMissing
	}
	id, err := strconv.ParseUint(m[1], 10, 32)
	if err != nil || id != 1289 {
		return ErrFilesMissing
	}
	return nil
}

var (
	resVersionPattern       = regexp.MustCompile(`<ResVersion>([0-9]+(?:\.[0-9]+)+)</ResVersion>`)
	resListVersionPattern   = regexp.MustCompile(`(?s)<ResList\b[^>]*\bversion="([^"]+)"`)
	configGameVersionRegexp = regexp.MustCompile(`(?m)^GameVersion=([0-9]+(?:\.[0-9]+)+)\s*$`)
)

// readInstalledVersion follows the compatibility specification's search order:
// patcher config.xml, then the launcher's ResList.xml, then its flat config.
// Missing or malformed metadata is not fatal: callers fall back to the launcher
// version.
func readInstalledVersion(root string) string {
	for _, file := range []struct {
		path    string
		pattern *regexp.Regexp
	}{
		{filepath.Join(root, `NTELauncher\UserData\Patcher\PatcherSDK\config.xml`), resVersionPattern},
		{filepath.Join(root, `.collapse-nte\installed\1289\ResList.xml`), resListVersionPattern},
	} {
		if version := readVersionMatch(file.path, file.pattern); version != "" {
			return version
		}
	}
	data, err := readBounded(filepath.Join(root, `.collapse_nte_config`), 64<<10)
	if err != nil {
		return ""
	}
	text, err := pointerText(data)
	if err != nil {
		return ""
	}
	if m := configGameVersionRegexp.FindStringSubmatch(text); m != nil {
		if version, err := NormalizeVersion(m[1]); err == nil {
			return version
		}
	}
	return ""
}

func readVersionMatch(path string, pattern *regexp.Regexp) string {
	data, err := readBounded(path, 64<<10)
	if err != nil {
		return ""
	}
	text, err := pointerText(data)
	if err != nil {
		return ""
	}
	m := pattern.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	version, err := NormalizeVersion(m[1])
	if err != nil {
		return ""
	}
	return version
}
func pointerText(b []byte) (string, error) {
	if bytes.HasPrefix(b, []byte{0xef, 0xbb, 0xbf}) {
		b = b[3:]
	}
	if len(b) >= 2 && (b[0] == 0xff && b[1] == 0xfe || b[0] == 0xfe && b[1] == 0xff) {
		var order binary.ByteOrder = binary.LittleEndian
		if b[0] == 0xfe {
			order = binary.BigEndian
		}
		b = b[2:]
		if len(b)%2 != 0 {
			return "", ErrFilesMissing
		}
		units := make([]uint16, len(b)/2)
		for i := range units {
			units[i] = order.Uint16(b[i*2:])
		}
		return string(utf16.Decode(units)), nil
	}
	if !utf8.Valid(b) {
		return "", ErrFilesMissing
	}
	return string(b), nil
}
func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !s.Mode().IsRegular() || s.Size() > limit {
		return nil, ErrFilesMissing
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, ErrFilesMissing
	}
	return b, nil
}
func hashFile(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", ErrFilesMissing
	}
	defer f.Close()
	s, err := f.Stat()
	if err != nil || !s.Mode().IsRegular() {
		return "", ErrFilesMissing
	}
	h := sha256.New()
	buf := make([]byte, 128<<10)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := f.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", ErrFilesMissing
		}
	}
	return strings.ToUpper(hex.EncodeToString(h.Sum(nil))), nil
}
func readPatcher(root string) PatcherPaths {
	dir := filepath.Join(root, `NTELauncher\ResFilesM\1289\PatcherConfig`)
	user := filepath.Join(root, `NTELauncher\UserData`)
	s, err := os.Stat(user)
	if err != nil || !s.IsDir() {
		return PatcherPaths{}
	}
	if _, err := readBounded(filepath.Join(dir, "PatcherPath.json"), 1<<20); err != nil {
		return PatcherPaths{}
	}
	b, err := readBounded(filepath.Join(dir, "PatcherConfig.json"), 1<<20)
	if err != nil {
		return PatcherPaths{}
	}
	d := json.NewDecoder(bytes.NewReader(b))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return PatcherPaths{}
	}
	seen := map[string]bool{}
	var id string
	for d.More() {
		tok, err = d.Token()
		if err != nil {
			return PatcherPaths{}
		}
		key, ok := tok.(string)
		if !ok || seen[key] {
			return PatcherPaths{}
		}
		seen[key] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return PatcherPaths{}
		}
		if key == "appId" {
			id = string(value)
			if len(value) > 0 && value[0] == '"' {
				if json.Unmarshal(value, &id) != nil {
					return PatcherPaths{}
				}
			}
		}
	}
	if _, err = d.Token(); err != nil {
		return PatcherPaths{}
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return PatcherPaths{}
	}
	if id == "" {
		return PatcherPaths{}
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return PatcherPaths{}
		}
	}
	n, err := strconv.ParseUint(id, 10, 32)
	if err != nil || n != 1289 {
		return PatcherPaths{}
	}
	return PatcherPaths{Config: dir + `\`, UserData: user + `\`, Log: filepath.Join(user, `Log\Patcher\log\Game`)}
}
