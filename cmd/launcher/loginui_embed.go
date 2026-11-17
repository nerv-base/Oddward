// SPDX-License-Identifier: GPL-3.0-or-later

//go:build loginui_embed

package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// loginUIPayload is produced by scripts/build-launcher.ps1 from a self-contained
// publish of compat/loginui. The helper is extracted once into the user's cache
// so the launcher remains a single executable.
//
//go:embed loginui_payload.zip
var loginUIPayload []byte

var (
	loginUIOnce sync.Once
	loginUIPath string
	loginUIErr  error
)

func embeddedLoginUI() (string, error) {
	loginUIOnce.Do(func() {
		sum := sha256.Sum256(loginUIPayload)
		cache, err := os.UserCacheDir()
		if err != nil {
			loginUIErr = fmt.Errorf("login UI cache: %w", err)
			return
		}
		root := filepath.Join(cache, "Oddward", "login-ui", hex.EncodeToString(sum[:8]))
		exe := filepath.Join(root, "Oddward.LoginUi.exe")
		if info, statErr := os.Stat(exe); statErr == nil && info.Mode().IsRegular() {
			loginUIPath = exe
			return
		}
		temp := root + ".tmp"
		_ = os.RemoveAll(temp)
		if err := extractLoginUI(temp); err != nil {
			_ = os.RemoveAll(temp)
			loginUIErr = err
			return
		}
		_ = os.RemoveAll(root)
		if err := os.Rename(temp, root); err != nil {
			_ = os.RemoveAll(temp)
			loginUIErr = fmt.Errorf("login UI install: %w", err)
			return
		}
		loginUIPath = exe
	})
	return loginUIPath, loginUIErr
}

func extractLoginUI(dir string) error {
	reader, err := zip.NewReader(bytes.NewReader(loginUIPayload), int64(len(loginUIPayload)))
	if err != nil {
		return fmt.Errorf("login UI payload: %w", err)
	}
	for _, file := range reader.File {
		name := filepath.Clean(file.Name)
		if name == "." || filepath.IsAbs(name) || strings.HasPrefix(name, ".."+string(filepath.Separator)) || name == ".." {
			return fmt.Errorf("login UI payload contains an unsafe path %q", file.Name)
		}
		target := filepath.Join(dir, name)
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("login UI payload: %w", err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("login UI payload: %w", err)
		}
		source, err := file.Open()
		if err != nil {
			return fmt.Errorf("login UI payload: %w", err)
		}
		destination, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			source.Close()
			return fmt.Errorf("login UI payload: %w", err)
		}
		_, copyErr := io.Copy(destination, source)
		source.Close()
		closeErr := destination.Close()
		if copyErr != nil {
			return fmt.Errorf("login UI payload: %w", copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("login UI payload: %w", closeErr)
		}
	}
	return nil
}
