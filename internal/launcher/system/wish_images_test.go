// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"errors"
	"testing"
)

type failingItemImages struct{}

func (failingItemImages) Get(context.Context, string) ([]byte, error) {
	return nil, errors.New("no game media")
}

type fakeArcIcons struct {
	data []byte
	err  error
}

func (f fakeArcIcons) Icon(context.Context, string) ([]byte, error) {
	return f.data, f.err
}

func TestWishImagesFallsBackToArcWikiOnlyForArcs(t *testing.T) {
	icon := []byte("wiki icon")
	s := &WishImages{cache: failingItemImages{}, wiki: fakeArcIcons{data: icon}}
	got, err := s.Image(context.Background(), "fork_wushoutieyu")
	if err != nil || string(got) != string(icon) {
		t.Fatalf("arc fallback = %q %v", got, err)
	}
	if _, err := s.Image(context.Background(), "11001"); err == nil {
		t.Fatal("character image used the arc fallback")
	}
}

func TestWishImagesKeepsOfficialIcon(t *testing.T) {
	official := []byte("official icon")
	s := &WishImages{cache: staticItemImages{data: official}, wiki: fakeArcIcons{err: errors.New("must not be used")}}
	got, err := s.Image(context.Background(), "fork_wushoutieyu")
	if err != nil || string(got) != string(official) {
		t.Fatalf("official icon = %q %v", got, err)
	}
}

type staticItemImages struct{ data []byte }

func (s staticItemImages) Get(context.Context, string) ([]byte, error) { return s.data, nil }
