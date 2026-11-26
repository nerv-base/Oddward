// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"path/filepath"
	"testing"

	"github.com/nerv-base/Oddward/internal/cnlogin/loginui"
)

func TestResolveLoginUIDefaultsToBuiltInMyGoRoleAndHonoursOverride(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "自有 构建", "oddward.exe")

	exe, args := resolveLoginUI(executable, "")
	if exe == "" || len(args) != 1 || args[0] != loginui.Mode {
		t.Fatalf("default login UI = (%q, %v), want running image with %q", exe, args, loginui.Mode)
	}

	if got, gotArgs := resolveLoginUI(executable, `D:\synthetic\helper.exe`); got != `D:\synthetic\helper.exe` || len(gotArgs) != 0 {
		t.Fatalf("explicit override = (%q, %v)", got, gotArgs)
	}
}
