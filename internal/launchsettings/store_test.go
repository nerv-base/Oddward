// SPDX-License-Identifier: GPL-3.0-or-later

package launchsettings_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nerv-base/Oddward/internal/launchsettings"
)

func TestSaveLoadNonSecretSettings(t *testing.T) {
	file := filepath.Join(t.TempDir(), "owned", "nested", "launch.json")
	store, err := launchsettings.NewStore(file)
	if err != nil {
		t.Fatal(err)
	}
	want := launchsettings.Settings{GameRoot: filepath.Join(t.TempDir(), "game"), RiskVersion: "standalone-login-v1"}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatalf("save non-secret launcher choices: %v", err)
	}
	// A fresh Store must read disk, not an in-memory cache.
	reopened, err := launchsettings.NewStore(file)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Load(context.Background())
	want.Downloads = launchsettings.DefaultDownloads()
	if err != nil || got != want {
		t.Fatalf("reopened settings = %+v, %v; want %+v", got, err, want)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if len(document) != 7 || string(document["version"]) != "6" || document["gameRoot"] == nil || document["riskVersion"] == nil || document["network"] == nil || document["skippedVersion"] == nil || document["downloads"] == nil || document["ui"] == nil {
		t.Fatalf("settings must contain only schema version and non-secret choices: %s", data)
	}
}

func TestInvalidDocumentsAreRejectedAndPreserved(t *testing.T) {
	cases := map[string]string{
		"malformed":            `{`,
		"unknown version":      `{"version":4,"gameRoot":"","riskVersion":""}`,
		"v1 with v2 field":     `{"version":1,"gameRoot":"","riskVersion":"","skippedVersion":""}`,
		"v2 missing network":   `{"version":2,"gameRoot":"","riskVersion":"","skippedVersion":""}`,
		"v2 network secret":    `{"version":2,"gameRoot":"","riskVersion":"","skippedVersion":"","network":{"proxy":"","proxyURL":"","mirror":false,"mirrorPrefix":"","password":"x"}}`,
		"v2 control chars":     "{\"version\":2,\"gameRoot\":\"\",\"riskVersion\":\"\",\"skippedVersion\":\"\\u0007\",\"network\":{\"proxy\":\"\",\"proxyURL\":\"\",\"mirror\":false,\"mirrorPrefix\":\"\"}}",
		"missing version":      `{"gameRoot":"","riskVersion":""}`,
		"unknown secret field": `{"version":1,"gameRoot":"","riskVersion":"","token":"not-a-real-token"}`,
		"duplicate field":      `{"version":1,"version":1,"gameRoot":"","riskVersion":""}`,
		"null document":        `null`,
		"null choice":          `{"version":1,"gameRoot":null,"riskVersion":""}`,
		"missing choice":       `{"version":1,"gameRoot":""}`,
		"wrong type":           `{"version":1,"gameRoot":12,"riskVersion":""}`,
		"trailing document":    `{"version":1,"gameRoot":"","riskVersion":""} {}`,
		"oversized":            strings.Repeat(" ", 16*1024+1),
	}
	for name, fixture := range cases {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "launch.json")
			before := []byte(fixture)
			if err := os.WriteFile(file, before, 0600); err != nil {
				t.Fatal(err)
			}
			store, err := launchsettings.NewStore(file)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := store.Load(context.Background()); err == nil || got != (launchsettings.Settings{}) {
				t.Errorf("invalid document accepted or leaked partial state: %+v, %v", got, err)
			}
			if err := store.Save(context.Background(), launchsettings.Settings{}); err == nil {
				t.Error("Save silently overwrote invalid or unsupported previous data")
			}
			after, err := os.ReadFile(file)
			if err != nil || !bytes.Equal(after, before) {
				t.Fatalf("invalid old document changed: %q, %v", after, err)
			}
		})
	}
}

func TestDocumentAtLimitAndChoicesCanBeReplacedAndCleared(t *testing.T) {
	file := filepath.Join(t.TempDir(), "launch.json")
	payload := `{"version":1,"gameRoot":"","riskVersion":""}`
	if err := os.WriteFile(file, []byte(payload+strings.Repeat(" ", 16*1024-len(payload))), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := launchsettings.NewStore(file)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(context.Background()); err != nil || got != (launchsettings.Settings{Downloads: launchsettings.DefaultDownloads(), UI: launchsettings.DefaultUI()}) {
		t.Fatalf("exact-limit document = %+v, %v", got, err)
	}
	for _, want := range []launchsettings.Settings{
		{GameRoot: filepath.Join(t.TempDir(), "游戏"), RiskVersion: strings.Repeat("界", 42) + "v1"},
		{GameRoot: filepath.Join(t.TempDir(), "replacement"), RiskVersion: "v2"},
		{},
	} {
		if err := store.Save(context.Background(), want); err != nil {
			t.Fatal(err)
		}
		got, err := store.Load(context.Background())
		want.Downloads = launchsettings.DefaultDownloads()
		if err != nil || got != want {
			t.Fatalf("replacement/clear = %+v, %v; want %+v", got, err, want)
		}
	}
}

func TestCanceledOperationsRetainPreviousSettings(t *testing.T) {
	file := filepath.Join(t.TempDir(), "launch.json")
	store, err := launchsettings.NewStore(file)
	if err != nil {
		t.Fatal(err)
	}
	old := launchsettings.Settings{GameRoot: filepath.Join(t.TempDir(), "old"), RiskVersion: "v1"}
	if err := store.Save(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := store.Load(ctx); !errors.Is(err, context.Canceled) || got != (launchsettings.Settings{}) {
		t.Errorf("canceled Load = %+v, %v", got, err)
	}
	if err := store.Save(ctx, launchsettings.Settings{}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled Save = %v", err)
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("canceled operation changed prior file: %v", err)
	}
}

func TestInvalidChoicesCannotOverwriteOrLoad(t *testing.T) {
	file := filepath.Join(t.TempDir(), "launch.json")
	store, err := launchsettings.NewStore(file)
	if err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"version":1,"gameRoot":"","riskVersion":""}`)
	cases := []launchsettings.Settings{
		{GameRoot: "relative"}, {GameRoot: `\\server\share\game`}, {GameRoot: `\\?\C:\game`},
		{GameRoot: `C:\game:stream`}, {GameRoot: `C:\bad.\game`}, {GameRoot: `C:\bad \game`},
		{GameRoot: `C:\NUL\game`}, {GameRoot: "C:\\bad\x00game"},
		{RiskVersion: "v1"}, {GameRoot: t.TempDir(), RiskVersion: strings.Repeat("r", 129)},
		{GameRoot: t.TempDir(), RiskVersion: "v1\nsecret"},
		{GameRoot: filepath.Join(t.TempDir(), "game"), RiskVersion: strings.Repeat("界", 10000)},
	}
	for i, choice := range cases {
		if err := os.WriteFile(file, old, 0600); err != nil {
			t.Fatal(err)
		}
		if err := store.Save(context.Background(), choice); err == nil {
			t.Errorf("invalid choice %d was saved", i)
		}
		after, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(after, old) {
			t.Errorf("invalid choice %d changed previous document: %v", i, err)
		}
		fixture, err := json.Marshal(map[string]any{"version": 1, "gameRoot": choice.GameRoot, "riskVersion": choice.RiskVersion})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, fixture, 0600); err != nil {
			t.Fatal(err)
		}
		if got, err := store.Load(context.Background()); err == nil || got != (launchsettings.Settings{}) {
			t.Errorf("invalid choice %d loaded or leaked partial state: %v", i, err)
		}
	}
}

func TestStoreRejectsUnsafePathForms(t *testing.T) {
	for _, path := range []string{"", ".", "launch.json", `C:relative`, `\root-relative`, `\\server\share\launch.json`, `//server/share/launch.json`, `\\?\C:\launch.json`, `\\.\C:\launch.json`, `C:\state:stream`, `C:\bad.\launch.json`, `C:\bad \launch.json`, `C:\NUL.json`, `C:\COM¹.json`, `C:\LPT²\launch.json`, "C:\\bad\x00path"} {
		if store, err := launchsettings.NewStore(path); err == nil || store != nil {
			t.Errorf("unsafe store path accepted: %q, %v", path, err)
		}
	}
}

func TestMissingSettingsReturnDefaultsWithoutCreatingFiles(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-created")
	store, err := launchsettings.NewStore(filepath.Join(parent, "launch.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background())
	want := launchsettings.Settings{Downloads: launchsettings.DefaultDownloads(), UI: launchsettings.DefaultUI()}
	if err != nil || got != want {
		t.Fatalf("missing settings = %+v, %v; want defaults without error", got, err)
	}
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Fatalf("read-only missing Load must not create parent: %v", err)
	}
}
