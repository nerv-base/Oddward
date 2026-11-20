// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/nerv-base/Oddward/internal/gameinstall"
	"github.com/nerv-base/Oddward/internal/gameres"
)

// Query-route admission is separate from manifest integrity. An advertisement
// never establishes the remote query URL. The backend hook is nil in production
// until that route has been observed and its semantics admitted.
func downloadPreloadEvidence(d gameDelivery, roots []string, known bool) (gameDelivery, gameinstall.PreloadManifest, error) {
	if !known {
		return gameDelivery{}, gameinstall.PreloadManifest{}, gameinstall.ErrPreloadBlocked
	}
	rootOK := false
	for _, root := range roots {
		if root == d.ConfigResponse.Root {
			rootOK = true
			break
		}
	}
	root, re := url.Parse(d.ConfigResponse.Root)
	address, ae := url.Parse(d.ConfigResponse.URL)
	if !rootOK || re != nil || ae != nil || root.Scheme != "https" || address.Scheme != root.Scheme || address.Host != root.Host || address.User != nil || root.User != nil || address.Fragment != "" || !strings.HasPrefix(address.EscapedPath(), strings.TrimRight(root.EscapedPath(), "/")+"/") {
		return gameDelivery{}, gameinstall.PreloadManifest{}, errDownloadMetadata
	}
	if _, err := gameres.ConfigURL(d.ConfigResponse.Root, d.Branch); err != nil {
		return gameDelivery{}, gameinstall.PreloadManifest{}, err
	}
	cfg, err := gameres.ParseConfig(d.ConfigResponse.Data)
	if err != nil {
		return gameDelivery{}, gameinstall.PreloadManifest{}, err
	}
	manifest, graph, err := gameres.ParseDelivery(cfg, d.XML)
	if err != nil {
		return gameDelivery{}, gameinstall.PreloadManifest{}, err
	}
	manifest, err = canonicalDownloadManifest(manifest)
	if err != nil {
		return gameDelivery{}, gameinstall.PreloadManifest{}, err
	}
	d.Config, d.Manifest, d.Graph = cfg, manifest, graph
	snapshot := gameinstall.PreloadManifest{Branch: d.Branch, Config: cfg, Manifest: &manifest, Graph: graph, Verified: true, RouteKnown: true, SameOrigin: true, Available: true}
	return d, snapshot, nil
}
func (b *GameDownloadBackend) preloadStore(root string) (gameinstall.PreloadStore, error) {
	key, err := gameinstall.InstallKey(root)
	if err != nil {
		return gameinstall.PreloadStore{}, err
	}
	return gameinstall.NewPreloadStore(b.DB, b.taskRoot(key), downloadWork(root), true)
}
func preloadSystemWork(root string, r gameinstall.PreloadRecord) string {
	return filepath.Join(downloadWork(root), "preload", r.Branch, r.Version)
}

// Metadata is read from the database: the preload mirror directories are a
// rollback aid, so a record whose mirror write failed is still evidence.
func (b *GameDownloadBackend) preloadRecords(ctx context.Context, root string) ([]gameinstall.PreloadRecord, error) {
	store, err := b.preloadStore(root)
	if err != nil {
		return nil, err
	}
	stored, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	records := make([]gameinstall.PreloadRecord, 0, len(stored))
	for _, r := range stored {
		if r.State != gameinstall.PreloadCompleted && r.State != gameinstall.PreloadUnavailable {
			records = append(records, r)
		}
	}
	return records, nil
}
func (b *GameDownloadBackend) preloadLatest(ctx context.Context, root string) (gameinstall.PreloadRecord, error) {
	records, err := b.preloadRecords(ctx, root)
	if err != nil {
		return gameinstall.PreloadRecord{}, err
	}
	if len(records) == 0 {
		return gameinstall.PreloadRecord{}, os.ErrNotExist
	}
	return records[0], nil
}
func (b *GameDownloadBackend) preloadBase(ctx context.Context, root string) (string, gameinstall.InstallRecord, downloadLocal, error) {
	root, err := downloadDirectory(root, false)
	if err != nil {
		return "", gameinstall.InstallRecord{}, downloadLocal{}, err
	}
	store, err := b.installStore()
	if err != nil {
		return "", gameinstall.InstallRecord{}, downloadLocal{}, err
	}
	base, err := store.ReadInstall(root)
	if err != nil {
		return "", gameinstall.InstallRecord{}, downloadLocal{}, err
	}
	trusted, err := store.TrustedTargets(ctx, root)
	if err != nil {
		return "", gameinstall.InstallRecord{}, downloadLocal{}, err
	}
	local := downloadLocal{installed: map[string]gameres.Object{}, cached: map[gameres.Object]bool{}}
	for _, target := range trusted {
		local.installed[strings.ToUpper(target.Path)] = gameres.Object{MD5: target.MD5, Size: target.Size}
	}
	if base.BaselineMismatch || len(base.Targets) == 0 || len(trusted) != len(base.Targets) {
		return "", gameinstall.InstallRecord{}, downloadLocal{}, gameinstall.ErrStaleState
	}
	return root, base, local, nil
}
