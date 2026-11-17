// SPDX-License-Identifier: GPL-3.0-or-later

package selfupdate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPackedReleaseStagesIntoAnIdenticalFolder(t *testing.T) {
	src := t.TempDir()
	for _, f := range appFiles("0.2.0") {
		p := filepath.Join(src, filepath.FromSlash(f.path))
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, f.body, 0o600)
	}
	var a, b bytes.Buffer
	files, err := Pack(src, &a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Pack(src, &b); err != nil || !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("packing is not reproducible")
	}
	if len(files) != 2 || files[0].Path != "licenses/loginhost/Upstream.MIT.txt" || files[1].Path != AppExe {
		t.Fatalf("files = %+v", files)
	}
	m := Manifest{Schema: 1, Repository: testRepo, Version: "0.2.0", Package: Asset{Name: "oddward-0.2.0-app.zip", Size: int64(a.Len()), SHA256: digest(a.Bytes())}, Files: files}
	root, l := installed(t, "0.1.0")
	gh := &fakeGitHub{assets: map[string][]byte{packageURL(m): a.Bytes()}}
	dir, err := newUpdater(t, l, gh).Stage(context.Background(), m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(root, "app-0.2.0") {
		t.Fatal(dir)
	}
}

func TestPackRefusesWhatAnUpdateCouldNotCarry(t *testing.T) {
	for name, layout := range map[string]map[string]string{
		"no launcher": {"readme.txt": "x"},
		"marker file": {AppExe: "MZ", ".ready": "x"},
		"unsafe name": {AppExe: "MZ", "a%b.txt": "x"},
		"nested dot":  {AppExe: "MZ", "licenses/.hidden": "x"},
	} {
		t.Run(name, func(t *testing.T) {
			src := t.TempDir()
			for p, body := range layout {
				full := filepath.Join(src, filepath.FromSlash(p))
				os.MkdirAll(filepath.Dir(full), 0o700)
				os.WriteFile(full, []byte(body), 0o600)
			}
			if _, err := Pack(src, new(bytes.Buffer)); err == nil {
				t.Fatal("packed")
			}
		})
	}
}
