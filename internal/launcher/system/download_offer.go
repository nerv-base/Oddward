// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/nerv-base/Oddward/internal/gameinstall"
	"github.com/nerv-base/Oddward/internal/gamelocate"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// offerRecord reads an existing install record without initializing the state
// directory on the first poll of an externally installed game.
func (b *GameDownloadBackend) offerRecord(root string) (gameinstall.InstallRecord, error) {
	if _, err := downloadDirectory(filepath.Join(b.StateRoot, "installs"), false); err != nil {
		return gameinstall.InstallRecord{}, err
	}
	store, err := b.installStore()
	if err != nil {
		return gameinstall.InstallRecord{}, err
	}
	return store.ReadInstall(root)
}

func (b *GameDownloadBackend) pollDownloadOffer(ctx context.Context, root string, event state.Event) (state.Event, error) {
	event.Kind = state.DownloadOfferPollFailed
	if err := ctx.Err(); err != nil {
		return event, err
	}
	root, err := downloadDirectory(root, false)
	if err != nil {
		return event, err
	}
	record, err := b.offerRecord(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, gameinstall.ErrStaleState) {
		return event, err
	}
	branch, version := record.Branch, record.Version
	event.NeedsAdoption = err != nil || record.BaselineMismatch || len(record.Targets) == 0 || version == ""
	if event.NeedsAdoption {
		found, err := gamelocate.Inspect(ctx, root)
		if err != nil {
			return event, err
		}
		branch = found.Branch
		version, err = gameinstall.ReadOfficialVersion(found)
		if err != nil {
			return event, err
		}
	} else {
		event.NeedsVerification = record.VerifyCoverage != "full" && record.VerifyCoverage != "metadata"
	}
	if !downloadVersion.MatchString(version) {
		return event, errDownloadMetadata
	}
	ad, _, _, err := b.metadata().formalConfig(ctx, branch)
	if err != nil {
		return event, err
	}
	// The formal offer needs only the two versions. A preload additionally
	// needs trusted targets, space and a coherent pre-release manifest.
	if ad.PreReleaseStatus == "start" && !event.NeedsAdoption && !event.NeedsVerification {
		_, base, local, err := b.preloadBase(ctx, root)
		if err == nil {
			input, _, _, err := b.preloadFromAdvertisement(ctx, root, base, local, ad)
			if err == nil {
				verdict := gameinstall.EvaluatePreload(input)
				event.Body, event.Enabled = verdict.Version, verdict.State == gameinstall.PreloadAvailable
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return event, err
	}
	event.Kind, event.CurrentVersion, event.Message = state.DownloadOfferPolled, version, ad.ResVersion
	return event, nil
}
