// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package elevatedipc

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestStartLaunchesRenamedImage covers a launcher whose executable file was
// renamed while it ran, as the Go toolchain does when it replaces a running
// build. The elevated host must be the image this process is running, because
// os.Executable() keeps naming the path and another file may now sit there.
func TestStartLaunchesRenamedImage(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(t.TempDir(), "oddward.exe")
	copyImage(t, self, copied)
	if code := runSyntheticChild(t, copied, "renamed-image"); code != 0 {
		t.Fatalf("renamed-image child exited %d", code)
	}
}

// TestSyntheticDuplexAfterRename runs the real handshake while this process's
// image sits under a renamed path, which is where a launcher ends up after a
// build replaced the file it was started from.
func TestSyntheticDuplexAfterRename(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	renameOwnImage(t)
	c, err := start(ctx, syntheticLaunch(t), false)
	if err != nil {
		t.Fatalf("start after rename: %v", err)
	}
	defer c.Close()
	cancel() // setup context must not own the delivered stream.
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(c, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply) != "pong" {
		t.Fatalf("unexpected reply %q", reply)
	}
}

// testRenamedImage renames its own image, then checks the path start launches.
// The launch itself fails on purpose so no real child is created.
func testRenamedImage(t *testing.T, ctx context.Context) {
	renamed := renameOwnImage(t)
	var launched string
	_, err := start(ctx, func(exe string, _ []string) (windows.Handle, error) {
		launched = exe
		return 0, errors.New("record only")
	}, false)
	if !errors.Is(err, errLaunch) {
		t.Fatalf("start after rename: %v", err)
	}
	if !strings.EqualFold(filepath.Clean(launched), filepath.Clean(renamed)) {
		t.Fatalf("launched %q, running image %q", launched, renamed)
	}
}

// renameOwnImage moves this process's executable aside the way the Go toolchain
// replaces a running file, and restores it when the test finishes.
func renameOwnImage(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	renamed := self + "~"
	if err := os.Remove(renamed); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Rename(self, renamed); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(renamed, self) })
	return renamed
}

func copyImage(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func runSyntheticChild(t *testing.T, exe, action string) uint32 {
	t.Helper()
	process, err := spawnChild(exe, []string{exe, "-test.run=^TestSyntheticChild$", "--", "--elevatedipc-test-child", action})
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process)
	if status, err := windows.WaitForSingleObject(process, 6000); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("synthetic child did not exit: status %d, %v", status, err)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(process, &code); err != nil {
		t.Fatal(err)
	}
	return code
}
