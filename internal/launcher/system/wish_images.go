// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"github.com/nerv-base/Oddward/internal/gamemedia"
	"net/http"
)

// WishImages owns the process's single game-icon cache for its directory.
// Transport, host policy, disk validation and negative caching stay in gamemedia.
type WishImages struct{ cache *gamemedia.Cache }

func NewWishImages(dir string, client *http.Client) (*WishImages, error) {
	cache, err := gamemedia.New(dir, gamemedia.Options{HTTP: client})
	if err != nil {
		return nil, err
	}
	return &WishImages{cache: cache}, nil
}
func (s *WishImages) Image(ctx context.Context, id string) ([]byte, error) {
	return s.cache.Get(ctx, id)
}
