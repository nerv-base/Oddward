// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"errors"
	"github.com/nerv-base/Oddward/internal/gamemedia"
	"net/http"
	"testing"
)

func TestWishImagesConstructorAndUnknownItem(t *testing.T) {
	if _, err := NewWishImages("relative", nil); err == nil {
		t.Fatal("relative cache must be rejected")
	}
	images, err := NewWishImages(t.TempDir(), &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	if images == nil {
		t.Fatal("constructor returned no image adapter")
	}
	if _, err := images.Image(context.Background(), "unrecorded-id"); !errors.Is(err, gamemedia.ErrUnknownItem) {
		t.Fatalf("unknown item error was not preserved: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := images.Image(ctx, "11001"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was not preserved: %v", err)
	}
}
