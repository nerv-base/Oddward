// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package overlaywin

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestOpenVerifiedAndProcessAlive(t *testing.T) {
	pid := uint32(os.Getpid())
	h, id, err := OpenIdentity(pid)
	if err != nil {
		t.Fatalf("OpenIdentity: %v", err)
	}
	defer CloseIdentity(h)
	if id.PID != pid || id.CreationTime == 0 {
		t.Fatalf("unexpected identity: %+v", id)
	}
	alive, err := ProcessAlive(h)
	if err != nil || !alive {
		t.Fatalf("current process alive=(%v,%v), want (true,nil)", alive, err)
	}
	if err := id.Verify(); err != nil {
		t.Fatalf("Verify valid identity: %v", err)
	}
	stale := Identity{PID: pid, CreationTime: id.CreationTime + 1}
	if err := stale.Verify(); err == nil {
		t.Fatal("stale creation time accepted")
	}
	if _, err := OpenVerified(stale); err == nil {
		t.Fatal("OpenVerified accepted stale creation time")
	}
}

func TestParseCreationTimeRejectsNonCanonical(t *testing.T) {
	for _, bad := range []string{"", "0", "01", "+1", " 1", "1 ", "-1", "18446744073709551616"} {
		if _, err := ParseCreationTime(bad); err == nil {
			t.Fatalf("accepted non-canonical creation time %q", bad)
		}
	}
	got, err := ParseCreationTime("18446744073709551615")
	if err != nil || got != ^uint64(0) {
		t.Fatalf("max uint64 parse failed: %d, %v", got, err)
	}
}

func TestProcessAliveDetectsExitCode259(t *testing.T) {
	if os.Getenv("ODDWARD_OVERLAYWIN_EXIT259") == "1" {
		os.Exit(259)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestProcessAliveDetectsExitCode259$")
	cmd.Env = append(os.Environ(), "ODDWARD_OVERLAYWIN_EXIT259=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h, id, err := OpenIdentity(uint32(cmd.Process.Pid))
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	defer CloseIdentity(h)
	if err := cmd.Wait(); err == nil {
		t.Fatal("expected exit code 259")
	}
	alive, err := ProcessAlive(h)
	if err != nil || alive {
		t.Fatalf("exited child with code 259 reported alive=%v err=%v (id=%+v)", alive, err, id)
	}
}

func TestCurrentImagePathMatchesRunningExecutable(t *testing.T) {
	img, err := CurrentImagePath()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(img) || !strings.HasSuffix(strings.ToLower(img), ".exe") {
		t.Fatalf("unexpected image path: %q", img)
	}
}

func TestWithThreadDPIRestoresOriginalContext(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	getThreadDPI := user32.NewProc("GetThreadDpiAwarenessContext")
	if getThreadDPI.Find() != nil || procSetThreadDpiAwareness.Find() != nil {
		t.Skip("thread DPI awareness APIs unavailable")
	}
	create := user32.NewProc("CreateWindowExW")
	destroy := user32.NewProc("DestroyWindow")
	class := windows.StringToUTF16Ptr("STATIC")

	before, _, _ := getThreadDPI.Call()
	// Create a System-DPI-aware fixture window (-2 = DPI_AWARENESS_CONTEXT_SYSTEM_AWARE).
	prev, _, _ := procSetThreadDpiAwareness.Call(^uintptr(1))
	if prev == 0 {
		t.Skip("SetThreadDpiAwarenessContext(SYSTEM_AWARE) rejected")
	}
	target, _, _ := create.Call(0, uintptr(unsafe.Pointer(class)), 0, WSPopup, 0, 0, 10, 10, 0, 0, 0, 0)
	procSetThreadDpiAwareness.Call(prev)
	if target == 0 {
		t.Fatal("failed to create DPI fixture window")
	}
	defer destroy.Call(target)

	var child uintptr
	if err := WithThreadDPI(target, func() error {
		child, _, _ = create.Call(0, uintptr(unsafe.Pointer(class)), 0, WSPopup, 0, 0, 10, 10, 0, 0, 0, 0)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if child == 0 {
		t.Fatal("failed to create DPI-matched window")
	}
	defer destroy.Call(child)

	if !SameDPI(child, target) {
		t.Fatalf("DPI-matched window context %#x != target %#x", DPI(child), DPI(target))
	}
	after, _, _ := getThreadDPI.Call()
	equal, _, _ := procDpiContextsEqual.Call(before, after)
	if equal == 0 {
		t.Fatalf("WithThreadDPI did not restore original thread DPI context: before=%#x after=%#x", before, after)
	}
}

func TestSetChildStripsTopLevelDecorationsAndVerifyReadback(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	create := user32.NewProc("CreateWindowExW")
	destroy := user32.NewProc("DestroyWindow")
	class := windows.StringToUTF16Ptr("STATIC")

	parent, _, _ := create.Call(0, uintptr(unsafe.Pointer(class)), 0, WSPopup, 0, 0, 200, 150, 0, 0, 0, 0)
	if parent == 0 {
		t.Fatal("parent fixture failed")
	}
	defer destroy.Call(parent)

	const framelessStyle = WSCaption | WSSysMenu | WSMinimizeBox | WSMaximizeBox | 0x02000000
	child, _, _ := create.Call(0, uintptr(unsafe.Pointer(class)), 0, framelessStyle, 0, 0, 100, 80, 0, 0, 0, 0)
	if child == 0 {
		t.Fatal("child fixture failed")
	}
	defer destroy.Call(child)

	if err := SetChild(child, parent, Rect{W: 200, H: 150}); err != nil {
		t.Fatalf("SetChild: %v", err)
	}
	if !ChildOf(child, parent) {
		t.Fatalf("ChildOf false after SetChild (parent=%#x want=%#x)", Parent(child), parent)
	}
	style, err := Style(child)
	if err != nil {
		t.Fatal(err)
	}
	if style&ChildDecorationMask != 0 {
		t.Fatalf("top-level decoration bits remained on child: %#x", style)
	}
	if err := SetChild(child, parent, Rect{W: 200, H: 150}); err == nil {
		t.Fatal("SetChild succeeded twice on already-child window")
	}
}

func TestMakeBannerStyleSetsNonActivatingClickThrough(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	create := user32.NewProc("CreateWindowExW")
	destroy := user32.NewProc("DestroyWindow")
	class := windows.StringToUTF16Ptr("STATIC")

	hwnd, _, _ := create.Call(0, uintptr(unsafe.Pointer(class)), 0, WSPopup, 0, 0, 100, 40, 0, 0, 0, 0)
	if hwnd == 0 {
		t.Fatal("banner fixture failed")
	}
	defer destroy.Call(hwnd)

	if _, err := MakeBannerStyle(hwnd); err != nil {
		t.Fatalf("MakeBannerStyle: %v", err)
	}
	ex, err := ExStyle(hwnd)
	if err != nil {
		t.Fatal(err)
	}
	want := uintptr(WSExTransparent | WSExToolWindow | WSExNoActivate)
	if ex&want != want {
		t.Fatalf("banner exStyle=%#x missing %#x", ex, want)
	}
}

func TestWatcherInstallAndUninstall(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	h, id, err := OpenIdentity(uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	_ = CloseIdentity(h)
	w, err := NewWatcher(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := w.Install(); err != nil {
		t.Fatalf("idempotent Install: %v", err)
	}
	w.Uninstall()
	w.Uninstall()
	_ = strconv.FormatUint(id.CreationTime, 10)
}

func TestCandidateRejectsNoActivateAndTransparentIndicators(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	create := user32.NewProc("CreateWindowExW")
	destroy := user32.NewProc("DestroyWindow")
	class := windows.StringToUTF16Ptr("STATIC")

	// Mirrors Windows 11's UAC_InputIndicatorOverlayWnd / UAC Input Indicator.
	for _, ex := range []uintptr{WSExNoActivate, WSExTransparent, WSExToolWindow} {
		hw, _, _ := create.Call(ex, uintptr(unsafe.Pointer(class)), 0, WSPopup|WSVisible, 0, 0, 50, 50, 0, 0, 0, 0)
		if hw == 0 {
			t.Fatalf("failed to create indicator fixture for ex=0x%x", ex)
		}
		got := Candidate(hw)
		destroy.Call(hw)
		if got {
			t.Fatalf("Candidate accepted non-game indicator window with ex=0x%x", ex)
		}
	}
}
