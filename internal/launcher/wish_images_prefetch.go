// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import "github.com/nerv-base/Oddward/internal/gacha"

// prefetchWishIcons warms the record icons the first screen of a just-loaded
// archive shows. The page only asks for icons while painting, so without this
// the frame after a load paints empty squares and every worker starts a frame
// late. Rows are picked in column order, one row per pool at a time, so the
// three columns fill together instead of the limited pool taking all workers.
func (s *Shell) prefetchWishIcons(archive gacha.Archive) {
	if !s.wishIconsEnabled() {
		return
	}
	rows := wishFirstScreenRows(s.viewportW, s.viewportH)
	if rows == 0 {
		return
	}
	columns := make(map[gacha.Pool][]string, 3)
	for _, record := range archive.Records {
		columns[record.Pool] = append(columns[record.Pool], record.ItemID)
	}
	for row := 0; row < rows; row++ {
		for _, pool := range []gacha.Pool{gacha.Limited, gacha.Permanent, gacha.Weapon} {
			if row < len(columns[pool]) {
				s.prefetchWishIcon(columns[pool][row])
			}
		}
	}
}

// wishFirstScreenRows estimates how many record rows one pool card shows. It
// mirrors wishPool: the card stacks header, meta, four stat rows and the
// section line above the scroller, and up to two footnote lines below it. The
// paint asks for the exact visible rows once they have bounds, so a slightly
// conservative estimate costs nothing: it never warms rows below the fold.
func wishFirstScreenRows(width, height float32) int {
	if width <= 0 || height <= 0 {
		return 0
	}
	available := width - railWidth - bezelWidth - 2*wishInset
	cardHeight := wishNarrowCardH
	if available >= 3*wishCardMinWidth+2*wishCardGap {
		// Charge both headers the page may add above the cards, so the
		// estimate never treats a covered row as visible.
		top := wishCardsTop + wishImportReportHeight + wishLinkNoteHeight
		cardHeight = height - top - wishBottomInset - 2*bezelWidth
	}
	stacked := 2*wishCardPaddingY + wishPoolHeaderHeight + wishPoolMetaHeight + 2*wishHairlineWidth +
		4*wishPoolStatRowHeight + wishPoolSectionHeight + 8*wishSpaceXXS
	below := 2*wishIconFootnoteLine + 3*wishSpaceXXS
	if cardHeight <= stacked+below {
		return 0
	}
	rows := int((cardHeight - stacked - below) / (wishRowHeight + wishRowGap))
	return min(max(rows, 0), wishIconFirstScreenMaxRows)
}
