// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionedAdoptionTracksOfficialConfigWhenLocalManifestIsMissing(t *testing.T) {
	root, manifest := adoptFixture(t)
	if err := os.Remove(filepath.Join(root, adoptOfficialDir, "ResList.xml")); err != nil {
		t.Fatal(err)
	}
	store := adoptStore(t)
	config := adoptConfig(manifest)
	_, err := store.AdoptQuick(context.Background(), root, &AdoptMetadata{ConfigXML: config, Manifest: manifest, Branch: "PC_150"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadInstall(root); err != nil {
		t.Fatalf("unchanged local version: %v", err)
	}
	adoptPut(t, root, adoptOfficialDir+"config.xml", []byte(strings.Replace(string(config), "1.2.3", "1.2.4", 1)))
	if _, err := store.VerifyInstallation(context.Background(), root, nil); !errors.Is(err, ErrStaleState) {
		t.Fatalf("changed official generation verified against the old download: %v", err)
	}
}
