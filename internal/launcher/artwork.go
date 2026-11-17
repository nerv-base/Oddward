// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"os"
	"path/filepath"

	"github.com/egoist/mygo/ui"
)

// LoadPoster uses only the explicitly supplied image or the video's bg_0.png
// sibling. It never scans an installation, generates thumbnails or writes files.
func LoadPoster(videoPath, posterPath string) (*ui.Bitmap, error) {
	if posterPath == "" && videoPath != "" {
		posterPath = filepath.Join(filepath.Dir(videoPath), "bg_0.png")
	}
	return LoadArtwork(posterPath)
}

// LoadArtwork reads explicitly configured local artwork; it never fetches or copies it.
func LoadArtwork(path string) (*ui.Bitmap, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ui.DecodeBitmap(data)
}
