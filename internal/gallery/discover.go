// SPDX-License-Identifier: GPL-3.0-or-later

package gallery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nerv-base/Oddward/internal/gamelocate"
)

// DiscoveryOptions controls automatic source discovery. Empty overrides use the
// real Windows Known Folders and gamelocate validation.
type DiscoveryOptions struct {
	GameRoot         string
	AutoDiscoverGame bool
	// ContentRootOverride is used by unit tests with synthetic directories that
	// do not include a full official PE64 HTGame.exe binary. Production leaves it empty.
	ContentRootOverride string
	// PicturesDirOverride overrides FOLDERID_Pictures for isolated tests.
	PicturesDirOverride string
}

// DiscoverSources probes trusted game-cache and exported media directories
// without modifying any files or following symlinks/junctions.
func DiscoverSources(ctx context.Context, opts DiscoveryOptions) ([]Source, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []Source
	seen := make(map[string]bool)
	add := func(src Source) {
		key := strings.ToLower(filepath.Clean(src.RootPath))
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, src)
	}

	if opts.AutoDiscoverGame {
		contentRoot := ""
		if opts.ContentRootOverride != "" {
			clean, err := checkLocalPath(ctx, opts.ContentRootOverride, true, false)
			if err == nil {
				contentRoot = clean
			}
		} else if opts.GameRoot != "" {
			if inst, err := gamelocate.Inspect(ctx, opts.GameRoot); err == nil {
				contentRoot = inst.ContentRoot
			}
		}
		if contentRoot != "" {
			gameSources, err := discoverGameCacheFromContentRoot(ctx, contentRoot)
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				// Best effort: skip unreadable/unsafe game subdirs without failing other sources.
			} else if err != nil {
				return nil, err
			}
			for _, s := range gameSources {
				add(s)
			}
		}
	}

	picsDir := opts.PicturesDirOverride
	if picsDir == "" {
		if resolved, err := DefaultPicturesDir(); err == nil {
			picsDir = resolved
		}
	}
	if picsDir != "" {
		exported := filepath.Join(picsDir, "NevernessToEverness")
		if clean, err := checkLocalPath(ctx, exported, true, false); err == nil {
			add(Source{
				RootPath:   clean,
				SourceType: SourceExported,
				Enabled:    true,
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].SourceType != out[j].SourceType {
			return out[i].SourceType < out[j].SourceType
		}
		if out[i].UID != out[j].UID {
			return out[i].UID < out[j].UID
		}
		return strings.ToLower(out[i].RootPath) < strings.ToLower(out[j].RootPath)
	})
	return out, nil
}

// discoverGameCacheFromContentRoot inspects the verified CN selfie layout:
//   - <ContentRoot>/Selfie/<UID>/ScreenShots
//   - <ContentRoot>/HT/Selfie/<UID>/ScreenShots (compatible variant)
//
// Derived sibling directories such as <UID>/FFMpeg (which contain video
// *_KeyFrame.jpg thumbnails) are intentionally excluded.
func discoverGameCacheFromContentRoot(ctx context.Context, contentRoot string) ([]Source, error) {
	var found []Source
	candidates := []string{
		filepath.Join(contentRoot, "Selfie"),
		filepath.Join(contentRoot, "HT", "Selfie"),
	}
	for _, selfieRoot := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cleanSelfie, err := checkLocalPath(ctx, selfieRoot, true, false)
		if err != nil {
			continue
		}
		entries, err := os.ReadDir(cleanSelfie)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !entry.IsDir() {
				continue
			}
			uid := entry.Name()
			if !ValidUIDSegment(uid) {
				continue
			}
			uidDir := filepath.Join(cleanSelfie, uid)
			reparse, err := isDirReparse(uidDir)
			if err != nil || reparse {
				continue
			}
			shotsDir, ok := findScreenshotsSubdir(ctx, uidDir)
			if !ok {
				continue
			}
			found = append(found, Source{
				RootPath:   shotsDir,
				SourceType: SourceGameCache,
				Channel:    "cn",
				UID:        uid,
				Enabled:    true,
			})
		}
	}
	return found, nil
}

func findScreenshotsSubdir(ctx context.Context, uidDir string) (string, bool) {
	entries, err := os.ReadDir(uidDir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !strings.EqualFold(e.Name(), "ScreenShots") {
			continue
		}
		candidate := filepath.Join(uidDir, e.Name())
		clean, err := checkLocalPath(ctx, candidate, true, false)
		if err != nil {
			return "", false
		}
		return clean, true
	}
	return "", false
}

// GameSelfieWatchRoots returns the parent Selfie roots that should be watched
// so newly created <UID>/ScreenShots folders are discovered dynamically during
// gameplay without watching the entire game installation.
func GameSelfieWatchRoots(ctx context.Context, gameRoot, contentRootOverride string) []string {
	contentRoot := ""
	if contentRootOverride != "" {
		if clean, err := checkLocalPath(ctx, contentRootOverride, true, false); err == nil {
			contentRoot = clean
		}
	} else if gameRoot != "" {
		if inst, err := gamelocate.Inspect(ctx, gameRoot); err == nil {
			contentRoot = inst.ContentRoot
		}
	}
	if contentRoot == "" {
		return nil
	}
	var roots []string
	for _, sub := range []string{
		filepath.Join(contentRoot, "Selfie"),
		filepath.Join(contentRoot, "HT", "Selfie"),
	} {
		if clean, err := checkLocalPath(ctx, sub, true, false); err == nil {
			roots = append(roots, clean)
		}
	}
	return roots
}

// ValidUIDSegment verifies a directory name representing a game UID folder.
func ValidUIDSegment(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// ValidateCustomSource validates a user-chosen directory before registering it.
func ValidateCustomSource(ctx context.Context, dir string) (string, error) {
	return checkLocalPath(ctx, dir, true, false)
}

// ValidateArchiveDir validates a configured archive directory. The leaf
// directory (and its Oddward/Gallery parents) may not exist yet.
func ValidateArchiveDir(ctx context.Context, dir string) (string, error) {
	return checkLocalPath(ctx, dir, true, true)
}
