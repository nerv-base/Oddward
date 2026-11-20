// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

func adoptPut(t *testing.T, root, name string, data []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func adoptFixture(t *testing.T) (string, []byte) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "synthetic install")
	pe := make([]byte, 90)
	copy(pe, "MZ")
	binary.LittleEndian.PutUint32(pe[60:64], 64)
	copy(pe[64:68], "PE\x00\x00")
	binary.LittleEndian.PutUint16(pe[68:70], 0x8664)
	binary.LittleEndian.PutUint16(pe[84:86], 2)
	binary.LittleEndian.PutUint16(pe[86:88], 2)
	binary.LittleEndian.PutUint16(pe[88:90], 0x20b)
	adoptPut(t, root, "Client/WindowsNoEditor/HT/Binaries/Win64/HTGame.exe", pe)
	adoptPut(t, root, "Client/WindowsNoEditor/PatcherConfig/Patch_Windows.json", []byte(`{"appId":"1289","branchName":"PC_150"}`))
	adoptPut(t, root, "Client/WindowsNoEditor/HT/Content/Paks/a.pak", []byte("correct first"))
	adoptPut(t, root, "Client/WindowsNoEditor/HT/Content/Paks/b.pak", []byte("correct second"))
	manifest := []byte(fmt.Sprintf(`<ResList version="1.2.3" tag="baseTag"><Res filename="Client/WindowsNoEditor/HT/Content/Paks/a.pak" md5="%x" filesize="13"/><Res filename="Client/WindowsNoEditor/HT/Content/Paks/b.pak" md5="%x" filesize="14"/><BaseVersion version="tag-version"><Res filename="Client/WindowsNoEditor/HT/Content/Paks/tag.pak" md5="%x" filesize="3"/></BaseVersion></ResList>`, md5.Sum([]byte("correct first")), md5.Sum([]byte("correct second")), md5.Sum([]byte("tag"))))
	adoptPut(t, root, "NTELauncher/UserData/Patcher/PatcherSDK/ResList.xml", manifest)
	adoptPut(t, root, "NTELauncher/UserData/Patcher/PatcherSDK/config.xml", adoptConfig(manifest))
	return root, manifest
}
func adoptConfig(manifest []byte) []byte {
	return []byte(fmt.Sprintf(`<config><ResVersion>1.2.3</ResVersion><LocalBranch>PC_150</LocalBranch><Extra><listHash>%x</listHash></Extra></config>`, md5.Sum(manifest)))
}
func adoptStore(t *testing.T) *Store {
	t.Helper()
	return newStore(t, dbtest.Open(t), t.TempDir())
}

// newStore opens a store over an explicit database, so a test can restart the
// launcher against the records a previous store committed.
func newStore(t *testing.T, db *database.DB, stateRoot string) *Store {
	t.Helper()
	s, err := NewStore(db, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// preloadStateStore opens the database-backed preload store over fresh roots.
func preloadStateStore(t *testing.T, engineCache bool) PreloadStore {
	t.Helper()
	s, err := NewPreloadStore(dbtest.Open(t), t.TempDir(), t.TempDir(), engineCache)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func adoptSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		out[path] = fmt.Sprintf("%x|%s", md5.Sum(b), st.ModTime())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}
