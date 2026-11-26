// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"crypto/md5"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nerv-base/Oddward/internal/database/dbtest"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestAdoptVerifiesInstalledVersionWhenLocalManifestIncomplete(t *testing.T) {
	const patcher = "NTELauncher/UserData/Patcher/PatcherSDK/"
	oldFile := `<Res filename="HT/Content/Paks/a.pak" md5="149603e6c03516362a8da23f624db945" filesize="3"/>`
	secondFile := `<Res filename="HT/Content/Paks/b.pak" md5="149603e6c03516362a8da23f624db945" filesize="3"/>`
	manifest := []byte(`<ResList version="1.0.0" tag="baseTag">` + oldFile + secondFile + `</ResList>`)
	config := fmt.Sprintf(`<config><LocalBranch>PC_150</LocalBranch><ResVersion>1.0.0</ResVersion><ResSize>6</ResSize><Extra><listHash>%x</listHash></Extra></config>`, md5.Sum(manifest))
	archive := preloadSyntheticArchive(t, manifest)
	for _, tc := range []struct {
		name                   string
		writeManifest, partial bool
	}{
		{"missing manifest", false, false}, {"partial manifest", true, true}, {"complete local projection", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := officialInstallFixture(t)
			preloadSystemPut(t, root, "Client/WindowsNoEditor/HT/Content/Paks/b.pak", []byte("old"))
			// An official launcher refresh can record the new advertised listHash while its installed resources still have the old ResVersion.
			preloadSystemPut(t, root, patcher+"config.xml", []byte(strings.Replace(config, fmt.Sprintf("%x", md5.Sum(manifest)), "d41d8cd98f00b204e9800998ecf8427e", 1)))
			if tc.writeManifest {
				list := string(manifest)
				if tc.partial {
					list = strings.Replace(list, secondFile, "", 1)
				}
				preloadSystemPut(t, root, patcher+"ResList.xml", []byte(list))
			} else if err := os.Remove(filepath.Join(root, patcher, "ResList.xml")); err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.writeManifest && !tc.partial {
					t.Errorf("complete installed projection required a network request: %s", r.URL.Path)
				}
				switch r.URL.Path {
				case "/PC_150/Version/Windows/version/1.0.0/config.xml":
					fmt.Fprint(w, config)
				case "/PC_150/Version/Windows/version/1.0.0/ResList.bin.zip":
					w.Write(archive)
				default:
					t.Errorf("adoption requested a different generation: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			b := &GameDownloadBackend{DB: dbtest.Open(t), StateRoot: t.TempDir(), Client: srv.Client(), roots: []string{srv.URL}, processes: func(context.Context) (downloadProcesses, error) { return downloadProcesses{}, nil }}
			ctx := context.Background()
			req := launcher.DownloadRequest{Root: root, Effect: state.Effect{Kind: state.EffectInstallAdopt}}
			if e := b.HandleDownload(ctx, req, nil); e.Kind != state.InstallAdopted || e.Enabled || e.Message != "1.0.0" {
				t.Fatalf("adopt old version: %+v", e)
			}
			req.Effect.Kind = state.EffectInstallVerify
			if e := b.HandleDownload(ctx, req, nil); e.Kind != state.InstallVerified || e.Message != "1.0.0" {
				t.Fatalf("verify old version: %+v", e)
			}
			store, err := b.installStore()
			if err != nil {
				t.Fatal(err)
			}
			verified, err := store.TrustedTargets(ctx, root)
			if err != nil || len(verified) != 2 {
				t.Fatalf("planning must see both verified installed files: %v, %v", verified, err)
			}
		})
	}
}
