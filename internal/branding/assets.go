// SPDX-License-Identifier: GPL-3.0-or-later

// Package branding embeds Oddward's original application identity.
package branding

import _ "embed"

// IconPNG is the application icon used by the native window and taskbar.
//
//go:embed icon.png
var IconPNG []byte

// SymbolSVG is the monochrome mark used inside the launcher.
//
//go:embed symbol.svg
var SymbolSVG []byte
