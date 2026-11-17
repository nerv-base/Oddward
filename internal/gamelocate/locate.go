// SPDX-License-Identifier: GPL-3.0-or-later

// Package gamelocate normalizes explicit game selections without running or
// modifying anything. A structural match is not integrity or login verification.
package gamelocate

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/nerv-base/Oddward/internal/game"
)

// SelectionKind records which explicitly supported input shape was selected.
type SelectionKind uint8

const (
	SelectedRoot SelectionKind = iota
	SelectedClient
	SelectedContentRoot
	SelectedExecutable
)

// Installation is a structural snapshot, not a verified or runnable game.
// Root is the installation directory containing Client; ContentRoot is the
// Client/WindowsNoEditor directory used by the game validator.
type Installation struct {
	Root, Client, ContentRoot, Executable, AppID, Branch string
	SelectedKind                                         SelectionKind
}

// ErrNotInstallation is the existing structural validator's sentinel, so callers
// can use errors.Is without depending on this adapter's diagnostic wording.
var ErrNotInstallation = game.ErrNotInstallation

// Inspect accepts an explicitly selected Root, Client, WindowsNoEditor, or the
// exact HTGame.exe path. It returns the installation's parent-of-Client root, never
// guesses a root by walking parents, and never executes or changes a file.
// The result is a read-only structural snapshot, not launch authorization:
// callers must revalidate at the actual host boundary to address later changes.
func Inspect(ctx context.Context, selected string) (Installation, error) {
	if err := ctx.Err(); err != nil {
		return Installation{}, err
	}
	if !filepath.IsAbs(selected) {
		return Installation{}, fmt.Errorf("%w: selection must be absolute", ErrNotInstallation)
	}
	selected = filepath.Clean(selected)
	kind := SelectedRoot
	content := filepath.Join(selected, "Client", "WindowsNoEditor")
	switch {
	case strings.EqualFold(filepath.Base(selected), "HTGame.exe"):
		kind = SelectedExecutable
		content = selected
		for range 4 {
			content = filepath.Dir(content)
		}
		want := filepath.Join(content, "HT", "Binaries", "Win64", "HTGame.exe")
		if !strings.EqualFold(selected, want) {
			return Installation{}, fmt.Errorf("%w: executable is outside the supported layout", ErrNotInstallation)
		}
	case strings.EqualFold(filepath.Base(selected), "WindowsNoEditor"):
		kind, content = SelectedContentRoot, selected
	case strings.EqualFold(filepath.Base(selected), "Client"):
		kind, content = SelectedClient, filepath.Join(selected, "WindowsNoEditor")
	}
	client := filepath.Dir(content)
	if !strings.EqualFold(filepath.Base(content), "WindowsNoEditor") || !strings.EqualFold(filepath.Base(client), "Client") {
		return Installation{}, fmt.Errorf("%w: expected Client/WindowsNoEditor", ErrNotInstallation)
	}

	// Reuse the mature validator: local volume, ancestor reparse rejection,
	// root-scoped access, bounded metadata, PE64 identity, content, app and branch.
	// Do not replace these with a few Stat calls or follow links to make a match.
	checked, err := game.Inspect(ctx, content)
	if err != nil {
		return Installation{}, err
	}
	if err := ctx.Err(); err != nil {
		return Installation{}, err
	}
	if !strings.EqualFold(checked.Root, content) {
		return Installation{}, fmt.Errorf("%w: selected layout did not match the validated content root", ErrNotInstallation)
	}
	return Installation{
		Root: filepath.Dir(client), Client: client, ContentRoot: checked.Root,
		Executable: checked.Executable, AppID: checked.AppID, Branch: checked.Branch,
		SelectedKind: kind,
	}, nil
}
