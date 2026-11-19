// SPDX-License-Identifier: GPL-3.0-or-later

package launchsettings_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nerv-base/Oddward/internal/launchsettings"
)

func TestLauncherUIChoicesDefaultOnAndPersist(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "launch.json")
	store, _ := launchsettings.NewStore(file)
	prefix := `,"gameRoot":"","riskVersion":"","skippedVersion":"","network":{},"downloads":{"rateLimitKiB":0}`
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"version":4` + prefix + `}`)
	got, err := store.Load(ctx)
	if err != nil || got.UI != launchsettings.DefaultUI() {
		t.Fatalf("v4 must inherit launcher UI defaults: %+v, %v", got.UI, err)
	}
	want := launchsettings.UI{CloseToTray: false, AccentFollow: false, CheckUpdates: false}
	if err := store.Update(ctx, func(s *launchsettings.Settings) { s.UI = want }); err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.Load(ctx)
	if err != nil || reloaded.UI != want {
		t.Fatalf("ui choices = %+v, %v", reloaded.UI, err)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"version":6`) || !strings.Contains(string(raw), `"closeToTray":false`) || !strings.Contains(string(raw), `"accentFollow":false`) || !strings.Contains(string(raw), `"checkUpdates":false`) {
		t.Fatalf("saved document must carry version 5 ui choices: %s", raw)
	}
	write(`{"version":5` + prefix + `,"ui":{"closeToTray":false,"accentFollow":false}}`)
	legacy, err := store.Load(ctx)
	if err != nil || legacy.UI != (launchsettings.UI{CloseToTray: false, AccentFollow: false, CheckUpdates: true}) {
		t.Fatalf("v5 must default the update check on: %+v, %v", legacy.UI, err)
	}
	for _, block := range []string{
		`,"ui":null`, `,"ui":{}`, `,"ui":{"closeToTray":true,"accentFollow":true}`,
		`,"ui":{"closeToTray":true,"accentFollow":true,"unknown":true}`,
		`,"ui":{"closeToTray":true,"closeToTray":false,"accentFollow":true,"checkUpdates":true}`,
	} {
		write(`{"version":6` + prefix + block + `}`)
		if _, err := store.Load(ctx); err == nil {
			t.Fatalf("accepted malformed ui block %s", block)
		}
	}
}
