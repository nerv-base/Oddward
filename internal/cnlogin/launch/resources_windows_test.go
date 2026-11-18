//go:build windows

// SPDX-License-Identifier: GPL-3.0-or-later
package launch

import (
	"context"
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"
)

func TestInstanceGateIsExclusiveUntilClosed(t *testing.T) {
	name := `Local\Oddward.Launch.Test.` + strings.ReplaceAll(filepath.Base(t.TempDir()), "-", "")
	g, err := acquireInstance(name)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if second, err := acquireInstance(name); !errors.Is(err, ErrHostRunning) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("second gate: %v", err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := acquireInstance(name)
	if err != nil {
		t.Fatal(err)
	}
	_ = again.Close()
}

func TestPrivateLogsProtectedACLAndDestroy(t *testing.T) {
	logs, err := createPrivateLogs(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	sd, err := windows.GetNamedSecurityInfo(logs.path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	ctrl, _, err := sd.Control()
	if err != nil || ctrl&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("DACL not protected: %v %x", err, ctrl)
	}
	sid, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	// Compare against the descriptor the launcher builds, in both cases formatted by the
	// same API: SDDL abbreviates well-known principals, and a CI agent that runs as the
	// built-in Administrator account is printed as "LA" instead of its SID. Equality still
	// pins the exact ACE set: SYSTEM plus this process' user, inheritable, full control.
	intended, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;" + sid.User.Sid.String() + ")")
	if err != nil {
		t.Fatal(err)
	}
	if text, want := sd.String(), intended.String(); text != want {
		t.Fatalf("DACL %q, want %q", text, want)
	}
	writeFile(t, filepath.Join(logs.path, "nested", "sdk.log"), "synthetic credential marker must be deleted")
	path := logs.path
	if err := logs.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("logs survive cleanup: %v", err)
	}
	if err := logs.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckProcessesUsesExactCaseInsensitiveNames(t *testing.T) {
	for _, name := range []string{"HTGame.exe", "ntegame.EXE", "NTEBrowser.exe", "NTEUpdate.exe", "NTELauncher.exe"} {
		if !blockedProcessName(name) {
			t.Errorf("allowed %q", name)
		}
	}
	for _, name := range []string{"OtherHTGame.exe", "HTGame.exe.old", "oddward.exe", "HTGame"} {
		if blockedProcessName(name) {
			t.Errorf("blocked %q", name)
		}
	}
	// Snapshot succeeds for synthetic names and finds this own test process.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	found, err := hasProcess(context.Background(), func(name string) bool { return strings.EqualFold(name, filepath.Base(exe)) })
	if err != nil || !found {
		t.Fatalf("own process missing %t %v", found, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := CheckProcesses(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestPrivateLogsRefuseReparseParent(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink permission unavailable: %v", err)
	}
	if logs, err := createPrivateLogs(context.Background(), link); err == nil {
		defer logs.Close()
		t.Fatal("accepted reparse log parent")
	}
}

// Keep this compile-time check near tests using native structures.
var _ = unsafe.Sizeof(windows.SecurityAttributes{})
