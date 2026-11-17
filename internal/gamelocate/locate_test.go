// SPDX-License-Identifier: GPL-3.0-or-later

package gamelocate

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nerv-base/Oddward/internal/game"
)

// locateFixture creates only tiny synthetic headers/metadata, never a runnable
// game, official asset, account record, or copy of a daily installation.
func locateFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "异环 synthetic")
	content := filepath.Join(root, "Client", "WindowsNoEditor")
	for _, dir := range []string{"HT/Binaries/Win64", "HT/Content/Paks", "PatcherConfig"} {
		if err := os.MkdirAll(filepath.Join(content, filepath.FromSlash(dir)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	pe := make([]byte, 90)
	copy(pe, "MZ")
	binary.LittleEndian.PutUint32(pe[60:64], 64)
	copy(pe[64:68], "PE\x00\x00")
	binary.LittleEndian.PutUint16(pe[68:70], 0x8664)
	binary.LittleEndian.PutUint16(pe[84:86], 2)
	binary.LittleEndian.PutUint16(pe[86:88], 0x0002)
	binary.LittleEndian.PutUint16(pe[88:90], 0x20b)
	putFixture(t, filepath.Join(content, "HT", "Binaries", "Win64", "HTGame.exe"), pe)
	putFixture(t, filepath.Join(content, "PatcherConfig", "Patch_Windows.json"), []byte(`{"appId":"1289","branchName":"PC_150"}`))
	return root
}

func putFixture(t *testing.T, name string, value []byte) {
	t.Helper()
	if err := os.WriteFile(name, value, 0600); err != nil {
		t.Fatal(err)
	}
}

func selections(root string) []struct {
	path string
	kind SelectionKind
} {
	return []struct {
		path string
		kind SelectionKind
	}{
		{root, SelectedRoot},
		{filepath.Join(root, "Client"), SelectedClient},
		{filepath.Join(root, "Client", "WindowsNoEditor"), SelectedContentRoot},
		{filepath.Join(root, "Client", "WindowsNoEditor", "HT", "Binaries", "Win64", "HTGame.exe"), SelectedExecutable},
	}
}

func TestInspectNormalizesFourExplicitShapes(t *testing.T) {
	root := locateFixture(t)
	before := snapshot(t, root)
	for _, selected := range selections(root) {
		t.Run(filepath.Base(selected.path), func(t *testing.T) {
			got, err := Inspect(context.Background(), selected.path)
			if err != nil {
				t.Fatal(err)
			}
			want := Installation{
				Root: root, Client: filepath.Join(root, "Client"), ContentRoot: filepath.Join(root, "Client", "WindowsNoEditor"),
				Executable: filepath.Join(root, "Client", "WindowsNoEditor", "HT", "Binaries", "Win64", "HTGame.exe"),
				AppID:      "1289", Branch: "PC_150", SelectedKind: selected.kind,
			}
			if got != want {
				t.Fatalf("selection must normalize to the installation root, got %+v want %+v", got, want)
			}
		})
	}
	if after := snapshot(t, root); !reflect.DeepEqual(after, before) {
		t.Fatal("inspection must not create or modify installation files")
	}
}

func TestInspectRetainsStrictUnderlyingIdentityChecks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, string)
	}{
		{"wrong game", func(t *testing.T, r string) {
			putFixture(t, filepath.Join(r, "PatcherConfig", "Patch_Windows.json"), []byte(`{"appId":"other","branchName":"PC_150"}`))
		}},
		{"malformed config", func(t *testing.T, r string) {
			putFixture(t, filepath.Join(r, "PatcherConfig", "Patch_Windows.json"), []byte(`{`))
		}},
		{"unsafe branch", func(t *testing.T, r string) {
			putFixture(t, filepath.Join(r, "PatcherConfig", "Patch_Windows.json"), []byte(`{"appId":"1289","branchName":"../other"}`))
		}},
		{"not a PE", func(t *testing.T, r string) {
			putFixture(t, filepath.Join(r, "HT", "Binaries", "Win64", "HTGame.exe"), []byte("not executable metadata"))
		}},
		{"x86 PE", func(t *testing.T, r string) {
			p := filepath.Join(r, "HT", "Binaries", "Win64", "HTGame.exe")
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			binary.LittleEndian.PutUint16(b[68:70], 0x014c)
			putFixture(t, p, b)
		}},
		{"missing content", func(t *testing.T, r string) {
			p := filepath.Join(r, "HT", "Content", "Paks")
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing executable", func(t *testing.T, r string) {
			p := filepath.Join(r, "HT", "Binaries", "Win64", "HTGame.exe")
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := locateFixture(t)
			tc.change(t, filepath.Join(root, "Client", "WindowsNoEditor"))
			for _, selected := range selections(root) {
				if got, err := Inspect(context.Background(), selected.path); !errors.Is(err, ErrNotInstallation) || got != (Installation{}) {
					t.Fatalf("must reject %s through every input shape: got %+v err=%v", tc.name, got, err)
				}
			}
		})
	}
}

func TestInspectRequiresAbsoluteLocalInput(t *testing.T) {
	for _, path := range []string{"", ".", `Client`, `C:relative`, `\root-relative`, `\\server\share\Client`, `\\?\C:\Client`, `\\.\C:\Client`, "C:\\bad\x00path"} {
		t.Run(path, func(t *testing.T) {
			if got, err := Inspect(context.Background(), path); !errors.Is(err, ErrNotInstallation) || got != (Installation{}) {
				t.Fatalf("must reject unsupported selection before treating it as installation: got %+v err=%v", got, err)
			}
		})
	}
}

func TestInspectDoesNotSearchParentsOrDescendants(t *testing.T) {
	root := locateFixture(t)
	for _, path := range []string{filepath.Dir(root), filepath.Join(root, "Client", "WindowsNoEditor", "HT"), filepath.Join(root, "Client", "WindowsNoEditor", "HT", "Binaries", "Win64")} {
		if got, err := Inspect(context.Background(), path); !errors.Is(err, ErrNotInstallation) || got != (Installation{}) {
			t.Fatalf("only the four explicit shapes are supported, not recursive/parent search: got %+v err=%v", got, err)
		}
	}
}

func TestInspectRejectsDifferentExecutableSelection(t *testing.T) {
	root := locateFixture(t)
	exe := filepath.Join(root, "Client", "WindowsNoEditor", "HT", "Binaries", "Win64", "Other.exe")
	putFixture(t, exe, []byte("not the selected game"))
	if got, err := Inspect(context.Background(), exe); !errors.Is(err, ErrNotInstallation) || got != (Installation{}) {
		t.Fatalf("a neighboring arbitrary executable must not select HTGame.exe: %+v %v", got, err)
	}
}

func TestInspectHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := Inspect(ctx, `Z:\synthetic-not-opened`); !errors.Is(err, context.Canceled) || got != (Installation{}) {
		t.Fatalf("cancellation must survive the adapter: %+v %v", got, err)
	}
}

func TestInspectRejectsUnsupportedRootLayout(t *testing.T) {
	root := locateFixture(t)
	client := filepath.Join(root, "Client")
	moved := filepath.Join(root, "Other")
	if err := os.Rename(client, moved); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(moved, "WindowsNoEditor")
	if _, err := game.Inspect(context.Background(), path); err != nil {
		t.Fatalf("control fixture must pass underlying validator: %v", err)
	}
	if got, err := Inspect(context.Background(), path); !errors.Is(err, ErrNotInstallation) || got != (Installation{}) {
		t.Fatalf("installation root requires Client/WindowsNoEditor, not a guessed arbitrary parent: %+v %v", got, err)
	}
}

func TestInspectDoesNotDescendIntoAlternateLayout(t *testing.T) {
	root := locateFixture(t)
	content := filepath.Join(root, "Client", "WindowsNoEditor")
	nested := filepath.Join(content, "Client", "WindowsNoEditor")
	if err := os.MkdirAll(filepath.Dir(nested), 0700); err != nil {
		t.Fatal(err)
	}
	// Build a valid nested installation under an invalid selected ContentRoot.
	other := locateFixture(t)
	otherContent := filepath.Join(other, "Client", "WindowsNoEditor")
	if err := os.Rename(otherContent, nested); err != nil {
		t.Fatal(err)
	}
	putFixture(t, filepath.Join(content, "PatcherConfig", "Patch_Windows.json"), []byte(`{"appId":"wrong","branchName":"PC_150"}`))
	if got, err := Inspect(context.Background(), content); !errors.Is(err, ErrNotInstallation) || got != (Installation{}) {
		t.Fatalf("must not substitute a nested installation for the selected ContentRoot: %+v %v", got, err)
	}
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			out[rel] = "directory"
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		out[rel] = string(data) + "|" + info.ModTime().String()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSelectionCaseAndCleanSyntax(t *testing.T) {
	root := locateFixture(t)
	selected := strings.ToUpper(filepath.Join(root, "Client", "WindowsNoEditor", "HT", "Binaries", "Win64", "HTGame.exe"))
	got, err := Inspect(context.Background(), selected)
	if err != nil || !strings.EqualFold(got.Root, root) || got.SelectedKind != SelectedExecutable {
		t.Fatalf("Windows case-insensitive explicit exe selection must work: %+v %v", got, err)
	}
}
