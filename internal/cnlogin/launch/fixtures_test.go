// SPDX-License-Identifier: GPL-3.0-or-later

package launch

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeInstall is a synthetic install tree in a temp dir. It contains no
// official binaries: every "executable" is a few placeholder bytes.
type fakeInstall struct {
	root, game, launcher, pointer, bridge string
}

func newFakeInstall(t *testing.T) fakeInstall {
	t.Helper()
	root := filepath.Join(t.TempDir(), "Game Root")
	f := fakeInstall{
		root:     root,
		game:     filepath.Join(root, `Client\WindowsNoEditor\HT\Binaries\Win64\HTGame.exe`),
		launcher: filepath.Join(root, `NTELauncher\NTEGame.exe`),
		pointer:  filepath.Join(root, `Client\WindowsNoEditor\HT\Binaries\Win64\GameLauncher.config`),
		bridge:   filepath.Join(root, `NTELauncher\sdk\WPGameClientSDK_64.dll`),
	}
	writeFile(t, f.game, "game-placeholder")
	writeFile(t, f.launcher, "launcher-placeholder")
	writeFile(t, f.bridge, "bridge-placeholder")
	writeFile(t, f.pointer, "  "+f.bridge+"\r\n")
	writeFile(t, filepath.Join(root, `NTELauncher\Config\Config.ini`), "[Main]\r\nOneAppId=1289\r\nOther=1\r\n")
	return f
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sha256Upper(s string) string {
	sum := sha256.Sum256([]byte(s))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// addPatcher creates a valid patcher configuration tree.
func addPatcher(t *testing.T, root, configJSON string) {
	t.Helper()
	dir := filepath.Join(root, `NTELauncher\ResFilesM\1289\PatcherConfig`)
	writeFile(t, filepath.Join(dir, "PatcherPath.json"), "{}")
	writeFile(t, filepath.Join(dir, "PatcherConfig.json"), configJSON)
	if err := os.MkdirAll(filepath.Join(root, `NTELauncher\UserData`), 0o755); err != nil {
		t.Fatal(err)
	}
}
