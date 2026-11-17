// SPDX-License-Identifier: GPL-3.0-or-later

// Stub is the installed Oddward.exe. It starts the newest ready app-X.Y.Z
// folder beside it and falls back to the previous version when a freshly
// updated one dies before opening its window.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"

	"github.com/nerv-base/Oddward/internal/selfupdate"
)

// healthWait bounds how long a new version may take to open its window.
const healthWait = 60 * time.Second

func main() {
	exe, err := os.Executable()
	if err != nil {
		fail(err)
	}
	if _, err := selfupdate.Launch(filepath.Dir(exe), os.Args[1:], start, healthWait); err != nil {
		fail(err)
	}
}

func start(exe string, args []string) (<-chan error, error) {
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return done, nil
}

func fail(err error) {
	text, _ := windows.UTF16PtrFromString("Oddward 无法启动：" + err.Error())
	title, _ := windows.UTF16PtrFromString("Oddward")
	windows.MessageBox(0, text, title, windows.MB_OK|windows.MB_ICONERROR)
	os.Exit(1)
}
