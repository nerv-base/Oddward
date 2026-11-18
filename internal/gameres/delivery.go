// SPDX-License-Identifier: GPL-3.0-or-later

package gameres

import "fmt"

// ParseDelivery binds decoded CDN manifests to their config before planning.
// Use ParseResList separately for the official installed-state projection.
func ParseDelivery(cfg Config, data ManifestXML) (ResList, *PatchGraph, error) {
	if err := VerifyManifestHash(data.ResList, cfg.ListHash); err != nil {
		return ResList{}, nil, fmt.Errorf("resource list: %w", err)
	}
	var graph *PatchGraph
	if len(data.LastDiff) != 0 || cfg.DiffHash != "" {
		if err := VerifyManifestHash(data.LastDiff, cfg.DiffHash); err != nil {
			return ResList{}, nil, fmt.Errorf("lastdiff: %w", err)
		}
		var err error
		graph, err = ParseDiffList(data.LastDiff)
		if err != nil {
			return ResList{}, nil, err
		}
	}
	m, err := ParseResList(data.ResList)
	if err != nil {
		return ResList{}, nil, err
	}
	if m.Version != cfg.ResVersion || m.Tag != "baseTag" || (cfg.Tag != "" && m.Tag != cfg.Tag) ||
		(cfg.BaseTag != "" && m.Tag != cfg.BaseTag) {
		return ResList{}, nil, fmt.Errorf("%w: delivery version or tag differs from config", ErrMalformedManifest)
	}
	_, size, err := plannedResources(&m)
	if err != nil {
		return ResList{}, nil, err
	}
	if cfg.ResSize != 0 && size != cfg.ResSize {
		return ResList{}, nil, fmt.Errorf("%w: delivery size differs from config", ErrMalformedManifest)
	}
	m.CheckHash = cfg.CheckHash
	return m, graph, nil
}
