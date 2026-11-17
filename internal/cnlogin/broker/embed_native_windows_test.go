// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package broker

import (
	"context"
	"golang.org/x/sys/windows"
	"runtime"
	"testing"
	"unsafe"
)

type nativeEmbedFailure struct {
	nativeOverlaySystem
	failFrame bool
}

func (s *nativeEmbedFailure) frameChanged(w uintptr) error {
	if s.failFrame {
		s.failFrame = false
		return errRemote
	}
	return s.nativeOverlaySystem.frameChanged(w)
}

// Only this process's hidden STATIC fixtures are changed. This does not verify
// cross-integrity hosting, real WPF rendering, game movement or foreground focus.
func TestNativeEmbedHiddenWindowParentStyleDPIAndRecovery(t *testing.T) {
	for _, mode := range []string{"child", "child-owned", "recover", "recover-owned"} {
		t.Run(mode, func(t *testing.T) {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			create := overlayUser32.NewProc("CreateWindowExW")
			destroy := overlayUser32.NewProc("DestroyWindow")
			class := windows.StringToUTF16Ptr("STATIC")
			newWindow := func() uintptr {
				w, _, err := create.Call(0, uintptr(unsafe.Pointer(class)), 0, overlayPopup, 0, 0, 1, 1, 0, 0, 0, 0)
				if w == 0 {
					t.Fatalf("create hidden native fixture: %v", err)
				}
				return w
			}
			target := newWindow()
			defer destroy.Call(target)
			originalOwner := newWindow()
			defer destroy.Call(originalOwner)
			ui := newWindow()
			defer destroy.Call(ui)
			s := &nativeEmbedFailure{}
			if mode == "child-owned" || mode == "recover-owned" {
				if err := s.setOwner(ui, originalOwner); err != nil {
					t.Fatal("fixture owner setup failed")
				}
			}
			before, err := s.window(ui)
			if err != nil {
				t.Fatal("fixture UI query failed")
			}
			game, err := s.window(target)
			if err != nil {
				t.Fatal("fixture target query failed")
			}
			if !s.sameDPI(before.dpi, game.dpi) {
				t.Skip("window DPI APIs unavailable for child native test")
			}
			s.failFrame = mode == "recover" || mode == "recover-owned"
			recovery := s.failFrame
			err = embedWindow(context.Background(), ui, target, before, game, s, func() bool { return true })
			actual, readErr := s.window(ui)
			if readErr != nil {
				t.Fatal("fixture native readback failed")
			}
			if recovery {
				if err == nil || actual.style != before.style || !actual.topLevel || actual.parent != 0 || actual.owner != before.owner {
					t.Fatalf("native child rollback mismatch: %v", err)
				}
			} else {
				if err != nil || actual.style&overlayChild == 0 || actual.style&overlayPopup != 0 || actual.parent != target || actual.owner != before.owner || actual.topLevel {
					t.Fatalf("native child readback mismatch: %v", err)
				}
				if err := embedWindow(context.Background(), ui, target, actual, game, s, func() bool { return true }); err != nil {
					t.Fatal("native same-game child retry failed")
				}
			}
			if actual.visible || actual.topmost || actual.pid != before.pid || actual.tid != before.tid || !s.sameDPI(actual.dpi, before.dpi) {
				t.Fatal("native child transition changed visibility, identity or DPI")
			}
			afterTarget, err := s.window(target)
			if err != nil || afterTarget != game {
				t.Fatal("native child transition changed target fixture")
			}
		})
	}
}
