// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"path/filepath"
	"testing"
)

func TestLoginUIHelperDefaultsNextToExecutableAndHonoursOverride(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "自有 构建", "oddward.exe")
	want := filepath.Join(filepath.Dir(executable), "login-compat", "Oddward.LoginUi.exe")
	if got := loginUIExecutable(executable, ""); got != want {
		t.Fatalf("login UI path=%q want=%q", got, want)
	}
	if got := loginUIExecutable(executable, `D:\synthetic\helper.exe`); got != `D:\synthetic\helper.exe` {
		t.Fatalf("login UI override=%q", got)
	}
}
