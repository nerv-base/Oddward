// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	_ "embed"

	"github.com/egoist/mygo/ui"
)

// Official in-game resource icons, embedded so the home card renders the same
// artwork as the game. The artwork remains the property of its rights holder;
// see THIRD_PARTY_NOTICES.md.
var (
	//go:embed assets/stamina.webp
	staminaIconData []byte
	//go:embed assets/city-stamina.webp
	cityStaminaIconData []byte
)

func decodeBundledIcon(data []byte) *ui.Bitmap {
	icon, err := ui.DecodeBitmap(data)
	if err != nil {
		return nil
	}
	return icon
}
