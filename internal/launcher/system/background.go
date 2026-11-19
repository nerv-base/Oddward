// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"net/http"

	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launchmedia"
)

type launcherBackground struct {
	client *launchmedia.Client
}

func NewLauncherBackground(dir string, transport http.RoundTripper) launcher.BackgroundSource {
	return &launcherBackground{client: launchmedia.New(dir, transport)}
}

func (b *launcherBackground) Cached() (launcher.BackgroundAssets, error) {
	cached, err := b.client.Cached()
	return launcher.BackgroundAssets{Video: cached.Video, Poster: cached.Poster}, err
}

func (b *launcherBackground) Refresh(ctx context.Context) (launcher.BackgroundRefresh, error) {
	update, err := b.client.Refresh(ctx)
	return launcher.BackgroundRefresh{
		Assets:        launcher.BackgroundAssets{Video: update.Video, Poster: update.Poster},
		VideoChanged:  update.VideoChanged,
		PosterChanged: update.PosterChanged,
	}, err
}
