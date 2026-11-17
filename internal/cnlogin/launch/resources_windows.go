//go:build windows

// SPDX-License-Identifier: GPL-3.0-or-later
package launch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// instanceName guards this launcher's own prepare/start transaction. It must
// stay distinct from the SDK-visible object `Local\Collapse.NTE.LoginHost.1289`
// that sdkhost creates for the game, otherwise Prepare would hold the very
// mutex the host needs and every Start would fail as already-running.
const instanceName = `Local\Oddward.CNLogin.Prepare.1289`

type instanceGate struct {
	mu     sync.Mutex
	handle windows.Handle
	err    error
}

func acquireInstance(name string) (*instanceGate, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	sd, err := privateDescriptor(false)
	if err != nil {
		return nil, err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	h, err := windows.CreateMutex(&sa, false, p)
	if err != nil {
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return nil, ErrHostRunning
		}
		return nil, err
	}
	return &instanceGate{handle: h}, nil
}
func (g *instanceGate) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.handle != 0 {
		g.err = windows.CloseHandle(g.handle)
		g.handle = 0
	}
	return g.err
}

func privateDescriptor(inherit bool) (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	flags := ""
	if inherit {
		flags = "OICI"
	}
	return windows.SecurityDescriptorFromString("D:P(A;" + flags + ";FA;;;SY)(A;" + flags + ";FA;;;" + user.User.Sid.String() + ")")
}

type privateLogs struct {
	mu     sync.Mutex
	path   string
	closed bool
	err    error
}

func createPrivateLogs(ctx context.Context, parent string) (*privateLogs, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(parent) {
		return nil, errors.New("launch: log parent must be absolute")
	}
	// Reject junctions and symlinks before creating any directory or removing logs.
	for path := filepath.Clean(parent); ; path = filepath.Dir(path) {
		s, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !s.IsDir() || isReparse(s) {
			return nil, errors.New("launch: unsafe log parent")
		}
		if filepath.Dir(path) == path {
			break
		}
	}
	sd, err := privateDescriptor(true)
	if err != nil {
		return nil, err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	for attempts := 0; attempts < 4; attempts++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, err
		}
		path := filepath.Join(parent, "Oddward.CN.LoginHost."+hex.EncodeToString(random[:]))
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return nil, err
		}
		if err := windows.CreateDirectory(name, &sa); err != nil {
			if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
				continue
			}
			return nil, err
		}
		return &privateLogs{path: path}, nil
	}
	return nil, errors.New("launch: private log name collision")
}
func isReparse(info os.FileInfo) bool {
	if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return true
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

// Close destroys all raw logs without collecting or reading their contents.
func (l *privateLogs) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		l.closed = true
		l.err = os.RemoveAll(l.path)
	}
	return l.err
}

// CheckProcesses refuses any matching executable name across users/sessions.
// A snapshot/enumeration failure is an error, never interpreted as no client.
func CheckProcesses(ctx context.Context) error {
	found, err := hasProcess(ctx, blockedProcessName)
	if err != nil {
		return err
	}
	if found {
		return ErrClientRunning
	}
	return nil
}
func hasProcess(ctx context.Context, match func(string) bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(snap)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	err = windows.Process32First(snap, &entry)
	for err == nil {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if match(windows.UTF16ToString(entry.ExeFile[:])) {
			return true, nil
		}
		err = windows.Process32Next(snap, &entry)
	}
	if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return false, nil
	}
	return false, err
}
func blockedProcessName(name string) bool {
	switch strings.ToUpper(name) {
	case "HTGAME.EXE", "NTEGAME.EXE", "NTEBROWSER.EXE", "NTEUPDATE.EXE", "NTELAUNCHER.EXE":
		return true
	}
	return false
}
