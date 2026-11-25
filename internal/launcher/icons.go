// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import "github.com/egoist/mygo/ui"

// Original 24-unit line drawings, with one stroke weight across chrome,
// navigation and actions. No font-dependent glyphs or third-party icon assets.
func lineIcon(paths string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">` + paths + `</svg>`))
}

var launcherIcons = map[string]*ui.SVG{
	"\uE80F": lineIcon(`<path d="M3.5 10.5 12 3l8.5 7.5M5.5 9v11h5v-6h3v6h5V9"/>`),
	"\uE9D8": lineIcon(`<path d="M12 3.2c.8 4.3 2.4 5.9 6.7 6.7-4.3.8-5.9 2.4-6.7 6.7-.8-4.3-2.4-5.9-6.7-6.7 4.3-.8 5.9-2.4 6.7-6.7Z"/><path d="M18.4 17.2c.3 1.7 1 2.4 2.7 2.7-1.7.3-2.4 1-2.7 2.7-.3-1.7-1-2.4-2.7-2.7 1.7-.3 2.4-1 2.7-2.7Z"/>`),
	"\uE9D7": lineIcon(`<path d="M3 21h18M6 21V9h4v12M10 21V5h4v16M14 21v-8h4v8"/>`),
	"\uE9D9": lineIcon(`<path d="M4 20V13m5 7v-5m5 5v-8m5 8v-4M14 3l1.8 3.8L20 8.5l-4.2 1.7L14 14l-1.8-3.8L8 8.5l4.2-1.7Z"/>`),
	"\uE787": lineIcon(`<rect x="4" y="5" width="16" height="16" rx="3"/><path d="M8 3v4m8-4v4M4 10h16m-12 5 2.5 2.5L16 13"/>`),
	"\uE8A5": lineIcon(`<rect x="4" y="4" width="7" height="7" rx="1"/><rect x="13" y="4" width="7" height="7" rx="1"/><rect x="4" y="13" width="7" height="7" rx="1"/><path d="M13 16.5h7M16.5 13v7"/>`),
	"\uE713": lineIcon(`<path d="m9 3-.5 3-2 1.2L3.8 6l-2 3.5L4 11v2l-2.2 1.5 2 3.5 2.7-1.2 2 1.2.5 3h6l.5-3 2-1.2 2.7 1.2 2-3.5L20 13v-2l2.2-1.5-2-3.5-2.7 1.2-2-1.2L15 3Z"/><circle cx="12" cy="12" r="3"/>`),
	"\uE774": lineIcon(`<circle cx="12" cy="12" r="9"/><ellipse cx="12" cy="12" rx="4" ry="9"/><path d="M3.7 9h16.6M3.7 15h16.6"/>`),
	"\uE946": lineIcon(`<circle cx="12" cy="12" r="9"/><path d="M12 11v5m0-8h.01"/>`),
	"\uE921": lineIcon(`<path d="M5 12h14"/>`),
	"\uE922": lineIcon(`<rect x="5" y="5" width="14" height="14" rx="1.5"/>`),
	"\uE8BB": lineIcon(`<path d="m6 6 12 12M18 6 6 18"/>`),
	"\uE768": lineIcon(`<path d="m8 4 12 8-12 8Z"/>`),
	"\uE769": lineIcon(`<path d="M8 5v14m8-14v14"/>`),
	"\uE896": lineIcon(`<path d="M12 3v12m-5-5 5 5 5-5M4 17v4h16v-4"/>`),
	"\uE72C": lineIcon(`<path d="M20 9a8 8 0 1 0 0 6M20 3v6h-6"/>`),
	"\uE895": lineIcon(`<circle cx="12" cy="12" r="8"/><path d="M12 7v5l3 2"/>`),
	"\uE73E": lineIcon(`<path d="m5 12 4.5 4.5L19 7"/>`),
	"\uE76C": lineIcon(`<path d="m9 5 7 7-7 7"/>`),
	"\uE70D": lineIcon(`<path d="m6 9 6 6 6-6"/>`),
	"\uE70E": lineIcon(`<path d="m6 15 6-6 6 6"/>`),
	"\uE700": lineIcon(`<path d="M5 7h14M5 12h14M5 17h14"/>`),
	"\uE72B": lineIcon(`<path d="m15 5-7 7 7 7"/>`),
	"\uE77B": lineIcon(`<circle cx="12" cy="8" r="4"/><path d="M4 21c1.2-4 4.3-6 8-6s6.8 2 8 6"/>`),
	"\uE74D": lineIcon(`<path d="M4 7h16M9 7V4h6v3m-9 0 1 13h10l1-13M10 11v6m4-6v6"/>`),
	"\uEB9F": lineIcon(`<rect x="3" y="4" width="18" height="16" rx="2.5"/><circle cx="8.5" cy="9.5" r="1.7"/><path d="m21 15.5-5-5L7 19.5"/>`),
	"\uE8B7": lineIcon(`<path d="M3 6.5A2.5 2.5 0 0 1 5.5 4H9l2 2.5h7.5A2.5 2.5 0 0 1 21 9v8.5a2.5 2.5 0 0 1-2.5 2.5h-13A2.5 2.5 0 0 1 3 17.5Z"/>`),
	"\uE8C8": lineIcon(`<rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>`),
	"\uE8F1": lineIcon(`<rect x="3" y="4" width="18" height="4.5" rx="1.2"/><path d="M5 8.5v9.5A2 2 0 0 0 7 20h10a2 2 0 0 0 2-2V8.5M10 13h4"/>`),
	"\uEDA2": lineIcon(`<ellipse cx="12" cy="6" rx="8" ry="3"/><path d="M4 6v6c0 1.66 3.58 3 8 3s8-1.34 8-3V6M4 12v6c0 1.66 3.58 3 8 3s8-1.34 8-3v-6"/>`),
	"\uEB51": lineIcon(`<path d="M12 20.5s-7.5-4.8-7.5-10.2A4.3 4.3 0 0 1 12 7.2a4.3 4.3 0 0 1 7.5 3.1c0 5.4-7.5 10.2-7.5 10.2Z"/>`),
	"\uEB52": lineIcon(`<path fill="currentColor" d="M12 20.5s-7.5-4.8-7.5-10.2A4.3 4.3 0 0 1 12 7.2a4.3 4.3 0 0 1 7.5 3.1c0 5.4-7.5 10.2-7.5 10.2Z"/>`),
	"\uE8EC": lineIcon(`<path d="M20.6 13.4 13.4 20.6a2 2 0 0 1-2.8 0L3 13V4h9l8.6 8.6a2 2 0 0 1 0 2.8Z"/><circle cx="8" cy="8.5" r="1.2"/>`),
	"\uE721": lineIcon(`<circle cx="11" cy="11" r="7"/><path d="m20 20-3.8-3.8"/>`),
	"\uE714": lineIcon(`<rect x="3" y="6" width="13" height="12" rx="2"/><path d="m16 10 5-3v10l-5-3Z"/>`),
	"\uE767": lineIcon(`<path d="M11 5 6 9H3v6h3l5 4Z"/><path d="M15.5 8.5a5 5 0 0 1 0 7M18.5 5.5a9 9 0 0 1 0 13"/>`),
	"\uE74F": lineIcon(`<path d="M11 5 6 9H3v6h3l5 4Z"/><path d="m16 9 5 6m0-6-5 6"/>`),
	"\uE91B": lineIcon(`<rect x="3" y="4" width="18" height="14" rx="2"/><path d="M8 21h8M12 18v3"/><path d="m6 15 4-4 3 3 3-3 2 2"/>`),
	"\uE710": lineIcon(`<path d="M12 5v14M5 12h14"/>`),
	"\uE70F": lineIcon(`<path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L8 18l-4 1 1-4Z"/>`),
}

func glyph(c *ui.Context, key string, size float32) {
	if icon := launcherIcons[key]; icon != nil {
		ui.Icon(c, icon).FontSize(size)
	}
}
