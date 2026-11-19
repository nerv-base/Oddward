// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"net/http"

	"github.com/nerv-base/Oddward/internal/bwiki"
	"github.com/nerv-base/Oddward/internal/gacha"
	"github.com/nerv-base/Oddward/internal/gamemedia"
)

// arcIcons is the small read surface WishImages needs from the community
// supplement; *bwiki.Store implements it.
type arcIcons interface {
	Icon(context.Context, string) ([]byte, error)
}

// itemImages is the read surface of the game-media cache.
type itemImages interface {
	Get(context.Context, string) ([]byte, error)
}

// WishImages owns the process's single game-icon cache for its directory.
// Transport, host policy, disk validation and negative caching stay in gamemedia.
type WishImages struct {
	cache itemImages
	wiki  arcIcons
}

func NewWishImages(dir string, client *http.Client) (*WishImages, error) {
	cache, err := gamemedia.New(dir, gamemedia.Options{HTTP: client})
	if err != nil {
		return nil, err
	}
	return &WishImages{cache: cache}, nil
}

// SetBWiki adds a fallback source for arc icons that the game-media index does
// not know. It never replaces an official icon.
func (s *WishImages) SetBWiki(store *bwiki.Store) { s.wiki = store }

func (s *WishImages) Image(ctx context.Context, id string) ([]byte, error) {
	data, err := s.cache.Get(ctx, id)
	if err == nil {
		return data, nil
	}
	if s.wiki != nil && gacha.Kind(id) == gacha.Arc {
		if icon, wikiErr := s.wiki.Icon(ctx, id); wikiErr == nil {
			return icon, nil
		}
	}
	return nil, err
}
