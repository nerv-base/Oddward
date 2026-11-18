// SPDX-License-Identifier: GPL-3.0-or-later

package game

import (
	"context"
	"debug/pe"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "异环 test", "Client", "WindowsNoEditor")
	for _, d := range []string{"HT/Binaries/Win64", "HT/Content/Paks", "PatcherConfig"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// Minimal parseable PE metadata, not a runnable game or authenticity fixture.
	f, err := os.Create(filepath.Join(root, "HT", "Binaries", "Win64", "HTGame.exe"))
	if err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 64)
	head[0] = 'M'
	head[1] = 'Z'
	binary.LittleEndian.PutUint32(head[60:], 64)
	if _, err = f.Write(append(head, 'P', 'E', 0, 0)); err != nil {
		t.Fatal(err)
	}
	fh := pe.FileHeader{Machine: pe.IMAGE_FILE_MACHINE_AMD64, SizeOfOptionalHeader: 240, Characteristics: pe.IMAGE_FILE_EXECUTABLE_IMAGE}
	if err := binary.Write(f, binary.LittleEndian, fh); err != nil {
		t.Fatal(err)
	}
	oh := pe.OptionalHeader64{Magic: 0x20b, NumberOfRvaAndSizes: 16}
	if err := binary.Write(f, binary.LittleEndian, oh); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "PatcherConfig", "Patch_Windows.json"), `{"appId":"1289","branchName":"PC_150","gameResUrl":["https://yhcdn1.wmupd.com/clientRes"]}`)
	return root
}
func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestInspectSupportedFolderLevels(t *testing.T) {
	root := fixture(t)
	// Inspect reports the caller's spelling of the tree it accepted. A temp directory can
	// be reached through its 8.3 short name (CI uses C:\Users\RUNNER~1\...), so compare
	// the directory each path names, not the characters it was spelled with.
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	wantExecutable := filepath.Join(want, filepath.FromSlash(executable))
	for _, selected := range []string{root, filepath.Dir(root), filepath.Dir(filepath.Dir(root))} {
		t.Run(filepath.Base(selected), func(t *testing.T) {
			got, err := Inspect(context.Background(), selected)
			if err != nil {
				t.Fatal(err)
			}
			gotRoot, err := filepath.EvalSymlinks(got.Root)
			if err != nil {
				t.Fatal(err)
			}
			gotExecutable, err := filepath.EvalSymlinks(got.Executable)
			if err != nil {
				t.Fatal(err)
			}
			if gotRoot != want || gotExecutable != wantExecutable || got.AppID != "1289" || got.Branch != "PC_150" {
				t.Fatalf("unexpected installation: %+v (root %q, executable %q)", got, gotRoot, gotExecutable)
			}
		})
	}
}
func TestInspectRejectsBadLayouts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, string)
	}{
		{"missing executable", func(t *testing.T, r string) {
			if err := os.Remove(filepath.Join(r, "HT", "Binaries", "Win64", "HTGame.exe")); err != nil {
				t.Fatal(err)
			}
		}},
		{"directory executable", func(t *testing.T, r string) {
			p := filepath.Join(r, "HT", "Binaries", "Win64", "HTGame.exe")
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(p, 0700); err != nil {
				t.Fatal(err)
			}
		}},
		{"invalid PE", func(t *testing.T, r string) {
			write(t, filepath.Join(r, "HT", "Binaries", "Win64", "HTGame.exe"), "not a PE")
		}},
		{"wrong machine", func(t *testing.T, r string) {
			p := filepath.Join(r, "HT", "Binaries", "Win64", "HTGame.exe")
			f, e := os.OpenFile(p, os.O_WRONLY, 0)
			if e != nil {
				t.Fatal(e)
			}
			defer f.Close()
			if _, e = f.WriteAt([]byte{0x4c, 0x01}, 68); e != nil {
				t.Fatal(e)
			}
		}},
		{"missing content", func(t *testing.T, r string) {
			if e := os.Remove(filepath.Join(r, "HT", "Content", "Paks")); e != nil {
				t.Fatal(e)
			}
		}},
		{"content path is a file", func(t *testing.T, r string) {
			p := filepath.Join(r, "HT", "Content", "Paks")
			if e := os.Remove(p); e != nil {
				t.Fatal(e)
			}
			write(t, p, "not a directory")
		}},
		{"missing config", func(t *testing.T, r string) {
			if e := os.Remove(filepath.Join(r, "PatcherConfig", "Patch_Windows.json")); e != nil {
				t.Fatal(e)
			}
		}},
		{"config as directory", func(t *testing.T, r string) {
			p := filepath.Join(r, "PatcherConfig", "Patch_Windows.json")
			if e := os.Remove(p); e != nil {
				t.Fatal(e)
			}
			if e := os.Mkdir(p, 0700); e != nil {
				t.Fatal(e)
			}
		}},
		{"missing config directory", func(t *testing.T, r string) {
			if e := os.RemoveAll(filepath.Join(r, "PatcherConfig")); e != nil {
				t.Fatal(e)
			}
		}},
		{"invalid config", func(t *testing.T, r string) { write(t, filepath.Join(r, "PatcherConfig", "Patch_Windows.json"), "{") }},
		{"different game", func(t *testing.T, r string) {
			write(t, filepath.Join(r, "PatcherConfig", "Patch_Windows.json"), `{"appId":"other","branchName":"PC_150"}`)
		}},
		{"unsafe branch", func(t *testing.T, r string) {
			write(t, filepath.Join(r, "PatcherConfig", "Patch_Windows.json"), `{"appId":"1289","branchName":"../other"}`)
		}},
		{"trailing config", func(t *testing.T, r string) {
			write(t, filepath.Join(r, "PatcherConfig", "Patch_Windows.json"), `{"appId":"1289","branchName":"PC_150"}{}`)
		}},
		{"oversized config", func(t *testing.T, r string) {
			write(t, filepath.Join(r, "PatcherConfig", "Patch_Windows.json"), string(make([]byte, 1<<20+1)))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := fixture(t)
			tc.change(t, r)
			if _, e := Inspect(context.Background(), r); !errors.Is(e, ErrNotInstallation) {
				t.Fatalf("got %v; want ErrNotInstallation", e)
			}
		})
	}
}
func TestInspectRejectsEmptyOrFileInput(t *testing.T) {
	r := fixture(t)
	for _, p := range []string{"", filepath.Join(r, "HT", "Binaries", "Win64", "HTGame.exe"), filepath.Join(r, "missing")} {
		if _, e := Inspect(context.Background(), p); !errors.Is(e, ErrNotInstallation) {
			t.Fatalf("%q got %v", p, e)
		}
	}
}
func TestInspectCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := Inspect(ctx, fixture(t)); !errors.Is(e, context.Canceled) {
		t.Fatalf("got %v", e)
	}
}
func TestInspectDoesNotChangeFiles(t *testing.T) {
	r := fixture(t)
	p := filepath.Join(r, "PatcherConfig", "Patch_Windows.json")
	before, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Inspect(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	after, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	if string(before) != string(after) {
		t.Fatal("inspection modified config")
	}
}

// makeJunction creates a directory junction with cmd's mklink, which needs no
// symlink privilege on ordinary NTFS, so a missing capability is a test failure
// rather than a skip.
func makeJunction(t *testing.T, link, target string) {
	t.Helper()
	out, err := exec.Command("cmd", "/d", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Fatalf("mklink /J %q -> %q failed: %v: %s", link, target, err, strings.TrimSpace(string(out)))
	}
}

func TestInspectRejectsEscapingLink(t *testing.T) {
	r := fixture(t)
	other := fixture(t)
	p := filepath.Join(r, "PatcherConfig", "Patch_Windows.json")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, "PatcherConfig", "Patch_Windows.json"), p); err != nil {
		// Fall back to a junction of the containing directory instead of skipping:
		// unprivileged processes can create junctions on ordinary NTFS.
		dir := filepath.Join(r, "PatcherConfig")
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		makeJunction(t, dir, filepath.Join(other, "PatcherConfig"))
	}
	if _, err := Inspect(context.Background(), r); !errors.Is(err, ErrNotInstallation) {
		t.Fatalf("escaping link accepted: %v", err)
	}
}

func TestInspectRejectsJunctionEscape(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(t *testing.T, r string) // replaces a required path with an escaping junction
	}{
		{"content directory", func(t *testing.T, r string) {
			outside := t.TempDir()
			if err := os.MkdirAll(filepath.Join(outside, "Paks"), 0700); err != nil {
				t.Fatal(err)
			}
			paks := filepath.Join(r, "HT", "Content", "Paks")
			if err := os.Remove(paks); err != nil {
				t.Fatal(err)
			}
			makeJunction(t, paks, filepath.Join(outside, "Paks"))
		}},
		{"executable directory", func(t *testing.T, r string) {
			outside := t.TempDir()
			image, _ := syntheticPE(t)
			writeBytes(t, filepath.Join(outside, "HTGame.exe"), image)
			dir := filepath.Join(r, "HT", "Binaries", "Win64")
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			makeJunction(t, dir, outside)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := fixture(t)
			tc.build(t, r)
			if _, err := Inspect(context.Background(), r); !errors.Is(err, ErrNotInstallation) {
				t.Fatalf("junction escape accepted: %v", err)
			}
		})
	}
}

// syntheticPE returns a minimal file whose header fields pass the structural PE
// checks, plus a slice aliasing the 26 header bytes checkPE64 reads so callers can
// corrupt individual fields. It is synthetic metadata, not a runnable game.
func syntheticPE(t *testing.T) (image, header []byte) {
	t.Helper()
	image = make([]byte, 64+26)
	image[0], image[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(image[60:], 64)
	header = image[64:]
	copy(header, "PE\x00\x00")
	binary.LittleEndian.PutUint16(header[4:], pe.IMAGE_FILE_MACHINE_AMD64)
	binary.LittleEndian.PutUint16(header[20:], 240) // SizeOfOptionalHeader
	binary.LittleEndian.PutUint16(header[22:], pe.IMAGE_FILE_EXECUTABLE_IMAGE)
	binary.LittleEndian.PutUint16(header[24:], 0x20b) // PE32+ optional header magic
	return image, header
}

func writeBytes(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func exeFile(root string) string {
	return filepath.Join(root, "HT", "Binaries", "Win64", "HTGame.exe")
}

func TestInspectRejectsMalformedPE(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(image, header []byte) []byte
	}{
		{"bad DOS signature", func(image, header []byte) []byte {
			image[0] = 'N'
			return image
		}},
		{"truncated DOS header", func(image, header []byte) []byte { return image[:10] }},
		{"header offset below minimum", func(image, header []byte) []byte {
			binary.LittleEndian.PutUint32(image[60:], 8)
			return image
		}},
		{"header offset beyond limit", func(image, header []byte) []byte {
			binary.LittleEndian.PutUint32(image[60:], 1<<21)
			return image
		}},
		{"header offset past end of file", func(image, header []byte) []byte {
			binary.LittleEndian.PutUint32(image[60:], uint32(len(image)-10))
			return image
		}},
		{"bad PE signature", func(image, header []byte) []byte {
			copy(header, "PX\x00\x00")
			return image
		}},
		{"wrong machine", func(image, header []byte) []byte {
			binary.LittleEndian.PutUint16(header[4:], 0x14c)
			return image
		}},
		{"missing executable image bit", func(image, header []byte) []byte {
			binary.LittleEndian.PutUint16(header[22:], 0x0020)
			return image
		}},
		{"dll image", func(image, header []byte) []byte {
			binary.LittleEndian.PutUint16(header[22:], pe.IMAGE_FILE_EXECUTABLE_IMAGE|0x2000)
			return image
		}},
		{"PE32 optional header", func(image, header []byte) []byte {
			binary.LittleEndian.PutUint16(header[24:], 0x10b)
			return image
		}},
		{"missing optional header", func(image, header []byte) []byte {
			binary.LittleEndian.PutUint16(header[20:], 0)
			return image
		}},
		{"truncated before header tail", func(image, header []byte) []byte { return image[:64+24] }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := fixture(t)
			image, header := syntheticPE(t)
			writeBytes(t, exeFile(r), tc.mutate(image, header))
			if _, err := Inspect(context.Background(), r); !errors.Is(err, ErrNotInstallation) {
				t.Fatalf("got %v; want ErrNotInstallation", err)
			}
		})
	}
	t.Run("unmutated image is accepted", func(t *testing.T) {
		r := fixture(t)
		image, _ := syntheticPE(t)
		writeBytes(t, exeFile(r), image)
		if _, err := Inspect(context.Background(), r); err != nil {
			t.Fatalf("synthetic control image rejected: %v", err)
		}
	})
}

func TestRejectUnsupportedPathForm(t *testing.T) {
	for _, p := range []string{
		`\\server\share\Client\WindowsNoEditor`,
		`//server/share/Client/WindowsNoEditor`,
		`\\?\C:\game`,
		`\\.\C:\game`,
		`\\?\UNC\server\share`,
		`C:game`,
		`c:..\game`,
		`\game`,
		`/game`,
		"",
		"bad\x00path",
	} {
		if err := rejectUnsupportedPathForm(p); err == nil {
			t.Errorf("rejectUnsupportedPathForm(%q) = nil; want rejection", p)
		}
	}
	for _, p := range []string{`C:\game`, `c:\game\Client`, `Z:/game`, `Client`, `.\Client`, `..\Client`} {
		if err := rejectUnsupportedPathForm(p); err != nil {
			t.Errorf("rejectUnsupportedPathForm(%q) = %v; want nil", p, err)
		}
	}
}

func TestClassifyDriveType(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind uint32
		ok   bool
	}{
		{"unknown", driveUnknown, false},
		{"no root directory", driveNoRootDir, false},
		{"removable", driveRemovable, true},
		{"fixed", driveFixed, true},
		{"remote", driveRemote, false},
		{"cdrom", driveCDROM, false},
		{"ramdisk", driveRAMDisk, false},
		{"unrecognized", 99, false},
		{"unrecognized high bit", 0x80000003, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := classifyDriveType(tc.kind)
			if tc.ok && err != nil {
				t.Fatalf("classifyDriveType(%d) = %v; want accepted", tc.kind, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("classifyDriveType(%d) = nil; want rejection", tc.kind)
			}
		})
	}
	if err := classifyDriveType(driveRemote); err == nil || !strings.Contains(err.Error(), "network") {
		t.Fatalf("remote volume rejection lacks a diagnostic: %v", err)
	}
}

func TestVolumeRoot(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{`C:\game\x`, `C:\`},
		{`d:/game/x`, `d:\`},
		{`Z:\`, `Z:\`},
	} {
		if got, err := volumeRoot(tc.path); err != nil || got != tc.want {
			t.Errorf("volumeRoot(%q) = %q, %v; want %q", tc.path, got, err, tc.want)
		}
	}
	for _, p := range []string{`\\server\share`, `C:game`, `\game`, `game`, ``, `C`} {
		if _, err := volumeRoot(p); err == nil {
			t.Errorf("volumeRoot(%q) = nil error; want rejection", p)
		}
	}
}

// TestCheckLocalVolumeAcceptsLocalTestVolume is the positive side of the volume
// classifier: the test fixture lives on a real local volume, so the probe must accept it.
func TestCheckLocalVolumeAcceptsLocalTestVolume(t *testing.T) {
	dir := t.TempDir()
	if err := checkLocalVolume(dir); err != nil {
		t.Fatalf("local temporary directory rejected: %v", err)
	}
	if err := checkLocalVolume(strings.ToLower(dir)); err != nil {
		t.Fatalf("lower-case drive letter rejected: %v", err)
	}
	if err := checkLocalVolume(fixture(t)); err != nil {
		t.Fatalf("local installation fixture rejected: %v", err)
	}
}

// TestInspectRejectsUnsupportedPathForms rejects UNC, device/extended-namespace,
// drive-relative and root-relative selections syntactically: the diagnostic comes from
// the path-form check, and no filesystem or network access is attempted for them.
func TestInspectRejectsUnsupportedPathForms(t *testing.T) {
	for _, p := range []string{
		`\\oddward-no-such-host\share\Client\WindowsNoEditor`,
		`//oddward-no-such-host/share/Client/WindowsNoEditor`,
		`\\?\C:\game`,
		`\\.\C:\game`,
		`C:relative\game`,
		`\game`,
	} {
		_, err := Inspect(context.Background(), p)
		if !errors.Is(err, ErrNotInstallation) {
			t.Fatalf("Inspect(%q) = %v; want ErrNotInstallation", p, err)
		}
		if !strings.Contains(err.Error(), "unsupported path form") {
			t.Fatalf("Inspect(%q) was not rejected syntactically: %v", p, err)
		}
	}
}

func TestInspectReportsCandidatesAndPreservesMissingPath(t *testing.T) {
	r := fixture(t)
	if err := os.Remove(exeFile(r)); err != nil {
		t.Fatal(err)
	}
	_, err := Inspect(context.Background(), r)
	if !errors.Is(err, ErrNotInstallation) {
		t.Fatalf("got %v; want ErrNotInstallation", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing executable error not preserved with %%w: %v", err)
	}
	for _, candidate := range []string{r, filepath.Join(r, "WindowsNoEditor"), filepath.Join(r, "Client", "WindowsNoEditor")} {
		if !strings.Contains(err.Error(), candidate) {
			t.Fatalf("candidate %q not labelled in: %v", candidate, err)
		}
	}
	if !strings.Contains(err.Error(), "HTGame.exe") {
		t.Fatalf("missing file name not reported: %v", err)
	}
	if _, err := Inspect(context.Background(), filepath.Join(t.TempDir(), "missing")); !errors.Is(err, ErrNotInstallation) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing selection: got %v; want wrapped ErrNotInstallation and os.ErrNotExist", err)
	}
}
