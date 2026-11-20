// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"github.com/nerv-base/Oddward/internal/database/dbtest"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/gameinstall"
	"github.com/nerv-base/Oddward/internal/gameres"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// This encoder wraps tiny synthetic XML only; it never reads official metadata.
func preloadSyntheticArchive(t *testing.T, xml []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	z := zlib.NewWriter(&compressed)
	if _, err := z.Write(xml); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	plain := append([]byte(nil), compressed.Bytes()...)
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(padding)}, padding)...)
	block, err := aes.NewCipher([]byte("1289@Patcher0000"))
	if err != nil {
		t.Fatal(err)
	}
	encrypted := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, []byte("PatcherSDK000000")).CryptBlocks(encrypted, plain)
	container := append([]byte("PatcherXML0\x00"), make([]byte, 4)...)
	binary.LittleEndian.PutUint32(container[len(container)-4:], uint32(len(xml)))
	container = append(container, encrypted...)
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	entry, err := archive.Create("ResList.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = entry.Write(container); err != nil {
		t.Fatal(err)
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestSystemPreloadSyntheticStageReconcileApply(t *testing.T) {
	ctx := context.Background()
	released := false
	casCalls := 0
	var delivery gameDelivery
	var manifestZIP []byte
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg, _ := gameres.ConfigURL(delivery.ConfigResponse.Root, "PC_150")
		manifest, _ := gameres.ManifestURL(delivery.ConfigResponse.Root, "PC_150", "1.1.0")
		cas, _ := gameres.CASURL(delivery.ConfigResponse.Root, "PC_160", gameres.Object{MD5: delivery.Manifest.Files[0].MD5, Size: 3})
		switch "https://" + r.Host + r.URL.Path {
		case cfg:
			if released {
				w.Write(delivery.ConfigResponse.Data)
			} else {
				fmt.Fprintf(w, `<config><ResVersion>1.0.0</ResVersion><PreReleaseStatus>start</PreReleaseStatus><PreReleaseBranch>PC_160</PreReleaseBranch><PreReleaseVersion>1.1.0</PreReleaseVersion><Extra><listHash>%s</listHash></Extra></config>`, delivery.Config.ListHash)
			}
		case manifest:
			w.Write(manifestZIP)
		case cas:
			if r.Method == http.MethodGet {
				casCalls++
			}
			http.ServeContent(w, r, "synthetic", time.Time{}, bytes.NewReader([]byte("new")))
		default:
			t.Errorf("unexpected synthetic route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	delivery = preloadSystemDelivery(t, srv.URL+"/clientRes")
	manifestZIP = preloadSyntheticArchive(t, delivery.XML.ResList)
	b := &GameDownloadBackend{DB: dbtest.Open(t), StateRoot: t.TempDir(), Client: srv.Client(), roots: []string{delivery.ConfigResponse.Root}, free: func(string) (int64, error) { return 8 << 30, nil }, processes: func(context.Context) (downloadProcesses, error) { return downloadProcesses{}, nil }, preloadQuery: func(context.Context, string, string) (gameDelivery, error) { return delivery, nil }}
	root := preloadSystemBase(t, b)
	request := launcher.DownloadRequest{Root: root, Kind: state.DownloadPreload}
	request.Effect.Kind = state.EffectDownloadPlan
	if event := b.HandleDownload(ctx, request, nil); event.Kind != state.DownloadPlanned {
		t.Fatalf("preload plan: %+v", event)
	}
	p := b.getPlan(root)
	if p == nil || p.preload == nil {
		t.Fatal("preload plan missing")
	}
	object := p.preload.Files[0].Object
	blob := filepath.Join(p.workRoot, "cache", "blobs", object.String())
	request.Effect.Kind = state.EffectPreloadRun
	if event := b.HandleDownload(ctx, request, nil); event.Kind != state.PreloadStaged {
		t.Fatalf("preload stage: %+v", event)
	}
	before, err := os.Stat(blob)
	if err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(root, "Client", "WindowsNoEditor", "HT", "Content", "Paks", "a.pak")
	if data, err := os.ReadFile(installed); err != nil || string(data) != "old" {
		t.Fatal("preload modified installation")
	}
	released = true
	request.Effect = state.Effect{Kind: state.EffectPreloadCheck, Enabled: true}
	if event := b.HandleDownload(ctx, request, nil); event.Kind != state.PreloadAvailableToApply {
		t.Fatalf("preload reconcile: %+v", event)
	}
	after, err := os.Stat(blob)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("reconcile copied staged bytes: %v", err)
	}
	request.Effect = state.Effect{Kind: state.EffectPreloadApply}
	if event := b.HandleDownload(ctx, request, nil); event.Kind != state.PreloadApplied {
		t.Fatalf("preload apply: %+v", event)
	}
	if data, err := os.ReadFile(installed); err != nil || string(data) != "new" {
		t.Fatalf("applied bytes: %q %v", data, err)
	}
	store, err := b.preloadStore(root)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Load(ctx, "PC_160", "1.1.0")
	if err != nil || record.State != gameinstall.PreloadCompleted {
		t.Fatalf("preload publication: %+v %v", record, err)
	}
	if casCalls != 1 {
		t.Fatalf("staged blob downloaded %d times", casCalls)
	}
}
