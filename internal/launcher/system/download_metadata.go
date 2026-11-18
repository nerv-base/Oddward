// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/nerv-base/Oddward/internal/gameres"
)

const (
	bootstrapBranch       = "PC_150"
	bootstrapUpdateBranch = "publish_Updata"
	bootstrapAppID        = "1289"
)

var officialDownloadRoots = [...]string{"https://yhcdn1.wmupd.com/clientRes", "https://yhcdn2.wmupd.com/clientRes"}

var errDownloadNotFound = errors.New("game resource is absent")
var errDownloadMetadata = errors.New("unable to verify delivery metadata")
var downloadVersion = regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}$`)

type remoteDownloads struct {
	client *http.Client
	roots  []string
}

type downloadResponse struct {
	Data                          []byte
	Root, URL, ETag, LastModified string
	ObservedAt                    time.Time
}

type gameDelivery struct {
	Config         gameres.Config
	Manifest       gameres.ResList
	Graph          *gameres.PatchGraph
	XML            gameres.ManifestXML
	ConfigResponse downloadResponse
	Branch         string
}

func (m remoteDownloads) get(ctx context.Context, root, address string, limit int64) (downloadResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return downloadResponse{}, errDownloadMetadata
	}
	req.Header.Set("Accept-Encoding", "identity")
	client := *m.client
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return downloadResponse{}, ctx.Err()
		}
		return downloadResponse{}, errDownloadMetadata
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return downloadResponse{}, errDownloadNotFound
	}
	if resp.StatusCode != http.StatusOK || resp.ContentLength > limit || resp.Uncompressed || (resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity") {
		return downloadResponse{}, fmt.Errorf("%w (HTTP %d)", errDownloadMetadata, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(data) == 0 || int64(len(data)) > limit {
		return downloadResponse{}, errDownloadMetadata
	}
	return downloadResponse{Data: data, Root: root, URL: address, ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified"), ObservedAt: time.Now().UTC()}, nil
}

func (m remoteDownloads) configAt(ctx context.Context, root, branch string) (gameres.Config, downloadResponse, error) {
	address, err := gameres.ConfigURL(root, branch)
	if err != nil {
		return gameres.Config{}, downloadResponse{}, err
	}
	res, err := m.get(ctx, root, address, 8<<20)
	if err != nil {
		return gameres.Config{}, downloadResponse{}, err
	}
	cfg, err := gameres.ParseConfig(res.Data)
	if err != nil || !downloadVersion.MatchString(cfg.ResVersion) {
		return gameres.Config{}, downloadResponse{}, errDownloadMetadata
	}
	return cfg, res, nil
}

func (m remoteDownloads) config(ctx context.Context, branch string) (gameres.Config, downloadResponse, error) {
	var failures []error
	for _, root := range m.roots {
		cfg, res, err := m.configAt(ctx, root, branch)
		if err == nil {
			return cfg, res, nil
		}
		if ctx.Err() != nil {
			return gameres.Config{}, downloadResponse{}, ctx.Err()
		}
		failures = append(failures, err)
	}
	return gameres.Config{}, downloadResponse{}, errors.Join(append(failures, errDownloadMetadata)...)
}

// A mirror supplies its own coherent config and archive. Mixing both mirrors'
// generations would turn an ordinary rollout into an apparent corruption.
func (m remoteDownloads) delivery(ctx context.Context, branch, version string) (gameDelivery, error) {
	var failures []error
	for _, root := range m.roots {
		d, err := m.deliveryAt(ctx, root, branch, version)
		if err == nil {
			return d, nil
		}
		if ctx.Err() != nil {
			return gameDelivery{}, ctx.Err()
		}
		failures = append(failures, err)
	}
	return gameDelivery{}, errors.Join(append(failures, errDownloadMetadata)...)
}

func (m remoteDownloads) deliveryAt(ctx context.Context, root, branch, version string) (gameDelivery, error) {
	cfg, provenance, err := m.configAt(ctx, root, branch)
	if err != nil {
		return gameDelivery{}, err
	}
	if version != "" && cfg.ResVersion != version {
		return gameDelivery{}, errDownloadMetadata
	}
	address, err := gameres.ManifestURL(root, branch, cfg.ResVersion)
	if err != nil {
		return gameDelivery{}, err
	}
	zip, err := m.get(ctx, root, address, 32<<20)
	if err != nil {
		return gameDelivery{}, err
	}
	xml, err := gameres.OpenManifestZIP(zip.Data)
	if err != nil {
		return gameDelivery{}, err
	}
	manifest, graph, err := gameres.ParseDelivery(cfg, xml)
	if err != nil {
		return gameDelivery{}, err
	}
	return gameDelivery{Config: cfg, Manifest: manifest, Graph: graph, XML: xml, ConfigResponse: provenance, Branch: branch}, nil
}

func (m remoteDownloads) resolve(branch string) func(context.Context, gameres.Source) ([]string, error) {
	return func(ctx context.Context, source gameres.Source) ([]string, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		urls := make([]string, 0, len(m.roots))
		for _, root := range m.roots {
			address, err := gameres.CASURL(root, branch, source.Object)
			if err != nil {
				return nil, err
			}
			urls = append(urls, address)
		}
		return urls, nil
	}
}
