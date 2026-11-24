// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !loginui_embed

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nerv-base/Oddward/internal/cnlogin/accounts"
	"github.com/nerv-base/Oddward/internal/launcher/system"
)

func TestDefaultBuildExplainsUnavailableWPFBeforeGamePreparation(t *testing.T) {
	app := filepath.Join(t.TempDir(), "oddward-launcher.exe")
	backend := system.NativeManagedBackend{
		ResolveLoginUI: func() (string, []string, error) {
			return resolveLoginUI(app, "wpf")
		},
		Accounts: &accounts.Store{},
	}
	_, err := backend.Prepare(context.Background(), "", nil)
	if err == nil || !strings.Contains(err.Error(), "-EmbedWPF") {
		t.Fatalf("expected instructions for the build containing WPF, got %v", err)
	}
}
