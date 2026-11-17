// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package elevatedipc

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Synthetic children run this test binary, never the launcher or a game. Only
// the elevation requirement is disabled; kernel peer identity checks stay real.
func TestSyntheticChild(t *testing.T) {
	for i, arg := range os.Args {
		if arg != "--elevatedipc-test-child" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		action := os.Args[i+1]
		c, err := accept(ctx, os.Args[i+2:], false)
		if action == "reject" {
			if c != nil {
				c.Close()
			}
			if err == nil {
				t.Fatal("untrusted connection accepted")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		cancel() // setup context must not own the delivered stream.
		message := make([]byte, 4)
		if _, err := io.ReadFull(c, message); err != nil {
			t.Fatal(err)
		}
		if string(message) != "ping" {
			t.Fatal("unexpected request")
		}
		if _, err := c.Write([]byte("pong")); err != nil {
			t.Fatal(err)
		}
		return
	}
}

func syntheticLaunch(t *testing.T) launchFunc { return childLaunch(t, "duplex") }

func childLaunch(t *testing.T, action string) launchFunc {
	t.Helper()
	return func(exe string, args []string) (windows.Handle, error) {
		if len(args) != 4 || args[0] != Mode {
			t.Error("unexpected host arguments")
		}
		argv := append([]string{exe, "-test.run=^TestSyntheticChild$", "--", "--elevatedipc-test-child", action}, args[1:]...)
		quoted := make([]string, len(argv))
		for i, arg := range argv {
			quoted[i] = windows.EscapeArg(arg)
		}
		command, err := windows.UTF16FromString(strings.Join(quoted, " "))
		if err != nil {
			return 0, err
		}
		startup := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{}))}
		var pi windows.ProcessInformation
		if err := windows.CreateProcess(windows.StringToUTF16Ptr(exe), &command[0], nil, nil, false, windows.CREATE_NO_WINDOW, nil, nil, &startup, &pi); err != nil {
			return 0, err
		}
		windows.CloseHandle(pi.Thread)
		var observer windows.Handle
		if err := windows.DuplicateHandle(windows.CurrentProcess(), pi.Process, windows.CurrentProcess(), &observer, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
			windows.CloseHandle(pi.Process)
			return 0, err
		}
		t.Cleanup(func() {
			defer windows.CloseHandle(observer)
			status, err := windows.WaitForSingleObject(observer, 6000)
			if err != nil || status != windows.WAIT_OBJECT_0 {
				t.Errorf("synthetic child failed to exit: status %d, %v", status, err)
				return
			}
			var code uint32
			if err := windows.GetExitCodeProcess(observer, &code); err != nil || code != 0 {
				t.Errorf("synthetic child exit: %d, %v", code, err)
			}
		})
		return pi.Process, nil
	}
}
