// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nerv-base/Oddward/internal/cnlogin/loginui"
)

func TestResolveLoginUIDefaultsToBuiltInMyGoRoleAndHonoursWPFFallback(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "自有 构建")
	executable := filepath.Join(dir, "oddward.exe")

	exe, args, err := resolveLoginUI(executable, "")
	if err != nil || exe == "" || len(args) != 1 || args[0] != loginui.Mode {
		t.Fatalf("default login UI = (%q, %v, %v), want running image with %q", exe, args, err, loginui.Mode)
	}

	wantWPF := filepath.Join(dir, "login-compat", "Oddward.LoginUi.exe")
	if err := os.MkdirAll(filepath.Dir(wantWPF), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wantWPF, []byte("stub"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, gotArgs, err := resolveLoginUI(executable, "wpf"); err != nil || got != wantWPF || len(gotArgs) != 0 {
		t.Fatalf("wpf fallback = (%q, %v, %v), want (%q, nil, nil)", got, gotArgs, err, wantWPF)
	}

	if got, gotArgs, err := resolveLoginUI(executable, `D:\synthetic\helper.exe`); err != nil || got != `D:\synthetic\helper.exe` || len(gotArgs) != 0 {
		t.Fatalf("explicit override = (%q, %v, %v)", got, gotArgs, err)
	}
}
