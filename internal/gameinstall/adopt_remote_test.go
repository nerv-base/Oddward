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

func TestAdoptRemoteChecksumMismatchRemainsUnverified(t *testing.T) {
	root, manifest := adoptFixture(t)
	s := adoptStore(t)
	if err := os.Remove(filepath.Join(root, adoptOfficialDir, "config.xml")); err != nil {
		t.Fatal(err)
	}
	remote := &AdoptMetadata{Branch: "PC_150", ConfigXML: adoptConfig(manifest), Manifest: append([]byte("\n"), manifest...)}
	got, err := s.AdoptQuick(context.Background(), root, remote)
	if err != nil || got.State != Adopted || !got.Record.BaselineMismatch || len(got.Warnings) == 0 {
		t.Fatalf("hash mismatch must stay adopted and unverified: %+v %v", got, err)
	}
	if _, err := s.VerifyInstallation(context.Background(), root, nil); !errors.Is(err, ErrStaleState) {
		t.Fatalf("unchecked metadata became verified: %v", err)
	}
}
func TestAdoptComparesOptionalRemoteWithoutReplacingLocalBaseline(t *testing.T) {
	root, manifest := adoptFixture(t)
	s := adoptStore(t)
	remoteManifest := []byte(strings.Replace(string(manifest), `version="1.2.3"`, `version="1.2.4"`, 1))
	remoteConfig := []byte(strings.Replace(string(adoptConfig(remoteManifest)), "<ResVersion>1.2.3</ResVersion>", "<ResVersion>1.2.4</ResVersion>", 1))
	got, err := s.AdoptQuick(context.Background(), root, &AdoptMetadata{Branch: "PC_150", ConfigXML: remoteConfig, Manifest: remoteManifest})
	if err != nil || got.Record.Version != "1.2.3" || got.Record.BaselineMismatch || len(got.Warnings) == 0 {
		t.Fatalf("remote comparison changed local baseline: %+v %v", got, err)
	}
}
